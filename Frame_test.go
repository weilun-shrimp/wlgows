package wlgows

import (
	"bytes"
	"errors"
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

/*
NewFrame is the single place the RFC 6455 5.2 length encoding lives, so these
pin every boundary in it: inline up to 125, a 16 bit extended length up to
65535, a 64 bit one above that.
*/
func TestNewFrameLengthEncoding(t *testing.T) {
	tests := []struct {
		name         string
		size         int
		wantLen      byte
		wantExtended uint64
	}{
		{"0 bytes", 0, 0, 0},
		{"1 byte", 1, 1, 0},
		{"125 bytes uses the 7 bit length", 125, 125, 0},
		{"126 bytes switches to the 16 bit length", 126, 126, 126},
		{"65535 bytes is the 16 bit ceiling", 65535, 126, 65535},
		{"65536 bytes switches to the 64 bit length", 65536, 127, 65536},
	}
	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			frame, err := newFrame(NewFrameConfig{
				Data: bytes.Repeat([]byte{'x'}, testCase.size), Opcode: 1, FIN: true,
			}, newFrameDI{
				generateMaskingKey: func() ([]byte, error) { return nil, nil },
			})
			if err != nil {
				t.Fatalf("newFrame: %v", err)
			}
			if !frame.FIN {
				t.Error("NewFrame should return a final frame")
			}
			if frame.PayloadLength != testCase.wantLen {
				t.Errorf("PayloadLength = %d, want %d", frame.PayloadLength, testCase.wantLen)
			}
			if frame.ExtendedPayloadLength != testCase.wantExtended {
				t.Errorf("ExtendedPayloadLength = %d, want %d", frame.ExtendedPayloadLength, testCase.wantExtended)
			}
			if len(frame.PayloadData) != testCase.size {
				t.Errorf("len(PayloadData) = %d, want %d", len(frame.PayloadData), testCase.size)
			}
		})
	}
}

// Empty data still produces a frame — a zero length frame is legal and is how
// an empty text message or a bare close goes out.
func TestNewFrameEmptyDataStillProducesAFrame(t *testing.T) {
	frame, err := NewFrame(NewFrameConfig{Opcode: 1, FIN: true})
	if err != nil {
		t.Fatalf("NewFrame: %v", err)
	}
	if frame == nil {
		t.Fatal("NewFrame must never return a nil frame without an error")
	}
	if frame.PayloadLength != 0 || len(frame.PayloadData) != 0 {
		t.Errorf("PayloadLength = %d, len(PayloadData) = %d, want 0 and 0",
			frame.PayloadLength, len(frame.PayloadData))
	}
}

func TestNewFrameMasking(t *testing.T) {
	t.Run("masks with the injected key", func(t *testing.T) {
		frame, err := newFrame(NewFrameConfig{
			Data: []byte("hi"), Opcode: 1, Mask: true, FIN: true,
		}, newFrameDI{
			generateMaskingKey: func() ([]byte, error) { return []byte{1, 2, 3, 4}, nil },
		})
		if err != nil {
			t.Fatalf("newFrame: %v", err)
		}
		if !frame.Mask {
			t.Error("Mask should be true")
		}
		if !bytes.Equal(frame.MaskingKey, []byte{1, 2, 3, 4}) {
			t.Errorf("MaskingKey = % x", frame.MaskingKey)
		}
		// PayloadData stays readable; Seal is what applies the mask.
		if string(frame.PayloadData) != "hi" {
			t.Errorf("PayloadData = %q, want it unmasked in the struct", frame.PayloadData)
		}
		// A fixed key makes the sealed bytes fully assertable.
		want := []byte{0x81, 0x82, 1, 2, 3, 4, 'h' ^ 1, 'i' ^ 2}
		if got := frame.Seal(); !bytes.Equal(got, want) {
			t.Errorf("Seal() = % x, want % x", got, want)
		}
	})

	t.Run("leaves the frame unmasked when not requested", func(t *testing.T) {
		frame, err := newFrame(NewFrameConfig{
			Data: []byte("hi"), Opcode: 1, FIN: true,
		}, newFrameDI{
			generateMaskingKey: func() ([]byte, error) {
				t.Fatal("generateMaskingKey must not be called when Mask is false")
				return nil, nil
			},
		})
		if err != nil {
			t.Fatalf("newFrame: %v", err)
		}
		if frame.Mask || frame.MaskingKey != nil {
			t.Error("frame should be unmasked with no key")
		}
	})

	t.Run("propagates a masking key error", func(t *testing.T) {
		want := errors.New("no entropy")
		_, err := newFrame(NewFrameConfig{
			Data: []byte("hi"), Opcode: 1, Mask: true, FIN: true,
		}, newFrameDI{
			generateMaskingKey: func() ([]byte, error) { return nil, want },
		})
		if !errors.Is(err, want) {
			t.Errorf("err = %v, want %v", err, want)
		}
	})
}

func TestNewFrameOpcode(t *testing.T) {
	for _, opcode := range []uint8{0, 1, 2, 8, 9, 10} {
		frame, err := newFrame(NewFrameConfig{
			Data: []byte("x"), Opcode: opcode, FIN: true,
		}, newFrameDI{
			generateMaskingKey: func() ([]byte, error) { return nil, nil },
		})
		if err != nil {
			t.Fatalf("newFrame: %v", err)
		}
		if frame.Opcode != opcode {
			t.Errorf("Opcode = %d, want %d", frame.Opcode, opcode)
		}
	}
}

func TestGenerateMaskingKey(t *testing.T) {
	t.Run("returns the injected bytes", func(t *testing.T) {
		got, err := generateMaskingKey(generateMaskingKeyDI{
			randRead: fixedRandRead(0xAA, 0xBB, 0xCC, 0xDD),
		})
		if err != nil {
			t.Fatalf("generateMaskingKey: %v", err)
		}
		if !bytes.Equal(got, []byte{0xAA, 0xBB, 0xCC, 0xDD}) {
			t.Errorf("got % x", got)
		}
	})

	t.Run("propagates the rand error", func(t *testing.T) {
		want := errors.New("entropy exhausted")
		got, err := generateMaskingKey(generateMaskingKeyDI{
			randRead: func([]byte) (int, error) { return 0, want },
		})
		if !errors.Is(err, want) {
			t.Errorf("err = %v, want %v", err, want)
		}
		if got != nil {
			t.Errorf("key should be nil on error, got % x", got)
		}
	})

	t.Run("real wiring returns the 4 bytes RFC 6455 requires", func(t *testing.T) {
		got, err := GenerateMaskingKey()
		if err != nil {
			t.Fatalf("GenerateMaskingKey: %v", err)
		}
		if len(got) != 4 {
			t.Errorf("len = %d, want 4", len(got))
		}
	})
}

/*
FIN is now the caller's to set, and its zero value is false. That is the one
sharp edge of the config struct: a caller who omits it builds a frame the peer
treats as fragmented and then waits for a continuation of.
*/
func TestNewFrameFIN(t *testing.T) {
	t.Run("carries FIN through", func(t *testing.T) {
		for _, want := range []bool{true, false} {
			frame, err := NewFrame(NewFrameConfig{Data: []byte("x"), Opcode: 1, FIN: want})
			if err != nil {
				t.Fatalf("NewFrame: %v", err)
			}
			if frame.FIN != want {
				t.Errorf("FIN = %v, want %v", frame.FIN, want)
			}
			// Seal puts FIN in the top bit of byte 0.
			if got := frame.Seal()[0]>>7 == 1; got != want {
				t.Errorf("sealed FIN bit = %v, want %v", got, want)
			}
		}
	})

	// Pins the zero value so a change to it cannot pass unnoticed.
	t.Run("omitting FIN yields a non final frame", func(t *testing.T) {
		frame, err := NewFrame(NewFrameConfig{Data: []byte("x"), Opcode: 1})
		if err != nil {
			t.Fatalf("NewFrame: %v", err)
		}
		if frame.FIN {
			t.Error("an omitted FIN must stay false")
		}
	})

	// The fragmentation shape the doc comment describes, round tripped through
	// Seal and back, then reassembled by Frames.
	t.Run("a fragmented message reassembles", func(t *testing.T) {
		head, err := NewFrame(NewFrameConfig{Data: []byte("中文"), Opcode: 1})
		if err != nil {
			t.Fatalf("NewFrame head: %v", err)
		}
		tail, err := NewFrame(NewFrameConfig{Data: []byte("字"), Opcode: 0, FIN: true})
		if err != nil {
			t.Fatalf("NewFrame tail: %v", err)
		}
		if head.FIN {
			t.Error("the leading frame must not set FIN")
		}
		if !tail.FIN {
			t.Error("the final frame must set FIN")
		}
		if tail.Opcode != 0 {
			t.Errorf("continuation Opcode = %d, want 0", tail.Opcode)
		}
		if got := (Frames{head, tail}).String(); got != "中文字" {
			t.Errorf("reassembled = %q, want 中文字", got)
		}
	})
}
