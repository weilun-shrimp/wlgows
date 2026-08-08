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
		if len(netConn.written()) != 0 {
			t.Errorf("%d bytes reached the socket", len(netConn.written()))
		}
		if wsConn.currentTransmitDataMsgOpened {
			t.Error("an empty fragment opened the message")
		}
	})

	// Nothing is held back, which is what lets a caller reuse its read buffer.
	t.Run("sends each fragment before returning", func(t *testing.T) {
		wsConn, netConn, _ := transmitting(t, OpcodeText)

		if err := wsConn.TransmitData([]byte("hello ")); err != nil {
			t.Fatalf("TransmitData: %v", err)
		}
		if sent := framesOn(t, netConn); len(sent) != 1 {
			t.Fatalf("%d frames on the wire after one fragment, want 1", len(sent))
		}

		if err := wsConn.TransmitData([]byte("world")); err != nil {
			t.Fatalf("TransmitData: %v", err)
		}

		sent := framesOn(t, netConn)
		if len(sent) != 2 {
			t.Fatalf("%d frames on the wire, want 2", len(sent))
		}
		// 5.4: the message's opcode opens it, the next frame continues it.
		if sent[0].Opcode != OpcodeText || sent[1].Opcode != OpcodeContinuation {
			t.Errorf("opcodes %#x, %#x — want %#x then %#x",
				sent[0].Opcode, sent[1].Opcode, OpcodeText, OpcodeContinuation)
		}
		for i, f := range sent {
			if f.FIN {
				t.Errorf("frame %d set FIN before the message ended", i)
			}
		}
	})

	/*
		The reason nothing is held. A caller streaming a file reads into one
		buffer over and over, which is what io.Copy does too — a frame keeping a
		window onto that buffer would go out carrying the next chunk's bytes,
		with every length still correct and only the contents wrong.
	*/
	t.Run("does not retain the caller's buffer", func(t *testing.T) {
		wsConn, netConn, _ := transmitting(t, OpcodeBinary)

		buf := make([]byte, 4)
		copy(buf, "AAAA")
		if err := wsConn.TransmitData(buf); err != nil {
			t.Fatalf("TransmitData: %v", err)
		}
		copy(buf, "BBBB") // the caller reuses its buffer for the next read
		if err := wsConn.TransmitData(buf); err != nil {
			t.Fatalf("TransmitData: %v", err)
		}
		if err := wsConn.EndLongDataTransmission(); err != nil {
			t.Fatalf("End: %v", err)
		}

		if got := framesOn(t, netConn).Bytes(); !bytes.Equal(got, []byte("AAAABBBB")) {
			t.Errorf("wire carries %q, want %q", got, "AAAABBBB")
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

	// The failing call is the failing fragment — nothing is queued, so an error
	// never points at an earlier one.
	t.Run("propagates a write error", func(t *testing.T) {
		wantErr := errors.New("socket gone")
		wsConn, netConn, _ := transmitting(t, OpcodeText)
		netConn.writeErr = wantErr

		if err := wsConn.TransmitData([]byte("hello")); !errors.Is(err, wantErr) {
			t.Errorf("TransmitData = %v, want %v", err, wantErr)
		}
		if wsConn.currentTransmitDataMsgOpened {
			t.Error("a fragment that never reached the socket opened the message")
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

	/*
		Started and never fed — an empty file streams exactly like this. No
		message was opened on the wire, so a terminator would reach the peer as
		a continuation with nothing to continue, and cost the connection.
	*/
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

	/*
		5.4 terminates a message with opcode 0 and FIN set, and constrains its
		length not at all — so an empty one ends the message without holding a
		fragment back to put FIN on.
	*/
	t.Run("terminates with an empty continuation carrying FIN", func(t *testing.T) {
		wsConn, netConn, locker := transmitting(t, OpcodeText)

		if err := wsConn.TransmitData([]byte("hello")); err != nil {
			t.Fatalf("TransmitData: %v", err)
		}
		if err := wsConn.EndLongDataTransmission(); err != nil {
			t.Fatalf("End: %v", err)
		}

		sent := framesOn(t, netConn)
		if len(sent) != 2 {
			t.Fatalf("%d frames on the wire, want 2 — the fragment and the terminator", len(sent))
		}
		last := sent[len(sent)-1]
		if last.Opcode != OpcodeContinuation {
			t.Errorf("terminator opcode = %#x, want %#x", last.Opcode, OpcodeContinuation)
		}
		if !last.FIN {
			t.Error("terminator did not set FIN — the peer would wait for more")
		}
		if len(last.PayloadData) != 0 {
			t.Errorf("terminator carries %d bytes, want none", len(last.PayloadData))
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
		if wsConn.currentTransmitDataMsgOpcode != 0 || wsConn.currentTransmitDataMsgOpened {
			t.Error("state survived a failed end, so the next transmission inherits it")
		}
	})

	t.Run("propagates a build error and still unlocks", func(t *testing.T) {
		wantErr := errors.New("cannot build")
		wsConn, _, locker := transmitting(t, OpcodeText)
		if err := wsConn.TransmitData([]byte("hello")); err != nil {
			t.Fatalf("TransmitData: %v", err)
		}
		wsConn.di.newDataFrame = func(NewFrameConfig) (*Frame, error) { return nil, wantErr }

		if err := wsConn.EndLongDataTransmission(); !errors.Is(err, wantErr) {
			t.Errorf("End = %v, want %v", err, wantErr)
		}
		if !locker.ok(1) {
			t.Errorf("locks=%d unlocks=%d held=%v, want 1/1/false",
				locker.locks, locker.unlocks, locker.held)
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
	if len(sent) != 4 {
		t.Fatalf("%d frames on the wire, want 4 — three fragments and the terminator", len(sent))
	}

	wantOpcodes := []byte{OpcodeText, OpcodeContinuation, OpcodeContinuation, OpcodeContinuation}
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
