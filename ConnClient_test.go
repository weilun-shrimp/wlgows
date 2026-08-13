package wlgows

import (
	"bufio"
	"encoding/base64"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
)

func TestNewClientConn(t *testing.T) {
	netConn := newFakeConn(nil)
	request := httptestRequest(t)

	clientConn := NewClientConn(netConn, request)

	if clientConn.ClientRequest != request {
		t.Error("ClientRequest was not set on the embedded Conn")
	}
	if clientConn.ServerResponse != nil {
		t.Error("ServerResponse should start nil")
	}
	if clientConn.Conn.di.getFrameFromTCPConn == nil {
		t.Error("the embedded Conn must get its own di")
	}
	for name, constructor := range map[string]any{
		"upgradeRequest":            clientConn.di.upgradeRequest,
		"sendHand":                  clientConn.di.sendHand,
		"readResponse":              clientConn.di.readResponse,
		"validateHandShakeResponse": clientConn.di.validateHandShakeResponse,
		"requestToPlainHTTPMsg":     clientConn.di.requestToPlainHTTPMsg,
		"bufioNewReader":            clientConn.di.bufioNewReader,
		"httpReadResponse":          clientConn.di.httpReadResponse,
	} {
		if constructor == nil {
			t.Errorf("NewClientConn left di.%s nil", name)
		}
	}
}

func TestClientConnHandShake(t *testing.T) {
	// order records which steps ran, so short-circuiting is observable.
	newStubbed := func(t *testing.T, order *[]string) *ClientConn {
		t.Helper()
		clientConn := NewClientConn(newFakeConn(nil), httptestRequest(t))
		clientConn.di.upgradeRequest = func(*http.Request) error {
			*order = append(*order, "upgrade")
			return nil
		}
		clientConn.di.sendHand = func() error {
			*order = append(*order, "send")
			return nil
		}
		clientConn.di.readResponse = func() error {
			*order = append(*order, "read")
			return nil
		}
		clientConn.di.validateHandShakeResponse = func(*http.Response, string) error {
			*order = append(*order, "validate")
			return nil
		}
		return clientConn
	}

	t.Run("runs all four steps in order", func(t *testing.T) {
		var order []string
		clientConn := newStubbed(t, &order)
		if err := clientConn.HandShake(); err != nil {
			t.Fatalf("HandShake: %v", err)
		}
		want := []string{"upgrade", "send", "read", "validate"}
		if strings.Join(order, ",") != strings.Join(want, ",") {
			t.Errorf("order = %v, want %v", order, want)
		}
	})

	t.Run("passes the Sec-WebSocket-Key to validation", func(t *testing.T) {
		var order []string
		clientConn := newStubbed(t, &order)
		clientConn.ClientRequest.Header.Set("Sec-WebSocket-Key", "the-key")
		var gotKey string
		clientConn.di.validateHandShakeResponse = func(_ *http.Response, key string) error {
			gotKey = key
			return nil
		}
		if err := clientConn.HandShake(); err != nil {
			t.Fatalf("HandShake: %v", err)
		}
		if gotKey != "the-key" {
			t.Errorf("key = %q, want %q", gotKey, "the-key")
		}
	})

	failures := []struct {
		name      string
		breakStep string
		wantOrder string
	}{
		{"upgrade failure stops everything", "upgrade", "upgrade"},
		{"send failure stops the read", "send", "upgrade,send"},
		{"read failure stops validation", "read", "upgrade,send,read"},
		{"validation failure surfaces", "validate", "upgrade,send,read,validate"},
	}
	for _, testCase := range failures {
		t.Run(testCase.name, func(t *testing.T) {
			var order []string
			clientConn := newStubbed(t, &order)
			want := errors.New(testCase.breakStep + " failed")
			switch testCase.breakStep {
			case "upgrade":
				clientConn.di.upgradeRequest = func(*http.Request) error {
					order = append(order, "upgrade")
					return want
				}
			case "send":
				clientConn.di.sendHand = func() error {
					order = append(order, "send")
					return want
				}
			case "read":
				clientConn.di.readResponse = func() error {
					order = append(order, "read")
					return want
				}
			case "validate":
				clientConn.di.validateHandShakeResponse = func(*http.Response, string) error {
					order = append(order, "validate")
					return want
				}
			}
			if err := clientConn.HandShake(); !errors.Is(err, want) {
				t.Fatalf("err = %v, want %v", err, want)
			}
			if strings.Join(order, ",") != testCase.wantOrder {
				t.Errorf("order = %v, want %s", order, testCase.wantOrder)
			}
		})
	}
}

func TestUpgradeRequest(t *testing.T) {
	t.Run("sets the four websocket headers", func(t *testing.T) {
		request := httptestRequest(t)
		err := upgradeRequest(request, upgradeRequestDI{
			generateWebSocketKey: func() string { return "FIXED-KEY" },
		})
		if err != nil {
			t.Fatalf("upgradeRequest: %v", err)
		}
		want := map[string]string{
			"Upgrade":               "websocket",
			"Connection":            "Upgrade",
			"Sec-Websocket-Key":     "FIXED-KEY",
			"Sec-Websocket-Version": "13",
		}
		for headerKey, wantValue := range want {
			if got := request.Header.Get(headerKey); got != wantValue {
				t.Errorf("header %s = %q, want %q", headerKey, got, wantValue)
			}
		}
	})

	t.Run("rejects firstKey nil request", func(t *testing.T) {
		err := upgradeRequest(nil, upgradeRequestDI{
			generateWebSocketKey: func() string {
				t.Fatal("key generation must not run for firstKey nil request")
				return ""
			},
		})
		if err == nil {
			t.Fatal("expected an error for firstKey nil request")
		}
		if !strings.Contains(err.Error(), "ClientRequest is nil") {
			t.Errorf("err = %q", err.Error())
		}
	})

	t.Run("real wiring produces firstKey usable key", func(t *testing.T) {
		request := httptestRequest(t)
		if err := UpgradeRequest(request); err != nil {
			t.Fatalf("UpgradeRequest: %v", err)
		}
		key := request.Header.Get("Sec-WebSocket-Key")
		raw, err := base64.StdEncoding.DecodeString(key)
		if err != nil {
			t.Fatalf("key %q is not base64: %v", key, err)
		}
		if len(raw) != 16 {
			t.Errorf("decoded key is %d bytes, RFC 6455 requires 16", len(raw))
		}
	})
}

func TestClientConnSendHand(t *testing.T) {
	t.Run("writes the plain http message", func(t *testing.T) {
		netConn := newFakeConn(nil)
		clientConn := NewClientConn(netConn, httptestRequest(t))
		clientConn.di.requestToPlainHTTPMsg = func(*http.Request) (string, error) {
			return "GET / HTTP/1.1\r\n\r\n", nil
		}
		if err := clientConn.SendHand(); err != nil {
			t.Fatalf("SendHand: %v", err)
		}
		if string(netConn.written()) != "GET / HTTP/1.1\r\n\r\n" {
			t.Errorf("written = %q", netConn.written())
		}
	})

	t.Run("rejects firstKey nil client request", func(t *testing.T) {
		clientConn := NewClientConn(newFakeConn(nil), nil)
		err := clientConn.SendHand()
		if err == nil || !strings.Contains(err.Error(), "ClientRequest is nil") {
			t.Errorf("err = %v, want firstKey nil-request error", err)
		}
	})

	t.Run("propagates firstKey serialisation error", func(t *testing.T) {
		want := errors.New("cannot serialise")
		clientConn := NewClientConn(newFakeConn(nil), httptestRequest(t))
		clientConn.di.requestToPlainHTTPMsg = func(*http.Request) (string, error) { return "", want }
		if err := clientConn.SendHand(); !errors.Is(err, want) {
			t.Errorf("err = %v, want %v", err, want)
		}
	})

	t.Run("propagates firstKey write error", func(t *testing.T) {
		want := errors.New("broken pipe")
		netConn := newFakeConn(nil)
		netConn.writeErr = want
		clientConn := NewClientConn(netConn, httptestRequest(t))
		clientConn.di.requestToPlainHTTPMsg = func(*http.Request) (string, error) { return "x", nil }
		if err := clientConn.SendHand(); !errors.Is(err, want) {
			t.Errorf("err = %v, want %v", err, want)
		}
	})
}

func TestClientConnReadResponse(t *testing.T) {
	t.Run("stores the parsed response", func(t *testing.T) {
		want := &http.Response{StatusCode: 101}
		clientConn := NewClientConn(newFakeConn(nil), httptestRequest(t))
		clientConn.di.httpReadResponse = func(*bufio.Reader, *http.Request) (*http.Response, error) {
			return want, nil
		}
		if err := clientConn.ReadResponse(); err != nil {
			t.Fatalf("ReadResponse: %v", err)
		}
		if clientConn.ServerResponse != want {
			t.Error("ServerResponse was not stored")
		}
	})

	t.Run("refuses to overwrite an existing response", func(t *testing.T) {
		clientConn := NewClientConn(newFakeConn(nil), httptestRequest(t))
		clientConn.ServerResponse = &http.Response{StatusCode: 200}
		err := clientConn.ReadResponse()
		if err == nil || !strings.Contains(err.Error(), "ServerResponse has been set") {
			t.Errorf("err = %v, want an already-set error", err)
		}
	})

	t.Run("rejects firstKey nil client request", func(t *testing.T) {
		clientConn := NewClientConn(newFakeConn(nil), nil)
		err := clientConn.ReadResponse()
		if err == nil || !strings.Contains(err.Error(), "ClientRequest is nil") {
			t.Errorf("err = %v, want firstKey nil-request error", err)
		}
	})

	t.Run("propagates firstKey parse error and leaves the response nil", func(t *testing.T) {
		want := errors.New("malformed response")
		clientConn := NewClientConn(newFakeConn(nil), httptestRequest(t))
		clientConn.di.httpReadResponse = func(*bufio.Reader, *http.Request) (*http.Response, error) {
			return nil, want
		}
		if err := clientConn.ReadResponse(); !errors.Is(err, want) {
			t.Fatalf("err = %v, want %v", err, want)
		}
		if clientConn.ServerResponse != nil {
			t.Error("ServerResponse must stay nil after firstKey failed read")
		}
	})

	t.Run("real wiring parses bytes off the socket", func(t *testing.T) {
		raw := "HTTP/1.1 101 Switching Protocols\r\n" +
			"Upgrade: websocket\r\n" +
			"Connection: Upgrade\r\n\r\n"
		clientConn := NewClientConn(newFakeConn([]byte(raw)), httptestRequest(t))
		if err := clientConn.ReadResponse(); err != nil {
			t.Fatalf("ReadResponse: %v", err)
		}
		if clientConn.ServerResponse.StatusCode != 101 {
			t.Errorf("StatusCode = %d, want 101", clientConn.ServerResponse.StatusCode)
		}
		if clientConn.ServerResponse.Header.Get("Upgrade") != "websocket" {
			t.Errorf("Upgrade = %q", clientConn.ServerResponse.Header.Get("Upgrade"))
		}
	})
}

func validHandShakeResponse(key string) *http.Response {
	response := &http.Response{
		StatusCode: 101,
		Proto:      "HTTP/1.1",
		Header:     http.Header{},
	}
	response.Header.Set("Connection", "Upgrade")
	response.Header.Set("Upgrade", "websocket")
	response.Header.Set("Sec-WebSocket-Accept", GenerateSecWebsocketAccept(key))
	return response
}

func TestValidateHandShakeResponse(t *testing.T) {
	const key = "dGhlIHNhbXBsZSBub25jZQ=="

	t.Run("accepts firstKey well formed response", func(t *testing.T) {
		if err := ValidateHandShakeResponse(validHandShakeResponse(key), key); err != nil {
			t.Errorf("ValidateHandShakeResponse: %v", err)
		}
	})

	t.Run("header comparison is case insensitive", func(t *testing.T) {
		response := validHandShakeResponse(key)
		response.Header.Set("Connection", "UPGRADE")
		response.Header.Set("Upgrade", "WebSocket")
		if err := ValidateHandShakeResponse(response, key); err != nil {
			t.Errorf("ValidateHandShakeResponse: %v", err)
		}
	})

	// The server side of the same rule: a response carrying the token among
	// others is conforming, and refusing it would fail a working connection.
	t.Run("Connection and Upgrade are read as token lists", func(t *testing.T) {
		response := validHandShakeResponse(key)
		response.Header.Set("Connection", "keep-alive, Upgrade")
		if err := ValidateHandShakeResponse(response, key); err != nil {
			t.Errorf("ValidateHandShakeResponse: %v", err)
		}
	})

	t.Run("a field split across two lines", func(t *testing.T) {
		response := validHandShakeResponse(key)
		response.Header.Del("Connection")
		response.Header.Add("Connection", "keep-alive")
		response.Header.Add("Connection", "Upgrade")
		if err := ValidateHandShakeResponse(response, key); err != nil {
			t.Errorf("ValidateHandShakeResponse: %v", err)
		}
	})

	tests := []struct {
		name    string
		mutate  func(*http.Response)
		wantSub string
	}{
		{
			name:    "wrong status code",
			mutate:  func(r *http.Response) { r.StatusCode = 200 },
			wantSub: "status code 200",
		},
		{
			name:    "wrong protocol",
			mutate:  func(r *http.Response) { r.Proto = "HTTP/2.0" },
			wantSub: "proto HTTP/2.0",
		},
		{
			name:    "connection header not upgrade",
			mutate:  func(r *http.Response) { r.Header.Set("Connection", "keep-alive") },
			wantSub: "header Connection keep-alive",
		},
		{
			name:    "upgrade header not websocket",
			mutate:  func(r *http.Response) { r.Header.Set("Upgrade", "h2c") },
			wantSub: "header Upgrade h2c",
		},
		{
			name:    "missing accept header",
			mutate:  func(r *http.Response) { r.Header.Del("Sec-WebSocket-Accept") },
			wantSub: "Sec-WebSocket-Accept header is required",
		},
		{
			name:    "accept header does not match the key",
			mutate:  func(r *http.Response) { r.Header.Set("Sec-WebSocket-Accept", "wrong") },
			wantSub: "Sec-WebSocket-Accept wrong",
		},
	}
	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			response := validHandShakeResponse(key)
			testCase.mutate(response)
			err := ValidateHandShakeResponse(response, key)
			if err == nil {
				t.Fatal("expected an error")
			}
			if !strings.Contains(err.Error(), testCase.wantSub) {
				t.Errorf("err = %q, want it to contain %q", err.Error(), testCase.wantSub)
			}
		})
	}

	t.Run("rejects an accept value computed from firstKey different key", func(t *testing.T) {
		response := validHandShakeResponse(key)
		if err := ValidateHandShakeResponse(response, "firstKey-different-key"); err == nil {
			t.Error("expected firstKey mismatch error")
		}
	})
}

func TestGenerateWebSocketKey(t *testing.T) {
	t.Run("encodes the injected random bytes", func(t *testing.T) {
		got := generateWebSocketKey(generateWebSocketKeyDI{randRead: fixedRandRead(0)})
		want := base64.StdEncoding.EncodeToString(make([]byte, 16))
		if got != want {
			t.Errorf("got %q, want %q", got, want)
		}
	})

	t.Run("asks for the 16 bytes RFC 6455 requires", func(t *testing.T) {
		var size int
		generateWebSocketKey(generateWebSocketKeyDI{
			randRead: func(secondKey []byte) (int, error) {
				size = len(secondKey)
				return len(secondKey), nil
			},
		})
		if size != 16 {
			t.Errorf("requested %d bytes, want 16", size)
		}
	})

	t.Run("real wiring is base64 and varies between calls", func(t *testing.T) {
		firstKey, secondKey := GenerateWebSocketKey(), GenerateWebSocketKey()
		if firstKey == secondKey {
			t.Error("two keys should not be identical")
		}
		for _, headerKey := range []string{firstKey, secondKey} {
			raw, err := base64.StdEncoding.DecodeString(headerKey)
			if err != nil {
				t.Fatalf("key %q is not base64: %v", headerKey, err)
			}
			if len(raw) != 16 {
				t.Errorf("decoded key is %d bytes, want 16", len(raw))
			}
		}
	})
}

func TestRequestToPlainHTTPMsg(t *testing.T) {
	t.Run("writes the request line and headers", func(t *testing.T) {
		request := httptestRequest(t)
		request.Header.Set("Sec-WebSocket-Version", "13")

		got, err := requestToPlainHTTPMsg(request, requestToPlainHTTPMsgDI{ioReadAll: io.ReadAll})
		if err != nil {
			t.Fatalf("requestToPlainHTTPMsg: %v", err)
		}
		if !strings.HasPrefix(got, "GET / HTTP/1.1\r\n") {
			t.Errorf("request line wrong, got %q", got)
		}
		if !strings.Contains(got, "Sec-Websocket-Version: 13\r\n") {
			t.Errorf("missing version header in %q", got)
		}
		if !strings.HasSuffix(got, "\r\n\r\n") {
			t.Errorf("headers should end with firstKey blank line, got %q", got)
		}
	})

	/*
		The request-target must be the origin form. Emitting request.URL instead
		would put "GET ws://host:port HTTP/1.1" on the wire, which servers read
		as firstKey proxy request; gin answers an empty path with firstKey 301 rather than
		upgrading, so "ws://localhost:8001" with no trailing slash never
		completes firstKey handshake.
	*/
	t.Run("writes the origin form request target", func(t *testing.T) {
		tests := []struct {
			rawURL string
			want   string
		}{
			{"ws://localhost:8001", "GET / HTTP/1.1\r\n"},
			{"ws://localhost:8001/", "GET / HTTP/1.1\r\n"},
			{"ws://localhost:8001/chat", "GET /chat HTTP/1.1\r\n"},
			{"ws://localhost:8001/chat?room=1", "GET /chat?room=1 HTTP/1.1\r\n"},
			{"wss://localhost:8001/deep/path", "GET /deep/path HTTP/1.1\r\n"},
		}
		for _, testCase := range tests {
			t.Run(testCase.rawURL, func(t *testing.T) {
				request, err := http.NewRequest("GET", testCase.rawURL, nil)
				if err != nil {
					t.Fatalf("http.NewRequest: %v", err)
				}
				got, err := requestToPlainHTTPMsg(request, requestToPlainHTTPMsgDI{ioReadAll: io.ReadAll})
				if err != nil {
					t.Fatalf("requestToPlainHTTPMsg: %v", err)
				}
				if !strings.HasPrefix(got, testCase.want) {
					t.Errorf("request line = %q, want prefix %q", got, testCase.want)
				}
				// The absolute form must not leak into the request line.
				if strings.HasPrefix(got, "GET ws") || strings.HasPrefix(got, "GET http") {
					t.Errorf("absolute form leaked into the request line: %q", got)
				}
			})
		}
	})

	t.Run("fills in Host from the url when absent", func(t *testing.T) {
		request := httptestRequest(t)
		got, err := requestToPlainHTTPMsg(request, requestToPlainHTTPMsgDI{ioReadAll: io.ReadAll})
		if err != nil {
			t.Fatalf("requestToPlainHTTPMsg: %v", err)
		}
		if !strings.Contains(got, "Host: localhost:8001\r\n") {
			t.Errorf("Host header not derived from the url: %q", got)
		}
	})

	t.Run("keeps an explicit Host header", func(t *testing.T) {
		request := httptestRequest(t)
		request.Header.Set("Host", "example.com")
		got, err := requestToPlainHTTPMsg(request, requestToPlainHTTPMsgDI{ioReadAll: io.ReadAll})
		if err != nil {
			t.Fatalf("requestToPlainHTTPMsg: %v", err)
		}
		if !strings.Contains(got, "Host: example.com\r\n") {
			t.Errorf("explicit Host was overwritten: %q", got)
		}
	})

	t.Run("appends the body", func(t *testing.T) {
		request, err := http.NewRequest("POST", "ws://localhost:8001/", strings.NewReader("payload"))
		if err != nil {
			t.Fatalf("http.NewRequest: %v", err)
		}
		got, err := requestToPlainHTTPMsg(request, requestToPlainHTTPMsgDI{ioReadAll: io.ReadAll})
		if err != nil {
			t.Fatalf("requestToPlainHTTPMsg: %v", err)
		}
		if !strings.HasSuffix(got, "\r\n\r\npayload") {
			t.Errorf("body not appended: %q", got)
		}
	})

	t.Run("propagates firstKey body read error", func(t *testing.T) {
		want := errors.New("body exploded")
		request, err := http.NewRequest("POST", "ws://localhost:8001/", strings.NewReader("x"))
		if err != nil {
			t.Fatalf("http.NewRequest: %v", err)
		}
		got, err := requestToPlainHTTPMsg(request, requestToPlainHTTPMsgDI{
			ioReadAll: func(io.Reader) ([]byte, error) { return nil, want },
		})
		if !errors.Is(err, want) {
			t.Fatalf("err = %v, want %v", err, want)
		}
		if got != "" {
			t.Errorf("result should be empty on error, got %q", got)
		}
	})
}
