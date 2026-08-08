package wlgows

import (
	"bytes"
	"errors"
	"strings"
	"testing"
)

func TestNewControlFrame(t *testing.T) {
	// Nothing in the type system says a byte is a control opcode. Without this
	// guard the frame would be built with FIN forced on and a 125 byte cap
	// applied — wrong for anything but a control frame, and impossible to
	// diagnose from the wire.
	//
	// 0x10 and 0x80 are not opcodes at all: the field is 4 bits, and Seal masks
	// with &15, so accepting them would emit a continuation frame.
	t.Run("refuses anything that is not close, ping or pong", func(t *testing.T) {
		for _, opcode := range []byte{
			OpcodeContinuation, OpcodeText, OpcodeBinary, // data
			0x3, 0x7, 0xB, 0xF, // reserved by 5.2
			0x10, 0x80, // out of range
		} {
			f, err := NewControlFrame(NewControlFrameConfig{Opcode: opcode})
			if !errors.Is(err, ErrNotControlFrameOpcode) {
				t.Errorf("opcode %#x: err = %v, want it to wrap ErrNotControlFrameOpcode", opcode, err)
			}
			if f != nil {
				t.Errorf("opcode %#x: a refused config must not yield a frame", opcode)
			}
		}
	})

	t.Run("accepts the three control opcodes", func(t *testing.T) {
		for _, opcode := range []byte{OpcodeClose, OpcodePing, OpcodePong} {
			if _, err := NewControlFrame(NewControlFrameConfig{Opcode: opcode}); err != nil {
				t.Errorf("opcode %#x should be accepted, got %v", opcode, err)
			}
		}
	})

	t.Run("refuses a payload over 125 bytes", func(t *testing.T) {
		f, err := NewControlFrame(NewControlFrameConfig{
			Opcode: OpcodePing, PayloadData: bytes.Repeat([]byte{'x'}, 126),
		})
		if !errors.Is(err, ErrControlFramePayloadTooLong) {
			t.Errorf("err = %v, want it to wrap ErrControlFramePayloadTooLong", err)
		}
		if f != nil {
			t.Error("a refused config must not yield a frame")
		}
	})

	t.Run("allows exactly 125 bytes", func(t *testing.T) {
		if _, err := NewControlFrame(NewControlFrameConfig{
			Opcode: OpcodePing, PayloadData: bytes.Repeat([]byte{'x'}, 125),
		}); err != nil {
			t.Errorf("125 bytes is the limit and must be accepted, got %v", err)
		}
	})

	// RFC 6455 5.5 forbids fragmenting a control frame, so FIN is not the
	// caller's to choose.
	t.Run("always sets FIN", func(t *testing.T) {
		f, err := NewControlFrame(NewControlFrameConfig{Opcode: OpcodePing})
		if err != nil {
			t.Fatalf("NewControlFrame: %v", err)
		}
		if !f.FIN {
			t.Error("a control frame must always set FIN")
		}
	})
}

/*
The three shapes RFC 6455 5.5.1 allows, and no fourth. The pointer is what
carries the distinction: a reason cannot exist without a status code, because it
is defined as whatever follows the two code bytes.
*/
func TestClosePayloadBytes(t *testing.T) {
	t.Run("a nil payload encodes no body at all", func(t *testing.T) {
		var payload *ClosePayload
		if got := payload.Bytes(); len(got) != 0 {
			t.Errorf("Bytes() = % x, want empty", got)
		}
	})

	t.Run("a status code alone is 2 bytes big endian", func(t *testing.T) {
		got := (&ClosePayload{StatusCode: CloseNormalClosure}).Bytes()
		if !bytes.Equal(got, []byte{0x03, 0xE8}) { // 1000
			t.Errorf("Bytes() = % x, want 03 e8", got)
		}
	})

	t.Run("a reason follows the status code", func(t *testing.T) {
		got := (&ClosePayload{StatusCode: CloseNormalClosure, Reason: "bye"}).Bytes()
		want := []byte{0x03, 0xE8, 'b', 'y', 'e'}
		if !bytes.Equal(got, want) {
			t.Errorf("Bytes() = % x, want % x", got, want)
		}
	})

	// The reason budget is bytes, not characters: 再見 is 2 characters and 6
	// bytes, so this payload is 8 and only 41 such characters fit in 123.
	t.Run("the reason is counted in bytes", func(t *testing.T) {
		got := (&ClosePayload{StatusCode: CloseGoingAway, Reason: "再見"}).Bytes()
		if len(got) != 8 {
			t.Errorf("Bytes() is %d bytes, want 8", len(got))
		}
	})

	// Never 1 byte: a status code cut in half is a protocol error, and this type
	// cannot express it.
	t.Run("no shape produces a one byte body", func(t *testing.T) {
		payloads := []*ClosePayload{
			nil,
			{},
			{StatusCode: CloseNormalClosure},
			{StatusCode: CloseNormalClosure, Reason: "x"},
		}
		for _, payload := range payloads {
			if got := len(payload.Bytes()); got == 1 {
				t.Errorf("%+v encoded to 1 byte, which RFC 6455 5.5.1 forbids", payload)
			}
		}
	})
}

// The status code spends 2 of the 125 bytes a control frame allows, so the
// reason is really capped at 123. Goes through NewControlFrame, since the cap is
// its rule and the arithmetic is ClosePayload's.
func TestClosePayloadAgainstTheControlFrameCap(t *testing.T) {
	for _, testCase := range []struct {
		reasonBytes int
		wantErr     bool
	}{
		{123, false},
		{124, true},
	} {
		payload := &ClosePayload{
			StatusCode: CloseNormalClosure,
			Reason:     strings.Repeat("x", testCase.reasonBytes),
		}
		_, err := NewControlFrame(NewControlFrameConfig{
			Opcode: OpcodeClose, PayloadData: payload.Bytes(),
		})
		if testCase.wantErr && !errors.Is(err, ErrControlFramePayloadTooLong) {
			t.Errorf("%d byte reason: err = %v, want the 2 byte status code counted",
				testCase.reasonBytes, err)
		}
		if !testCase.wantErr && err != nil {
			t.Errorf("%d byte reason fits exactly, got %v", testCase.reasonBytes, err)
		}
	}
}

func TestFrameGetClosePayload(t *testing.T) {
	t.Run("round trips what ClosePayload.Bytes produced", func(t *testing.T) {
		for _, want := range []*ClosePayload{
			{StatusCode: CloseNormalClosure},
			{StatusCode: CloseNormalClosure, Reason: "bye"},
			{StatusCode: CloseGoingAway, Reason: "再見"},
		} {
			f := &Frame{Opcode: OpcodeClose, PayloadData: want.Bytes()}
			got, err := f.GetClosePayload()
			if err != nil {
				t.Fatalf("GetClosePayload: %v", err)
			}
			if got.StatusCode != want.StatusCode || got.Reason != want.Reason {
				t.Errorf("got %+v, want %+v", got, want)
			}
		}
	})

	// An absent body is not status 0 — it is no status at all, which a caller
	// reports locally as 1005.
	t.Run("no body decodes to nil", func(t *testing.T) {
		got, err := (&Frame{Opcode: OpcodeClose}).GetClosePayload()
		if err != nil {
			t.Fatalf("GetClosePayload: %v", err)
		}
		if got != nil {
			t.Errorf("got %+v, want nil", got)
		}
	})

	// The shape RFC 6455 5.5.1 forbids. Without this the slice would panic.
	t.Run("a one byte body is refused", func(t *testing.T) {
		got, err := (&Frame{Opcode: OpcodeClose, PayloadData: []byte{0x03}}).GetClosePayload()
		if !errors.Is(err, ErrClosePayloadTooShort) {
			t.Errorf("err = %v, want it to wrap ErrClosePayloadTooShort", err)
		}
		if got != nil {
			t.Errorf("got %+v, want nil", got)
		}
	})

	t.Run("refuses a frame that is not a close frame", func(t *testing.T) {
		for _, opcode := range []byte{OpcodeText, OpcodeBinary, OpcodePing, OpcodePong} {
			_, err := (&Frame{Opcode: opcode, PayloadData: []byte{0x03, 0xE8}}).GetClosePayload()
			if !errors.Is(err, ErrNotCloseFrameOpcode) {
				t.Errorf("opcode %#x: err = %v, want it to wrap ErrNotCloseFrameOpcode", opcode, err)
			}
		}
	})
}
