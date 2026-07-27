package wlgows

import (
	"bytes"
	"errors"
	"net"
	"net/http"
	"testing"
)

func TestNewConn(t *testing.T) {
	fc := newFakeConn(nil)
	req := httptestRequest(t)
	res := &http.Response{StatusCode: 101}

	c := NewConn(fc, req, res)

	if c.Conn != net.Conn(fc) {
		t.Error("embedded net.Conn was not set")
	}
	if c.ClientRequest != req {
		t.Error("ClientRequest was not set")
	}
	if c.ServerResponse != res {
		t.Error("ServerResponse was not set")
	}
	// Constructors are mandatory precisely because they populate di.
	if c.di.getFrameFromTCPConn == nil || c.di.getMsgFromTCPConn == nil {
		t.Error("NewConn must populate every di field")
	}
}

func TestConnGetNextFrame(t *testing.T) {
	t.Run("delegates to di", func(t *testing.T) {
		want := &Frame{FIN: true, Opcode: 1, PayloadData: []byte("x")}
		c := NewConn(newFakeConn(nil), nil, nil)
		var gotConn net.Conn
		c.di.getFrameFromTCPConn = func(conn net.Conn) (*Frame, error) {
			gotConn = conn
			return want, nil
		}

		got, err := c.GetNextFrame()
		if err != nil {
			t.Fatalf("GetNextFrame: %v", err)
		}
		if got != want {
			t.Error("GetNextFrame did not return the di result")
		}
		if gotConn != c.Conn {
			t.Error("GetNextFrame must pass the embedded conn through")
		}
	})

	t.Run("propagates the error", func(t *testing.T) {
		want := errors.New("read failed")
		c := NewConn(newFakeConn(nil), nil, nil)
		c.di.getFrameFromTCPConn = func(net.Conn) (*Frame, error) { return nil, want }
		if _, err := c.GetNextFrame(); !errors.Is(err, want) {
			t.Errorf("err = %v, want %v", err, want)
		}
	})
}

func TestConnGetNextMsg(t *testing.T) {
	t.Run("delegates to di", func(t *testing.T) {
		c := NewConn(newFakeConn(nil), nil, nil)
		c.di.getMsgFromTCPConn = func(net.Conn) (Msg, error) {
			return Msg{Frames: []*Frame{{PayloadData: []byte("hello")}}}, nil
		}
		got, err := c.GetNextMsg()
		if err != nil {
			t.Fatalf("GetNextMsg: %v", err)
		}
		if got.GetStr() != "hello" {
			t.Errorf("GetStr() = %q", got.GetStr())
		}
	})

	t.Run("propagates the error", func(t *testing.T) {
		want := errors.New("msg failed")
		c := NewConn(newFakeConn(nil), nil, nil)
		c.di.getMsgFromTCPConn = func(net.Conn) (Msg, error) { return Msg{}, want }
		if _, err := c.GetNextMsg(); !errors.Is(err, want) {
			t.Errorf("err = %v, want %v", err, want)
		}
	})
}

func TestConnSendMsg(t *testing.T) {
	t.Run("writes every frame sealed", func(t *testing.T) {
		fc := newFakeConn(nil)
		c := NewConn(fc, nil, nil)
		m := &Msg{Frames: []*Frame{
			{FIN: false, Opcode: 1, PayloadLength: 2, PayloadData: []byte("wl")},
			{FIN: true, Opcode: 0, PayloadLength: 4, PayloadData: []byte("gows")},
		}}

		if err := c.SendMsg(m); err != nil {
			t.Fatalf("SendMsg: %v", err)
		}

		want := append(m.Frames[0].Seal(), m.Frames[1].Seal()...)
		if !bytes.Equal(fc.written(), want) {
			t.Errorf("written = % x, want % x", fc.written(), want)
		}
	})

	t.Run("empty message writes nothing", func(t *testing.T) {
		fc := newFakeConn(nil)
		c := NewConn(fc, nil, nil)
		if err := c.SendMsg(&Msg{}); err != nil {
			t.Fatalf("SendMsg: %v", err)
		}
		if len(fc.written()) != 0 {
			t.Errorf("wrote %d bytes, want 0", len(fc.written()))
		}
	})

	t.Run("stops at the first write error", func(t *testing.T) {
		want := errors.New("broken pipe")
		fc := newFakeConn(nil)
		fc.writeErr = want
		c := NewConn(fc, nil, nil)
		m := &Msg{Frames: []*Frame{
			{FIN: false, PayloadData: []byte("a")},
			{FIN: true, PayloadData: []byte("b")},
		}}
		if err := c.SendMsg(m); !errors.Is(err, want) {
			t.Errorf("err = %v, want %v", err, want)
		}
		if len(fc.written()) != 0 {
			t.Error("nothing should have landed in the buffer")
		}
	})
}

func TestConnClose(t *testing.T) {
	t.Run("closes the socket and marks both messages closed", func(t *testing.T) {
		fc := newFakeConn(nil)
		req := httptestRequest(t)
		res := &http.Response{StatusCode: 101}
		c := NewConn(fc, req, res)

		if err := c.Close(); err != nil {
			t.Fatalf("Close: %v", err)
		}
		if !fc.closed {
			t.Error("underlying conn was not closed")
		}
		if !req.Close {
			t.Error("ClientRequest.Close should be true")
		}
		if !res.Close {
			t.Error("ServerResponse.Close should be true")
		}
	})

	t.Run("tolerates nil request and response", func(t *testing.T) {
		fc := newFakeConn(nil)
		c := NewConn(fc, nil, nil)
		if err := c.Close(); err != nil {
			t.Fatalf("Close: %v", err)
		}
		if !fc.closed {
			t.Error("underlying conn was not closed")
		}
	})

	t.Run("returns early when the socket fails to close", func(t *testing.T) {
		want := errors.New("close failed")
		fc := newFakeConn(nil)
		fc.closeErr = want
		req := httptestRequest(t)
		c := NewConn(fc, req, nil)

		if err := c.Close(); !errors.Is(err, want) {
			t.Fatalf("err = %v, want %v", err, want)
		}
		if req.Close {
			t.Error("ClientRequest.Close must not be set when the socket close failed")
		}
	})
}

// Shared helper: a minimal valid client request.
func httptestRequest(t *testing.T) *http.Request {
	t.Helper()
	req, err := http.NewRequest("GET", "ws://localhost:8001/", nil)
	if err != nil {
		t.Fatalf("http.NewRequest: %v", err)
	}
	return req
}
