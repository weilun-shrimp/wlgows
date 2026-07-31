package wlgows

import (
	"bytes"
	"errors"
	"net"
	"net/http"
	"testing"
)

func TestNewConn(t *testing.T) {
	netConn := newFakeConn(nil)
	request := httptestRequest(t)
	response := &http.Response{StatusCode: 101}

	wsConn := NewConn(netConn, request, response)

	if wsConn.Conn != net.Conn(netConn) {
		t.Error("embedded net.Conn was not set")
	}
	if wsConn.ClientRequest != request {
		t.Error("ClientRequest was not set")
	}
	if wsConn.ServerResponse != response {
		t.Error("ServerResponse was not set")
	}
	// Constructors are mandatory precisely because they populate di.
	if wsConn.di.getFrameFromTCPConn == nil || wsConn.di.getMsgFromTCPConn == nil {
		t.Error("NewConn must populate every di field")
	}
	if wsConn.di.writeLocker == nil || wsConn.di.readLocker == nil {
		t.Error("NewConn must populate both lockers")
	}
}

/*
The lockers guard the socket against concurrent use. These assert the seam is
taken, and how widely — a three frame message reports locks=3 if someone moves
the lock inside SendMsg's loop, where frames of different messages could
interleave again.
*/
func TestConnLocking(t *testing.T) {
	threeFrames := func() *Msg {
		return &Msg{Frames: []*Frame{
			{PayloadLength: 1, PayloadData: []byte("a")},
			{PayloadLength: 1, PayloadData: []byte("b")},
			{FIN: true, PayloadLength: 1, PayloadData: []byte("c")},
		}}
	}

	t.Run("SendMsg locks once around the whole frame loop", func(t *testing.T) {
		locker := &fakeLocker{}
		netConn := newFakeConn(nil)
		wsConn := NewConn(netConn, nil, nil)
		wsConn.di.writeLocker = locker

		// Counting alone cannot tell a lock that spans the writes from one
		// released immediately after being taken, so check the lock is actually
		// held at the moment each frame goes out.
		writes, held := 0, 0
		netConn.onWrite = func() {
			writes++
			if locker.held {
				held++
			}
		}

		if err := wsConn.SendMsg(threeFrames()); err != nil {
			t.Fatalf("SendMsg: %v", err)
		}
		if !locker.ok(1) {
			t.Errorf("locks=%d unlocks=%d misuse=%d held=%v, want 1/1/0/false",
				locker.locks, locker.unlocks, locker.misuse, locker.held)
		}
		if writes != 3 || held != 3 {
			t.Errorf("%d of %d frames written while the lock was held, want 3 of 3", held, writes)
		}
	})

	t.Run("SendMsg unlocks after a write error", func(t *testing.T) {
		locker := &fakeLocker{}
		netConn := newFakeConn(nil)
		netConn.writeErr = errors.New("broken pipe")
		wsConn := NewConn(netConn, nil, nil)
		wsConn.di.writeLocker = locker

		if err := wsConn.SendMsg(threeFrames()); err == nil {
			t.Fatal("SendMsg should have failed")
		}
		if !locker.ok(1) {
			t.Errorf("locks=%d unlocks=%d misuse=%d held=%v — the lock must be released on the error path",
				locker.locks, locker.unlocks, locker.misuse, locker.held)
		}
	})

	t.Run("GetNextFrame locks", func(t *testing.T) {
		locker := &fakeLocker{}
		wsConn := NewConn(newFakeConn(nil), nil, nil)
		wsConn.di.readLocker = locker
		wsConn.di.getFrameFromTCPConn = func(net.Conn) (*Frame, error) {
			if !locker.held {
				t.Error("the frame was read outside the lock")
			}
			return &Frame{}, nil
		}

		if _, err := wsConn.GetNextFrame(); err != nil {
			t.Fatalf("GetNextFrame: %v", err)
		}
		if !locker.ok(1) {
			t.Errorf("locks=%d unlocks=%d misuse=%d held=%v, want 1/1/0/false",
				locker.locks, locker.unlocks, locker.misuse, locker.held)
		}
	})

	t.Run("GetNextMsg locks", func(t *testing.T) {
		locker := &fakeLocker{}
		wsConn := NewConn(newFakeConn(nil), nil, nil)
		wsConn.di.readLocker = locker
		wsConn.di.getMsgFromTCPConn = func(net.Conn) (Msg, error) {
			if !locker.held {
				t.Error("the message was read outside the lock")
			}
			return Msg{}, nil
		}

		if _, err := wsConn.GetNextMsg(); err != nil {
			t.Fatalf("GetNextMsg: %v", err)
		}
		if !locker.ok(1) {
			t.Errorf("locks=%d unlocks=%d misuse=%d held=%v, want 1/1/0/false",
				locker.locks, locker.unlocks, locker.misuse, locker.held)
		}
	})
}

func TestConnGetNextFrame(t *testing.T) {
	t.Run("delegates to di", func(t *testing.T) {
		want := &Frame{FIN: true, Opcode: 1, PayloadData: []byte("x")}
		wsConn := NewConn(newFakeConn(nil), nil, nil)
		var gotConn net.Conn
		wsConn.di.getFrameFromTCPConn = func(conn net.Conn) (*Frame, error) {
			gotConn = conn
			return want, nil
		}

		got, err := wsConn.GetNextFrame()
		if err != nil {
			t.Fatalf("GetNextFrame: %v", err)
		}
		if got != want {
			t.Error("GetNextFrame did not return the di result")
		}
		if gotConn != wsConn.Conn {
			t.Error("GetNextFrame must pass the embedded conn through")
		}
	})

	t.Run("propagates the error", func(t *testing.T) {
		want := errors.New("read failed")
		wsConn := NewConn(newFakeConn(nil), nil, nil)
		wsConn.di.getFrameFromTCPConn = func(net.Conn) (*Frame, error) { return nil, want }
		if _, err := wsConn.GetNextFrame(); !errors.Is(err, want) {
			t.Errorf("err = %v, want %v", err, want)
		}
	})
}

func TestConnGetNextMsg(t *testing.T) {
	t.Run("delegates to di", func(t *testing.T) {
		wsConn := NewConn(newFakeConn(nil), nil, nil)
		wsConn.di.getMsgFromTCPConn = func(net.Conn) (Msg, error) {
			return Msg{Frames: []*Frame{{PayloadData: []byte("hello")}}}, nil
		}
		got, err := wsConn.GetNextMsg()
		if err != nil {
			t.Fatalf("GetNextMsg: %v", err)
		}
		if got.GetStr() != "hello" {
			t.Errorf("GetStr() = %q", got.GetStr())
		}
	})

	t.Run("propagates the error", func(t *testing.T) {
		want := errors.New("msg failed")
		wsConn := NewConn(newFakeConn(nil), nil, nil)
		wsConn.di.getMsgFromTCPConn = func(net.Conn) (Msg, error) { return Msg{}, want }
		if _, err := wsConn.GetNextMsg(); !errors.Is(err, want) {
			t.Errorf("err = %v, want %v", err, want)
		}
	})
}

func TestConnSendMsg(t *testing.T) {
	t.Run("writes every frame sealed", func(t *testing.T) {
		netConn := newFakeConn(nil)
		wsConn := NewConn(netConn, nil, nil)
		msg := &Msg{Frames: []*Frame{
			{FIN: false, Opcode: 1, PayloadLength: 2, PayloadData: []byte("wl")},
			{FIN: true, Opcode: 0, PayloadLength: 4, PayloadData: []byte("gows")},
		}}

		if err := wsConn.SendMsg(msg); err != nil {
			t.Fatalf("SendMsg: %v", err)
		}

		want := append(msg.Frames[0].Seal(), msg.Frames[1].Seal()...)
		if !bytes.Equal(netConn.written(), want) {
			t.Errorf("written = % x, want % x", netConn.written(), want)
		}
	})

	t.Run("empty message writes nothing", func(t *testing.T) {
		netConn := newFakeConn(nil)
		wsConn := NewConn(netConn, nil, nil)
		if err := wsConn.SendMsg(&Msg{}); err != nil {
			t.Fatalf("SendMsg: %v", err)
		}
		if len(netConn.written()) != 0 {
			t.Errorf("wrote %d bytes, want 0", len(netConn.written()))
		}
	})

	t.Run("stops at the first write error", func(t *testing.T) {
		want := errors.New("broken pipe")
		netConn := newFakeConn(nil)
		netConn.writeErr = want
		wsConn := NewConn(netConn, nil, nil)
		msg := &Msg{Frames: []*Frame{
			{FIN: false, PayloadData: []byte("a")},
			{FIN: true, PayloadData: []byte("b")},
		}}
		if err := wsConn.SendMsg(msg); !errors.Is(err, want) {
			t.Errorf("err = %v, want %v", err, want)
		}
		if len(netConn.written()) != 0 {
			t.Error("nothing should have landed in the buffer")
		}
	})
}

func TestConnClose(t *testing.T) {
	t.Run("closes the socket and marks both messages closed", func(t *testing.T) {
		netConn := newFakeConn(nil)
		request := httptestRequest(t)
		response := &http.Response{StatusCode: 101}
		wsConn := NewConn(netConn, request, response)

		if err := wsConn.Close(); err != nil {
			t.Fatalf("Close: %v", err)
		}
		if !netConn.closed {
			t.Error("underlying conn was not closed")
		}
		if !request.Close {
			t.Error("ClientRequest.Close should be true")
		}
		if !response.Close {
			t.Error("ServerResponse.Close should be true")
		}
	})

	t.Run("tolerates nil request and response", func(t *testing.T) {
		netConn := newFakeConn(nil)
		wsConn := NewConn(netConn, nil, nil)
		if err := wsConn.Close(); err != nil {
			t.Fatalf("Close: %v", err)
		}
		if !netConn.closed {
			t.Error("underlying conn was not closed")
		}
	})

	t.Run("returns early when the socket fails to close", func(t *testing.T) {
		want := errors.New("close failed")
		netConn := newFakeConn(nil)
		netConn.closeErr = want
		request := httptestRequest(t)
		wsConn := NewConn(netConn, request, nil)

		if err := wsConn.Close(); !errors.Is(err, want) {
			t.Fatalf("err = %v, want %v", err, want)
		}
		if request.Close {
			t.Error("ClientRequest.Close must not be set when the socket close failed")
		}
	})
}

// Shared helper: a minimal valid client request.
func httptestRequest(t *testing.T) *http.Request {
	t.Helper()
	request, err := http.NewRequest("GET", "ws://localhost:8001/", nil)
	if err != nil {
		t.Fatalf("http.NewRequest: %v", err)
	}
	return request
}
