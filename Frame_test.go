package wlgows

import (
	"bytes"
	"testing"
)

func TestBoolToInt(t *testing.T) {
	if got := boolToInt(true); got != 1 {
		t.Errorf("boolToInt(true) = %d, want 1", got)
	}
	if got := boolToInt(false); got != 0 {
		t.Errorf("boolToInt(false) = %d, want 0", got)
	}
}

func TestFrameGetMaxPayloadLength(t *testing.T) {
	tests := []struct {
		name          string
		payloadLength byte
		extended      uint64
		want          uint64
	}{
		{"small length is used directly", 125, 0, 125},
		{"zero length", 0, 999, 0},
		{"126 defers to extended", 126, 300, 300},
		{"127 defers to extended", 127, 70000, 70000},
	}
	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			frame := &Frame{PayloadLength: testCase.payloadLength, ExtendedPayloadLength: testCase.extended}
			if got := frame.GetMaxPayloadLength(); got != testCase.want {
				t.Errorf("GetMaxPayloadLength() = %d, want %d", got, testCase.want)
			}
		})
	}
}

func TestFrameSeal(t *testing.T) {
	tests := []struct {
		name  string
		frame Frame
		want  []byte
	}{
		{
			name: "unmasked text frame",
			frame: Frame{
				FIN: true, Opcode: 1, PayloadLength: 2, PayloadData: []byte("hi"),
			},
			want: []byte{0x81, 0x02, 'h', 'i'},
		},
		{
			name: "masked text frame xors the payload",
			frame: Frame{
				FIN: true, Opcode: 1, Mask: true, PayloadLength: 2,
				MaskingKey: []byte{1, 2, 3, 4}, PayloadData: []byte("hi"),
			},
			want: []byte{0x81, 0x82, 1, 2, 3, 4, 'h' ^ 1, 'i' ^ 2},
		},
		{
			name: "rsv bits and non-final frame",
			frame: Frame{
				FIN: false, RSV1: true, RSV2: true, RSV3: true, Opcode: 2,
				PayloadLength: 1, PayloadData: []byte{0xFF},
			},
			want: []byte{0x72, 0x01, 0xFF},
		},
		{
			name: "opcode is truncated to 4 bits",
			frame: Frame{
				FIN: true, Opcode: 0xFF, PayloadLength: 0, PayloadData: []byte{},
			},
			want: []byte{0x8F, 0x00},
		},
		{
			name: "126 writes a 2 byte extended length",
			frame: Frame{
				FIN: true, Opcode: 1, PayloadLength: 126, ExtendedPayloadLength: 300,
				PayloadData: bytes.Repeat([]byte{'a'}, 300),
			},
			want: append([]byte{0x81, 0x7E, 0x01, 0x2C}, bytes.Repeat([]byte{'a'}, 300)...),
		},
		{
			name: "127 writes an 8 byte extended length",
			frame: Frame{
				FIN: true, Opcode: 2, PayloadLength: 127, ExtendedPayloadLength: 70000,
				PayloadData: bytes.Repeat([]byte{'b'}, 70000),
			},
			want: append(
				[]byte{0x82, 0x7F, 0, 0, 0, 0, 0, 0x01, 0x11, 0x70},
				bytes.Repeat([]byte{'b'}, 70000)...,
			),
		},
	}
	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			if got := testCase.frame.Seal(); !bytes.Equal(got, testCase.want) {
				if len(got) > 32 {
					t.Errorf("Seal() len = %d (want %d), head = % x (want % x)",
						len(got), len(testCase.want), got[:12], testCase.want[:12])
					return
				}
				t.Errorf("Seal() = % x, want % x", got, testCase.want)
			}
		})
	}
}

// Seal masks with key[i%4], so a payload longer than the key cycles it.
func TestFrameSealCyclesMaskingKey(t *testing.T) {
	frame := &Frame{
		FIN: true, Opcode: 1, Mask: true, PayloadLength: 6,
		MaskingKey: []byte{0x10, 0x20, 0x30, 0x40}, PayloadData: []byte("abcdef"),
	}
	got := frame.Seal()
	want := []byte{
		0x81, 0x86, 0x10, 0x20, 0x30, 0x40,
		'a' ^ 0x10, 'b' ^ 0x20, 'c' ^ 0x30, 'd' ^ 0x40, 'e' ^ 0x10, 'f' ^ 0x20,
	}
	if !bytes.Equal(got, want) {
		t.Errorf("Seal() = % x, want % x", got, want)
	}
}
