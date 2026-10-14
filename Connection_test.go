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
		if wsConn.di.getFrameHeader == nil || wsConn.di.fillFramePayload == nil || wsConn.di.newControlFrame == nil {
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

// The read lock guards one frame against a second reader. The caller takes it
// with LockRead around both read steps, so the steps themselves leave it alone.
func TestConnLocking(t *testing.T) {
	t.Run("LockRead and UnlockRead take and release the read lock", func(t *testing.T) {
		var steps []string
		wsConn, _ := NewConn(newFakeConn(nil), bufio.NewReader(newFakeConn(nil)), bufio.NewWriter(newFakeConn(nil)), false)
		wsConn.di.readLocker = fakeFuncLocker{
			lock:   func() { steps = append(steps, "read lock") },
			unlock: func() { steps = append(steps, "read unlock") },
		}

		wsConn.LockRead()
		wsConn.UnlockRead()
		if want := []string{"read lock", "read unlock"}; !slices.Equal(steps, want) {
			t.Errorf("steps %q, want %q", steps, want)
		}
	})

	// The caller holds the lock across both steps, so neither takes it: a
	// sync.Mutex would deadlock on the second Lock.
	t.Run("the two read steps leave the lock to the caller", func(t *testing.T) {
		var steps []string
		wsConn, _ := NewConn(newFakeConn(nil), bufio.NewReader(newFakeConn(nil)), bufio.NewWriter(newFakeConn(nil)), false)
		wsConn.di.readLocker = fakeFuncLocker{
			lock:   func() { steps = append(steps, "read lock") },
			unlock: func() { steps = append(steps, "read unlock") },
		}
		wsConn.di.getFrameHeader = func(*bufio.Reader) (*Frame, error) {
			steps = append(steps, "read header")
			return &Frame{}, nil
		}
		wsConn.di.fillFramePayload = func(*Frame, *bufio.Reader) error {
			steps = append(steps, "read payload")
			return nil
		}

		frame, _ := wsConn.GetNextFrameHeader()
		wsConn.ReadFramePayload(frame)
		if want := []string{"read header", "read payload"}; !slices.Equal(steps, want) {
			t.Errorf("steps %q, want %q", steps, want)
		}
	})
}

func TestConnGetNextFrameHeader(t *testing.T) {
	want := &Frame{FIN: true, Opcode: OpcodeText, PayloadLength: 5}
	wantErr := errors.New("read failed")
	wsConn, _ := NewConn(newFakeConn(nil), bufio.NewReader(newFakeConn(nil)), bufio.NewWriter(newFakeConn(nil)), false)
	var gotReader *bufio.Reader
	wsConn.di.getFrameHeader = func(reader *bufio.Reader) (*Frame, error) {
		gotReader = reader
		return want, wantErr
	}

	got, err := wsConn.GetNextFrameHeader()
	if got != want || !errors.Is(err, wantErr) {
		t.Errorf("got (%v, %v), want the di result (%v, %v)", got, err, want, wantErr)
	}
	if gotReader != wsConn.reader {
		t.Error("GetNextFrameHeader must pass Conn's reader through")
	}
}

func TestConnReadFramePayload(t *testing.T) {
	frame := &Frame{Opcode: OpcodeText, PayloadLength: 5}
	wantErr := errors.New("read failed")
	wsConn, _ := NewConn(newFakeConn(nil), bufio.NewReader(newFakeConn(nil)), bufio.NewWriter(newFakeConn(nil)), false)
	var gotReader *bufio.Reader
	var gotFrame *Frame
	wsConn.di.fillFramePayload = func(payloadFrame *Frame, reader *bufio.Reader) error {
		gotReader, gotFrame = reader, payloadFrame
		return wantErr
	}

	if err := wsConn.ReadFramePayload(frame); !errors.Is(err, wantErr) {
		t.Errorf("err = %v, want %v", err, wantErr)
	}
	if gotReader != wsConn.reader || gotFrame != frame {
		t.Error("ReadFramePayload must pass Conn's reader and the frame through")
	}
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
