package wlgows

import (
	"bytes"
	"errors"
	"testing"
)

// transmitting returns a Conn with an open transmission and the fakes behind it.
func transmitting(t *testing.T, opcode uint8) (*Conn, *fakeConn, *fakeLocker) {
	t.Helper()
	netConn, locker := newFakeConn(nil), &fakeLocker{}
	wsConn := NewConn(netConn, nil, nil, false)
	wsConn.di.dataFramesWriteLocker = locker

	if err := wsConn.StartLongDataTransmission(opcode); err != nil {
		t.Fatalf("StartLongDataTransmission: %v", err)
	}
	return wsConn, netConn, locker
}

// framesOn parses back everything written, so a test can assert what the peer
// would actually read rather than what the code meant to send.
func framesOn(t *testing.T, netConn *fakeConn) Frames {
	t.Helper()
	wire := newFakeConn(netConn.written())
	var frames Frames
	for {
		f, err := GetFrameFromTCPConn(wire, 0)
		if err != nil {
			return frames
		}
		frames = append(frames, f)
	}
}

func TestConnStartLongDataTransmission(t *testing.T) {
	/*
		A refused opcode must leave the lock untaken, or the deferred
		EndLongDataTransmission a caller writes after checking the error would
		unlock a mutex nobody holds and panic.
	*/
	t.Run("refuses an opcode that cannot open a message", func(t *testing.T) {
		for _, testCase := range []struct {
			opcode byte
			want   error
		}{
			// 5.4: there is nothing for it to continue.
			{OpcodeContinuation, ErrContinuationFrameWithoutMsg},
			// 5.5: a control frame is never fragmented.
			{OpcodeClose, ErrNotDataFrameOpcode},
			{OpcodePing, ErrNotDataFrameOpcode},
			{OpcodePong, ErrNotDataFrameOpcode},
			{0x3, ErrNotDataFrameOpcode}, // reserved by 5.2
		} {
			locker := &fakeLocker{}
			wsConn := NewConn(newFakeConn(nil), nil, nil, false)
			wsConn.di.dataFramesWriteLocker = locker

			if err := wsConn.StartLongDataTransmission(testCase.opcode); !errors.Is(err, testCase.want) {
				t.Errorf("opcode %#x: err = %v, want %v", testCase.opcode, err, testCase.want)
			}
			if locker.locks != 0 {
				t.Errorf("opcode %#x: took the lock on a refused start", testCase.opcode)
			}
		}
	})

	t.Run("claims the connection for a data opcode", func(t *testing.T) {
		for _, opcode := range []byte{OpcodeText, OpcodeBinary} {
			wsConn, _, locker := transmitting(t, opcode)
			if !locker.held {
				t.Errorf("opcode %#x: the lock was not taken", opcode)
			}
			if wsConn.currentTransmitDataMsgOpcode != opcode {
				t.Errorf("opcode %#x: recorded %#x", opcode, wsConn.currentTransmitDataMsgOpcode)
			}
		}
	})
}

func TestConnTransmitData(t *testing.T) {
	// Without a start there is no lock held, so writing would race every other
	// sender on the connection.
	t.Run("refuses with no transmission open", func(t *testing.T) {
		netConn := newFakeConn(nil)
		wsConn := NewConn(netConn, nil, nil, false)

		if err := wsConn.TransmitData([]byte("hello")); !errors.Is(err, ErrLongDataTransmissionNotStarted) {
			t.Errorf("TransmitData = %v, want ErrLongDataTransmissionNotStarted", err)
		}
		if len(netConn.written()) != 0 {
			t.Errorf("%d bytes reached the socket", len(netConn.written()))
		}
	})

	t.Run("drops empty data", func(t *testing.T) {
		wsConn, netConn, _ := transmitting(t, OpcodeText)

		if err := wsConn.TransmitData(nil); err != nil {
			t.Fatalf("TransmitData(nil): %v", err)
		}
		if wsConn.currentTransmitDataFrame != nil {
			t.Error("an empty fragment became a frame")
		}
		if len(netConn.written()) != 0 {
			t.Errorf("%d bytes reached the socket", len(netConn.written()))
		}
	})

	// The lookahead. Nothing can go out yet, because this fragment may turn out
	// to be the last and need FIN.
	t.Run("holds the first fragment back", func(t *testing.T) {
		wsConn, netConn, _ := transmitting(t, OpcodeText)

		if err := wsConn.TransmitData([]byte("hello")); err != nil {
			t.Fatalf("TransmitData: %v", err)
		}
		if len(netConn.written()) != 0 {
			t.Errorf("%d bytes reached the socket, want the fragment held back", len(netConn.written()))
		}
		if wsConn.currentTransmitDataFrame == nil {
			t.Fatal("nothing was held back")
		}
		if wsConn.currentTransmitDataFrame.Opcode != OpcodeText {
			t.Errorf("held frame opcode = %#x, want %#x", wsConn.currentTransmitDataFrame.Opcode, OpcodeText)
		}
	})

	t.Run("flushes the previous fragment when the next arrives", func(t *testing.T) {
		wsConn, netConn, _ := transmitting(t, OpcodeText)

		if err := wsConn.TransmitData([]byte("hello ")); err != nil {
			t.Fatalf("TransmitData: %v", err)
		}
		if err := wsConn.TransmitData([]byte("world")); err != nil {
			t.Fatalf("TransmitData: %v", err)
		}

		sent := framesOn(t, netConn)
		if len(sent) != 1 {
			t.Fatalf("%d frames on the wire, want 1 — only the first is flushed", len(sent))
		}
		if sent[0].Opcode != OpcodeText {
			t.Errorf("Opcode = %#x, want %#x", sent[0].Opcode, OpcodeText)
		}
		if sent[0].FIN {
			t.Error("FIN was set on a fragment that is not the last")
		}
		// 5.4: the second fragment continues the message, it does not restart it.
		if wsConn.currentTransmitDataFrame.Opcode != OpcodeContinuation {
			t.Errorf("held frame opcode = %#x, want %#x",
				wsConn.currentTransmitDataFrame.Opcode, OpcodeContinuation)
		}
	})

	t.Run("propagates a build error", func(t *testing.T) {
		wantErr := errors.New("cannot build")
		wsConn, netConn, _ := transmitting(t, OpcodeText)
		wsConn.di.newDataFrame = func(NewFrameConfig) (*Frame, error) { return nil, wantErr }

		if err := wsConn.TransmitData([]byte("hello")); !errors.Is(err, wantErr) {
			t.Errorf("TransmitData = %v, want %v", err, wantErr)
		}
		if len(netConn.written()) != 0 {
			t.Errorf("%d bytes reached the socket after a build failure", len(netConn.written()))
		}
	})

	t.Run("propagates a write error from the flush", func(t *testing.T) {
		wantErr := errors.New("socket gone")
		wsConn, netConn, _ := transmitting(t, OpcodeText)

		if err := wsConn.TransmitData([]byte("hello")); err != nil {
			t.Fatalf("TransmitData: %v", err)
		}
		netConn.writeErr = wantErr
		if err := wsConn.TransmitData([]byte("world")); !errors.Is(err, wantErr) {
			t.Errorf("TransmitData = %v, want %v", err, wantErr)
		}
	})
}

func TestConnEndLongDataTransmission(t *testing.T) {
	// Unlocking a mutex nobody holds panics, so this has to refuse rather than
	// release.
	t.Run("refuses with nothing open", func(t *testing.T) {
		locker := &fakeLocker{}
		wsConn := NewConn(newFakeConn(nil), nil, nil, false)
		wsConn.di.dataFramesWriteLocker = locker

		if err := wsConn.EndLongDataTransmission(); !errors.Is(err, ErrLongDataTransmissionNotStarted) {
			t.Errorf("End = %v, want ErrLongDataTransmissionNotStarted", err)
		}
		if locker.unlocks != 0 {
			t.Error("released a lock it never took")
		}
	})

	// Started and never fed: no message was ever opened on the wire, so there is
	// nothing to terminate.
	t.Run("writes nothing when no fragment was transmitted", func(t *testing.T) {
		wsConn, netConn, locker := transmitting(t, OpcodeBinary)

		if err := wsConn.EndLongDataTransmission(); err != nil {
			t.Fatalf("End: %v", err)
		}
		if len(netConn.written()) != 0 {
			t.Errorf("%d bytes reached the socket", len(netConn.written()))
		}
		if !locker.ok(1) {
			t.Errorf("locks=%d unlocks=%d held=%v, want 1/1/false",
				locker.locks, locker.unlocks, locker.held)
		}
	})

	t.Run("sends the held fragment with FIN", func(t *testing.T) {
		wsConn, netConn, locker := transmitting(t, OpcodeText)

		if err := wsConn.TransmitData([]byte("hello")); err != nil {
			t.Fatalf("TransmitData: %v", err)
		}
		if err := wsConn.EndLongDataTransmission(); err != nil {
			t.Fatalf("End: %v", err)
		}

		sent := framesOn(t, netConn)
		if len(sent) != 1 {
			t.Fatalf("%d frames on the wire, want 1", len(sent))
		}
		if !sent[0].FIN {
			t.Error("FIN was not set — the peer would wait for more")
		}
		if !locker.ok(1) {
			t.Errorf("locks=%d unlocks=%d held=%v, want 1/1/false",
				locker.locks, locker.unlocks, locker.held)
		}
	})

	// A deferred End has to free the connection whatever happened, or one dead
	// socket blocks every other sender for good.
	t.Run("releases the lock even when the write fails", func(t *testing.T) {
		wantErr := errors.New("socket gone")
		wsConn, netConn, locker := transmitting(t, OpcodeText)

		if err := wsConn.TransmitData([]byte("hello")); err != nil {
			t.Fatalf("TransmitData: %v", err)
		}
		netConn.writeErr = wantErr

		if err := wsConn.EndLongDataTransmission(); !errors.Is(err, wantErr) {
			t.Errorf("End = %v, want %v", err, wantErr)
		}
		if !locker.ok(1) {
			t.Errorf("locks=%d unlocks=%d held=%v, want 1/1/false",
				locker.locks, locker.unlocks, locker.held)
		}
		if wsConn.currentTransmitDataFrame != nil || wsConn.currentTransmitDataMsgOpcode != 0 {
			t.Error("state survived a failed end, so the next transmission inherits it")
		}
	})
}

/*
The whole thing, read back off the wire. RFC 6455 5.4 makes a message the
concatenation of its fragments, with the opcode on the first frame,
OpcodeContinuation on the rest, and FIN only on the last — get any of those
wrong and a conforming peer closes the connection instead of assembling this.
*/
func TestConnLongDataTransmissionWholeMessage(t *testing.T) {
	wsConn, netConn, locker := transmitting(t, OpcodeText)

	for _, chunk := range []string{"hello ", "long ", "world"} {
		if err := wsConn.TransmitData([]byte(chunk)); err != nil {
			t.Fatalf("TransmitData(%q): %v", chunk, err)
		}
	}
	if err := wsConn.EndLongDataTransmission(); err != nil {
		t.Fatalf("End: %v", err)
	}

	sent := framesOn(t, netConn)
	if len(sent) != 3 {
		t.Fatalf("%d frames on the wire, want 3", len(sent))
	}

	wantOpcodes := []byte{OpcodeText, OpcodeContinuation, OpcodeContinuation}
	for i, f := range sent {
		if f.Opcode != wantOpcodes[i] {
			t.Errorf("frame %d opcode = %#x, want %#x", i, f.Opcode, wantOpcodes[i])
		}
		if wantFIN := i == len(sent)-1; f.FIN != wantFIN {
			t.Errorf("frame %d FIN = %v, want %v", i, f.FIN, wantFIN)
		}
	}
	if got := sent.Bytes(); !bytes.Equal(got, []byte("hello long world")) {
		t.Errorf("assembled %q, want %q", got, "hello long world")
	}

	// The connection is free and clean, so the next message starts fresh.
	if !locker.ok(1) {
		t.Errorf("locks=%d unlocks=%d held=%v, want 1/1/false",
			locker.locks, locker.unlocks, locker.held)
	}
	if err := wsConn.StartLongDataTransmission(OpcodeBinary); err != nil {
		t.Errorf("a second transmission was refused: %v", err)
	}
}
