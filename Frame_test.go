package wlgows

import (
	"bytes"
	"errors"
	"reflect"
	"testing"
)

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

func TestBoolToInt(t *testing.T) {
	if got := boolToInt(true); got != 1 {
		t.Errorf("boolToInt(true) = %d, want 1", got)
	}
	if got := boolToInt(false); got != 0 {
		t.Errorf("boolToInt(false) = %d, want 0", got)
	}
}

func TestFrameGetHeaderSize(t *testing.T) {
	for _, testCase := range []struct {
		payloadLength byte
		mask          bool
		want          int
	}{
		{0, false, 2}, {125, false, 2}, // the length fits in byte 1
		{126, false, 4},                               // a 2 byte extended length
		{127, false, 10},                              // an 8 byte extended length
		{0, true, 6}, {126, true, 8}, {127, true, 14}, // a 4 byte key on top
	} {
		f := &Frame{PayloadLength: testCase.payloadLength, Mask: testCase.mask}
		if got := f.getHeaderSize(); got != testCase.want {
			t.Errorf("PayloadLength %d mask %v: getHeaderSize = %d, want %d",
				testCase.payloadLength, testCase.mask, got, testCase.want)
		}
	}
}

func TestFrameAppendSealedHeader(t *testing.T) {
	for _, testCase := range []struct {
		name  string
		frame Frame
		want  []byte
	}{
		{
			"byte 0 carries FIN, RSV1-3 and the opcode",
			Frame{FIN: true, RSV1: true, RSV2: false, RSV3: true, Opcode: OpcodePing},
			[]byte{0b1101_1001, 0x00},
		},
		{
			"only the low 4 bits of the opcode",
			Frame{Opcode: 0xF2},
			[]byte{0x02, 0x00},
		},
		{
			"byte 1 carries MASK and the 7 bit length, then the key",
			Frame{Opcode: OpcodeText, Mask: true, PayloadLength: 125, MaskingKey: [4]byte{0xA, 0xB, 0xC, 0xD}},
			[]byte{0x01, 0x80 | 125, 0xA, 0xB, 0xC, 0xD},
		},
		{
			"126 is followed by a big endian uint16",
			Frame{Opcode: OpcodeBinary, PayloadLength: 126, ExtendedPayloadLength: 0xBEEF},
			[]byte{0x02, 126, 0xBE, 0xEF},
		},
		{
			"127 is followed by a big endian uint64, then the key",
			Frame{
				Opcode: OpcodeBinary, Mask: true, PayloadLength: 127,
				ExtendedPayloadLength: 0x0102030405060708, MaskingKey: [4]byte{9, 8, 7, 6},
			},
			[]byte{0x02, 0x80 | 127, 1, 2, 3, 4, 5, 6, 7, 8, 9, 8, 7, 6},
		},
		{
			"PayloadData is not written",
			Frame{Opcode: OpcodeText, PayloadLength: 3, PayloadData: []byte("abc")},
			[]byte{0x01, 3},
		},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			if got := testCase.frame.appendSealedHeader(nil); !bytes.Equal(got, testCase.want) {
				t.Errorf("appendSealedHeader(nil) = % x, want % x", got, testCase.want)
			}
		})
	}

	t.Run("appends after what buffer holds", func(t *testing.T) {
		f := &Frame{FIN: true, Opcode: OpcodeText}
		if got := f.appendSealedHeader([]byte("ab")); !bytes.Equal(got, []byte{'a', 'b', 0x81, 0x00}) {
			t.Errorf("appendSealedHeader = % x, want 61 62 81 00", got)
		}
	})

	// Room for the whole header is made once, up front, rather than at each
	// append. The longest header is 4 appends: bytes 0-1, the length, the key.
	t.Run("grows buffer at most once", func(t *testing.T) {
		f := &Frame{
			Opcode: OpcodeBinary, Mask: true, PayloadLength: 127,
			ExtendedPayloadLength: 1 << 20, MaskingKey: [4]byte{1, 2, 3, 4},
		}
		for _, buffer := range [][]byte{nil, make([]byte, 10, 12)} {
			var got []byte
			if allocs := testing.AllocsPerRun(10, func() { got = f.appendSealedHeader(buffer) }); allocs != 1 {
				t.Errorf("len %d cap %d: allocs = %v, want 1", len(buffer), cap(buffer), allocs)
			}
			if len(got) != len(buffer)+14 {
				t.Errorf("len %d cap %d: appended %d bytes, want 14", len(buffer), cap(buffer), len(got)-len(buffer))
			}
		}
	})

	t.Run("allocates nothing when buffer has room", func(t *testing.T) {
		f := &Frame{Opcode: OpcodeBinary, Mask: true, PayloadLength: 127, ExtendedPayloadLength: 1 << 20}
		buffer := make([]byte, 0, 14)
		if allocs := testing.AllocsPerRun(10, func() { f.appendSealedHeader(buffer) }); allocs != 0 {
			t.Errorf("allocs = %v, want 0", allocs)
		}
	})
}

// The want is masked with a plain byte loop. The lengths sit on each side of
// the 8 byte steps, so the tail always starts on key[0].
func TestMaskPayload(t *testing.T) {
	key := [4]byte{0xA5, 0x5A, 0xF0, 0x0F}
	for _, length := range []int{0, 1, 2, 3, 4, 5, 7, 8, 9, 11, 16, 19, 64, 1001} {
		payload := make([]byte, length)
		for i := range payload {
			payload[i] = byte(i*13 + 3)
		}
		want := make([]byte, length)
		for i := range payload {
			want[i] = payload[i] ^ key[i%4]
		}

		got := bytes.Clone(payload)
		MaskPayload(got, key)
		if !bytes.Equal(got, want) {
			t.Errorf("%d bytes: masked % x, want % x", length, got, want)
		}
		// Masking is its own inverse.
		MaskPayload(got, key)
		if !bytes.Equal(got, payload) {
			t.Errorf("%d bytes: masking twice did not restore the payload", length)
		}
	}
}

// Empty data still produces a frame — a zero length frame is legal and is how
// an empty text message or a bare close goes out.
func TestNewFrameEmptyDataStillProducesAFrame(t *testing.T) {
	frame := NewFrame(NewFrameConfig{Opcode: 1, FIN: true})
	if frame == nil {
		t.Fatal("NewFrame must never return a nil frame")
	}
	if frame.PayloadLength != 0 || len(frame.PayloadData) != 0 {
		t.Errorf("PayloadLength = %d, len(PayloadData) = %d, want 0 and 0",
			frame.PayloadLength, len(frame.PayloadData))
	}
}

// NewFrame copies the config and nothing else: Mask, MaskingKey and the length
// fields are the send's to set, and PayloadData is carried, not copied.
func TestNewFrame(t *testing.T) {
	payload := make([]byte, 300)
	frame := NewFrame(NewFrameConfig{PayloadData: payload, Opcode: OpcodeBinary, FIN: true})

	if &frame.PayloadData[0] != &payload[0] || len(frame.PayloadData) != len(payload) {
		t.Error("PayloadData is not the config's slice")
	}
	got := *frame
	got.PayloadData = nil
	if want := (Frame{Opcode: OpcodeBinary, FIN: true}); !reflect.DeepEqual(got, want) {
		t.Errorf("frame = %+v, want %+v", got, want)
	}
}

/*
FIN is now the caller's to set, and its zero value is false. That is the one
sharp edge of the config struct: a caller who omits it builds a frame the peer
treats as fragmented and then waits for a continuation of.
*/
func TestNewFrameFIN(t *testing.T) {
	t.Run("carries FIN through", func(t *testing.T) {
		for _, want := range []bool{true, false} {
			frame := NewFrame(NewFrameConfig{PayloadData: []byte("x"), Opcode: 1, FIN: want})
			if frame.FIN != want {
				t.Errorf("FIN = %v, want %v", frame.FIN, want)
			}
		}
	})

	// Pins the zero value so a change to it cannot pass unnoticed.
	t.Run("omitting FIN yields a non final frame", func(t *testing.T) {
		frame := NewFrame(NewFrameConfig{PayloadData: []byte("x"), Opcode: 1})
		if frame.FIN {
			t.Error("an omitted FIN must stay false")
		}
	})

	// The fragmentation shape the doc comment describes, reassembled by Frames.
	t.Run("a fragmented message reassembles", func(t *testing.T) {
		head := NewFrame(NewFrameConfig{PayloadData: []byte("中文"), Opcode: 1})
		tail := NewFrame(NewFrameConfig{PayloadData: []byte("字"), Opcode: 0, FIN: true})
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

func TestFrameFillPayloadLength(t *testing.T) {
	for _, testCase := range []struct {
		size         int
		wantLen      byte
		wantExtended uint64
	}{
		{0, 0, 0},
		{125, 125, 0},       // the largest inline length
		{126, 126, 126},     // the smallest 16 bit one
		{65535, 126, 65535}, // the largest 16 bit one
		{65536, 127, 65536}, // the smallest 64 bit one
	} {
		// Stale fields from a frame read or sent before must not survive.
		f := &Frame{PayloadLength: 99, ExtendedPayloadLength: 99, PayloadData: make([]byte, testCase.size)}
		f.fillPayloadLength()
		if f.PayloadLength != testCase.wantLen || f.ExtendedPayloadLength != testCase.wantExtended {
			t.Errorf("%d bytes: PayloadLength=%d ExtendedPayloadLength=%d, want %d and %d",
				testCase.size, f.PayloadLength, f.ExtendedPayloadLength, testCase.wantLen, testCase.wantExtended)
		}
	}
}

func TestFillMaskingKey(t *testing.T) {
	t.Run("returns the injected bytes", func(t *testing.T) {
		var key [4]byte
		err := fillMaskingKey(&key, fillMaskingKeyDI{
			randRead: fixedRandRead(0xAA, 0xBB, 0xCC, 0xDD),
		})
		if err != nil {
			t.Fatalf("fillMaskingKey: %v", err)
		}
		if key != [4]byte{0xAA, 0xBB, 0xCC, 0xDD} {
			t.Errorf("got % x", key)
		}
	})

	t.Run("propagates the rand error", func(t *testing.T) {
		want := errors.New("entropy exhausted")
		var key [4]byte
		err := fillMaskingKey(&key, fillMaskingKeyDI{
			randRead: func([]byte) (int, error) { return 0, want },
		})
		if !errors.Is(err, want) {
			t.Errorf("err = %v, want %v", err, want)
		}
	})

	t.Run("real wiring fills the key", func(t *testing.T) {
		var key [4]byte
		if err := FillMaskingKey(&key); err != nil {
			t.Fatalf("FillMaskingKey: %v", err)
		}
	})
}
