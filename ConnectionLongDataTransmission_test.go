package wlgows

import (
	"bufio"
	"errors"
	"fmt"
	"slices"
	"testing"
)

func TestConnStartLongDataTransmission(t *testing.T) {
	base_conn := func() *Conn {
		netConn := newFakeConn(nil)
		conn, _ := NewConn(netConn, bufio.NewReader(netConn), bufio.NewWriter(netConn), false)
		return conn
	}

	// A refused Start must leave the lock untaken, or the deferred Release a
	// caller writes after checking the error would unlock what nobody holds.
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
			var steps []string
			conn := base_conn()
			conn.di.writeLocker = fakeFuncLocker{
				lock:   func() { steps = append(steps, "write lock") },
				unlock: func() { steps = append(steps, "write unlock") },
			}
			conn.di.dataFramesWriteLocker = fakeFuncLocker{
				lock:   func() { steps = append(steps, "data lock") },
				unlock: func() { steps = append(steps, "data unlock") },
			}

			if err := conn.StartLongDataTransmission(testCase.opcode); !errors.Is(err, testCase.want) {
				t.Errorf("opcode %#x: err = %v, want %v", testCase.opcode, err, testCase.want)
			}
			if len(steps) != 0 || conn.currentTransmitDataMsgOpcode != 0 {
				t.Errorf("opcode %#x: steps %q, opcode %#x — want no lock taken and nothing opened",
					testCase.opcode, steps, conn.currentTransmitDataMsgOpcode)
			}
		}
	})

	// closeSent is read under the write lock, which is released before the
	// data lock is taken and held for the whole message.
	t.Run("claims the connection for a data opcode", func(t *testing.T) {
		for _, opcode := range []byte{OpcodeText, OpcodeBinary} {
			var steps []string
			conn := base_conn()
			conn.di.writeLocker = fakeFuncLocker{
				lock:   func() { steps = append(steps, "write lock") },
				unlock: func() { steps = append(steps, "write unlock") },
			}
			conn.di.dataFramesWriteLocker = fakeFuncLocker{
				lock:   func() { steps = append(steps, "data lock") },
				unlock: func() { steps = append(steps, "data unlock") },
			}

			if err := conn.StartLongDataTransmission(opcode); err != nil {
				t.Fatalf("opcode %#x: %v", opcode, err)
			}
			if want := []string{"write lock", "write unlock", "data lock"}; !slices.Equal(steps, want) {
				t.Errorf("opcode %#x: steps %q, want %q", opcode, steps, want)
			}
			if conn.currentTransmitDataMsgOpcode != opcode {
				t.Errorf("opcode %#x: recorded %#x", opcode, conn.currentTransmitDataMsgOpcode)
			}
		}
	})

	// 5.5.1: no message can open once a close has gone out.
	t.Run("refuses after a close", func(t *testing.T) {
		var steps []string
		conn := base_conn()
		conn.di.writeLocker = fakeFuncLocker{
			lock:   func() { steps = append(steps, "write lock") },
			unlock: func() { steps = append(steps, "write unlock") },
		}
		conn.di.dataFramesWriteLocker = fakeFuncLocker{
			lock:   func() { steps = append(steps, "data lock") },
			unlock: func() { steps = append(steps, "data unlock") },
		}
		conn.closeSent = true

		if err := conn.StartLongDataTransmission(OpcodeBinary); !errors.Is(err, ErrCloseAlreadySent) {
			t.Errorf("err = %v, want ErrCloseAlreadySent", err)
		}
		if want := []string{"write lock", "write unlock"}; !slices.Equal(steps, want) {
			t.Errorf("steps %q, want %q", steps, want)
		}
		if conn.currentTransmitDataMsgOpcode != 0 {
			t.Error("a refused start opened a transmission")
		}
	})
}

func TestTransmitData(t *testing.T) {
	// base_di has a text transmission open on a real Conn, and a sendFrame that
	// succeeds; each case overrides what it looks at.
	base_di := func() transmitDataDI {
		netConn := newFakeConn(nil)
		conn, _ := NewConn(netConn, bufio.NewReader(netConn), bufio.NewWriter(netConn), false)
		conn.currentTransmitDataMsgOpcode = OpcodeText
		return transmitDataDI{
			conn:      conn,
			sendFrame: func(f *Frame) error { return nil },
		}
	}

	// 5.4: the message's opcode opens it, the rest continue it, and none of
	// them is the last. Masking is the send's, so the frame is built unmasked.
	t.Run("sends the fragment's frame", func(t *testing.T) {
		for _, opened := range []bool{false, true} {
			var sent []*Frame
			di := base_di()
			di.conn.currentTransmitDataMsgOpened = opened
			di.sendFrame = func(f *Frame) error {
				sent = append(sent, f)
				return nil
			}

			if err := transmitData([]byte("hello"), di); err != nil {
				t.Fatalf("transmitData: %v", err)
			}
			wantOpcode := uint8(OpcodeText)
			if opened {
				wantOpcode = OpcodeContinuation
			}
			if len(sent) != 1 || sent[0].Opcode != wantOpcode || sent[0].Mask ||
				string(sent[0].PayloadData) != "hello" || sent[0].FIN {
				t.Fatalf("opened %v: sent %+v, want one unmasked frame opcode %#x \"hello\" no FIN",
					opened, sent, wantOpcode)
			}
			if !di.conn.currentTransmitDataMsgOpened {
				t.Error("a sent fragment did not open the message")
			}
		}
	})

	// The write holds the lock closeSent is set under.
	t.Run("sends under the write lock", func(t *testing.T) {
		var steps []string
		di := base_di()
		di.conn.di.writeLocker = fakeFuncLocker{
			lock:   func() { steps = append(steps, "write lock") },
			unlock: func() { steps = append(steps, "write unlock") },
		}
		di.sendFrame = func(*Frame) error {
			steps = append(steps, "send")
			return nil
		}

		if err := transmitData([]byte("hello"), di); err != nil {
			t.Fatalf("transmitData: %v", err)
		}
		if want := []string{"write lock", "send", "write unlock"}; !slices.Equal(steps, want) {
			t.Errorf("steps %q, want %q", steps, want)
		}
	})

	t.Run("refuses with no transmission open", func(t *testing.T) {
		di := base_di()
		di.conn.currentTransmitDataMsgOpcode = 0
		di.sendFrame = func(*Frame) error {
			t.Error("sent with no transmission open")
			return nil
		}

		if err := transmitData([]byte("hello"), di); !errors.Is(err, ErrLongDataTransmissionNotStarted) {
			t.Errorf("transmitData = %v, want ErrLongDataTransmissionNotStarted", err)
		}
	})

	t.Run("sends empty data", func(t *testing.T) {
		var sent []*Frame
		di := base_di()
		di.sendFrame = func(f *Frame) error {
			sent = append(sent, f)
			return nil
		}

		if err := transmitData(nil, di); err != nil {
			t.Fatalf("transmitData: %v", err)
		}
		if len(sent) != 1 || sent[0].Opcode != OpcodeText || sent[0].Mask ||
			len(sent[0].PayloadData) != 0 || sent[0].FIN {
			t.Fatalf("sent %+v, want one unmasked empty frame opcode %#x no FIN", sent, OpcodeText)
		}
		if !di.conn.currentTransmitDataMsgOpened {
			t.Error("an empty fragment did not open the message")
		}
	})

	t.Run("propagates a build error", func(t *testing.T) {
		wantErr := errors.New("cannot build")
		di := base_di()
		di.conn.di.newDataFrame = func(NewFrameConfig) (*Frame, error) { return nil, wantErr }
		di.sendFrame = func(*Frame) error {
			t.Error("sent after a build failure")
			return nil
		}

		if err := transmitData([]byte("hello"), di); !errors.Is(err, wantErr) {
			t.Errorf("transmitData = %v, want %v", err, wantErr)
		}
	})

	// A fragment that never went out did not open the message.
	t.Run("propagates a send error", func(t *testing.T) {
		wantErr := errors.New("socket gone")
		di := base_di()
		di.sendFrame = func(*Frame) error { return wantErr }

		if err := transmitData([]byte("hello"), di); !errors.Is(err, wantErr) {
			t.Errorf("transmitData = %v, want %v", err, wantErr)
		}
		if di.conn.currentTransmitDataMsgOpened {
			t.Error("a failed fragment opened the message")
		}
	})
}

func TestEndLongDataTransmission(t *testing.T) {
	// base_di has a binary transmission open on a real Conn, and a sendFrame
	// that succeeds; each case overrides what it looks at.
	base_di := func() endLongDataTransmissionDI {
		netConn := newFakeConn(nil)
		conn, _ := NewConn(netConn, bufio.NewReader(netConn), bufio.NewWriter(netConn), false)
		conn.currentTransmitDataMsgOpcode = OpcodeBinary
		return endLongDataTransmissionDI{
			conn:      conn,
			sendFrame: func(f *Frame) error { return nil },
		}
	}

	// FIN set, unmasked as the send masks, and the message's own opcode only
	// when nothing went before (5.4).
	t.Run("sends the last frame", func(t *testing.T) {
		for _, opened := range []bool{false, true} {
			var sent []*Frame
			di := base_di()
			di.conn.currentTransmitDataMsgOpened = opened
			di.sendFrame = func(f *Frame) error {
				sent = append(sent, f)
				return nil
			}

			if err := endLongDataTransmission([]byte("last"), di); err != nil {
				t.Fatalf("endLongDataTransmission: %v", err)
			}
			wantOpcode := uint8(OpcodeBinary)
			if opened {
				wantOpcode = OpcodeContinuation
			}
			if len(sent) != 1 || sent[0].Opcode != wantOpcode || sent[0].Mask ||
				string(sent[0].PayloadData) != "last" || !sent[0].FIN {
				t.Fatalf("opened %v: sent %+v, want one unmasked frame opcode %#x \"last\" FIN",
					opened, sent, wantOpcode)
			}
		}
	})

	// 5.6 allows an empty message, so End sends one even with nothing before.
	t.Run("sends an empty last frame", func(t *testing.T) {
		var sent []*Frame
		di := base_di()
		di.sendFrame = func(f *Frame) error {
			sent = append(sent, f)
			return nil
		}

		if err := endLongDataTransmission(nil, di); err != nil {
			t.Fatalf("endLongDataTransmission: %v", err)
		}
		if len(sent) != 1 || sent[0].Opcode != OpcodeBinary || len(sent[0].PayloadData) != 0 || !sent[0].FIN {
			t.Fatalf("sent %+v, want one empty binary frame with FIN", sent)
		}
	})

	// Release frees the connection, not End.
	t.Run("sends under the write lock and keeps the data lock", func(t *testing.T) {
		var steps []string
		di := base_di()
		di.conn.di.writeLocker = fakeFuncLocker{
			lock:   func() { steps = append(steps, "write lock") },
			unlock: func() { steps = append(steps, "write unlock") },
		}
		di.conn.di.dataFramesWriteLocker = fakeFuncLocker{
			lock:   func() { steps = append(steps, "data lock") },
			unlock: func() { steps = append(steps, "data unlock") },
		}
		di.sendFrame = func(*Frame) error {
			steps = append(steps, "send")
			return nil
		}

		if err := endLongDataTransmission(nil, di); err != nil {
			t.Fatalf("endLongDataTransmission: %v", err)
		}
		if want := []string{"write lock", "send", "write unlock"}; !slices.Equal(steps, want) {
			t.Errorf("steps %q, want %q", steps, want)
		}
		if di.conn.currentTransmitDataMsgOpcode == 0 {
			t.Error("End released the transmission; Release does that")
		}
	})

	t.Run("refuses with no transmission open", func(t *testing.T) {
		di := base_di()
		di.conn.currentTransmitDataMsgOpcode = 0
		di.sendFrame = func(*Frame) error {
			t.Error("sent with no transmission open")
			return nil
		}

		if err := endLongDataTransmission(nil, di); !errors.Is(err, ErrLongDataTransmissionNotStarted) {
			t.Errorf("endLongDataTransmission = %v, want ErrLongDataTransmissionNotStarted", err)
		}
	})

	t.Run("propagates a build error", func(t *testing.T) {
		wantErr := errors.New("cannot build")
		di := base_di()
		di.conn.di.newDataFrame = func(NewFrameConfig) (*Frame, error) { return nil, wantErr }
		di.sendFrame = func(*Frame) error {
			t.Error("sent after a build failure")
			return nil
		}

		if err := endLongDataTransmission(nil, di); !errors.Is(err, wantErr) {
			t.Errorf("endLongDataTransmission = %v, want %v", err, wantErr)
		}
	})

	t.Run("propagates a send error", func(t *testing.T) {
		wantErr := errors.New("socket gone")
		di := base_di()
		di.sendFrame = func(*Frame) error { return wantErr }

		if err := endLongDataTransmission(nil, di); !errors.Is(err, wantErr) {
			t.Errorf("endLongDataTransmission = %v, want %v", err, wantErr)
		}
	})
}

// Both fields, or the next transmission inherits this one: a stale opened
// would send its first frame as a continuation. The unlock comes last, so the
// next transmission never sees them before they are reset.
func TestConnReleaseLongDataTransmission(t *testing.T) {
	var steps []string
	netConn := newFakeConn(nil)
	conn, _ := NewConn(netConn, bufio.NewReader(netConn), bufio.NewWriter(netConn), false)
	conn.di.dataFramesWriteLocker = fakeFuncLocker{
		lock: func() { steps = append(steps, "data lock") },
		unlock: func() {
			steps = append(steps, fmt.Sprintf("data unlock opcode=%#x opened=%v",
				conn.currentTransmitDataMsgOpcode, conn.currentTransmitDataMsgOpened))
		},
	}
	conn.currentTransmitDataMsgOpcode = OpcodeText
	conn.currentTransmitDataMsgOpened = true

	conn.ReleaseLongDataTransmission()

	if want := []string{"data unlock opcode=0x0 opened=false"}; !slices.Equal(steps, want) {
		t.Errorf("steps %q, want %q", steps, want)
	}
}
