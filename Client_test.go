package wlgows

import (
	"context"
	"crypto/tls"
	"errors"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"
)

func TestDialRealWiringRejectsABadUrl(t *testing.T) {
	if _, _, err := Dial("ftp://localhost:8001", nil); err == nil {
		t.Error("Dial should reject a non websocket scheme")
	}
}

func TestDialContext(t *testing.T) {
	// base_di builds requests and validates urls for real, and both dials
	// succeed; each case overrides what it looks at.
	base_di := func() dialContextDI {
		return dialContextDI{
			httpNewRequest:       http.NewRequest,
			validateWebsocketUrl: ValidateWebsocketUrl,
			defaultDialer:        &net.Dialer{},
			dialContext: func(*net.Dialer, context.Context, string, string) (net.Conn, error) {
				return newFakeConn(nil), nil
			},
			tlsDialContext: func(*tls.Dialer, context.Context, string, string) (net.Conn, error) {
				return newFakeConn(nil), nil
			},
		}
	}
	type ctxKey struct{}
	ctx := context.WithValue(context.Background(), ctxKey{}, "caller")

	t.Run("ws and http dial plain tcp with the caller's dialer and ctx", func(t *testing.T) {
		for _, scheme := range []string{"ws", "http"} {
			netConn := newFakeConn(nil)
			dialer := &net.Dialer{Timeout: time.Second}
			var gotDialer *net.Dialer
			var gotCtx context.Context
			var gotNetwork, gotAddress string
			di := base_di()
			di.dialContext = func(d *net.Dialer, c context.Context, network, address string) (net.Conn, error) {
				gotDialer, gotCtx, gotNetwork, gotAddress = d, c, network, address
				return netConn, nil
			}
			di.tlsDialContext = func(*tls.Dialer, context.Context, string, string) (net.Conn, error) {
				t.Errorf("%s: tlsDialContext must not run for a plain scheme", scheme)
				return nil, nil
			}

			conn, req, err := dialContext(ctx, scheme+"://localhost:8001/chat", dialer, nil, di)
			if err != nil {
				t.Fatalf("%s: dialContext: %v", scheme, err)
			}
			if gotDialer != dialer || gotCtx != ctx || gotNetwork != "tcp" || gotAddress != "localhost:8001" {
				t.Errorf("%s: dialContext(%p, %v, %q, %q), want (%p, caller ctx, tcp, localhost:8001)",
					scheme, gotDialer, gotCtx, gotNetwork, gotAddress, dialer)
			}
			if conn != net.Conn(netConn) {
				t.Errorf("%s: should return the dialled conn", scheme)
			}
			if req == nil || req.Method != "GET" || req.URL.String() != scheme+"://localhost:8001/chat" {
				t.Errorf("%s: req %+v, want GET %s://localhost:8001/chat", scheme, req, scheme)
			}
		}
	})

	t.Run("wss and https dial tls over the caller's dialer, config and ctx", func(t *testing.T) {
		for _, scheme := range []string{"wss", "https"} {
			netConn := newFakeConn(nil)
			dialer := &net.Dialer{Timeout: time.Second}
			config := &tls.Config{ServerName: "example.com"}
			var gotDialer *tls.Dialer
			var gotCtx context.Context
			var gotNetwork, gotAddress string
			di := base_di()
			di.tlsDialContext = func(d *tls.Dialer, c context.Context, network, address string) (net.Conn, error) {
				gotDialer, gotCtx, gotNetwork, gotAddress = d, c, network, address
				return netConn, nil
			}
			di.dialContext = func(*net.Dialer, context.Context, string, string) (net.Conn, error) {
				t.Errorf("%s: dialContext must not run for a tls scheme", scheme)
				return nil, nil
			}

			conn, req, err := dialContext(ctx, scheme+"://localhost:8001/chat", dialer, config, di)
			if err != nil {
				t.Fatalf("%s: dialContext: %v", scheme, err)
			}
			if gotDialer == nil || gotDialer.NetDialer != dialer || gotDialer.Config != config ||
				gotCtx != ctx || gotNetwork != "tcp" || gotAddress != "localhost:8001" {
				t.Errorf("%s: tlsDialContext(%+v, %v, %q, %q), want ({NetDialer: %p, Config: %p}, caller ctx, tcp, localhost:8001)",
					scheme, gotDialer, gotCtx, gotNetwork, gotAddress, dialer, config)
			}
			if conn != net.Conn(netConn) {
				t.Errorf("%s: should return the dialled conn", scheme)
			}
			if req == nil || req.Method != "GET" || req.URL.String() != scheme+"://localhost:8001/chat" {
				t.Errorf("%s: req %+v, want GET %s://localhost:8001/chat", scheme, req, scheme)
			}
		}
	})

	t.Run("a nil dialer is replaced by defaultDialer", func(t *testing.T) {
		defaultDialer := &net.Dialer{}
		var gotPlain, gotTls *net.Dialer
		di := base_di()
		di.defaultDialer = defaultDialer
		di.dialContext = func(d *net.Dialer, _ context.Context, _, _ string) (net.Conn, error) {
			gotPlain = d
			return newFakeConn(nil), nil
		}
		di.tlsDialContext = func(d *tls.Dialer, _ context.Context, _, _ string) (net.Conn, error) {
			gotTls = d.NetDialer
			return newFakeConn(nil), nil
		}

		if _, _, err := dialContext(ctx, "ws://localhost:8001", nil, nil, di); err != nil {
			t.Fatalf("dialContext ws: %v", err)
		}
		if _, _, err := dialContext(ctx, "wss://localhost:8001", nil, nil, di); err != nil {
			t.Fatalf("dialContext wss: %v", err)
		}
		if gotPlain != defaultDialer || gotTls != defaultDialer {
			t.Errorf("ws dialer %p, wss NetDialer %p, want defaultDialer %p", gotPlain, gotTls, defaultDialer)
		}
	})

	// RFC 6455 3: ws defaults to port 80 and wss to 443.
	t.Run("a url without a port dials the scheme's default port", func(t *testing.T) {
		cases := []struct {
			url         string
			wantAddress string
			wantHost    string
		}{
			{url: "ws://example.com/chat", wantAddress: "example.com:80", wantHost: "example.com"},
			{url: "http://example.com/chat", wantAddress: "example.com:80", wantHost: "example.com"},
			{url: "wss://example.com/chat", wantAddress: "example.com:443", wantHost: "example.com"},
			{url: "https://example.com/chat", wantAddress: "example.com:443", wantHost: "example.com"},
			{url: "ws://[::1]/chat", wantAddress: "[::1]:80", wantHost: "[::1]"},
			{url: "wss://[::1]/chat", wantAddress: "[::1]:443", wantHost: "[::1]"},
			{url: "ws://example.com:/chat", wantAddress: "example.com:80", wantHost: "example.com"},
		}
		for _, testCase := range cases {
			var gotAddress string
			di := base_di()
			di.dialContext = func(_ *net.Dialer, _ context.Context, _, address string) (net.Conn, error) {
				gotAddress = address
				return newFakeConn(nil), nil
			}
			di.tlsDialContext = func(_ *tls.Dialer, _ context.Context, _, address string) (net.Conn, error) {
				gotAddress = address
				return newFakeConn(nil), nil
			}

			_, request, err := dialContext(ctx, testCase.url, nil, nil, di)
			if err != nil {
				t.Fatalf("%s: dialContext: %v", testCase.url, err)
			}
			if gotAddress != testCase.wantAddress {
				t.Errorf("%s: dialled %q, want %q", testCase.url, gotAddress, testCase.wantAddress)
			}
			// The request keeps the url's own host, so the Host header leaves the default port out.
			if request.URL.Host != testCase.wantHost {
				t.Errorf("%s: request url host %q, want %q", testCase.url, request.URL.Host, testCase.wantHost)
			}
		}
	})

	t.Run("returns a request build error without dialing", func(t *testing.T) {
		wantErr := errors.New("bad request")
		di := base_di()
		di.httpNewRequest = func(string, string, io.Reader) (*http.Request, error) { return nil, wantErr }
		di.dialContext = func(*net.Dialer, context.Context, string, string) (net.Conn, error) {
			t.Error("dialled after the request failed to build")
			return nil, nil
		}

		conn, req, err := dialContext(ctx, "ws://localhost:8001", nil, nil, di)
		if conn != nil || req != nil || !errors.Is(err, wantErr) {
			t.Errorf("got (%v, %v, %v), want (nil, nil, %v)", conn, req, err, wantErr)
		}
	})

	t.Run("returns a url validation error without dialing", func(t *testing.T) {
		wantErr := errors.New("bad url")
		di := base_di()
		di.validateWebsocketUrl = func(*url.URL) error { return wantErr }
		di.dialContext = func(*net.Dialer, context.Context, string, string) (net.Conn, error) {
			t.Error("dialled after validation failed")
			return nil, nil
		}

		conn, req, err := dialContext(ctx, "ws://localhost:8001", nil, nil, di)
		if conn != nil || req != nil || !errors.Is(err, wantErr) {
			t.Errorf("got (%v, %v, %v), want (nil, nil, %v)", conn, req, err, wantErr)
		}
	})

	t.Run("returns a plain dial error", func(t *testing.T) {
		wantErr := errors.New("connection refused")
		di := base_di()
		di.dialContext = func(*net.Dialer, context.Context, string, string) (net.Conn, error) { return nil, wantErr }

		conn, req, err := dialContext(ctx, "ws://localhost:8001", nil, nil, di)
		if conn != nil || req != nil || !errors.Is(err, wantErr) {
			t.Errorf("got (%v, %v, %v), want (nil, nil, %v)", conn, req, err, wantErr)
		}
	})

	t.Run("returns a tls dial error", func(t *testing.T) {
		wantErr := errors.New("handshake failure")
		di := base_di()
		di.tlsDialContext = func(*tls.Dialer, context.Context, string, string) (net.Conn, error) {
			return nil, wantErr
		}

		conn, req, err := dialContext(ctx, "wss://localhost:8001", nil, nil, di)
		if conn != nil || req != nil || !errors.Is(err, wantErr) {
			t.Errorf("got (%v, %v, %v), want (nil, nil, %v)", conn, req, err, wantErr)
		}
	})

	t.Run("returns ErrUrlSchemeInvalid for a scheme validation let through", func(t *testing.T) {
		di := base_di()
		di.validateWebsocketUrl = func(*url.URL) error { return nil }
		di.dialContext = func(*net.Dialer, context.Context, string, string) (net.Conn, error) {
			t.Error("dialled for an unknown scheme")
			return nil, nil
		}
		di.tlsDialContext = func(*tls.Dialer, context.Context, string, string) (net.Conn, error) {
			t.Error("dialled tls for an unknown scheme")
			return nil, nil
		}

		conn, req, err := dialContext(ctx, "ftp://localhost:8001", nil, nil, di)
		if conn != nil || req != nil || !errors.Is(err, ErrUrlSchemeInvalid) {
			t.Errorf("got (%v, %v, %v), want (nil, nil, %v)", conn, req, err, ErrUrlSchemeInvalid)
		}
	})
}

func TestValidateWebsocketUrl(t *testing.T) {
	t.Run("accepts every supported scheme", func(t *testing.T) {
		for _, scheme := range []string{"http", "https", "ws", "wss"} {
			u, err := url.Parse(scheme + "://localhost:8001/chat")
			if err != nil {
				t.Fatalf("url.Parse: %v", err)
			}
			if err := ValidateWebsocketUrl(u); err != nil {
				t.Errorf("scheme %q rejected: %v", scheme, err)
			}
		}
	})

	t.Run("rejects an unsupported scheme", func(t *testing.T) {
		u, err := url.Parse("ftp://localhost:8001")
		if err != nil {
			t.Fatalf("url.Parse: %v", err)
		}
		if got := ValidateWebsocketUrl(u); !errors.Is(got, ErrUrlSchemeInvalid) {
			t.Errorf("err = %v, want %v", got, ErrUrlSchemeInvalid)
		}
	})

	t.Run("rejects an empty host", func(t *testing.T) {
		u, err := url.Parse("ws:///chat")
		if err != nil {
			t.Fatalf("url.Parse: %v", err)
		}
		if got := ValidateWebsocketUrl(u); !errors.Is(got, ErrUrlHostEmpty) {
			t.Errorf("err = %v, want %v", got, ErrUrlHostEmpty)
		}
	})

	t.Run("rejects a fragment", func(t *testing.T) {
		u, err := url.Parse("ws://localhost:8001/chat?room=a#b")
		if err != nil {
			t.Fatalf("url.Parse: %v", err)
		}
		if got := ValidateWebsocketUrl(u); !errors.Is(got, ErrUrlFragmentSet) {
			t.Errorf("err = %v, want %v", got, ErrUrlFragmentSet)
		}
	})

	t.Run("accepts a # escaped as %23", func(t *testing.T) {
		u, err := url.Parse("ws://localhost:8001/chat?room=a%23b")
		if err != nil {
			t.Fatalf("url.Parse: %v", err)
		}
		if err := ValidateWebsocketUrl(u); err != nil {
			t.Errorf("rejected: %v", err)
		}
	})

	t.Run("the error quotes the offending url", func(t *testing.T) {
		u, err := url.Parse("ftp://example.com")
		if err != nil {
			t.Fatalf("url.Parse: %v", err)
		}
		if got := ValidateWebsocketUrl(u); !strings.Contains(got.Error(), "ftp://example.com") {
			t.Errorf("err should quote the url, got %q", got.Error())
		}
	})
}
