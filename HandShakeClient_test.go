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

func TestClientHandShake(t *testing.T) {
	// order records which steps ran, so short-circuiting is observable.
	newOrderTrackingDI := func(order *[]string) clientHandShakeDI {
		return clientHandShakeDI{
			upgradeRequest: func(*http.Request) error {
				*order = append(*order, "upgrade")
				return nil
			},
			sendHandShakeRequest: func(io.Writer, *http.Request) error {
				*order = append(*order, "send")
				return nil
			},
			readHandShakeResponse: func(*bufio.Reader, *http.Request) (*http.Response, error) {
				*order = append(*order, "read")
				return &http.Response{StatusCode: 101}, nil
			},
			validateHandShakeResponse: func(*http.Response, string) error {
				*order = append(*order, "validate")
				return nil
			},
		}
	}

	t.Run("runs all four steps in order", func(t *testing.T) {
		var order []string
		req := httptestRequest(t)
		if _, err := clientHandShake(nil, nil, req, newOrderTrackingDI(&order)); err != nil {
			t.Fatalf("clientHandShake: %v", err)
		}
		want := []string{"upgrade", "send", "read", "validate"}
		if strings.Join(order, ",") != strings.Join(want, ",") {
			t.Errorf("order = %v, want %v", order, want)
		}
	})

	t.Run("passes the Sec-WebSocket-Key to validation", func(t *testing.T) {
		var order []string
		di := newOrderTrackingDI(&order)
		req := httptestRequest(t)
		req.Header.Set("Sec-WebSocket-Key", "the-key")
		var gotKey string
		di.validateHandShakeResponse = func(_ *http.Response, key string) error {
			gotKey = key
			return nil
		}
		if _, err := clientHandShake(nil, nil, req, di); err != nil {
			t.Fatalf("clientHandShake: %v", err)
		}
		if gotKey != "the-key" {
			t.Errorf("key = %q, want %q", gotKey, "the-key")
		}
	})

	t.Run("returns the response even when validation fails", func(t *testing.T) {
		var order []string
		di := newOrderTrackingDI(&order)
		want := errors.New("bad accept")
		di.validateHandShakeResponse = func(*http.Response, string) error { return want }
		res, err := clientHandShake(nil, nil, httptestRequest(t), di)
		if !errors.Is(err, want) {
			t.Fatalf("err = %v, want %v", err, want)
		}
		if res == nil || res.StatusCode != 101 {
			t.Errorf("res = %v, want the parsed response to survive a validation failure", res)
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
			di := newOrderTrackingDI(&order)
			want := errors.New(testCase.breakStep + " failed")
			switch testCase.breakStep {
			case "upgrade":
				di.upgradeRequest = func(*http.Request) error {
					order = append(order, "upgrade")
					return want
				}
			case "send":
				di.sendHandShakeRequest = func(io.Writer, *http.Request) error {
					order = append(order, "send")
					return want
				}
			case "read":
				di.readHandShakeResponse = func(*bufio.Reader, *http.Request) (*http.Response, error) {
					order = append(order, "read")
					return nil, want
				}
			case "validate":
				di.validateHandShakeResponse = func(*http.Response, string) error {
					order = append(order, "validate")
					return want
				}
			}
			if _, err := clientHandShake(nil, nil, httptestRequest(t), di); !errors.Is(err, want) {
				t.Fatalf("err = %v, want %v", err, want)
			}
			if strings.Join(order, ",") != testCase.wantOrder {
				t.Errorf("order = %v, want %s", order, testCase.wantOrder)
			}
		})
	}
}

// The success path's Conn-wrapping is trivial composition of clientHandShake
// (tested above via DI) and NewConn (tested in Connection_test.go), and isn't
// retested here for real: the real UpgradeRequest picks a fresh crypto/rand
// key on every call, so a canned wire response can't be made to match it
// without a live two-ended connection.
func TestClientHandShakeRealWiringReturnsNoConnOnFailure(t *testing.T) {
	netConn := newFakeConn(nil) // empty: ReadHandShakeResponse fails immediately
	r := bufio.NewReader(netConn)
	conn, _, err := ClientHandShake(netConn, r, httptestRequest(t))
	if err == nil {
		t.Fatal("expected an error")
	}
	if conn != nil {
		t.Error("expected no Conn when the handshake fails")
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

	t.Run("rejects a nil request", func(t *testing.T) {
		err := upgradeRequest(nil, upgradeRequestDI{
			generateWebSocketKey: func() string {
				t.Fatal("key generation must not run for a nil request")
				return ""
			},
		})
		if !errors.Is(err, ErrHandshakeRequestNil) {
			t.Errorf("err = %v, want %v", err, ErrHandshakeRequestNil)
		}
	})

	t.Run("real wiring produces a usable key", func(t *testing.T) {
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

func TestSendHandShakeRequest(t *testing.T) {
	t.Run("writes the plain http message", func(t *testing.T) {
		w := &fakeIOWriter{}
		err := sendHandShakeRequest(w, httptestRequest(t), sendHandShakeRequestDI{
			requestToPlainHTTPMsg: func(*http.Request) (string, error) {
				return "GET / HTTP/1.1\r\n\r\n", nil
			},
		})
		if err != nil {
			t.Fatalf("sendHandShakeRequest: %v", err)
		}
		if w.String() != "GET / HTTP/1.1\r\n\r\n" {
			t.Errorf("written = %q", w.String())
		}
	})

	t.Run("rejects a nil request", func(t *testing.T) {
		err := sendHandShakeRequest(&fakeIOWriter{}, nil, sendHandShakeRequestDI{
			requestToPlainHTTPMsg: func(*http.Request) (string, error) {
				t.Fatal("must not run for a nil request")
				return "", nil
			},
		})
		if !errors.Is(err, ErrHandshakeRequestNil) {
			t.Errorf("err = %v, want %v", err, ErrHandshakeRequestNil)
		}
	})

	t.Run("propagates a serialisation error", func(t *testing.T) {
		want := errors.New("cannot serialise")
		err := sendHandShakeRequest(&fakeIOWriter{}, httptestRequest(t), sendHandShakeRequestDI{
			requestToPlainHTTPMsg: func(*http.Request) (string, error) { return "", want },
		})
		if !errors.Is(err, want) {
			t.Errorf("err = %v, want %v", err, want)
		}
	})

	t.Run("propagates a write error", func(t *testing.T) {
		want := errors.New("broken pipe")
		w := &fakeIOWriter{err: want}
		err := sendHandShakeRequest(w, httptestRequest(t), sendHandShakeRequestDI{
			requestToPlainHTTPMsg: func(*http.Request) (string, error) { return "x", nil },
		})
		if !errors.Is(err, want) {
			t.Errorf("err = %v, want %v", err, want)
		}
	})
}

func TestReadHandShakeResponse(t *testing.T) {
	t.Run("returns the parsed response", func(t *testing.T) {
		want := &http.Response{StatusCode: 101}
		res, err := readHandShakeResponse(bufio.NewReader(&fakeIOReader{}), httptestRequest(t), readHandShakeResponseDI{
			httpReadResponse: func(*bufio.Reader, *http.Request) (*http.Response, error) {
				return want, nil
			},
		})
		if err != nil {
			t.Fatalf("readHandShakeResponse: %v", err)
		}
		if res != want {
			t.Error("response was not returned")
		}
	})

	t.Run("rejects a nil request", func(t *testing.T) {
		_, err := readHandShakeResponse(bufio.NewReader(&fakeIOReader{}), nil, readHandShakeResponseDI{
			httpReadResponse: func(*bufio.Reader, *http.Request) (*http.Response, error) {
				t.Fatal("must not run for a nil request")
				return nil, nil
			},
		})
		if !errors.Is(err, ErrHandshakeRequestNil) {
			t.Errorf("err = %v, want %v", err, ErrHandshakeRequestNil)
		}
	})

	t.Run("propagates a parse error", func(t *testing.T) {
		want := errors.New("malformed response")
		_, err := readHandShakeResponse(bufio.NewReader(&fakeIOReader{}), httptestRequest(t), readHandShakeResponseDI{
			httpReadResponse: func(*bufio.Reader, *http.Request) (*http.Response, error) {
				return nil, want
			},
		})
		if !errors.Is(err, want) {
			t.Fatalf("err = %v, want %v", err, want)
		}
	})

	t.Run("real wiring parses bytes off the reader", func(t *testing.T) {
		raw := "HTTP/1.1 101 Switching Protocols\r\n" +
			"Upgrade: websocket\r\n" +
			"Connection: Upgrade\r\n\r\n"
		r := &fakeIOReader{}
		r.Reset([]byte(raw))
		res, err := ReadHandShakeResponse(bufio.NewReader(r), httptestRequest(t))
		if err != nil {
			t.Fatalf("ReadHandShakeResponse: %v", err)
		}
		if res.StatusCode != 101 {
			t.Errorf("StatusCode = %d, want 101", res.StatusCode)
		}
		if res.Header.Get("Upgrade") != "websocket" {
			t.Errorf("Upgrade = %q", res.Header.Get("Upgrade"))
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

	t.Run("accepts a well formed response", func(t *testing.T) {
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
		wantErr error
	}{
		{
			name:    "wrong status code",
			mutate:  func(r *http.Response) { r.StatusCode = 200 },
			wantErr: ErrHandshakeResponseStatusCodeInvalid,
		},
		{
			name:    "wrong protocol",
			mutate:  func(r *http.Response) { r.Proto = "HTTP/2.0" },
			wantErr: ErrHandshakeResponseProtoInvalid,
		},
		{
			name:    "connection header not upgrade",
			mutate:  func(r *http.Response) { r.Header.Set("Connection", "keep-alive") },
			wantErr: ErrHandshakeResponseConnectionHeaderInvalid,
		},
		{
			name:    "upgrade header not websocket",
			mutate:  func(r *http.Response) { r.Header.Set("Upgrade", "h2c") },
			wantErr: ErrHandshakeResponseUpgradeHeaderInvalid,
		},
		{
			name:    "missing accept header",
			mutate:  func(r *http.Response) { r.Header.Del("Sec-WebSocket-Accept") },
			wantErr: ErrHandshakeResponseAcceptHeaderMissing,
		},
		{
			name:    "accept header does not match the key",
			mutate:  func(r *http.Response) { r.Header.Set("Sec-WebSocket-Accept", "wrong") },
			wantErr: ErrHandshakeResponseAcceptHeaderMismatch,
		},
	}
	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			response := validHandShakeResponse(key)
			testCase.mutate(response)
			err := ValidateHandShakeResponse(response, key)
			if !errors.Is(err, testCase.wantErr) {
				t.Errorf("err = %v, want %v", err, testCase.wantErr)
			}
		})
	}

	t.Run("rejects an accept value computed from a different key", func(t *testing.T) {
		response := validHandShakeResponse(key)
		if err := ValidateHandShakeResponse(response, "a-different-key"); !errors.Is(err, ErrHandshakeResponseAcceptHeaderMismatch) {
			t.Errorf("err = %v, want %v", err, ErrHandshakeResponseAcceptHeaderMismatch)
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
		request, err := http.NewRequest("GET", "ws://localhost:8001/", nil)
		if err != nil {
			t.Fatalf("http.NewRequest: %v", err)
		}
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
			t.Errorf("headers should end with a blank line, got %q", got)
		}
	})

	/*
		The request-target must be the origin form. Emitting request.URL instead
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
		request, err := http.NewRequest("GET", "ws://localhost:8001/", nil)
		if err != nil {
			t.Fatalf("http.NewRequest: %v", err)
		}
		got, err := requestToPlainHTTPMsg(request, requestToPlainHTTPMsgDI{ioReadAll: io.ReadAll})
		if err != nil {
			t.Fatalf("requestToPlainHTTPMsg: %v", err)
		}
		if !strings.Contains(got, "Host: localhost:8001\r\n") {
			t.Errorf("Host header not derived from the url: %q", got)
		}
	})

	t.Run("keeps an explicit Host header", func(t *testing.T) {
		request, err := http.NewRequest("GET", "ws://localhost:8001/", nil)
		if err != nil {
			t.Fatalf("http.NewRequest: %v", err)
		}
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

	t.Run("propagates a body read error", func(t *testing.T) {
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
