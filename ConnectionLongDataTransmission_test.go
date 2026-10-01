package wlgows

import (
	"bufio"
	"errors"
	"testing"
)

func TestConnStartLongDataTransmission(t *testing.T) {
	base_conn := func() *Conn {
		netConn := newFakeConn(nil)
		return NewConn(netConn, bufio.NewReader(netConn), false)
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
			dataLocker := &fakeLocker{}
			conn := base_conn()
			conn.di.dataFramesWriteLocker = dataLocker

			if err := conn.StartLongDataTransmission(testCase.opcode); !errors.Is(err, testCase.want) {
				t.Errorf("opcode %#x: err = %v, want %v", testCase.opcode, err, testCase.want)
			}
			if dataLocker.locks != 0 || conn.currentTransmitDataMsgOpcode != 0 {
				t.Errorf("opcode %#x: a refused start opened a transmission", testCase.opcode)
			}
		}
	})

	// closeSent is read under the write lock, which is released before the
	// data lock is taken and held for the whole message.
	t.Run("claims the connection for a data opcode", func(t *testing.T) {
		for _, opcode := range []byte{OpcodeText, OpcodeBinary} {
			writeLocker, dataLocker := &fakeLocker{}, &fakeLocker{}
			conn := base_conn()
			conn.di.writeLocker, conn.di.dataFramesWriteLocker = writeLocker, dataLocker

			if err := conn.StartLongDataTransmission(opcode); err != nil {
				t.Fatalf("opcode %#x: %v", opcode, err)
			}
			if !writeLocker.ok(1) {
				t.Errorf("opcode %#x: write locks=%d unlocks=%d, want 1/1", opcode, writeLocker.locks, writeLocker.unlocks)
			}
			if !dataLocker.held || dataLocker.locks != 1 {
				t.Errorf("opcode %#x: the data lock was not taken", opcode)
			}
			if conn.currentTransmitDataMsgOpcode != opcode {
				t.Errorf("opcode %#x: recorded %#x", opcode, conn.currentTransmitDataMsgOpcode)
			}
		}
	})

	// 5.5.1: no message can open once a close has gone out.
	t.Run("refuses after a close", func(t *testing.T) {
		writeLocker, dataLocker := &fakeLocker{}, &fakeLocker{}
		conn := base_conn()
		conn.di.writeLocker, conn.di.dataFramesWriteLocker = writeLocker, dataLocker
		conn.closeSent = true

		if err := conn.StartLongDataTransmission(OpcodeBinary); !errors.Is(err, ErrCloseAlreadySent) {
			t.Errorf("err = %v, want ErrCloseAlreadySent", err)
		}
		if !writeLocker.ok(1) {
			t.Errorf("write locks=%d unlocks=%d, want 1/1", writeLocker.locks, writeLocker.unlocks)
		}
		if dataLocker.locks != 0 || conn.currentTransmitDataMsgOpcode != 0 {
			t.Error("a refused start opened a transmission")
		}
	})
}

func TestTransmitData(t *testing.T) {
	// base_di has a text transmission open on a real Conn, and a sendFrame that
	// succeeds; each case overrides what it looks at.
	base_di := func() transmitDataDI {
		netConn := newFakeConn(nil)
		conn := NewConn(netConn, bufio.NewReader(netConn), false)
		conn.currentTransmitDataMsgOpcode = OpcodeText
		return transmitDataDI{
			conn:      conn,
			sendFrame: func(f *Frame) error { return nil },
		}
	}

	// 5.4: the message's opcode opens it, the rest continue it, and none of
	// them is the last. 5.1: masked exactly when the Conn masks.
	t.Run("sends the fragment's frame", func(t *testing.T) {
		for _, maskSendFrame := range []bool{false, true} {
			for _, opened := range []bool{false, true} {
				var sent []*Frame
				di := base_di()
				di.conn.maskSendFrame = maskSendFrame
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
				if len(sent) != 1 || sent[0].Opcode != wantOpcode || sent[0].Mask != maskSendFrame ||
					string(sent[0].PayloadData) != "hello" || sent[0].FIN {
					t.Fatalf("mask %v opened %v: sent %+v, want one frame opcode %#x mask %v \"hello\" no FIN",
						maskSendFrame, opened, sent, wantOpcode, maskSendFrame)
				}
				if !di.conn.currentTransmitDataMsgOpened {
					t.Error("a sent fragment did not open the message")
				}
			}
		}
	})

	// The check and the write share the lock SendClose sets closeSent under.
	t.Run("sends under the write lock", func(t *testing.T) {
		writeLocker := &fakeLocker{}
		di := base_di()
		di.conn.di.writeLocker = writeLocker
		di.sendFrame = func(*Frame) error {
			if !writeLocker.held {
				t.Error("sent without holding writeLocker")
			}
			return nil
		}

		if err := transmitData([]byte("hello"), di); err != nil {
			t.Fatalf("transmitData: %v", err)
		}
		if !writeLocker.ok(1) {
			t.Errorf("write locks=%d unlocks=%d, want 1/1", writeLocker.locks, writeLocker.unlocks)
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

	t.Run("drops empty data", func(t *testing.T) {
		di := base_di()
		di.sendFrame = func(*Frame) error {
			t.Error("sent an empty fragment")
			return nil
		}

		if err := transmitData(nil, di); err != nil {
			t.Fatalf("transmitData: %v", err)
		}
		if di.conn.currentTransmitDataMsgOpened {
			t.Error("an empty fragment opened the message")
		}
	})

	// 5.5.1: no data frame after a close, checked per fragment.
	t.Run("refuses after a close", func(t *testing.T) {
		writeLocker := &fakeLocker{}
		di := base_di()
		di.conn.di.writeLocker = writeLocker
		di.conn.closeSent = true
		di.sendFrame = func(*Frame) error {
			t.Error("sent after a close")
			return nil
		}

		if err := transmitData([]byte("hello"), di); !errors.Is(err, ErrCloseAlreadySent) {
			t.Errorf("transmitData = %v, want ErrCloseAlreadySent", err)
		}
		if !writeLocker.ok(1) {
			t.Errorf("write locks=%d unlocks=%d, want 1/1", writeLocker.locks, writeLocker.unlocks)
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
		conn := NewConn(netConn, bufio.NewReader(netConn), false)
		conn.currentTransmitDataMsgOpcode = OpcodeBinary
		return endLongDataTransmissionDI{
			conn:      conn,
			sendFrame: func(f *Frame) error { return nil },
		}
	}

	// FIN set, masked exactly when the Conn masks (5.1), and the message's own
	// opcode only when nothing went before (5.4).
	t.Run("sends the last frame", func(t *testing.T) {
		for _, maskSendFrame := range []bool{false, true} {
			for _, opened := range []bool{false, true} {
				var sent []*Frame
				di := base_di()
				di.conn.maskSendFrame = maskSendFrame
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
				if len(sent) != 1 || sent[0].Opcode != wantOpcode || sent[0].Mask != maskSendFrame ||
					string(sent[0].PayloadData) != "last" || !sent[0].FIN {
					t.Fatalf("mask %v opened %v: sent %+v, want one frame opcode %#x mask %v \"last\" FIN",
						maskSendFrame, opened, sent, wantOpcode, maskSendFrame)
				}
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
		writeLocker, dataLocker := &fakeLocker{}, &fakeLocker{}
		di := base_di()
		di.conn.di.writeLocker = writeLocker
		di.conn.di.dataFramesWriteLocker = dataLocker
		di.sendFrame = func(*Frame) error {
			if !writeLocker.held {
				t.Error("sent without holding writeLocker")
			}
			return nil
		}

		if err := endLongDataTransmission(nil, di); err != nil {
			t.Fatalf("endLongDataTransmission: %v", err)
		}
		if !writeLocker.ok(1) {
			t.Errorf("write locks=%d unlocks=%d, want 1/1", writeLocker.locks, writeLocker.unlocks)
		}
		if dataLocker.unlocks != 0 || di.conn.currentTransmitDataMsgOpcode == 0 {
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

	// The FIN frame is a data frame too (5.5.1).
	t.Run("refuses after a close", func(t *testing.T) {
		writeLocker := &fakeLocker{}
		di := base_di()
		di.conn.di.writeLocker = writeLocker
		di.conn.closeSent = true
		di.sendFrame = func(*Frame) error {
			t.Error("sent after a close")
			return nil
		}

		if err := endLongDataTransmission(nil, di); !errors.Is(err, ErrCloseAlreadySent) {
			t.Errorf("endLongDataTransmission = %v, want ErrCloseAlreadySent", err)
		}
		if !writeLocker.ok(1) {
			t.Errorf("write locks=%d unlocks=%d, want 1/1", writeLocker.locks, writeLocker.unlocks)
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
// would send its first frame as a continuation, a held lock would block it.
func TestConnReleaseLongDataTransmission(t *testing.T) {
	dataLocker := &fakeLocker{locks: 1, held: true}
	netConn := newFakeConn(nil)
	conn := NewConn(netConn, bufio.NewReader(netConn), false)
	conn.di.dataFramesWriteLocker = dataLocker
	conn.currentTransmitDataMsgOpcode = OpcodeText
	conn.currentTransmitDataMsgOpened = true

	conn.ReleaseLongDataTransmission()

	if conn.currentTransmitDataMsgOpcode != 0 || conn.currentTransmitDataMsgOpened {
		t.Errorf("opcode=%#x opened=%v, want both reset",
			conn.currentTransmitDataMsgOpcode, conn.currentTransmitDataMsgOpened)
	}
	if !dataLocker.ok(1) {
		t.Errorf("data locks=%d unlocks=%d misuse=%d held=%v, want the one lock released",
			dataLocker.locks, dataLocker.unlocks, dataLocker.misuse, dataLocker.held)
	}
}
