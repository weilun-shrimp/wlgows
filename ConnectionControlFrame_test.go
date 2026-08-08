package wlgows

import (
	"errors"
	"testing"
)

/*
One test per method. Opcode classification, the 125 byte cap and FIN belong to
NewControlFrame and are covered in FrameControl_test.go; what is left for Conn is
which opcode each method picks, what it relays, and that the frame reaches the
socket under the write lock.
*/

// capture holds what a Send method asked NewControlFrame for, and the socket and
// locker the frame went out through.
type capture struct {
	config NewControlFrameConfig
	conn   *fakeConn
	locker *fakeLocker
	held   bool // was the write lock held at the moment of the write?
}

func captureSend(t *testing.T, send func(*Conn) error) capture {
	t.Helper()
	got := capture{conn: newFakeConn(nil), locker: &fakeLocker{}}
	wsConn := NewConn(got.conn, nil, nil)
	wsConn.di.writeLocker = got.locker
	wsConn.di.newControlFrame = func(config NewControlFrameConfig) (*Frame, error) {
		got.config = config
		return NewControlFrame(config)
	}
	got.conn.onWrite = func() { got.held = got.locker.held }

	if err := send(wsConn); err != nil {
		t.Fatalf("send: %v", err)
	}
	return got
}

// The frame must reach the socket, and do so inside the write lock.
func (got capture) assertWrittenUnderLock(t *testing.T) {
	t.Helper()
	if len(got.conn.written()) == 0 {
		t.Error("nothing reached the socket")
	}
	if !got.held {
		t.Error("the frame was written outside the write lock")
	}
	if !got.locker.ok(1) {
		t.Errorf("locks=%d unlocks=%d misuse=%d held=%v, want 1/1/0/false",
			got.locker.locks, got.locker.unlocks, got.locker.misuse, got.locker.held)
	}
}

// assertPropagatesBuildError substitutes a failing builder and checks the error
// reaches the caller with nothing written.
func assertPropagatesBuildError(t *testing.T, send func(*Conn) error) {
	t.Helper()
	want := errors.New("cannot build")
	netConn := newFakeConn(nil)
	wsConn := NewConn(netConn, nil, nil)
	wsConn.di.newControlFrame = func(NewControlFrameConfig) (*Frame, error) { return nil, want }
	if err := send(wsConn); !errors.Is(err, want) {
		t.Errorf("err = %v, want %v", err, want)
	}
	if len(netConn.written()) != 0 {
		t.Errorf("%d bytes reached the socket after a build failure", len(netConn.written()))
	}
}

func TestConnSendClose(t *testing.T) {
	t.Run("nil payload sends no body", func(t *testing.T) {
		got := captureSend(t, func(c *Conn) error { return c.SendClose(false, nil) })
		if got.config.Opcode != OpcodeClose {
			t.Errorf("Opcode = %#x, want %#x", got.config.Opcode, OpcodeClose)
		}
		if len(got.config.PayloadData) != 0 {
			t.Errorf("PayloadData = % x, want empty", got.config.PayloadData)
		}
		got.assertWrittenUnderLock(t)
	})

	t.Run("relays mask and the encoded payload", func(t *testing.T) {
		payload := &ClosePayload{StatusCode: CloseNormalClosure, Reason: "bye"}
		got := captureSend(t, func(c *Conn) error { return c.SendClose(true, payload) })
		if !got.config.Mask {
			t.Error("Mask was not relayed")
		}
		if string(got.config.PayloadData) != string(payload.Bytes()) {
			t.Errorf("PayloadData = % x, want % x", got.config.PayloadData, payload.Bytes())
		}
	})

	t.Run("propagates a build error", func(t *testing.T) {
		assertPropagatesBuildError(t, func(c *Conn) error { return c.SendClose(false, nil) })
	})
}

func TestConnSendPing(t *testing.T) {
	t.Run("relays mask and payload unchanged", func(t *testing.T) {
		got := captureSend(t, func(c *Conn) error { return c.SendPing(true, []byte("hb")) })
		if got.config.Opcode != OpcodePing {
			t.Errorf("Opcode = %#x, want %#x", got.config.Opcode, OpcodePing)
		}
		if !got.config.Mask {
			t.Error("Mask was not relayed")
		}
		if string(got.config.PayloadData) != "hb" {
			t.Errorf("PayloadData = %q, want %q", got.config.PayloadData, "hb")
		}
		got.assertWrittenUnderLock(t)
	})

	t.Run("propagates a build error", func(t *testing.T) {
		assertPropagatesBuildError(t, func(c *Conn) error { return c.SendPing(false, nil) })
	})
}

func TestConnSendPong(t *testing.T) {
	t.Run("relays mask and payload unchanged", func(t *testing.T) {
		got := captureSend(t, func(c *Conn) error { return c.SendPong(true, []byte("hb")) })
		if got.config.Opcode != OpcodePong {
			t.Errorf("Opcode = %#x, want %#x", got.config.Opcode, OpcodePong)
		}
		if !got.config.Mask {
			t.Error("Mask was not relayed")
		}
		if string(got.config.PayloadData) != "hb" {
			t.Errorf("PayloadData = %q, want %q", got.config.PayloadData, "hb")
		}
		got.assertWrittenUnderLock(t)
	})

	t.Run("propagates a build error", func(t *testing.T) {
		assertPropagatesBuildError(t, func(c *Conn) error { return c.SendPong(false, nil) })
	})
}
