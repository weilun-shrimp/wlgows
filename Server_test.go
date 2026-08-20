package wlgows

import (
	"errors"
	"net"
	"testing"
)

func TestRun(t *testing.T) {
	t.Run("resolves, listens, and builds the server", func(t *testing.T) {
		wantAddr := &net.TCPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 8001}
		wantListener := &net.TCPListener{}
		var gotNetwork, gotService string

		server, err := run("127.0.0.1:8001", runDI{
			netResolveTCPAddr: func(network, address string) (*net.TCPAddr, error) {
				gotNetwork, gotService = network, address
				return wantAddr, nil
			},
			netListenTCP: func(network string, laddr *net.TCPAddr) (*net.TCPListener, error) {
				if network != "tcp" {
					t.Errorf("listen network = %q, want tcp", network)
				}
				if laddr != wantAddr {
					t.Error("the resolved address should be handed to netListenTCP")
				}
				return wantListener, nil
			},
			newServer: newServer,
		})
		if err != nil {
			t.Fatalf("run: %v", err)
		}
		if gotNetwork != "tcp4" || gotService != "127.0.0.1:8001" {
			t.Errorf("resolve(%q, %q), want (tcp4, 127.0.0.1:8001)", gotNetwork, gotService)
		}
		if server.TCPAddr != wantAddr || server.TCPListener != wantListener {
			t.Error("the server should carry the resolved address and listener")
		}
	})

	t.Run("propagates a resolve error", func(t *testing.T) {
		want := errors.New("cannot resolve")
		_, err := run("nonsense", runDI{
			netResolveTCPAddr: func(string, string) (*net.TCPAddr, error) { return nil, want },
			netListenTCP: func(string, *net.TCPAddr) (*net.TCPListener, error) {
				t.Fatal("must not listen when resolution failed")
				return nil, nil
			},
			newServer: newServer,
		})
		if !errors.Is(err, want) {
			t.Errorf("err = %v, want %v", err, want)
		}
	})

	t.Run("propagates a listen error", func(t *testing.T) {
		want := errors.New("address in use")
		_, err := run("127.0.0.1:8001", runDI{
			netResolveTCPAddr: func(string, string) (*net.TCPAddr, error) {
				return &net.TCPAddr{}, nil
			},
			netListenTCP: func(string, *net.TCPAddr) (*net.TCPListener, error) { return nil, want },
			newServer:    newServer,
		})
		if !errors.Is(err, want) {
			t.Errorf("err = %v, want %v", err, want)
		}
	})
}

func TestNewServer(t *testing.T) {
	addr := &net.TCPAddr{Port: 8001}
	listener := &net.TCPListener{}

	server := newServer(addr, listener)

	if server.TCPAddr != addr || server.TCPListener != listener {
		t.Error("fields were not set")
	}
}

func TestServerAccept(t *testing.T) {
	listener, err := net.ListenTCP("tcp", &net.TCPAddr{})
	if err != nil {
		t.Fatalf("net.ListenTCP: %v", err)
	}
	server := newServer(listener.Addr().(*net.TCPAddr), listener)
	defer server.Close()

	go func() {
		c, err := net.Dial("tcp", server.TCPListener.Addr().String())
		if err == nil {
			defer c.Close()
		}
	}()

	conn, err := server.Accept()
	if err != nil {
		t.Fatalf("Accept: %v", err)
	}
	defer conn.Close()

	if conn == nil {
		t.Error("Accept should return the accepted net.Conn")
	}
}

func TestServerAcceptAfterClose(t *testing.T) {
	server, err := Run("127.0.0.1:0")
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	server.Close()

	if _, err := server.Accept(); err == nil {
		t.Error("Accept should fail once the listener is closed")
	}
}

func TestRunRejectsABadService(t *testing.T) {
	if _, err := Run("not-a-valid-address"); err == nil {
		t.Error("Run should reject an unresolvable service string")
	}
}

func TestRunBindsAndReportsItsAddress(t *testing.T) {
	server, err := Run("127.0.0.1:0")
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	defer server.Close()

	if server.TCPAddr == nil || server.TCPListener == nil {
		t.Fatal("Run should populate both the address and the listener")
	}
	// Port 0 asks the OS to choose, so the listener knows the real port.
	if addr, ok := server.TCPListener.Addr().(*net.TCPAddr); !ok || addr.Port == 0 {
		t.Errorf("listener address = %v, want a bound port", server.TCPListener.Addr())
	}
}
