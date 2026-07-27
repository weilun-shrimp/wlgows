package wlgows

import (
	"crypto/tls"
	"errors"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"testing"
)

// baseDialDI wires everything to a working default; each test overrides one field.
func baseDialDI(conn net.Conn) dialDI {
	return dialDI{
		httpNewRequest:       http.NewRequest,
		validateWebsocketUrl: ValidateWebsocketUrl,
		netDial:              func(string, string) (net.Conn, error) { return conn, nil },
		tlsDial:              func(string, string, *tls.Config) (*tls.Conn, error) { return nil, nil },
		newClientConn:        NewClientConn,
	}
}

func TestDial(t *testing.T) {
	t.Run("ws and http schemes go through netDial", func(t *testing.T) {
		for _, scheme := range []string{"ws", "http"} {
			t.Run(scheme, func(t *testing.T) {
				fc := newFakeConn(nil)
				di := baseDialDI(fc)
				var gotNetwork, gotAddress string
				di.netDial = func(network, address string) (net.Conn, error) {
					gotNetwork, gotAddress = network, address
					return fc, nil
				}
				di.tlsDial = func(string, string, *tls.Config) (*tls.Conn, error) {
					t.Fatal("tlsDial must not run for a plaintext scheme")
					return nil, nil
				}

				cc, err := dial(scheme+"://localhost:8001/chat", nil, di)
				if err != nil {
					t.Fatalf("dial: %v", err)
				}
				if gotNetwork != "tcp" || gotAddress != "localhost:8001" {
					t.Errorf("netDial(%q, %q), want (tcp, localhost:8001)", gotNetwork, gotAddress)
				}
				if cc.Conn.Conn != net.Conn(fc) {
					t.Error("the dialled conn should be embedded in the ClientConn")
				}
				if cc.ClientRequest == nil || cc.ClientRequest.URL.Scheme != scheme {
					t.Error("the built request should be stored on the ClientConn")
				}
				if cc.di.upgradeRequest == nil {
					t.Error("dial must build the ClientConn through its constructor")
				}
			})
		}
	})

	t.Run("wss and https schemes go through tlsDial", func(t *testing.T) {
		for _, scheme := range []string{"wss", "https"} {
			t.Run(scheme, func(t *testing.T) {
				di := baseDialDI(newFakeConn(nil))
				called := false
				var gotConfig *tls.Config
				want := &tls.Config{ServerName: "example.com"}
				di.tlsDial = func(_, _ string, cfg *tls.Config) (*tls.Conn, error) {
					called = true
					gotConfig = cfg
					return nil, nil
				}
				di.netDial = func(string, string) (net.Conn, error) {
					t.Fatal("netDial must not run for a tls scheme")
					return nil, nil
				}

				if _, err := dial(scheme+"://localhost:8001/", want, di); err != nil {
					t.Fatalf("dial: %v", err)
				}
				if !called {
					t.Error("tlsDial was not used")
				}
				if gotConfig != want {
					t.Error("the caller's tls.Config should be passed straight through")
				}
			})
		}
	})

	t.Run("propagates a request build error", func(t *testing.T) {
		wantErr := errors.New("bad request")
		di := baseDialDI(newFakeConn(nil))
		di.httpNewRequest = func(string, string, io.Reader) (*http.Request, error) {
			return nil, wantErr
		}
		if _, err := dial("ws://localhost:8001", nil, di); !errors.Is(err, wantErr) {
			t.Errorf("err = %v, want %v", err, wantErr)
		}
	})

	t.Run("propagates a url validation error", func(t *testing.T) {
		wantErr := errors.New("bad url")
		di := baseDialDI(newFakeConn(nil))
		di.validateWebsocketUrl = func(*url.URL) error { return wantErr }
		di.netDial = func(string, string) (net.Conn, error) {
			t.Fatal("must not dial when validation failed")
			return nil, nil
		}
		if _, err := dial("ws://localhost:8001", nil, di); !errors.Is(err, wantErr) {
			t.Errorf("err = %v, want %v", err, wantErr)
		}
	})

	t.Run("propagates a netDial error", func(t *testing.T) {
		wantErr := errors.New("connection refused")
		di := baseDialDI(nil)
		di.netDial = func(string, string) (net.Conn, error) { return nil, wantErr }
		if _, err := dial("ws://localhost:8001", nil, di); !errors.Is(err, wantErr) {
			t.Errorf("err = %v, want %v", err, wantErr)
		}
	})

	t.Run("propagates a tlsDial error", func(t *testing.T) {
		wantErr := errors.New("handshake failure")
		di := baseDialDI(nil)
		di.tlsDial = func(string, string, *tls.Config) (*tls.Conn, error) { return nil, wantErr }
		if _, err := dial("wss://localhost:8001", nil, di); !errors.Is(err, wantErr) {
			t.Errorf("err = %v, want %v", err, wantErr)
		}
	})

	/*
		The scheme switch has no default branch, so a scheme that slips past
		validation leaves both conn and err nil and yields a ClientConn wrapping
		a nil socket. Unreachable in production because the real
		ValidateWebsocketUrl gates it — only a permissive fake exposes it.
	*/
	t.Run("an unknown scheme yields a connection with no socket", func(t *testing.T) {
		di := baseDialDI(nil)
		di.validateWebsocketUrl = func(*url.URL) error { return nil }
		di.netDial = func(string, string) (net.Conn, error) {
			t.Fatal("no dial should happen for an unknown scheme")
			return nil, nil
		}
		cc, err := dial("ftp://localhost:8001", nil, di)
		if err != nil {
			t.Fatalf("dial: %v", err)
		}
		if cc.Conn.Conn != nil {
			t.Error("expected a nil socket; the switch has no default branch")
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
		got := ValidateWebsocketUrl(u)
		if got == nil {
			t.Fatal("expected an error")
		}
		if !strings.Contains(got.Error(), "scheme is not valid") {
			t.Errorf("err = %q", got.Error())
		}
	})

	t.Run("rejects an empty host", func(t *testing.T) {
		u, err := url.Parse("ws:///chat")
		if err != nil {
			t.Fatalf("url.Parse: %v", err)
		}
		got := ValidateWebsocketUrl(u)
		if got == nil {
			t.Fatal("expected an error")
		}
		if !strings.Contains(got.Error(), "host is empty") {
			t.Errorf("err = %q", got.Error())
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

func TestDialRealWiringRejectsABadUrl(t *testing.T) {
	if _, err := Dial("ftp://localhost:8001", nil); err == nil {
		t.Error("Dial should reject a non websocket scheme")
	}
}
