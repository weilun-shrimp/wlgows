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

	wsConn := NewConn(netConn, request, response, false)

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
	if wsConn.di.getFrameFromTCPConn == nil || wsConn.di.newControlFrame == nil {
		t.Error("NewConn must populate every di field")
	}
	if wsConn.di.writeLocker == nil || wsConn.di.readLocker == nil {
		t.Error("NewConn must populate both lockers")
	}
}

// The lockers guard the socket against concurrent use. This asserts the read
// seam is taken, and that the frame is read while the lock is actually held.
func TestConnLocking(t *testing.T) {
	t.Run("GetNextFrame locks", func(t *testing.T) {
		locker := &fakeLocker{}
		wsConn := NewConn(newFakeConn(nil), nil, nil, false)
		wsConn.di.readLocker = locker
		wsConn.di.getFrameFromTCPConn = func(net.Conn, uint64) (*Frame, error) {
			if !locker.held {
				t.Error("the frame was read outside the lock")
			}
			return &Frame{}, nil
		}

		if _, err := wsConn.GetNextFrame(0); err != nil {
			t.Fatalf("GetNextFrame: %v", err)
		}
		if !locker.ok(1) {
			t.Errorf("locks=%d unlocks=%d misuse=%d held=%v, want 1/1/0/false",
				locker.locks, locker.unlocks, locker.misuse, locker.held)
		}
	})

	t.Run("GetNextFrame unlocks after a read error", func(t *testing.T) {
		locker := &fakeLocker{}
		wsConn := NewConn(newFakeConn(nil), nil, nil, false)
		wsConn.di.readLocker = locker
		wsConn.di.getFrameFromTCPConn = func(net.Conn, uint64) (*Frame, error) {
			return nil, errors.New("boom")
		}

		if _, err := wsConn.GetNextFrame(0); err == nil {
			t.Fatal("GetNextFrame should have failed")
		}
		if !locker.ok(1) {
			t.Errorf("locks=%d unlocks=%d misuse=%d held=%v — the lock must be released on the error path",
				locker.locks, locker.unlocks, locker.misuse, locker.held)
		}
	})
}

func TestConnGetNextFrame(t *testing.T) {
	t.Run("delegates to di", func(t *testing.T) {
		want := &Frame{FIN: true, Opcode: 1, PayloadData: []byte("x")}
		wsConn := NewConn(newFakeConn(nil), nil, nil, false)
		var gotConn net.Conn
		wsConn.di.getFrameFromTCPConn = func(conn net.Conn, _ uint64) (*Frame, error) {
			gotConn = conn
			return want, nil
		}

		got, err := wsConn.GetNextFrame(0)
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

	// The limit is the caller's, per call — nothing on Conn remembers it, so it
	// has to arrive at the frame reader untouched.
	t.Run("passes the max byte length straight through", func(t *testing.T) {
		wsConn := NewConn(newFakeConn(nil), nil, nil, false)
		var got uint64
		wsConn.di.getFrameFromTCPConn = func(_ net.Conn, maxByteLength uint64) (*Frame, error) {
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
		wsConn := NewConn(newFakeConn(nil), nil, nil, false)
		wsConn.di.getFrameFromTCPConn = func(net.Conn, uint64) (*Frame, error) { return nil, want }
		if _, err := wsConn.GetNextFrame(0); !errors.Is(err, want) {
			t.Errorf("err = %v, want %v", err, want)
		}
	})
}

func TestConnClose(t *testing.T) {
	t.Run("closes the socket and marks both messages closed", func(t *testing.T) {
		netConn := newFakeConn(nil)
		request := httptestRequest(t)
		response := &http.Response{StatusCode: 101}
		wsConn := NewConn(netConn, request, response, false)

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
		wsConn := NewConn(netConn, nil, nil, false)
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
		wsConn := NewConn(netConn, request, nil, false)

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

/*
RFC 6455 5.1 gives each side one answer, so SendFrame brings the frame into line
rather than sending what it was handed. Both directions matter: a client frame
built unmasked has to gain a key, and a server frame built masked has to lose
one — a server that masks is failed by the client just as surely.
*/
func TestConnSendFrameMatchesTheConnsMasking(t *testing.T) {
	t.Run("client masks a frame built unmasked", func(t *testing.T) {
		f, err := NewFrame(NewFrameConfig{PayloadData: []byte("hello"), Opcode: OpcodeText, FIN: true})
		if err != nil {
			t.Fatalf("NewFrame: %v", err)
		}
		netConn := newFakeConn(nil)
		wsConn := NewConn(netConn, nil, nil, true)

		if err := wsConn.SendFrame(f); err != nil {
			t.Fatalf("SendFrame: %v", err)
		}
		if !f.Mask {
			t.Error("Mask was not set")
		}
		if len(f.MaskingKey) != 4 {
			t.Fatalf("MaskingKey is %d bytes, want 4 — Seal panics without it", len(f.MaskingKey))
		}
		// The wire carries masked bytes while PayloadData stays plaintext.
		if string(f.PayloadData) != "hello" {
			t.Errorf("PayloadData = %q, want it left plaintext", f.PayloadData)
		}
		if bytes.Contains(netConn.written(), []byte("hello")) {
			t.Error("the payload reached the socket unmasked")
		}
	})

	t.Run("server unmasks a frame built masked", func(t *testing.T) {
		f, err := NewFrame(NewFrameConfig{PayloadData: []byte("hello"), Opcode: OpcodeText, Mask: true, FIN: true})
		if err != nil {
			t.Fatalf("NewFrame: %v", err)
		}
		netConn := newFakeConn(nil)
		wsConn := NewConn(netConn, nil, nil, false)

		if err := wsConn.SendFrame(f); err != nil {
			t.Fatalf("SendFrame: %v", err)
		}
		if f.Mask {
			t.Error("Mask was not cleared")
		}
		if f.MaskingKey != nil {
			t.Errorf("MaskingKey = % x, want nil — a key without the bit is a frame no one can read", f.MaskingKey)
		}
		if !bytes.Contains(netConn.written(), []byte("hello")) {
			t.Error("the payload did not reach the socket in plaintext")
		}
	})

	// Already correct on both sides: nothing is touched.
	t.Run("leaves a matching frame alone", func(t *testing.T) {
		for _, maskSendFrame := range []bool{false, true} {
			f, err := NewFrame(NewFrameConfig{
				PayloadData: []byte("hello"), Opcode: OpcodeText, Mask: maskSendFrame, FIN: true,
			})
			if err != nil {
				t.Fatalf("NewFrame: %v", err)
			}
			wantKey := f.MaskingKey
			wsConn := NewConn(newFakeConn(nil), nil, nil, maskSendFrame)

			if err := wsConn.SendFrame(f); err != nil {
				t.Fatalf("SendFrame: %v", err)
			}
			if f.Mask != maskSendFrame {
				t.Errorf("maskSendFrame=%v: Mask became %v", maskSendFrame, f.Mask)
			}
			if !bytes.Equal(f.MaskingKey, wantKey) {
				t.Errorf("maskSendFrame=%v: MaskingKey was replaced", maskSendFrame)
			}
		}
	})
}

/*
A key that cannot be generated must stop the send. Writing the frame anyway
would put an unmasked frame from a client on the wire, which RFC 6455 5.1
forbids and the server fails the connection on — the very thing masking here is
for.
*/
func TestConnSendFrameStopsWhenTheKeyCannotBeMade(t *testing.T) {
	wantErr := errors.New("no entropy")
	f, err := NewFrame(NewFrameConfig{PayloadData: []byte("hello"), Opcode: OpcodeText, FIN: true})
	if err != nil {
		t.Fatalf("NewFrame: %v", err)
	}

	netConn := newFakeConn(nil)
	wsConn := NewConn(netConn, nil, nil, true)
	wsConn.di.generateMaskingKey = func() ([]byte, error) { return nil, wantErr }

	if err := wsConn.SendFrame(f); !errors.Is(err, wantErr) {
		t.Errorf("SendFrame = %v, want %v", err, wantErr)
	}
	if len(netConn.written()) != 0 {
		t.Errorf("%d bytes reached the socket after the key failed", len(netConn.written()))
	}
	if f.Mask {
		t.Error("Mask was set even though there is no key — Seal would panic on this frame")
	}
}
