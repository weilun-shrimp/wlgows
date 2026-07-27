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
	fc := newFakeConn(nil)
	req := httptestRequest(t)

	cc := NewClientConn(fc, req)

	if cc.ClientRequest != req {
		t.Error("ClientRequest was not set on the embedded Conn")
	}
	if cc.ServerResponse != nil {
		t.Error("ServerResponse should start nil")
	}
	if cc.Conn.di.getFrameFromTCPConn == nil {
		t.Error("the embedded Conn must get its own di")
	}
	for name, fn := range map[string]any{
		"upgradeRequest":            cc.di.upgradeRequest,
		"sendHand":                  cc.di.sendHand,
		"readResponse":              cc.di.readResponse,
		"validateHandShakeResponse": cc.di.validateHandShakeResponse,
		"requestToPlainHTTPMsg":     cc.di.requestToPlainHTTPMsg,
		"bufioNewReader":            cc.di.bufioNewReader,
		"httpReadResponse":          cc.di.httpReadResponse,
		"newMsg":                    cc.di.newMsg,
	} {
		if fn == nil {
			t.Errorf("NewClientConn left di.%s nil", name)
		}
	}
}

func TestClientConnHandShake(t *testing.T) {
	// order records which steps ran, so short-circuiting is observable.
	newStubbed := func(t *testing.T, order *[]string) *ClientConn {
		t.Helper()
		cc := NewClientConn(newFakeConn(nil), httptestRequest(t))
		cc.di.upgradeRequest = func(*http.Request) error {
			*order = append(*order, "upgrade")
			return nil
		}
		cc.di.sendHand = func() error {
			*order = append(*order, "send")
			return nil
		}
		cc.di.readResponse = func() error {
			*order = append(*order, "read")
			return nil
		}
		cc.di.validateHandShakeResponse = func(*http.Response, string) error {
			*order = append(*order, "validate")
			return nil
		}
		return cc
	}

	t.Run("runs all four steps in order", func(t *testing.T) {
		var order []string
		cc := newStubbed(t, &order)
		if err := cc.HandShake(); err != nil {
			t.Fatalf("HandShake: %v", err)
		}
		want := []string{"upgrade", "send", "read", "validate"}
		if strings.Join(order, ",") != strings.Join(want, ",") {
			t.Errorf("order = %v, want %v", order, want)
		}
	})

	t.Run("passes the Sec-WebSocket-Key to validation", func(t *testing.T) {
		var order []string
		cc := newStubbed(t, &order)
		cc.ClientRequest.Header.Set("Sec-WebSocket-Key", "the-key")
		var gotKey string
		cc.di.validateHandShakeResponse = func(_ *http.Response, key string) error {
			gotKey = key
			return nil
		}
		if err := cc.HandShake(); err != nil {
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
	for _, tt := range failures {
		t.Run(tt.name, func(t *testing.T) {
			var order []string
			cc := newStubbed(t, &order)
			want := errors.New(tt.breakStep + " failed")
			switch tt.breakStep {
			case "upgrade":
				cc.di.upgradeRequest = func(*http.Request) error {
					order = append(order, "upgrade")
					return want
				}
			case "send":
				cc.di.sendHand = func() error {
					order = append(order, "send")
					return want
				}
			case "read":
				cc.di.readResponse = func() error {
					order = append(order, "read")
					return want
				}
			case "validate":
				cc.di.validateHandShakeResponse = func(*http.Response, string) error {
					order = append(order, "validate")
					return want
				}
			}
			if err := cc.HandShake(); !errors.Is(err, want) {
				t.Fatalf("err = %v, want %v", err, want)
			}
			if strings.Join(order, ",") != tt.wantOrder {
				t.Errorf("order = %v, want %s", order, tt.wantOrder)
			}
		})
	}
}

func TestUpgradeRequest(t *testing.T) {
	t.Run("sets the four websocket headers", func(t *testing.T) {
		req := httptestRequest(t)
		err := upgradeRequest(req, upgradeRequestDI{
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
		for k, v := range want {
			if got := req.Header.Get(k); got != v {
				t.Errorf("header %s = %q, want %q", k, got, v)
			}
		}
	})

	t.Run("rejects a nil request", func(t *testing.T) {
		err := upgradeRequest(nil, upgradeRequestDI{
			generateWebSocketKey: func() string {
				t.Fatal("key generation must not run for a nil request")
				return ""
			},
		})
		if err == nil {
			t.Fatal("expected an error for a nil request")
		}
		if !strings.Contains(err.Error(), "ClientRequest is nil") {
			t.Errorf("err = %q", err.Error())
		}
	})

	t.Run("real wiring produces a usable key", func(t *testing.T) {
		req := httptestRequest(t)
		if err := UpgradeRequest(req); err != nil {
			t.Fatalf("UpgradeRequest: %v", err)
		}
		key := req.Header.Get("Sec-WebSocket-Key")
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
		fc := newFakeConn(nil)
		cc := NewClientConn(fc, httptestRequest(t))
		cc.di.requestToPlainHTTPMsg = func(*http.Request) (string, error) {
			return "GET / HTTP/1.1\r\n\r\n", nil
		}
		if err := cc.SendHand(); err != nil {
			t.Fatalf("SendHand: %v", err)
		}
		if string(fc.written()) != "GET / HTTP/1.1\r\n\r\n" {
			t.Errorf("written = %q", fc.written())
		}
	})

	t.Run("rejects a nil client request", func(t *testing.T) {
		cc := NewClientConn(newFakeConn(nil), nil)
		err := cc.SendHand()
		if err == nil || !strings.Contains(err.Error(), "ClientRequest is nil") {
			t.Errorf("err = %v, want a nil-request error", err)
		}
	})

	t.Run("propagates a serialisation error", func(t *testing.T) {
		want := errors.New("cannot serialise")
		cc := NewClientConn(newFakeConn(nil), httptestRequest(t))
		cc.di.requestToPlainHTTPMsg = func(*http.Request) (string, error) { return "", want }
		if err := cc.SendHand(); !errors.Is(err, want) {
			t.Errorf("err = %v, want %v", err, want)
		}
	})

	t.Run("propagates a write error", func(t *testing.T) {
		want := errors.New("broken pipe")
		fc := newFakeConn(nil)
		fc.writeErr = want
		cc := NewClientConn(fc, httptestRequest(t))
		cc.di.requestToPlainHTTPMsg = func(*http.Request) (string, error) { return "x", nil }
		if err := cc.SendHand(); !errors.Is(err, want) {
			t.Errorf("err = %v, want %v", err, want)
		}
	})
}

func TestClientConnReadResponse(t *testing.T) {
	t.Run("stores the parsed response", func(t *testing.T) {
		want := &http.Response{StatusCode: 101}
		cc := NewClientConn(newFakeConn(nil), httptestRequest(t))
		cc.di.httpReadResponse = func(*bufio.Reader, *http.Request) (*http.Response, error) {
			return want, nil
		}
		if err := cc.ReadResponse(); err != nil {
			t.Fatalf("ReadResponse: %v", err)
		}
		if cc.ServerResponse != want {
			t.Error("ServerResponse was not stored")
		}
	})

	t.Run("refuses to overwrite an existing response", func(t *testing.T) {
		cc := NewClientConn(newFakeConn(nil), httptestRequest(t))
		cc.ServerResponse = &http.Response{StatusCode: 200}
		err := cc.ReadResponse()
		if err == nil || !strings.Contains(err.Error(), "ServerResponse has been set") {
			t.Errorf("err = %v, want an already-set error", err)
		}
	})

	t.Run("rejects a nil client request", func(t *testing.T) {
		cc := NewClientConn(newFakeConn(nil), nil)
		err := cc.ReadResponse()
		if err == nil || !strings.Contains(err.Error(), "ClientRequest is nil") {
			t.Errorf("err = %v, want a nil-request error", err)
		}
	})

	t.Run("propagates a parse error and leaves the response nil", func(t *testing.T) {
		want := errors.New("malformed response")
		cc := NewClientConn(newFakeConn(nil), httptestRequest(t))
		cc.di.httpReadResponse = func(*bufio.Reader, *http.Request) (*http.Response, error) {
			return nil, want
		}
		if err := cc.ReadResponse(); !errors.Is(err, want) {
			t.Fatalf("err = %v, want %v", err, want)
		}
		if cc.ServerResponse != nil {
			t.Error("ServerResponse must stay nil after a failed read")
		}
	})

	t.Run("real wiring parses bytes off the socket", func(t *testing.T) {
		raw := "HTTP/1.1 101 Switching Protocols\r\n" +
			"Upgrade: websocket\r\n" +
			"Connection: Upgrade\r\n\r\n"
		cc := NewClientConn(newFakeConn([]byte(raw)), httptestRequest(t))
		if err := cc.ReadResponse(); err != nil {
			t.Fatalf("ReadResponse: %v", err)
		}
		if cc.ServerResponse.StatusCode != 101 {
			t.Errorf("StatusCode = %d, want 101", cc.ServerResponse.StatusCode)
		}
		if cc.ServerResponse.Header.Get("Upgrade") != "websocket" {
			t.Errorf("Upgrade = %q", cc.ServerResponse.Header.Get("Upgrade"))
		}
	})
}

func validHandShakeResponse(key string) *http.Response {
	res := &http.Response{
		StatusCode: 101,
		Proto:      "HTTP/1.1",
		Header:     http.Header{},
	}
	res.Header.Set("Connection", "Upgrade")
	res.Header.Set("Upgrade", "websocket")
	res.Header.Set("Sec-WebSocket-Accept", GenerateSecWebsocketAccept(key))
	return res
}

func TestValidateHandShakeResponse(t *testing.T) {
	const key = "dGhlIHNhbXBsZSBub25jZQ=="

	t.Run("accepts a well formed response", func(t *testing.T) {
		if err := ValidateHandShakeResponse(validHandShakeResponse(key), key); err != nil {
			t.Errorf("ValidateHandShakeResponse: %v", err)
		}
	})

	t.Run("header comparison is case insensitive", func(t *testing.T) {
		res := validHandShakeResponse(key)
		res.Header.Set("Connection", "UPGRADE")
		res.Header.Set("Upgrade", "WebSocket")
		if err := ValidateHandShakeResponse(res, key); err != nil {
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
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			res := validHandShakeResponse(key)
			tt.mutate(res)
			err := ValidateHandShakeResponse(res, key)
			if err == nil {
				t.Fatal("expected an error")
			}
			if !strings.Contains(err.Error(), tt.wantSub) {
				t.Errorf("err = %q, want it to contain %q", err.Error(), tt.wantSub)
			}
		})
	}

	t.Run("rejects an accept value computed from a different key", func(t *testing.T) {
		res := validHandShakeResponse(key)
		if err := ValidateHandShakeResponse(res, "a-different-key"); err == nil {
			t.Error("expected a mismatch error")
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
			randRead: func(b []byte) (int, error) {
				size = len(b)
				return len(b), nil
			},
		})
		if size != 16 {
			t.Errorf("requested %d bytes, want 16", size)
		}
	})

	t.Run("real wiring is base64 and varies between calls", func(t *testing.T) {
		a, b := GenerateWebSocketKey(), GenerateWebSocketKey()
		if a == b {
			t.Error("two keys should not be identical")
		}
		for _, k := range []string{a, b} {
			raw, err := base64.StdEncoding.DecodeString(k)
			if err != nil {
				t.Fatalf("key %q is not base64: %v", k, err)
			}
			if len(raw) != 16 {
				t.Errorf("decoded key is %d bytes, want 16", len(raw))
			}
		}
	})
}

func TestRequestToPlainHTTPMsg(t *testing.T) {
	t.Run("writes the request line and headers", func(t *testing.T) {
		req := httptestRequest(t)
		req.Header.Set("Sec-WebSocket-Version", "13")

		got, err := requestToPlainHTTPMsg(req, requestToPlainHTTPMsgDI{ioReadAll: io.ReadAll})
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
			t.Errorf("headers should end with a blank line, got %q", got)
		}
	})

	/*
		The request-target must be the origin form. Emitting req.URL instead
		would put "GET ws://host:port HTTP/1.1" on the wire, which servers read
		as a proxy request; gin answers an empty path with a 301 rather than
		upgrading, so "ws://localhost:8001" with no trailing slash never
		completes a handshake.
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
		for _, tt := range tests {
			t.Run(tt.rawURL, func(t *testing.T) {
				req, err := http.NewRequest("GET", tt.rawURL, nil)
				if err != nil {
					t.Fatalf("http.NewRequest: %v", err)
				}
				got, err := requestToPlainHTTPMsg(req, requestToPlainHTTPMsgDI{ioReadAll: io.ReadAll})
				if err != nil {
					t.Fatalf("requestToPlainHTTPMsg: %v", err)
				}
				if !strings.HasPrefix(got, tt.want) {
					t.Errorf("request line = %q, want prefix %q", got, tt.want)
				}
				// The absolute form must not leak into the request line.
				if strings.HasPrefix(got, "GET ws") || strings.HasPrefix(got, "GET http") {
					t.Errorf("absolute form leaked into the request line: %q", got)
				}
			})
		}
	})

	t.Run("fills in Host from the url when absent", func(t *testing.T) {
		req := httptestRequest(t)
		got, err := requestToPlainHTTPMsg(req, requestToPlainHTTPMsgDI{ioReadAll: io.ReadAll})
		if err != nil {
			t.Fatalf("requestToPlainHTTPMsg: %v", err)
		}
		if !strings.Contains(got, "Host: localhost:8001\r\n") {
			t.Errorf("Host header not derived from the url: %q", got)
		}
	})

	t.Run("keeps an explicit Host header", func(t *testing.T) {
		req := httptestRequest(t)
		req.Header.Set("Host", "example.com")
		got, err := requestToPlainHTTPMsg(req, requestToPlainHTTPMsgDI{ioReadAll: io.ReadAll})
		if err != nil {
			t.Fatalf("requestToPlainHTTPMsg: %v", err)
		}
		if !strings.Contains(got, "Host: example.com\r\n") {
			t.Errorf("explicit Host was overwritten: %q", got)
		}
	})

	t.Run("appends the body", func(t *testing.T) {
		req, err := http.NewRequest("POST", "ws://localhost:8001/", strings.NewReader("payload"))
		if err != nil {
			t.Fatalf("http.NewRequest: %v", err)
		}
		got, err := requestToPlainHTTPMsg(req, requestToPlainHTTPMsgDI{ioReadAll: io.ReadAll})
		if err != nil {
			t.Fatalf("requestToPlainHTTPMsg: %v", err)
		}
		if !strings.HasSuffix(got, "\r\n\r\npayload") {
			t.Errorf("body not appended: %q", got)
		}
	})

	t.Run("propagates a body read error", func(t *testing.T) {
		want := errors.New("body exploded")
		req, err := http.NewRequest("POST", "ws://localhost:8001/", strings.NewReader("x"))
		if err != nil {
			t.Fatalf("http.NewRequest: %v", err)
		}
		got, err := requestToPlainHTTPMsg(req, requestToPlainHTTPMsgDI{
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

// RFC 6455 requires every client-to-server frame to be masked.
func TestClientConnSendTextAndSendByteAlwaysMask(t *testing.T) {
	tests := []struct {
		name       string
		send       func(*ClientConn, []byte) error
		wantOpcode uint8
	}{
		{"SendText uses opcode 1", (*ClientConn).SendText, 1},
		{"SendByte uses opcode 2", (*ClientConn).SendByte, 2},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cc := NewClientConn(newFakeConn(nil), httptestRequest(t))
			var gotOpcode uint8
			var gotMask bool
			var gotData []byte
			cc.di.newMsg = func(data []byte, opcode uint8, need_mask bool) (*Msg, error) {
				gotData, gotOpcode, gotMask = data, opcode, need_mask
				return &Msg{}, nil
			}

			if err := tt.send(cc, []byte("payload")); err != nil {
				t.Fatalf("send: %v", err)
			}
			if gotOpcode != tt.wantOpcode {
				t.Errorf("opcode = %d, want %d", gotOpcode, tt.wantOpcode)
			}
			if !gotMask {
				t.Error("client frames must be masked")
			}
			if string(gotData) != "payload" {
				t.Errorf("data = %q", gotData)
			}
		})
	}
}

func TestClientConnSendPropagatesNewMsgError(t *testing.T) {
	sends := map[string]func(*ClientConn, []byte) error{
		"SendText": (*ClientConn).SendText,
		"SendByte": (*ClientConn).SendByte,
	}
	for name, send := range sends {
		t.Run(name, func(t *testing.T) {
			want := errors.New("cannot build message")
			cc := NewClientConn(newFakeConn(nil), httptestRequest(t))
			cc.di.newMsg = func([]byte, uint8, bool) (*Msg, error) { return nil, want }
			if err := send(cc, []byte("x")); !errors.Is(err, want) {
				t.Errorf("err = %v, want %v", err, want)
			}
		})
	}
}

// End to end through the real newMsg: the bytes on the wire are masked.
func TestClientConnSendTextWritesMaskedBytes(t *testing.T) {
	fc := newFakeConn(nil)
	cc := NewClientConn(fc, httptestRequest(t))
	if err := cc.SendText([]byte("hi")); err != nil {
		t.Fatalf("SendText: %v", err)
	}
	got := fc.written()
	if len(got) != 8 {
		t.Fatalf("wrote %d bytes, want 8 (2 header + 4 key + 2 payload)", len(got))
	}
	if got[0] != 0x81 {
		t.Errorf("first byte = %#x, want 0x81 (FIN + opcode 1)", got[0])
	}
	if got[1]&0x80 == 0 {
		t.Error("mask bit must be set on a client frame")
	}
	key := got[2:6]
	if string([]byte{got[6] ^ key[0], got[7] ^ key[1]}) != "hi" {
		t.Error("payload does not unmask back to the original text")
	}
}
