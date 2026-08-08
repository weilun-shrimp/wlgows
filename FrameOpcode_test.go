package wlgows

import "testing"

// The RFC 6455 5.2 values. Pinned because they are wire format: a rename is
// harmless, a changed number silently breaks every peer.
func TestOpcodeConstants(t *testing.T) {
	tests := []struct {
		name     string
		constant byte
		want     byte
	}{
		{"continuation", OpcodeContinuation, 0x0},
		{"text", OpcodeText, 0x1},
		{"binary", OpcodeBinary, 0x2},
		{"close", OpcodeClose, 0x8},
		{"ping", OpcodePing, 0x9},
		{"pong", OpcodePong, 0xA},
	}
	for _, testCase := range tests {
		if testCase.constant != testCase.want {
			t.Errorf("%s = %#x, want %#x", testCase.name, testCase.constant, testCase.want)
		}
	}
}

// 0x10 and 0x80 are in the tables because Frame.Opcode is a byte while the
// field is 4 bits — neither is an opcode, so neither belongs to a class.
func TestIsControlOpcode(t *testing.T) {
	for _, opcode := range []byte{OpcodeClose, OpcodePing, OpcodePong} {
		if !IsControlOpcode(opcode) {
			t.Errorf("IsControlOpcode(%#x) = false, want true", opcode)
		}
	}
	for _, opcode := range []byte{
		OpcodeContinuation, OpcodeText, OpcodeBinary,
		0x3, 0x7, 0xB, 0xF, // reserved by RFC 6455 5.2
		0x10, 0x80, // out of range
	} {
		if IsControlOpcode(opcode) {
			t.Errorf("IsControlOpcode(%#x) = true, want false", opcode)
		}
	}
}

func TestIsDataOpcode(t *testing.T) {
	for _, opcode := range []byte{OpcodeContinuation, OpcodeText, OpcodeBinary} {
		if !IsDataOpcode(opcode) {
			t.Errorf("IsDataOpcode(%#x) = false, want true", opcode)
		}
	}
	for _, opcode := range []byte{
		OpcodeClose, OpcodePing, OpcodePong,
		0x3, 0x7, 0xB, 0xF, // reserved by RFC 6455 5.2
		0x10, 0x80, // out of range
	} {
		if IsDataOpcode(opcode) {
			t.Errorf("IsDataOpcode(%#x) = true, want false", opcode)
		}
	}
}
