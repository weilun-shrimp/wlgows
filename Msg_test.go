package wlgows

import (
	"bytes"
	"errors"
	"io"
	"net"
	"testing"
	"unicode/utf8"
)

func TestMsgGetStr(t *testing.T) {
	t.Run("empty message", func(t *testing.T) {
		msg := Msg{}
		if got := msg.GetStr(); got != "" {
			t.Errorf("GetStr() = %q, want empty", got)
		}
	})

	t.Run("concatenates every frame payload", func(t *testing.T) {
		msg := Msg{Frames: []*Frame{
			{PayloadData: []byte("hello ")},
			{PayloadData: []byte("wl")},
			{PayloadData: []byte("gows")},
		}}
		if got := msg.GetStr(); got != "hello wlgows" {
			t.Errorf("GetStr() = %q, want %q", got, "hello wlgows")
		}
	})

	// The whole reason GetStr uses strings.Builder over string([]byte) per
	// frame: a multi-byte rune may straddle a frame boundary.
	t.Run("rejoins a utf-8 rune split across frames", func(t *testing.T) {
		msg := Msg{Frames: []*Frame{
			{PayloadData: []byte{0xE4, 0xB8}}, // first 2 bytes of 中
			{PayloadData: []byte{0xAD}},       // last byte of 中
		}}
		if got := msg.GetStr(); got != "中" {
			t.Errorf("GetStr() = %q, want %q", got, "中")
		}
	})
}

func TestMsgGetBytes(t *testing.T) {
	t.Run("empty message", func(t *testing.T) {
		msg := Msg{}
		if got := msg.GetBytes(); len(got) != 0 {
			t.Errorf("GetBytes() = % x, want empty", got)
		}
	})

	t.Run("concatenates every frame payload", func(t *testing.T) {
		msg := Msg{Frames: []*Frame{
			{PayloadData: []byte("hello ")},
			{PayloadData: []byte("wl")},
			{PayloadData: []byte("gows")},
		}}
		if got := msg.GetBytes(); !bytes.Equal(got, []byte("hello wlgows")) {
			t.Errorf("GetBytes() = %q, want %q", got, "hello wlgows")
		}
	})

	// Binary payloads are the point of GetBytes: arbitrary bytes must survive
	// untouched, including ones that are not valid UTF-8.
	t.Run("carries bytes no string round trip would survive", func(t *testing.T) {
		want := []byte{0x00, 0xFF, 0xFE, 0x80, 0x00}
		msg := Msg{Frames: []*Frame{
			{PayloadData: want[:2]},
			{PayloadData: want[2:]},
		}}
		if got := msg.GetBytes(); !bytes.Equal(got, want) {
			t.Errorf("GetBytes() = % x, want % x", got, want)
		}
	})

	t.Run("agrees with GetStr", func(t *testing.T) {
		msg := Msg{Frames: []*Frame{
			{PayloadData: []byte{0xE4, 0xB8}}, // first 2 bytes of 中
			{PayloadData: []byte{0xAD}},       // last byte of 中
		}}
		if got := string(msg.GetBytes()); got != msg.GetStr() {
			t.Errorf("string(GetBytes()) = %q, GetStr() = %q", got, msg.GetStr())
		}
	})

	// The documented contract: the caller owns the result outright.
	t.Run("returns a copy the caller may mutate", func(t *testing.T) {
		msg := Msg{Frames: []*Frame{{PayloadData: []byte("hello")}}}
		got := msg.GetBytes()
		got[0] = 'j'
		if string(msg.Frames[0].PayloadData) != "hello" {
			t.Errorf("mutating the result reached the frame: %q", msg.Frames[0].PayloadData)
		}
	})

	// The whole reason for the method — one allocation, exactly sized, where
	// []byte(GetStr()) needs the builder's buffer plus the conversion's copy.
	t.Run("allocates once at the exact size", func(t *testing.T) {
		msg := Msg{Frames: []*Frame{
			{PayloadData: bytes.Repeat([]byte{'x'}, 700)},
			{PayloadData: bytes.Repeat([]byte{'y'}, 300)},
		}}
		var got []byte

		// It runs that function 100 times,
		// counts how many times the Go runtime allocated memory from the heap, and returns the average per run.
		allocs := testing.AllocsPerRun(100, func() { got = msg.GetBytes() })
		if allocs != 1 {
			t.Errorf("GetBytes() allocated %v times, want 1", allocs)
		}
		if cap(got) != 1000 {
			t.Errorf("cap = %d, want 1000 (exactly sized)", cap(got))
		}
	})
}

func TestMsgPayloadByteLength(t *testing.T) {
	t.Run("empty message", func(t *testing.T) {
		msg := Msg{}
		if got := msg.PayloadByteLength(); got != 0 {
			t.Errorf("PayloadByteLength() = %d, want 0", got)
		}
	})

	t.Run("sums every frame", func(t *testing.T) {
		msg := Msg{Frames: []*Frame{
			{PayloadData: []byte("hello ")},
			{PayloadData: []byte("wl")},
			{PayloadData: []byte("gows")},
		}}
		if got := msg.PayloadByteLength(); got != 12 {
			t.Errorf("PayloadByteLength() = %d, want 12", got)
		}
	})

	// It has to be what the assemblers actually produce, or the exact-size
	// allocation they share with it is wrong.
	t.Run("matches what GetStr and GetBytes return", func(t *testing.T) {
		msg := Msg{Frames: []*Frame{
			{PayloadData: bytes.Repeat([]byte{'x'}, 700)},
			{PayloadData: bytes.Repeat([]byte{'y'}, 300)},
		}}
		want := msg.PayloadByteLength()
		if got := len(msg.GetBytes()); got != want {
			t.Errorf("len(GetBytes()) = %d, PayloadByteLength() = %d", got, want)
		}
		if got := len(msg.GetStr()); got != want {
			t.Errorf("len(GetStr()) = %d, PayloadByteLength() = %d", got, want)
		}
	})

	// The exact claim the doc comment makes. Each CJK rune is 3 bytes in UTF-8,
	// so a 3 character payload is 9 bytes.
	t.Run("counts bytes rather than characters", func(t *testing.T) {
		msg := Msg{Frames: []*Frame{{PayloadData: []byte("中文字")}}}
		if got := msg.PayloadByteLength(); got != 9 {
			t.Errorf("PayloadByteLength() = %d, want 9", got)
		}
		if got := utf8.RuneCountInString(msg.GetStr()); got != 3 {
			t.Errorf("the payload should still be 3 characters, got %d", got)
		}
	})

	// A rune straddling a frame boundary is counted once, not per fragment.
	t.Run("counts a rune split across frames once", func(t *testing.T) {
		msg := Msg{Frames: []*Frame{
			{PayloadData: []byte{0xE4, 0xB8}}, // first 2 bytes of 中
			{PayloadData: []byte{0xAD}},       // last byte of 中
		}}
		if got := msg.PayloadByteLength(); got != 3 {
			t.Errorf("PayloadByteLength() = %d, want 3", got)
		}
		if msg.GetStr() != "中" {
			t.Errorf("GetStr() = %q, want 中", msg.GetStr())
		}
	})

	// Reading the size must not cost an allocation — that is the point of
	// having it instead of len(msg.GetBytes()).
	t.Run("allocates nothing", func(t *testing.T) {
		msg := Msg{Frames: []*Frame{{PayloadData: bytes.Repeat([]byte{'x'}, 1000)}}}
		var got int
		if allocs := testing.AllocsPerRun(100, func() { got = msg.PayloadByteLength() }); allocs != 0 {
			t.Errorf("PayloadByteLength() allocated %v times, want 0", allocs)
		}
		if got != 1000 {
			t.Errorf("PayloadByteLength() = %d, want 1000", got)
		}
	})
}

// Sinks: without them the compiler drops a []byte(string) conversion whose
// result is unused, and the benchmark measures nothing.
var (
	benchmarkMsgAssemblyByteSink []byte
	benchmarkMsgAssemblyStrSink  string
)

// Plain `go test` skips benchmarks. Run this one with:
//
//	go test -bench BenchmarkMsgAssembly .
//
// The B/op and allocs/op columns come from the b.ReportAllocs() calls below, so
// no -benchmem flag is needed here.
func BenchmarkMsgAssembly(b *testing.B) {
	// A 70000 byte message split the way the reader delivers it, mirroring the
	// large payload case Integration_test.go covers.
	frames := make([]*Frame, 0, 7)
	for i := 0; i < 7; i++ {
		frames = append(frames, &Frame{PayloadData: bytes.Repeat([]byte{'x'}, 10000)})
	}
	msg := Msg{Frames: frames}

	// b.N is set by the framework, never by us: it is how many times to repeat
	// the operation this round. A single call is far too fast for the clock to
	// time, so Go calls each sub-benchmark again and again with a larger b.N
	// (1, 100, 10000, ...) until one round lasts about a second, then divides
	// that round's time by b.N. Only the setup above stays outside the loop —
	// everything inside it is what gets measured.
	b.Run("[]byte(GetStr())", func(b *testing.B) {
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			benchmarkMsgAssemblyByteSink = []byte(msg.GetStr())
		}
	})
	b.Run("GetBytes()", func(b *testing.B) {
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			benchmarkMsgAssemblyByteSink = msg.GetBytes()
		}
	})
	b.Run("GetStr()", func(b *testing.B) {
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			benchmarkMsgAssemblyStrSink = msg.GetStr()
		}
	})
}

func TestGetMsgFromTCPConn(t *testing.T) {
	t.Run("collects frames until FIN", func(t *testing.T) {
		frames := []*Frame{
			{FIN: false, PayloadData: []byte("a")},
			{FIN: false, PayloadData: []byte("b")},
			{FIN: true, PayloadData: []byte("c")},
			{FIN: true, PayloadData: []byte("NOT READ")},
		}
		i := 0
		msg, err := getMsgFromTCPConn(newFakeConn(nil), getMsgFromTCPConnDI{
			getFrameFromTCPConn: func(net.Conn) (*Frame, error) {
				frame := frames[i]
				i++
				return frame, nil
			},
		})
		if err != nil {
			t.Fatalf("getMsgFromTCPConn: %v", err)
		}
		if len(msg.Frames) != 3 {
			t.Fatalf("len(Frames) = %d, want 3", len(msg.Frames))
		}
		if msg.GetStr() != "abc" {
			t.Errorf("GetStr() = %q, want %q", msg.GetStr(), "abc")
		}
	})

	t.Run("single final frame", func(t *testing.T) {
		msg, err := getMsgFromTCPConn(newFakeConn(nil), getMsgFromTCPConnDI{
			getFrameFromTCPConn: func(net.Conn) (*Frame, error) {
				return &Frame{FIN: true, PayloadData: []byte("solo")}, nil
			},
		})
		if err != nil {
			t.Fatalf("getMsgFromTCPConn: %v", err)
		}
		if len(msg.Frames) != 1 || msg.GetStr() != "solo" {
			t.Errorf("Frames=%d GetStr=%q", len(msg.Frames), msg.GetStr())
		}
	})

	t.Run("propagates a frame read error", func(t *testing.T) {
		want := errors.New("frame boom")
		_, err := getMsgFromTCPConn(newFakeConn(nil), getMsgFromTCPConnDI{
			getFrameFromTCPConn: func(net.Conn) (*Frame, error) { return nil, want },
		})
		if !errors.Is(err, want) {
			t.Errorf("err = %v, want %v", err, want)
		}
	})

	t.Run("real wiring parses two frames off the wire", func(t *testing.T) {
		first := (&Frame{FIN: false, Opcode: 1, PayloadLength: 2, PayloadData: []byte("wl")}).Seal()
		last := (&Frame{FIN: true, Opcode: 0, PayloadLength: 4, PayloadData: []byte("gows")}).Seal()
		msg, err := GetMsgFromTCPConn(newFakeConn(append(first, last...)))
		if err != nil {
			t.Fatalf("GetMsgFromTCPConn: %v", err)
		}
		if msg.GetStr() != "wlgows" {
			t.Errorf("GetStr() = %q, want %q", msg.GetStr(), "wlgows")
		}
	})
}

func TestNewMsgLengthEncoding(t *testing.T) {
	tests := []struct {
		name         string
		size         int
		wantLen      byte
		wantExtended uint64
	}{
		{"125 bytes uses the 7 bit length", 125, 125, 0},
		{"126 bytes switches to the 16 bit length", 126, 126, 126},
		{"65535 bytes is the 16 bit ceiling", 65535, 126, 65535},
		{"65536 bytes switches to the 64 bit length", 65536, 127, 65536},
		{"1 byte", 1, 1, 0},
	}
	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			msg, err := newMsg(bytes.Repeat([]byte{'x'}, testCase.size), 1, false, newMsgDI{
				generateMaskingKey: func() ([]byte, error) { return nil, nil },
			})
			if err != nil {
				t.Fatalf("newMsg: %v", err)
			}
			if len(msg.Frames) != 1 {
				t.Fatalf("len(Frames) = %d, want 1", len(msg.Frames))
			}
			frame := msg.Frames[0]
			if !frame.FIN {
				t.Error("FIN should be true for a single frame message")
			}
			if frame.PayloadLength != testCase.wantLen {
				t.Errorf("PayloadLength = %d, want %d", frame.PayloadLength, testCase.wantLen)
			}
			if frame.ExtendedPayloadLength != testCase.wantExtended {
				t.Errorf("ExtendedPayloadLength = %d, want %d", frame.ExtendedPayloadLength, testCase.wantExtended)
			}
			if uint64(len(frame.PayloadData)) != uint64(testCase.size) {
				t.Errorf("len(PayloadData) = %d, want %d", len(frame.PayloadData), testCase.size)
			}
		})
	}
}

// Empty data never enters the loop, so no frame is produced at all.
func TestNewMsgEmptyDataProducesNoFrames(t *testing.T) {
	msg, err := newMsg(nil, 1, false, newMsgDI{
		generateMaskingKey: func() ([]byte, error) { return nil, nil },
	})
	if err != nil {
		t.Fatalf("newMsg: %v", err)
	}
	if len(msg.Frames) != 0 {
		t.Errorf("len(Frames) = %d, want 0", len(msg.Frames))
	}
	if msg.GetStr() != "" {
		t.Errorf("GetStr() = %q, want empty", msg.GetStr())
	}
}

func TestNewMsgMasking(t *testing.T) {
	t.Run("masks with the injected key", func(t *testing.T) {
		msg, err := newMsg([]byte("hi"), 1, true, newMsgDI{
			generateMaskingKey: func() ([]byte, error) { return []byte{1, 2, 3, 4}, nil },
		})
		if err != nil {
			t.Fatalf("newMsg: %v", err)
		}
		frame := msg.Frames[0]
		if !frame.Mask {
			t.Error("Mask should be true")
		}
		if !bytes.Equal(frame.MaskingKey, []byte{1, 2, 3, 4}) {
			t.Errorf("MaskingKey = % x", frame.MaskingKey)
		}
		// A fixed key makes the sealed bytes fully assertable.
		want := []byte{0x81, 0x82, 1, 2, 3, 4, 'h' ^ 1, 'i' ^ 2}
		if got := frame.Seal(); !bytes.Equal(got, want) {
			t.Errorf("Seal() = % x, want % x", got, want)
		}
	})

	t.Run("leaves the frame unmasked when not requested", func(t *testing.T) {
		msg, err := newMsg([]byte("hi"), 1, false, newMsgDI{
			generateMaskingKey: func() ([]byte, error) {
				t.Fatal("generateMaskingKey must not be called when need_mask is false")
				return nil, nil
			},
		})
		if err != nil {
			t.Fatalf("newMsg: %v", err)
		}
		if msg.Frames[0].Mask || msg.Frames[0].MaskingKey != nil {
			t.Error("frame should be unmasked with no key")
		}
	})

	t.Run("propagates a masking key error", func(t *testing.T) {
		want := errors.New("no entropy")
		_, err := newMsg([]byte("hi"), 1, true, newMsgDI{
			generateMaskingKey: func() ([]byte, error) { return nil, want },
		})
		if !errors.Is(err, want) {
			t.Errorf("err = %v, want %v", err, want)
		}
	})
}

func TestNewMsgOpcode(t *testing.T) {
	for _, opcode := range []uint8{1, 2, 8, 9, 10} {
		msg, err := newMsg([]byte("x"), opcode, false, newMsgDI{
			generateMaskingKey: func() ([]byte, error) { return nil, nil },
		})
		if err != nil {
			t.Fatalf("newMsg: %v", err)
		}
		if msg.Frames[0].Opcode != opcode {
			t.Errorf("Opcode = %d, want %d", msg.Frames[0].Opcode, opcode)
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

// Guards the reader against a truncated stream.
func TestGetMsgFromTCPConnTruncatedStream(t *testing.T) {
	partial := (&Frame{FIN: true, Opcode: 1, PayloadLength: 10, PayloadData: []byte("short")}).Seal()
	_, err := GetMsgFromTCPConn(newFakeConn(partial[:4]))
	if err == nil {
		t.Fatal("expected an error on a truncated frame")
	}
	if !errors.Is(err, io.ErrUnexpectedEOF) && !errors.Is(err, io.EOF) {
		t.Errorf("err = %v, want an EOF variant", err)
	}
}
