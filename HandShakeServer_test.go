package wlgows

import (
	"bufio"
	"errors"
	"io"
	"net"
	"net/http"
	"slices"
	"strings"
	"testing"
)

func validClientHandShakeRequest(t *testing.T) *http.Request {
	t.Helper()
	request := httptestRequest(t)
	request.Header.Set("Sec-WebSocket-Key", "dGhlIHNhbXBsZSBub25jZQ==")
	request.Header.Set("Connection", "Upgrade")
	request.Header.Set("Upgrade", "websocket")
	request.Header.Set("Sec-WebSocket-Version", "13") // 4.2.1 requires it
	return request
}

// fakeResponseWriter is a ServerHandShakeRespWriter double: it records which
// method serverHandShake called it with, instead of running the real
// ResponseWriter logic.
type fakeResponseWriter struct {
	declineCalled bool
	declinedWith  error
	upgradeCalled bool
	upgradeKey    string
	response      *http.Response
}

func (w *fakeResponseWriter) DeclineByError(err error) {
	w.declineCalled = true
	w.declinedWith = err
}

func (w *fakeResponseWriter) UpgradeForWebsocket(key string) {
	w.upgradeCalled = true
	w.upgradeKey = key
}

func (w *fakeResponseWriter) GenerateResponse() *http.Response {
	if w.response == nil {
		w.response = &http.Response{}
	}
	return w.response
}

func TestServerHandShake(t *testing.T) {
	t.Run("rejects a nil request", func(t *testing.T) {
		_, _, err := serverHandShake(newFakeConn(nil), nil, nil, nil, ServerHandShakeConfig{}, serverHandShakeDI{
			validateHandShakeRequest: func(*http.Request) error {
				t.Fatal("must not run for a nil request")
				return nil
			},
			newResponseWriter: func() ServerHandShakeRespWriter {
				t.Fatal("must not run for a nil request")
				return nil
			},
			sendHandShakeResponse: func(io.Writer, *http.Response) error {
				t.Fatal("must not run for a nil request")
				return nil
			},
			newConn: func(net.Conn, *bufio.Reader, *bufio.Writer, bool) (*Conn, error) {
				t.Fatal("must not run for a nil request")
				return nil, nil
			},
		})
		if !errors.Is(err, ErrHandshakeRequestNil) {
			t.Errorf("err = %v, want %v", err, ErrHandshakeRequestNil)
		}
	})

	// The send is what actually reached the peer, so its error outranks
	// whatever validation already decided — a caller can't fix a write that
	// already happened, but validate's error would otherwise mask it.
	t.Run("propagates the send error over the validation error", func(t *testing.T) {
		wantSendErr := errors.New("broken pipe")
		_, _, err := serverHandShake(newFakeConn(nil), nil, nil, validClientHandShakeRequest(t), ServerHandShakeConfig{}, serverHandShakeDI{
			validateHandShakeRequest: func(*http.Request) error { return errors.New("invalid") },
			newResponseWriter:        func() ServerHandShakeRespWriter { return &fakeResponseWriter{} },
			sendHandShakeResponse:    func(io.Writer, *http.Response) error { return wantSendErr },
			newConn:                  func(net.Conn, *bufio.Reader, *bufio.Writer, bool) (*Conn, error) { return &Conn{}, nil },
		})
		if !errors.Is(err, wantSendErr) {
			t.Errorf("err = %v, want %v", err, wantSendErr)
		}
	})

	t.Run("links the request and response both ways", func(t *testing.T) {
		req := validClientHandShakeRequest(t)
		_, res, err := serverHandShake(newFakeConn(nil), nil, nil, req, ServerHandShakeConfig{}, serverHandShakeDI{
			validateHandShakeRequest: func(*http.Request) error { return nil },
			newResponseWriter:        func() ServerHandShakeRespWriter { return &fakeResponseWriter{} },
			sendHandShakeResponse:    func(io.Writer, *http.Response) error { return nil },
			newConn:                  func(net.Conn, *bufio.Reader, *bufio.Writer, bool) (*Conn, error) { return &Conn{}, nil },
		})
		if err != nil {
			t.Fatalf("serverHandShake: %v", err)
		}
		if req.Response != res {
			t.Error("req.Response was not linked")
		}
		if res.Request != req {
			t.Error("res.Request was not linked")
		}
	})

	t.Run("calls newResponseWriter exactly once", func(t *testing.T) {
		calls := 0
		_, _, err := serverHandShake(newFakeConn(nil), nil, nil, validClientHandShakeRequest(t), ServerHandShakeConfig{}, serverHandShakeDI{
			validateHandShakeRequest: func(*http.Request) error { return nil },
			newResponseWriter: func() ServerHandShakeRespWriter {
				calls++
				return &fakeResponseWriter{}
			},
			sendHandShakeResponse: func(io.Writer, *http.Response) error { return nil },
			newConn:               func(net.Conn, *bufio.Reader, *bufio.Writer, bool) (*Conn, error) { return &Conn{}, nil },
		})
		if err != nil {
			t.Fatalf("serverHandShake: %v", err)
		}
		if calls != 1 {
			t.Errorf("newResponseWriter called %d times, want 1", calls)
		}
	})

	t.Run("calls UpgradeForWebsocket with the request's key when valid", func(t *testing.T) {
		req := validClientHandShakeRequest(t)
		fw := &fakeResponseWriter{}
		_, _, err := serverHandShake(newFakeConn(nil), nil, nil, req, ServerHandShakeConfig{}, serverHandShakeDI{
			validateHandShakeRequest: func(*http.Request) error { return nil },
			newResponseWriter:        func() ServerHandShakeRespWriter { return fw },
			sendHandShakeResponse:    func(io.Writer, *http.Response) error { return nil },
			newConn:                  func(net.Conn, *bufio.Reader, *bufio.Writer, bool) (*Conn, error) { return &Conn{}, nil },
		})
		if err != nil {
			t.Fatalf("serverHandShake: %v", err)
		}
		if !fw.upgradeCalled {
			t.Error("UpgradeForWebsocket was not called")
		}
		if fw.declineCalled {
			t.Error("DeclineByError must not be called for a valid request")
		}
		if fw.upgradeKey != req.Header.Get("Sec-WebSocket-Key") {
			t.Errorf("upgradeKey = %q, want %q", fw.upgradeKey, req.Header.Get("Sec-WebSocket-Key"))
		}
	})

	t.Run("calls DeclineByError with the validation error when invalid", func(t *testing.T) {
		wantErr := errors.New("nope")
		fw := &fakeResponseWriter{}
		_, _, err := serverHandShake(newFakeConn(nil), nil, nil, validClientHandShakeRequest(t), ServerHandShakeConfig{}, serverHandShakeDI{
			validateHandShakeRequest: func(*http.Request) error { return wantErr },
			newResponseWriter:        func() ServerHandShakeRespWriter { return fw },
			sendHandShakeResponse:    func(io.Writer, *http.Response) error { return nil },
			newConn:                  func(net.Conn, *bufio.Reader, *bufio.Writer, bool) (*Conn, error) { return &Conn{}, nil },
		})
		if !errors.Is(err, wantErr) {
			t.Errorf("err = %v, want %v", err, wantErr)
		}
		if !fw.declineCalled {
			t.Error("DeclineByError was not called")
		}
		if fw.upgradeCalled {
			t.Error("UpgradeForWebsocket must not be called for an invalid request")
		}
		if !errors.Is(fw.declinedWith, wantErr) {
			t.Errorf("declinedWith = %v, want %v", fw.declinedWith, wantErr)
		}
	})

	t.Run("returns and sends exactly what GenerateResponse produced", func(t *testing.T) {
		want := &http.Response{StatusCode: 999}
		fw := &fakeResponseWriter{response: want}
		var gotRes *http.Response
		_, res, err := serverHandShake(newFakeConn(nil), nil, nil, validClientHandShakeRequest(t), ServerHandShakeConfig{}, serverHandShakeDI{
			validateHandShakeRequest: func(*http.Request) error { return nil },
			newResponseWriter:        func() ServerHandShakeRespWriter { return fw },
			sendHandShakeResponse: func(_ io.Writer, r *http.Response) error {
				gotRes = r
				return nil
			},
			newConn: func(net.Conn, *bufio.Reader, *bufio.Writer, bool) (*Conn, error) { return &Conn{}, nil },
		})
		if err != nil {
			t.Fatalf("serverHandShake: %v", err)
		}
		if res != want {
			t.Error("serverHandShake did not return GenerateResponse's result")
		}
		if gotRes != want {
			t.Error("sendHandShakeResponse did not receive GenerateResponse's result")
		}
	})

	t.Run("sets Subprotocol on an upgrade", func(t *testing.T) {
		request := validClientHandShakeRequest(t)
		request.Header.Set("Sec-WebSocket-Protocol", "v1, v2")
		respWriter := &fakeResponseWriter{response: &http.Response{Header: http.Header{}}}
		var sentSubprotocol []string // what the header held when the resp went out
		_, _, err := serverHandShake(newFakeConn(nil), nil, nil, request, ServerHandShakeConfig{Subprotocol: "v2"}, serverHandShakeDI{
			validateHandShakeRequest: func(*http.Request) error { return nil },
			newResponseWriter:        func() ServerHandShakeRespWriter { return respWriter },
			sendHandShakeResponse: func(_ io.Writer, givenResp *http.Response) error {
				sentSubprotocol = slices.Clone(givenResp.Header.Values("Sec-WebSocket-Protocol"))
				return nil
			},
			newConn: func(net.Conn, *bufio.Reader, *bufio.Writer, bool) (*Conn, error) { return &Conn{}, nil },
		})
		if err != nil {
			t.Fatalf("serverHandShake: %v", err)
		}
		if len(sentSubprotocol) != 1 || sentSubprotocol[0] != "v2" {
			t.Errorf("Sec-WebSocket-Protocol sent = %q, want [\"v2\"]", sentSubprotocol)
		}
	})

	t.Run("sends no subprotocol when Subprotocol is empty", func(t *testing.T) {
		request := validClientHandShakeRequest(t)
		request.Header.Set("Sec-WebSocket-Protocol", "v1") // offered, but not picked
		respWriter := &fakeResponseWriter{response: &http.Response{Header: http.Header{}}}
		_, resp, err := serverHandShake(newFakeConn(nil), nil, nil, request, ServerHandShakeConfig{}, serverHandShakeDI{
			validateHandShakeRequest: func(*http.Request) error { return nil },
			newResponseWriter:        func() ServerHandShakeRespWriter { return respWriter },
			sendHandShakeResponse:    func(io.Writer, *http.Response) error { return nil },
			newConn:                  func(net.Conn, *bufio.Reader, *bufio.Writer, bool) (*Conn, error) { return &Conn{}, nil },
		})
		if err != nil {
			t.Fatalf("serverHandShake: %v", err)
		}
		if _, ok := resp.Header["Sec-Websocket-Protocol"]; ok {
			t.Errorf("Sec-WebSocket-Protocol = %q, want no header", resp.Header.Values("Sec-WebSocket-Protocol"))
		}
	})

	t.Run("sends no Subprotocol the client did not offer and still upgrades", func(t *testing.T) {
		request := validClientHandShakeRequest(t)
		request.Header.Set("Sec-WebSocket-Protocol", "v1")
		respWriter := &fakeResponseWriter{response: &http.Response{Header: http.Header{}}}
		conn, resp, err := serverHandShake(newFakeConn(nil), nil, nil, request, ServerHandShakeConfig{Subprotocol: "v2"}, serverHandShakeDI{
			validateHandShakeRequest: func(*http.Request) error { return nil },
			newResponseWriter:        func() ServerHandShakeRespWriter { return respWriter },
			sendHandShakeResponse:    func(io.Writer, *http.Response) error { return nil },
			newConn:                  func(net.Conn, *bufio.Reader, *bufio.Writer, bool) (*Conn, error) { return &Conn{}, nil },
		})
		if err != nil {
			t.Fatalf("serverHandShake: %v", err)
		}
		if _, ok := resp.Header["Sec-Websocket-Protocol"]; ok {
			t.Errorf("Sec-WebSocket-Protocol = %q, want no header", resp.Header.Values("Sec-WebSocket-Protocol"))
		}
		if !respWriter.upgradeCalled || conn == nil {
			t.Error("expected the upgrade to go ahead without a subprotocol")
		}
	})

	t.Run("sends no subprotocol on a decline", func(t *testing.T) {
		request := validClientHandShakeRequest(t)
		request.Header.Set("Sec-WebSocket-Protocol", "v2")
		respWriter := &fakeResponseWriter{response: &http.Response{Header: http.Header{}}}
		_, resp, _ := serverHandShake(newFakeConn(nil), nil, nil, request, ServerHandShakeConfig{Subprotocol: "v2"}, serverHandShakeDI{
			validateHandShakeRequest: func(*http.Request) error { return errors.New("invalid") },
			newResponseWriter:        func() ServerHandShakeRespWriter { return respWriter },
			sendHandShakeResponse:    func(io.Writer, *http.Response) error { return nil },
			newConn:                  func(net.Conn, *bufio.Reader, *bufio.Writer, bool) (*Conn, error) { return &Conn{}, nil },
		})
		if _, ok := resp.Header["Sec-Websocket-Protocol"]; ok {
			t.Errorf("Sec-WebSocket-Protocol = %q, want no header", resp.Header.Values("Sec-WebSocket-Protocol"))
		}
	})

	t.Run("sends the response on the net.Conn, not the bufio.Writer", func(t *testing.T) {
		netConn := newFakeConn(nil)
		var gotWriter io.Writer
		_, _, err := serverHandShake(netConn, nil, bufio.NewWriter(netConn), validClientHandShakeRequest(t), ServerHandShakeConfig{}, serverHandShakeDI{
			validateHandShakeRequest: func(*http.Request) error { return nil },
			newResponseWriter:        func() ServerHandShakeRespWriter { return &fakeResponseWriter{} },
			sendHandShakeResponse: func(givenWriter io.Writer, _ *http.Response) error {
				gotWriter = givenWriter
				return nil
			},
			newConn: func(net.Conn, *bufio.Reader, *bufio.Writer, bool) (*Conn, error) { return &Conn{}, nil },
		})
		if err != nil {
			t.Fatalf("serverHandShake: %v", err)
		}
		if gotWriter != io.Writer(netConn) {
			t.Errorf("sendHandShakeResponse wrote to %v, want the net.Conn", gotWriter)
		}
	})

	t.Run("calls newConn after an upgrade and returns its Conn", func(t *testing.T) {
		netConn := newFakeConn(nil)
		reader := bufio.NewReader(netConn)
		writer := bufio.NewWriter(netConn)
		want := &Conn{}
		var gotNetConn net.Conn
		var gotReader *bufio.Reader
		var gotWriter *bufio.Writer
		gotMaskSendFrame := true
		conn, _, err := serverHandShake(netConn, reader, writer, validClientHandShakeRequest(t), ServerHandShakeConfig{}, serverHandShakeDI{
			validateHandShakeRequest: func(*http.Request) error { return nil },
			newResponseWriter:        func() ServerHandShakeRespWriter { return &fakeResponseWriter{} },
			sendHandShakeResponse:    func(io.Writer, *http.Response) error { return nil },
			newConn: func(givenNetConn net.Conn, givenReader *bufio.Reader, givenWriter *bufio.Writer, maskSendFrame bool) (*Conn, error) {
				gotNetConn, gotReader, gotWriter, gotMaskSendFrame = givenNetConn, givenReader, givenWriter, maskSendFrame
				return want, nil
			},
		})
		if err != nil {
			t.Fatalf("serverHandShake: %v", err)
		}
		if conn != want {
			t.Error("serverHandShake did not return newConn's Conn")
		}
		if gotNetConn != net.Conn(netConn) || gotReader != reader || gotWriter != writer || gotMaskSendFrame {
			t.Errorf("newConn(%v, %v, %v, %v), want (the net.Conn, the bufio.Reader, the bufio.Writer, false): a server must not mask what it sends", gotNetConn, gotReader, gotWriter, gotMaskSendFrame)
		}
	})

	t.Run("does not call newConn on a decline", func(t *testing.T) {
		conn, _, _ := serverHandShake(newFakeConn(nil), nil, nil, validClientHandShakeRequest(t), ServerHandShakeConfig{}, serverHandShakeDI{
			validateHandShakeRequest: func(*http.Request) error { return errors.New("invalid") },
			newResponseWriter:        func() ServerHandShakeRespWriter { return &fakeResponseWriter{} },
			sendHandShakeResponse:    func(io.Writer, *http.Response) error { return nil },
			newConn: func(net.Conn, *bufio.Reader, *bufio.Writer, bool) (*Conn, error) {
				t.Fatal("must not run on a decline")
				return nil, nil
			},
		})
		if conn != nil {
			t.Error("expected no Conn on a decline")
		}
	})

	t.Run("does not call newConn when the send fails", func(t *testing.T) {
		wantErr := errors.New("broken pipe")
		conn, _, err := serverHandShake(newFakeConn(nil), nil, nil, validClientHandShakeRequest(t), ServerHandShakeConfig{}, serverHandShakeDI{
			validateHandShakeRequest: func(*http.Request) error { return nil },
			newResponseWriter:        func() ServerHandShakeRespWriter { return &fakeResponseWriter{} },
			sendHandShakeResponse:    func(io.Writer, *http.Response) error { return wantErr },
			newConn: func(net.Conn, *bufio.Reader, *bufio.Writer, bool) (*Conn, error) {
				t.Fatal("must not run when the send fails")
				return nil, nil
			},
		})
		if !errors.Is(err, wantErr) {
			t.Errorf("err = %v, want %v", err, wantErr)
		}
		if conn != nil {
			t.Error("expected no Conn when the send fails")
		}
	})

	t.Run("returns newConn's error with the response and no Conn", func(t *testing.T) {
		wantErr := errors.New("socket gone")
		wantResp := &http.Response{StatusCode: http.StatusSwitchingProtocols}
		conn, resp, err := serverHandShake(newFakeConn(nil), nil, nil, validClientHandShakeRequest(t), ServerHandShakeConfig{}, serverHandShakeDI{
			validateHandShakeRequest: func(*http.Request) error { return nil },
			newResponseWriter:        func() ServerHandShakeRespWriter { return &fakeResponseWriter{response: wantResp} },
			sendHandShakeResponse:    func(io.Writer, *http.Response) error { return nil },
			newConn:                  func(net.Conn, *bufio.Reader, *bufio.Writer, bool) (*Conn, error) { return nil, wantErr },
		})
		if !errors.Is(err, wantErr) {
			t.Errorf("err = %v, want %v", err, wantErr)
		}
		if resp != wantResp {
			t.Error("expected the upgrade response that went out")
		}
		if conn != nil {
			t.Error("expected no Conn when newConn fails")
		}
	})
}

func TestRequestSubprotocols(t *testing.T) {
	tests := []struct {
		name  string
		lines []string // one Sec-WebSocket-Protocol line each
		want  []string
	}{
		{name: "a list on one line, in the client's order", lines: []string{"v2, v1"}, want: []string{"v2", "v1"}},
		{name: "a list split across two lines", lines: []string{"v2", "v1"}, want: []string{"v2", "v1"}},
		{name: "spaces around a token are trimmed", lines: []string{" v2 ,  v1 "}, want: []string{"v2", "v1"}},
		{name: "empty tokens are skipped", lines: []string{"v2,, v1,", " "}, want: []string{"v2", "v1"}},
		{name: "no header", lines: nil, want: nil},
	}
	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			request := validClientHandShakeRequest(t)
			for _, line := range testCase.lines {
				request.Header.Add("Sec-WebSocket-Protocol", line)
			}
			if got := RequestSubprotocols(request); !slices.Equal(got, testCase.want) {
				t.Errorf("RequestSubprotocols = %q, want %q", got, testCase.want)
			}
		})
	}
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

	// 4.2.1 asks for the token, not the whole field: a client behind a proxy
	// sends "keep-alive, Upgrade" and is conforming.
	t.Run("Connection and Upgrade are read as token lists", func(t *testing.T) {
		lists := []string{"keep-alive, Upgrade", "Upgrade, keep-alive", "Keep-Alive,upgrade"}
		for _, list := range lists {
			request := validClientHandShakeRequest(t)
			request.Header.Set("Connection", list)
			if err := ValidateHandShakeRequest(request); err != nil {
				t.Errorf("ValidateHandShakeRequest with Connection %q: %v", list, err)
			}
		}
	})

	t.Run("a field split across two lines", func(t *testing.T) {
		request := validClientHandShakeRequest(t)
		request.Header.Del("Connection")
		request.Header.Add("Connection", "keep-alive")
		request.Header.Add("Connection", "Upgrade")
		if err := ValidateHandShakeRequest(request); err != nil {
			t.Errorf("ValidateHandShakeRequest: %v", err)
		}
	})

	tests := []struct {
		name    string
		mutate  func(*http.Request)
		wantErr error
	}{
		{
			name:    "non GET method",
			mutate:  func(r *http.Request) { r.Method = "POST" },
			wantErr: ErrHttpMethodNotAllowed,
		},
		{
			name:    "wrong protocol",
			mutate:  func(r *http.Request) { r.Proto = "HTTP/2.0" },
			wantErr: ErrHttpProtocolOrVersionNotAllowed,
		},
		{
			name:    "missing websocket key",
			mutate:  func(r *http.Request) { r.Header.Del("Sec-WebSocket-Key") },
			wantErr: ErrHttpSecWebSocketKeyHeaderNotSet,
		},
		{
			name:    "websocket key not base64",
			mutate:  func(r *http.Request) { r.Header.Set("Sec-WebSocket-Key", "not base64!") },
			wantErr: ErrHttpSecWebSocketKeyInvalid,
		},
		{
			name:    "websocket key decodes to 15 bytes",
			mutate:  func(r *http.Request) { r.Header.Set("Sec-WebSocket-Key", "AAAAAAAAAAAAAAAAAAAA") },
			wantErr: ErrHttpSecWebSocketKeyInvalid,
		},
		// The right 16 bytes with the trailing "==" dropped. Standard base64
		// requires that padding.
		{
			name:    "websocket key without padding",
			mutate:  func(r *http.Request) { r.Header.Set("Sec-WebSocket-Key", "dGhlIHNhbXBsZSBub25jZQ") },
			wantErr: ErrHttpSecWebSocketKeyInvalid,
		},
		{
			name:    "connection header not upgrade",
			mutate:  func(r *http.Request) { r.Header.Set("Connection", "keep-alive") },
			wantErr: ErrHttpConnectionHeaderNotUpgrade,
		},
		// A token list is matched token by token, so a value merely containing
		// "upgrade" is still refused.
		{
			name:    "connection header only contains the token as a substring",
			mutate:  func(r *http.Request) { r.Header.Set("Connection", "keep-alive, no-upgrade") },
			wantErr: ErrHttpConnectionHeaderNotUpgrade,
		},
		{
			name:    "upgrade header not websocket",
			mutate:  func(r *http.Request) { r.Header.Set("Upgrade", "h2c") },
			wantErr: ErrHttpUpgradeHeaderNotWebsocket,
		},
		{
			name:    "upgrade header only contains the token as a substring",
			mutate:  func(r *http.Request) { r.Header.Set("Upgrade", "websocket2") },
			wantErr: ErrHttpUpgradeHeaderNotWebsocket,
		},
		// 4.2.1 requires the header and fixes it at 13. A client on an older
		// draft sends 8 or 7, and gets told which version to retry with.
		{
			name:    "missing websocket version",
			mutate:  func(r *http.Request) { r.Header.Del("Sec-WebSocket-Version") },
			wantErr: ErrHttpSecWebSocketVersionNotSupported,
		},
		{
			name:    "an older draft version",
			mutate:  func(r *http.Request) { r.Header.Set("Sec-WebSocket-Version", "8") },
			wantErr: ErrHttpSecWebSocketVersionNotSupported,
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
			if !errors.Is(got, testCase.wantErr) {
				t.Errorf("err = %v, want it to wrap %v", got, testCase.wantErr)
			}
			// Wrapping has to add context, not just relay the sentinel.
			if got.Error() == testCase.wantErr.Error() {
				t.Errorf("err = %q, want context around the sentinel", got.Error())
			}
		})
	}

	// Method is checked before the headers.
	t.Run("reports the first failure it finds", func(t *testing.T) {
		request := httptestRequest(t)
		request.Method = "DELETE"
		if got := ValidateHandShakeRequest(request); !errors.Is(got, ErrHttpMethodNotAllowed) {
			t.Errorf("err = %v, want the method error first", got)
		}
	})
}

func TestSendHandShakeResponse(t *testing.T) {
	t.Run("writes the plain http message", func(t *testing.T) {
		w := &fakeIOWriter{}
		err := sendHandShakeResponse(w, &http.Response{StatusCode: 101}, sendHandShakeResponseDI{
			responseToPlainHTTPMsg: func(*http.Response) (string, error) {
				return "HTTP/1.1 101 Switching Protocols\r\n\r\n", nil
			},
		})
		if err != nil {
			t.Fatalf("sendHandShakeResponse: %v", err)
		}
		if w.String() != "HTTP/1.1 101 Switching Protocols\r\n\r\n" {
			t.Errorf("written = %q", w.String())
		}
	})

	t.Run("rejects a nil response", func(t *testing.T) {
		err := sendHandShakeResponse(&fakeIOWriter{}, nil, sendHandShakeResponseDI{
			responseToPlainHTTPMsg: func(*http.Response) (string, error) {
				t.Fatal("must not run for a nil response")
				return "", nil
			},
		})
		if !errors.Is(err, ErrHandshakeResponseNil) {
			t.Errorf("err = %v, want %v", err, ErrHandshakeResponseNil)
		}
	})

	t.Run("propagates a serialisation error", func(t *testing.T) {
		want := errors.New("cannot serialise")
		err := sendHandShakeResponse(&fakeIOWriter{}, &http.Response{}, sendHandShakeResponseDI{
			responseToPlainHTTPMsg: func(*http.Response) (string, error) { return "", want },
		})
		if !errors.Is(err, want) {
			t.Errorf("err = %v, want %v", err, want)
		}
	})

	t.Run("propagates a write error", func(t *testing.T) {
		want := errors.New("broken pipe")
		w := &fakeIOWriter{err: want}
		err := sendHandShakeResponse(w, &http.Response{}, sendHandShakeResponseDI{
			responseToPlainHTTPMsg: func(*http.Response) (string, error) { return "x", nil },
		})
		if !errors.Is(err, want) {
			t.Errorf("err = %v, want %v", err, want)
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
