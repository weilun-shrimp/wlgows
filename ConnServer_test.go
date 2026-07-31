package wlgows

import (
	"bufio"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
)

func validClientHandShakeRequest(t *testing.T) *http.Request {
	t.Helper()
	request := httptestRequest(t)
	request.Header.Set("Sec-WebSocket-Key", "dGhlIHNhbXBsZSBub25jZQ==")
	request.Header.Set("Connection", "Upgrade")
	request.Header.Set("Upgrade", "websocket")
	return request
}

func TestNewServerConn(t *testing.T) {
	netConn := newFakeConn(nil)
	request := validClientHandShakeRequest(t)

	serverConn := NewServerConn(netConn, request)

	if serverConn.ClientRequest != request {
		t.Error("ClientRequest was not set on the embedded Conn")
	}
	if serverConn.Conn.di.getMsgFromTCPConn == nil {
		t.Error("the embedded Conn must get its own di")
	}
	for name, constructor := range map[string]any{
		"readRequest":              serverConn.di.readRequest,
		"validateHandShakeRequest": serverConn.di.validateHandShakeRequest,
		"newResponseWriter":        serverConn.di.newResponseWriter,
		"sendHand":                 serverConn.di.sendHand,
		"responseToPlainHTTPMsg":   serverConn.di.responseToPlainHTTPMsg,
		"bufioNewReader":           serverConn.di.bufioNewReader,
		"httpReadRequest":          serverConn.di.httpReadRequest,
		"newMsg":                   serverConn.di.newMsg,
		"fmtPrintln":               serverConn.di.fmtPrintln,
	} {
		if constructor == nil {
			t.Errorf("NewServerConn left di.%s nil", name)
		}
	}
}

func TestServerConnHandShake(t *testing.T) {
	t.Run("refuses when a response was already sent", func(t *testing.T) {
		serverConn := NewServerConn(newFakeConn(nil), validClientHandShakeRequest(t))
		serverConn.ServerResponse = &http.Response{StatusCode: 101}
		response, err := serverConn.HandShake()
		if err == nil || !strings.Contains(err.Error(), "Server Response has been set") {
			t.Errorf("err = %v, want an already-sent error", err)
		}
		if response != nil {
			t.Error("no response should be produced")
		}
	})

	t.Run("upgrades a valid request", func(t *testing.T) {
		serverConn := NewServerConn(newFakeConn(nil), validClientHandShakeRequest(t))
		var writer *ResponseWriter
		serverConn.di.newResponseWriter = func() *ResponseWriter {
			writer = NewResponseWriter()
			return writer
		}
		serverConn.di.readRequest = func() (*http.Request, *Error) {
			t.Fatal("readRequest must not run when ClientRequest is already set")
			return nil, nil
		}
		var sentWriter *ResponseWriter
		serverConn.di.sendHand = func(writer *ResponseWriter) (*http.Response, error) {
			sentWriter = writer
			return &http.Response{StatusCode: writer.statusCode}, nil
		}

		response, err := serverConn.HandShake()
		if err != nil {
			t.Fatalf("HandShake: %v", err)
		}
		if response.StatusCode != 101 {
			t.Errorf("StatusCode = %d, want 101", response.StatusCode)
		}
		if sentWriter != writer {
			t.Error("the writer built by di must be the one handed to sendHand")
		}
		if writer.Header().Get("Sec-Websocket-Accept") == "" {
			t.Error("upgrade path should set Sec-Websocket-Accept")
		}
	})

	t.Run("reads the request when none is set yet", func(t *testing.T) {
		serverConn := NewServerConn(newFakeConn(nil), nil)
		called := false
		serverConn.di.readRequest = func() (*http.Request, *Error) {
			called = true
			serverConn.ClientRequest = validClientHandShakeRequest(t)
			return serverConn.ClientRequest, nil
		}
		serverConn.di.sendHand = func(writer *ResponseWriter) (*http.Response, error) {
			return &http.Response{StatusCode: writer.statusCode}, nil
		}
		if _, err := serverConn.HandShake(); err != nil {
			t.Fatalf("HandShake: %v", err)
		}
		if !called {
			t.Error("readRequest should run when ClientRequest is nil")
		}
	})

	t.Run("declines when reading the request fails", func(t *testing.T) {
		serverConn := NewServerConn(newFakeConn(nil), nil)
		invalid := &Error{Type: HttpMsgFormationInvalid, Msg: "bad http"}
		serverConn.di.readRequest = func() (*http.Request, *Error) { return nil, invalid }
		serverConn.di.validateHandShakeRequest = func(*http.Request) *Error {
			t.Fatal("validation must be skipped when the read already failed")
			return nil
		}
		var writer *ResponseWriter
		serverConn.di.newResponseWriter = func() *ResponseWriter {
			writer = NewResponseWriter()
			return writer
		}
		serverConn.di.sendHand = func(writer *ResponseWriter) (*http.Response, error) {
			return &http.Response{StatusCode: writer.statusCode}, nil
		}

		response, err := serverConn.HandShake()
		// The decline response is still returned alongside the error.
		if response == nil {
			t.Fatal("a decline response should still be produced")
		}
		if !errors.Is(err, error(invalid)) {
			t.Errorf("err = %v, want the invalid error", err)
		}
		if writer.statusCode != http.StatusBadRequest {
			t.Errorf("statusCode = %d, want 400", writer.statusCode)
		}
	})

	t.Run("declines an invalid handshake request", func(t *testing.T) {
		serverConn := NewServerConn(newFakeConn(nil), httptestRequest(t)) // no websocket headers
		var writer *ResponseWriter
		serverConn.di.newResponseWriter = func() *ResponseWriter {
			writer = NewResponseWriter()
			return writer
		}
		serverConn.di.sendHand = func(writer *ResponseWriter) (*http.Response, error) {
			return &http.Response{StatusCode: writer.statusCode}, nil
		}

		_, err := serverConn.HandShake()
		if err == nil {
			t.Fatal("expected the validation error to surface")
		}
		if writer.statusCode != http.StatusBadRequest {
			t.Errorf("statusCode = %d, want 400", writer.statusCode)
		}
		if writer.Header().Get("Sec-Websocket-Accept") != "" {
			t.Error("a declined request must not be upgraded")
		}
	})

	t.Run("a send failure wins over the validation error", func(t *testing.T) {
		sendErr := errors.New("write failed")
		serverConn := NewServerConn(newFakeConn(nil), httptestRequest(t))
		serverConn.di.sendHand = func(*ResponseWriter) (*http.Response, error) {
			return nil, sendErr
		}
		if _, err := serverConn.HandShake(); !errors.Is(err, sendErr) {
			t.Errorf("err = %v, want %v", err, sendErr)
		}
	})
}

func TestServerConnSendHand(t *testing.T) {
	t.Run("writes the response and cross links it with the request", func(t *testing.T) {
		netConn := newFakeConn(nil)
		request := validClientHandShakeRequest(t)
		serverConn := NewServerConn(netConn, request)
		writer := NewResponseWriter()
		writer.UpgradeForWebsocket("dGhlIHNhbXBsZSBub25jZQ==")

		response, err := serverConn.SendHand(writer)
		if err != nil {
			t.Fatalf("SendHand: %v", err)
		}
		if serverConn.ServerResponse != response {
			t.Error("ServerResponse was not stored")
		}
		if request.Response != response || response.Request != request {
			t.Error("request and response should reference each other")
		}
		if !strings.HasPrefix(string(netConn.written()), "HTTP/1.1 101 Switching Protocols\r\n") {
			t.Errorf("status line wrong: %q", netConn.written())
		}
	})

	t.Run("tolerates a nil client request", func(t *testing.T) {
		serverConn := NewServerConn(newFakeConn(nil), nil)
		response, err := serverConn.SendHand(NewResponseWriter())
		if err != nil {
			t.Fatalf("SendHand: %v", err)
		}
		if serverConn.ServerResponse != response {
			t.Error("ServerResponse should still be stored")
		}
	})

	t.Run("propagates a serialisation error", func(t *testing.T) {
		want := errors.New("cannot serialise")
		serverConn := NewServerConn(newFakeConn(nil), nil)
		serverConn.di.responseToPlainHTTPMsg = func(*http.Response) (string, error) { return "", want }
		if _, err := serverConn.SendHand(NewResponseWriter()); !errors.Is(err, want) {
			t.Errorf("err = %v, want %v", err, want)
		}
	})

	t.Run("does not store the response when the write fails", func(t *testing.T) {
		want := errors.New("broken pipe")
		netConn := newFakeConn(nil)
		netConn.writeErr = want
		serverConn := NewServerConn(netConn, nil)
		if _, err := serverConn.SendHand(NewResponseWriter()); !errors.Is(err, want) {
			t.Fatalf("err = %v, want %v", err, want)
		}
		if serverConn.ServerResponse != nil {
			t.Error("ServerResponse must stay nil after a failed write")
		}
	})
}

func TestServerConnReadRequest(t *testing.T) {
	t.Run("stores the parsed request", func(t *testing.T) {
		want := validClientHandShakeRequest(t)
		serverConn := NewServerConn(newFakeConn(nil), nil)
		serverConn.di.httpReadRequest = func(*bufio.Reader) (*http.Request, error) { return want, nil }

		got, invalid := serverConn.ReadRequest()
		if invalid != nil {
			t.Fatalf("unexpected error: %v", invalid)
		}
		if got != want || serverConn.ClientRequest != want {
			t.Error("the parsed request should be returned and stored")
		}
	})

	t.Run("refuses to read twice", func(t *testing.T) {
		existing := validClientHandShakeRequest(t)
		serverConn := NewServerConn(newFakeConn(nil), existing)
		serverConn.di.httpReadRequest = func(*bufio.Reader) (*http.Request, error) {
			t.Fatal("must not read when a request is already set")
			return nil, nil
		}

		got, invalid := serverConn.ReadRequest()
		if invalid == nil || invalid.Type != ClientRequestHasSet {
			t.Fatalf("invalid = %v, want ClientRequestHasSet", invalid)
		}
		// The existing request is handed back alongside the error.
		if got != existing {
			t.Error("the already-set request should still be returned")
		}
	})

	t.Run("maps a parse failure to HttpMsgFormationInvalid", func(t *testing.T) {
		serverConn := NewServerConn(newFakeConn(nil), nil)
		serverConn.di.httpReadRequest = func(*bufio.Reader) (*http.Request, error) {
			return nil, errors.New("garbage on the wire")
		}
		var logged bool
		serverConn.di.fmtPrintln = func(...any) (int, error) { logged = true; return 0, nil }

		got, invalid := serverConn.ReadRequest()
		if got != nil {
			t.Error("no request should be returned")
		}
		if invalid == nil || invalid.Type != HttpMsgFormationInvalid {
			t.Fatalf("invalid = %v, want HttpMsgFormationInvalid", invalid)
		}
		if !strings.Contains(invalid.Msg, "garbage on the wire") {
			t.Errorf("the raw error should be quoted, got %q", invalid.Msg)
		}
		if !logged {
			t.Error("the parse failure should go through di.fmtPrintln")
		}
		if serverConn.ClientRequest != nil {
			t.Error("ClientRequest must stay nil after a failed read")
		}
	})

	t.Run("real wiring parses bytes off the socket", func(t *testing.T) {
		raw := "GET / HTTP/1.1\r\n" +
			"Host: localhost:8001\r\n" +
			"Upgrade: websocket\r\n" +
			"Connection: Upgrade\r\n" +
			"Sec-WebSocket-Key: dGhlIHNhbXBsZSBub25jZQ==\r\n\r\n"
		serverConn := NewServerConn(newFakeConn([]byte(raw)), nil)
		request, invalid := serverConn.ReadRequest()
		if invalid != nil {
			t.Fatalf("unexpected error: %v", invalid)
		}
		if request.Method != "GET" || request.Header.Get("Upgrade") != "websocket" {
			t.Errorf("parsed request wrong: %+v", request)
		}
	})
}

func TestValidateHandShakeRequest(t *testing.T) {
	t.Run("accepts a well formed request", func(t *testing.T) {
		if err := ValidateHandShakeRequest(validClientHandShakeRequest(t)); err != nil {
			t.Errorf("ValidateHandShakeRequest: %v", err)
		}
	})

	t.Run("header comparison is case insensitive", func(t *testing.T) {
		request := validClientHandShakeRequest(t)
		request.Header.Set("Connection", "UPGRADE")
		request.Header.Set("Upgrade", "WEBSOCKET")
		if err := ValidateHandShakeRequest(request); err != nil {
			t.Errorf("ValidateHandShakeRequest: %v", err)
		}
	})

	tests := []struct {
		name     string
		mutate   func(*http.Request)
		wantType string
	}{
		{
			name:     "non GET method",
			mutate:   func(r *http.Request) { r.Method = "POST" },
			wantType: HttpMethodNotAllowed,
		},
		{
			name:     "wrong protocol",
			mutate:   func(r *http.Request) { r.Proto = "HTTP/2.0" },
			wantType: HttpProtocolOrVersionNotAllowed,
		},
		{
			name:     "missing websocket key",
			mutate:   func(r *http.Request) { r.Header.Del("Sec-WebSocket-Key") },
			wantType: HttpSecWebSocketKeyHeaderNotSet,
		},
		{
			name:     "connection header not upgrade",
			mutate:   func(r *http.Request) { r.Header.Set("Connection", "keep-alive") },
			wantType: HttpConnectionHeaderNotUpgrade,
		},
		{
			name:     "upgrade header not websocket",
			mutate:   func(r *http.Request) { r.Header.Set("Upgrade", "h2c") },
			wantType: HttpUpgradeHeaderNotWebsocket,
		},
	}
	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			request := validClientHandShakeRequest(t)
			testCase.mutate(request)
			got := ValidateHandShakeRequest(request)
			if got == nil {
				t.Fatal("expected an error")
			}
			if got.Type != testCase.wantType {
				t.Errorf("Type = %q, want %q", got.Type, testCase.wantType)
			}
			if got.Msg == "" {
				t.Error("Msg should explain the failure")
			}
		})
	}

	// Method is checked before the headers.
	t.Run("reports the first failure it finds", func(t *testing.T) {
		request := httptestRequest(t)
		request.Method = "DELETE"
		if got := ValidateHandShakeRequest(request); got.Type != HttpMethodNotAllowed {
			t.Errorf("Type = %q, want the method error first", got.Type)
		}
	})
}

func TestResponseToPlainHTTPMsg(t *testing.T) {
	newResponse := func() *http.Response {
		response := &http.Response{
			Proto:      "HTTP/1.1",
			StatusCode: 101,
			Header:     http.Header{},
		}
		response.Header.Set("Upgrade", "websocket")
		return response
	}

	t.Run("writes the status line and headers", func(t *testing.T) {
		got, err := responseToPlainHTTPMsgInner(newResponse(), responseToPlainHTTPMsgDI{
			ioReadAll:      io.ReadAll,
			httpStatusText: http.StatusText,
		})
		if err != nil {
			t.Fatalf("responseToPlainHTTPMsgInner: %v", err)
		}
		if !strings.HasPrefix(got, "HTTP/1.1 101 Switching Protocols\r\n") {
			t.Errorf("status line wrong: %q", got)
		}
		if !strings.Contains(got, "Upgrade: websocket\r\n") {
			t.Errorf("missing header: %q", got)
		}
		if !strings.HasSuffix(got, "\r\n\r\n") {
			t.Errorf("headers should end with a blank line: %q", got)
		}
	})

	t.Run("uses the injected status text", func(t *testing.T) {
		got, err := responseToPlainHTTPMsgInner(newResponse(), responseToPlainHTTPMsgDI{
			ioReadAll:      io.ReadAll,
			httpStatusText: func(int) string { return "CUSTOM" },
		})
		if err != nil {
			t.Fatalf("responseToPlainHTTPMsgInner: %v", err)
		}
		if !strings.HasPrefix(got, "HTTP/1.1 101 CUSTOM\r\n") {
			t.Errorf("status text not injected: %q", got)
		}
	})

	t.Run("appends the body", func(t *testing.T) {
		response := newResponse()
		response.Body = io.NopCloser(strings.NewReader("the body"))
		got, err := responseToPlainHTTPMsgInner(response, responseToPlainHTTPMsgDI{
			ioReadAll:      io.ReadAll,
			httpStatusText: http.StatusText,
		})
		if err != nil {
			t.Fatalf("responseToPlainHTTPMsgInner: %v", err)
		}
		if !strings.HasSuffix(got, "\r\n\r\nthe body") {
			t.Errorf("body not appended: %q", got)
		}
	})

	t.Run("propagates a body read error", func(t *testing.T) {
		want := errors.New("body exploded")
		response := newResponse()
		response.Body = io.NopCloser(strings.NewReader("x"))
		got, err := responseToPlainHTTPMsgInner(response, responseToPlainHTTPMsgDI{
			ioReadAll:      func(io.Reader) ([]byte, error) { return nil, want },
			httpStatusText: http.StatusText,
		})
		if !errors.Is(err, want) {
			t.Fatalf("err = %v, want %v", err, want)
		}
		if got != "" {
			t.Errorf("result should be empty on error: %q", got)
		}
	})
}

// RFC 6455 forbids the server from masking its frames.
func TestServerConnSendTextAndSendByteNeverMask(t *testing.T) {
	tests := []struct {
		name       string
		send       func(*ServerConn, []byte) error
		wantOpcode uint8
	}{
		{"SendText uses opcode 1", (*ServerConn).SendText, 1},
		{"SendByte uses opcode 2", (*ServerConn).SendByte, 2},
	}
	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			serverConn := NewServerConn(newFakeConn(nil), nil)
			var gotOpcode uint8
			var gotMask bool
			serverConn.di.newMsg = func(_ []byte, opcode uint8, need_mask bool) (*Msg, error) {
				gotOpcode, gotMask = opcode, need_mask
				return &Msg{}, nil
			}

			if err := testCase.send(serverConn, []byte("payload")); err != nil {
				t.Fatalf("send: %v", err)
			}
			if gotOpcode != testCase.wantOpcode {
				t.Errorf("opcode = %d, want %d", gotOpcode, testCase.wantOpcode)
			}
			if gotMask {
				t.Error("server frames must NOT be masked")
			}
		})
	}
}

func TestServerConnSendTextWritesUnmaskedBytes(t *testing.T) {
	netConn := newFakeConn(nil)
	serverConn := NewServerConn(netConn, nil)
	if err := serverConn.SendText([]byte("hi")); err != nil {
		t.Fatalf("SendText: %v", err)
	}
	want := []byte{0x81, 0x02, 'h', 'i'}
	got := netConn.written()
	if string(got) != string(want) {
		t.Errorf("written = % x, want % x", got, want)
	}
	if got[1]&0x80 != 0 {
		t.Error("mask bit must be clear on a server frame")
	}
}

func TestServerConnSendPropagatesNewMsgError(t *testing.T) {
	sends := map[string]func(*ServerConn, []byte) error{
		"SendText": (*ServerConn).SendText,
		"SendByte": (*ServerConn).SendByte,
	}
	for name, send := range sends {
		t.Run(name, func(t *testing.T) {
			want := errors.New("cannot build message")
			serverConn := NewServerConn(newFakeConn(nil), nil)
			serverConn.di.newMsg = func([]byte, uint8, bool) (*Msg, error) { return nil, want }
			if err := send(serverConn, []byte("x")); !errors.Is(err, want) {
				t.Errorf("err = %v, want %v", err, want)
			}
		})
	}
}
