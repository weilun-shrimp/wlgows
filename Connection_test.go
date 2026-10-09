package wlgows

import (
	"bufio"
	"errors"
	"net"
	"net/http"
	"slices"
	"testing"
)

func TestNewConn(t *testing.T) {
	t.Run("builds a Conn from what it is given", func(t *testing.T) {
		netConn := newFakeConn(nil)
		r := bufio.NewReader(netConn)
		w := bufio.NewWriterSize(netConn, 100)

		wsConn, err := NewConn(netConn, r, w, true)
		if err != nil {
			t.Fatalf("NewConn: %v", err)
		}
		if wsConn.Conn != net.Conn(netConn) {
			t.Error("embedded net.Conn was not set")
		}
		if wsConn.reader != r {
			t.Error("reader was not set")
		}
		if wsConn.writer != w {
			t.Error("writer was not set")
		}
		if !wsConn.maskSendFrame {
			t.Error("maskSendFrame was not set")
		}
		// Constructors are mandatory precisely because they populate di.
		if wsConn.di.getFrameFromReader == nil || wsConn.di.newControlFrame == nil {
			t.Error("NewConn must populate every di field")
		}
		if wsConn.di.writeLocker == nil || wsConn.di.readLocker == nil {
			t.Error("NewConn must populate both lockers")
		}
	})

	t.Run("a small writer that fails to flush returns no Conn", func(t *testing.T) {
		wantErr := errors.New("socket gone")
		netConn := newFakeConn(nil)
		netConn.writeErr = wantErr
		w := bufio.NewWriterSize(netConn, 5)
		w.WriteString("abc")

		wsConn, err := NewConn(netConn, bufio.NewReader(netConn), w, true)
		if !errors.Is(err, wantErr) || wsConn != nil {
			t.Errorf("NewConn = %v, %v, want nil, %v", wsConn, err, wantErr)
		}
	})
}

// The lockers guard the socket against concurrent use. This asserts the read
// seam is taken, and that the frame is read while the lock is actually held.
func TestConnLocking(t *testing.T) {
	t.Run("GetNextFrame locks", func(t *testing.T) {
		var steps []string
		wsConn, _ := NewConn(newFakeConn(nil), bufio.NewReader(newFakeConn(nil)), bufio.NewWriter(newFakeConn(nil)), false)
		wsConn.di.readLocker = fakeFuncLocker{
			lock:   func() { steps = append(steps, "read lock") },
			unlock: func() { steps = append(steps, "read unlock") },
		}
		wsConn.di.getFrameFromReader = func(*bufio.Reader, uint64) (*Frame, error) {
			steps = append(steps, "read")
			return &Frame{}, nil
		}

		if _, err := wsConn.GetNextFrame(0); err != nil {
			t.Fatalf("GetNextFrame: %v", err)
		}
		if want := []string{"read lock", "read", "read unlock"}; !slices.Equal(steps, want) {
			t.Errorf("steps %q, want %q", steps, want)
		}
	})

	t.Run("GetNextFrame unlocks after a read error", func(t *testing.T) {
		var steps []string
		wsConn, _ := NewConn(newFakeConn(nil), bufio.NewReader(newFakeConn(nil)), bufio.NewWriter(newFakeConn(nil)), false)
		wsConn.di.readLocker = fakeFuncLocker{
			lock:   func() { steps = append(steps, "read lock") },
			unlock: func() { steps = append(steps, "read unlock") },
		}
		wsConn.di.getFrameFromReader = func(*bufio.Reader, uint64) (*Frame, error) {
			steps = append(steps, "read")
			return nil, errors.New("boom")
		}

		if _, err := wsConn.GetNextFrame(0); err == nil {
			t.Fatal("GetNextFrame should have failed")
		}
		if want := []string{"read lock", "read", "read unlock"}; !slices.Equal(steps, want) {
			t.Errorf("steps %q, want %q — the lock must be released on the error path", steps, want)
		}
	})
}

func TestConnGetNextFrame(t *testing.T) {
	t.Run("delegates to di", func(t *testing.T) {
		want := &Frame{FIN: true, Opcode: 1, PayloadData: []byte("x")}
		wsConn, _ := NewConn(newFakeConn(nil), bufio.NewReader(newFakeConn(nil)), bufio.NewWriter(newFakeConn(nil)), false)
		var gotConn *bufio.Reader
		wsConn.di.getFrameFromReader = func(r *bufio.Reader, _ uint64) (*Frame, error) {
			gotConn = r
			return want, nil
		}

		got, err := wsConn.GetNextFrame(0)
		if err != nil {
			t.Fatalf("GetNextFrame: %v", err)
		}
		if got != want {
			t.Error("GetNextFrame did not return the di result")
		}
		if gotConn != wsConn.reader {
			t.Error("GetNextFrame must pass Conn's reader through")
		}
	})

	// The limit is the caller's, per call — nothing on Conn remembers it, so it
	// has to arrive at the frame reader untouched.
	t.Run("passes the max byte length straight through", func(t *testing.T) {
		wsConn, _ := NewConn(newFakeConn(nil), bufio.NewReader(newFakeConn(nil)), bufio.NewWriter(newFakeConn(nil)), false)
		var got uint64
		wsConn.di.getFrameFromReader = func(_ *bufio.Reader, maxByteLength uint64) (*Frame, error) {
			got = maxByteLength
			return &Frame{}, nil
		}

		for _, want := range []uint64{0, 1, 10 << 20, 1<<64 - 1} {
			if _, err := wsConn.GetNextFrame(want); err != nil {
				t.Fatalf("GetNextFrame(%d): %v", want, err)
			}
			if got != want {
				t.Errorf("frame reader received maxByteLength %d, want %d", got, want)
			}
		}
	})

	t.Run("propagates the error", func(t *testing.T) {
		want := errors.New("read failed")
		wsConn, _ := NewConn(newFakeConn(nil), bufio.NewReader(newFakeConn(nil)), bufio.NewWriter(newFakeConn(nil)), false)
		wsConn.di.getFrameFromReader = func(*bufio.Reader, uint64) (*Frame, error) { return nil, want }
		if _, err := wsConn.GetNextFrame(0); !errors.Is(err, want) {
			t.Errorf("err = %v, want %v", err, want)
		}
	})
}

func TestConnClose(t *testing.T) {
	t.Run("closes the socket", func(t *testing.T) {
		netConn := newFakeConn(nil)
		wsConn, _ := NewConn(netConn, bufio.NewReader(netConn), bufio.NewWriter(netConn), false)

		if err := wsConn.Close(); err != nil {
			t.Fatalf("Close: %v", err)
		}
		if !netConn.closed {
			t.Error("underlying conn was not closed")
		}
	})

	t.Run("propagates a close error", func(t *testing.T) {
		want := errors.New("close failed")
		netConn := newFakeConn(nil)
		netConn.closeErr = want
		wsConn, _ := NewConn(netConn, bufio.NewReader(netConn), bufio.NewWriter(netConn), false)

		if err := wsConn.Close(); !errors.Is(err, want) {
			t.Fatalf("err = %v, want %v", err, want)
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
