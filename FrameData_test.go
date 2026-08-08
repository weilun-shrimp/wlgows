package wlgows

import (
	"errors"
	"testing"
)

func TestNewDataFrame(t *testing.T) {
	// A control opcode slipping through would build a frame with no 125 byte cap
	// and no forced FIN, which RFC 6455 5.5 requires of every control frame.
	//
	// 0x10 and 0x80 are not opcodes at all: the field is 4 bits and Seal masks
	// with &15, so accepting them would emit a continuation frame.
	t.Run("refuses anything that is not continuation, text or binary", func(t *testing.T) {
		for _, opcode := range []byte{
			OpcodeClose, OpcodePing, OpcodePong, // control
			0x3, 0x7, 0xB, 0xF, // reserved by 5.2
			0x10, 0x80, // out of range
		} {
			f, err := NewDataFrame(NewFrameConfig{Opcode: opcode})
			if !errors.Is(err, ErrNotDataFrameOpcode) {
				t.Errorf("opcode %#x: err = %v, want it to wrap ErrNotDataFrameOpcode", opcode, err)
			}
			if f != nil {
				t.Errorf("opcode %#x: a refused config must not yield a frame", opcode)
			}
		}
	})

	t.Run("accepts the three data opcodes", func(t *testing.T) {
		for _, opcode := range []byte{OpcodeContinuation, OpcodeText, OpcodeBinary} {
			if _, err := NewDataFrame(NewFrameConfig{Opcode: opcode}); err != nil {
				t.Errorf("opcode %#x should be accepted, got %v", opcode, err)
			}
		}
	})

	/*
		The difference from NewControlFrame. Forcing FIN would make 5.4's
		fragmentation unreachable, so a false stays false and the caller decides
		which frame ends the message.
	*/
	t.Run("leaves FIN alone", func(t *testing.T) {
		for _, fin := range []bool{false, true} {
			f, err := NewDataFrame(NewFrameConfig{Opcode: OpcodeText, FIN: fin})
			if err != nil {
				t.Fatalf("NewDataFrame: %v", err)
			}
			if f.FIN != fin {
				t.Errorf("FIN = %v, want %v", f.FIN, fin)
			}
		}
	})

	// The 125 byte cap is a control frame rule (5.5). A data frame is bounded
	// only by what the length field can encode.
	t.Run("does not cap the payload", func(t *testing.T) {
		payload := make([]byte, ControlFramePayloadMaxByteLength*10)
		f, err := NewDataFrame(NewFrameConfig{PayloadData: payload, Opcode: OpcodeBinary, FIN: true})
		if err != nil {
			t.Fatalf("NewDataFrame: %v", err)
		}
		if f.GetMaxPayloadLength() != uint64(len(payload)) {
			t.Errorf("payload length = %d, want %d", f.GetMaxPayloadLength(), len(payload))
		}
	})
}
