package wlgows

import (
	"bytes"
	"errors"
	"testing"
)

func TestConnSendText(t *testing.T) {
	// FIN is the one NewFrameConfig field whose zero value is wrong here: a text
	// message sent without it leaves the peer waiting for a continuation that
	// never comes.
	t.Run("builds one whole text message", func(t *testing.T) {
		var got NewFrameConfig
		netConn := newFakeConn(nil)
		wsConn := NewConn(netConn, nil, nil, false)
		wsConn.di.newDataFrame = func(config NewFrameConfig) (*Frame, error) {
			got = config
			return NewDataFrame(config)
		}

		if err := wsConn.SendText([]byte("hello")); err != nil {
			t.Fatalf("SendText: %v", err)
		}
		if got.Opcode != OpcodeText {
			t.Errorf("Opcode = %#x, want %#x", got.Opcode, OpcodeText)
		}
		if !got.FIN {
			t.Error("FIN was not set — the peer would wait for a continuation")
		}
		if string(got.PayloadData) != "hello" {
			t.Errorf("PayloadData = %q, want %q", got.PayloadData, "hello")
		}
		if !bytes.Contains(netConn.written(), []byte("hello")) {
			t.Error("the message did not reach the socket")
		}
	})

	t.Run("takes the mask from the Conn", func(t *testing.T) {
		for _, maskSendFrame := range []bool{false, true} {
			var got NewFrameConfig
			wsConn := NewConn(newFakeConn(nil), nil, nil, maskSendFrame)
			wsConn.di.newDataFrame = func(config NewFrameConfig) (*Frame, error) {
				got = config
				return NewDataFrame(config)
			}

			if err := wsConn.SendText([]byte("hello")); err != nil {
				t.Fatalf("SendText: %v", err)
			}
			if got.Mask != maskSendFrame {
				t.Errorf("Conn built with maskSendFrame=%v asked for Mask=%v", maskSendFrame, got.Mask)
			}
		}
	})

	/*
		Both locks, and in this order: dataFramesWriteLocker for the message,
		writeLocker inside it for the frame. Taking them the other way round on
		any path would deadlock against this one.
	*/
	t.Run("holds the data lock around the frame write", func(t *testing.T) {
		dataLocker, frameLocker := &fakeLocker{}, &fakeLocker{}
		netConn := newFakeConn(nil)
		wsConn := NewConn(netConn, nil, nil, false)
		wsConn.di.dataFramesWriteLocker = dataLocker
		wsConn.di.writeLocker = frameLocker

		dataHeldAtWrite, frameHeldAtWrite := false, false
		netConn.onWrite = func() {
			dataHeldAtWrite, frameHeldAtWrite = dataLocker.held, frameLocker.held
		}

		if err := wsConn.SendText([]byte("hello")); err != nil {
			t.Fatalf("SendText: %v", err)
		}
		if !dataHeldAtWrite {
			t.Error("wrote without holding dataFramesWriteLocker")
		}
		if !frameHeldAtWrite {
			t.Error("wrote without holding writeLocker")
		}
		if !dataLocker.ok(1) || !frameLocker.ok(1) {
			t.Errorf("data locks=%d unlocks=%d, frame locks=%d unlocks=%d, want 1 each",
				dataLocker.locks, dataLocker.unlocks, frameLocker.locks, frameLocker.unlocks)
		}
	})

	/*
		5.6 defines a text payload as UTF-8 and 8.1 has the peer fail the
		connection over it, so an invalid message costs a close 1007 and the
		connection. The Listener refuses these on the way in; refusing them on
		the way out is the same rule.

		The bytes are a truncated 3 byte rune — the shape a caller hits by
		slicing a string at a byte offset.
	*/
	t.Run("refuses a payload that is not valid UTF-8", func(t *testing.T) {
		netConn := newFakeConn(nil)
		wsConn := NewConn(netConn, nil, nil, false)

		if err := wsConn.SendText([]byte{0xe4, 0xb8}); !errors.Is(err, ErrInvalidUTF8) {
			t.Errorf("SendText = %v, want ErrInvalidUTF8", err)
		}
		if len(netConn.written()) != 0 {
			t.Errorf("%d bytes reached the socket", len(netConn.written()))
		}
	})

	// The same rune whole, to show the check passes what 5.6 allows rather than
	// just refusing anything non ASCII.
	t.Run("accepts multi byte UTF-8", func(t *testing.T) {
		netConn := newFakeConn(nil)
		wsConn := NewConn(netConn, nil, nil, false)

		if err := wsConn.SendText([]byte("中文字")); err != nil {
			t.Fatalf("SendText: %v", err)
		}
		if !bytes.Contains(netConn.written(), []byte("中文字")) {
			t.Error("the message did not reach the socket")
		}
	})

	t.Run("propagates a build error with nothing written", func(t *testing.T) {
		wantErr := errors.New("cannot build")
		netConn := newFakeConn(nil)
		wsConn := NewConn(netConn, nil, nil, false)
		wsConn.di.newDataFrame = func(NewFrameConfig) (*Frame, error) { return nil, wantErr }

		if err := wsConn.SendText([]byte("hello")); !errors.Is(err, wantErr) {
			t.Errorf("SendText = %v, want %v", err, wantErr)
		}
		if len(netConn.written()) != 0 {
			t.Errorf("%d bytes reached the socket after a build failure", len(netConn.written()))
		}
	})
}

func TestConnSendBinary(t *testing.T) {
	t.Run("builds one whole binary message", func(t *testing.T) {
		var got NewFrameConfig
		netConn := newFakeConn(nil)
		wsConn := NewConn(netConn, nil, nil, false)
		wsConn.di.newDataFrame = func(config NewFrameConfig) (*Frame, error) {
			got = config
			return NewDataFrame(config)
		}

		payload := []byte{0x00, 0x01, 0x02}
		if err := wsConn.SendBinary(payload); err != nil {
			t.Fatalf("SendBinary: %v", err)
		}
		if got.Opcode != OpcodeBinary {
			t.Errorf("Opcode = %#x, want %#x", got.Opcode, OpcodeBinary)
		}
		if !got.FIN {
			t.Error("FIN was not set — the peer would wait for a continuation")
		}
		if !bytes.Equal(got.PayloadData, payload) {
			t.Errorf("PayloadData = % x, want % x", got.PayloadData, payload)
		}
		if !bytes.Contains(netConn.written(), payload) {
			t.Error("the message did not reach the socket")
		}
	})

	t.Run("takes the mask from the Conn", func(t *testing.T) {
		for _, maskSendFrame := range []bool{false, true} {
			var got NewFrameConfig
			wsConn := NewConn(newFakeConn(nil), nil, nil, maskSendFrame)
			wsConn.di.newDataFrame = func(config NewFrameConfig) (*Frame, error) {
				got = config
				return NewDataFrame(config)
			}

			if err := wsConn.SendBinary([]byte{0x01}); err != nil {
				t.Fatalf("SendBinary: %v", err)
			}
			if got.Mask != maskSendFrame {
				t.Errorf("Conn built with maskSendFrame=%v asked for Mask=%v", maskSendFrame, got.Mask)
			}
		}
	})

	t.Run("holds the data lock around the frame write", func(t *testing.T) {
		dataLocker, frameLocker := &fakeLocker{}, &fakeLocker{}
		netConn := newFakeConn(nil)
		wsConn := NewConn(netConn, nil, nil, false)
		wsConn.di.dataFramesWriteLocker = dataLocker
		wsConn.di.writeLocker = frameLocker

		dataHeldAtWrite, frameHeldAtWrite := false, false
		netConn.onWrite = func() {
			dataHeldAtWrite, frameHeldAtWrite = dataLocker.held, frameLocker.held
		}

		if err := wsConn.SendBinary([]byte{0x01}); err != nil {
			t.Fatalf("SendBinary: %v", err)
		}
		if !dataHeldAtWrite || !frameHeldAtWrite {
			t.Errorf("wrote with dataFramesWriteLocker=%v writeLocker=%v, want both held",
				dataHeldAtWrite, frameHeldAtWrite)
		}
		if !dataLocker.ok(1) || !frameLocker.ok(1) {
			t.Errorf("data locks=%d unlocks=%d, frame locks=%d unlocks=%d, want 1 each",
				dataLocker.locks, dataLocker.unlocks, frameLocker.locks, frameLocker.unlocks)
		}
	})

	/*
		The difference from SendText. RFC 6455 5.6 gives binary no encoding, so
		the bytes SendText refuses go out here untouched — a truncated rune is
		only malformed if something claims it is text.
	*/
	t.Run("sends bytes SendText refuses", func(t *testing.T) {
		payload := []byte{0xe4, 0xb8}
		netConn := newFakeConn(nil)
		wsConn := NewConn(netConn, nil, nil, false)

		if err := wsConn.SendText(payload); !errors.Is(err, ErrInvalidUTF8) {
			t.Fatalf("SendText = %v, want ErrInvalidUTF8 — the premise of this test", err)
		}
		if err := wsConn.SendBinary(payload); err != nil {
			t.Errorf("SendBinary: %v", err)
		}
		if !bytes.Contains(netConn.written(), payload) {
			t.Error("the payload did not reach the socket")
		}
	})

	t.Run("propagates a build error with nothing written", func(t *testing.T) {
		wantErr := errors.New("cannot build")
		netConn := newFakeConn(nil)
		wsConn := NewConn(netConn, nil, nil, false)
		wsConn.di.newDataFrame = func(NewFrameConfig) (*Frame, error) { return nil, wantErr }

		if err := wsConn.SendBinary([]byte{0x01}); !errors.Is(err, wantErr) {
			t.Errorf("SendBinary = %v, want %v", err, wantErr)
		}
		if len(netConn.written()) != 0 {
			t.Errorf("%d bytes reached the socket after a build failure", len(netConn.written()))
		}
	})
}
