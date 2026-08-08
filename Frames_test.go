package wlgows

import (
	"bytes"
	"testing"
	"unicode/utf8"
)

func TestFramesString(t *testing.T) {
	t.Run("empty message", func(t *testing.T) {
		msg := Frames{}
		if got := msg.String(); got != "" {
			t.Errorf("GetStr() = %q, want empty", got)
		}
	})

	t.Run("concatenates every frame payload", func(t *testing.T) {
		msg := Frames{
			{PayloadData: []byte("hello ")},
			{PayloadData: []byte("wl")},
			{PayloadData: []byte("gows")},
		}
		if got := msg.String(); got != "hello wlgows" {
			t.Errorf("GetStr() = %q, want %q", got, "hello wlgows")
		}
	})

	// The whole reason GetStr uses strings.Builder over string([]byte) per
	// frame: a multi-byte rune may straddle a frame boundary.
	t.Run("rejoins a utf-8 rune split across frames", func(t *testing.T) {
		msg := Frames{
			{PayloadData: []byte{0xE4, 0xB8}}, // first 2 bytes of 中
			{PayloadData: []byte{0xAD}},       // last byte of 中
		}
		if got := msg.String(); got != "中" {
			t.Errorf("GetStr() = %q, want %q", got, "中")
		}
	})
}

func TestFramesBytes(t *testing.T) {
	t.Run("empty message", func(t *testing.T) {
		msg := Frames{}
		if got := msg.Bytes(); len(got) != 0 {
			t.Errorf("GetBytes() = % x, want empty", got)
		}
	})

	t.Run("concatenates every frame payload", func(t *testing.T) {
		msg := Frames{
			{PayloadData: []byte("hello ")},
			{PayloadData: []byte("wl")},
			{PayloadData: []byte("gows")},
		}
		if got := msg.Bytes(); !bytes.Equal(got, []byte("hello wlgows")) {
			t.Errorf("GetBytes() = %q, want %q", got, "hello wlgows")
		}
	})

	// Binary payloads are the point of GetBytes: arbitrary bytes must survive
	// untouched, including ones that are not valid UTF-8.
	t.Run("carries bytes no string round trip would survive", func(t *testing.T) {
		want := []byte{0x00, 0xFF, 0xFE, 0x80, 0x00}
		msg := Frames{
			{PayloadData: want[:2]},
			{PayloadData: want[2:]},
		}
		if got := msg.Bytes(); !bytes.Equal(got, want) {
			t.Errorf("GetBytes() = % x, want % x", got, want)
		}
	})

	t.Run("agrees with GetStr", func(t *testing.T) {
		msg := Frames{
			{PayloadData: []byte{0xE4, 0xB8}}, // first 2 bytes of 中
			{PayloadData: []byte{0xAD}},       // last byte of 中
		}
		if got := string(msg.Bytes()); got != msg.String() {
			t.Errorf("string(GetBytes()) = %q, GetStr() = %q", got, msg.String())
		}
	})

	// The documented contract: the caller owns the result outright.
	t.Run("returns a copy the caller may mutate", func(t *testing.T) {
		msg := Frames{{PayloadData: []byte("hello")}}
		got := msg.Bytes()
		got[0] = 'j'
		if string(msg[0].PayloadData) != "hello" {
			t.Errorf("mutating the result reached the frame: %q", msg[0].PayloadData)
		}
	})

	// The whole reason for the method — one allocation, exactly sized, where
	// []byte(GetStr()) needs the builder's buffer plus the conversion's copy.
	t.Run("allocates once at the exact size", func(t *testing.T) {
		msg := Frames{
			{PayloadData: bytes.Repeat([]byte{'x'}, 700)},
			{PayloadData: bytes.Repeat([]byte{'y'}, 300)},
		}
		var got []byte

		// It runs that function 100 times,
		// counts how many times the Go runtime allocated memory from the heap, and returns the average per run.
		allocs := testing.AllocsPerRun(100, func() { got = msg.Bytes() })
		if allocs != 1 {
			t.Errorf("GetBytes() allocated %v times, want 1", allocs)
		}
		if cap(got) != 1000 {
			t.Errorf("cap = %d, want 1000 (exactly sized)", cap(got))
		}
	})
}

func TestFramesByteLen(t *testing.T) {
	t.Run("empty message", func(t *testing.T) {
		msg := Frames{}
		if got := msg.ByteLen(); got != 0 {
			t.Errorf("PayloadByteLength() = %d, want 0", got)
		}
	})

	t.Run("sums every frame", func(t *testing.T) {
		msg := Frames{
			{PayloadData: []byte("hello ")},
			{PayloadData: []byte("wl")},
			{PayloadData: []byte("gows")},
		}
		if got := msg.ByteLen(); got != 12 {
			t.Errorf("PayloadByteLength() = %d, want 12", got)
		}
	})

	// It has to be what the assemblers actually produce, or the exact-size
	// allocation they share with it is wrong.
	t.Run("matches what GetStr and GetBytes return", func(t *testing.T) {
		msg := Frames{
			{PayloadData: bytes.Repeat([]byte{'x'}, 700)},
			{PayloadData: bytes.Repeat([]byte{'y'}, 300)},
		}
		want := msg.ByteLen()
		if got := len(msg.Bytes()); got != want {
			t.Errorf("len(GetBytes()) = %d, PayloadByteLength() = %d", got, want)
		}
		if got := len(msg.String()); got != want {
			t.Errorf("len(GetStr()) = %d, PayloadByteLength() = %d", got, want)
		}
	})

	// The exact claim the doc comment makes. Each CJK rune is 3 bytes in UTF-8,
	// so a 3 character payload is 9 bytes.
	t.Run("counts bytes rather than characters", func(t *testing.T) {
		msg := Frames{{PayloadData: []byte("中文字")}}
		if got := msg.ByteLen(); got != 9 {
			t.Errorf("PayloadByteLength() = %d, want 9", got)
		}
		if got := utf8.RuneCountInString(msg.String()); got != 3 {
			t.Errorf("the payload should still be 3 characters, got %d", got)
		}
	})

	// A rune straddling a frame boundary is counted once, not per fragment.
	t.Run("counts a rune split across frames once", func(t *testing.T) {
		msg := Frames{
			{PayloadData: []byte{0xE4, 0xB8}}, // first 2 bytes of 中
			{PayloadData: []byte{0xAD}},       // last byte of 中
		}
		if got := msg.ByteLen(); got != 3 {
			t.Errorf("PayloadByteLength() = %d, want 3", got)
		}
		if msg.String() != "中" {
			t.Errorf("GetStr() = %q, want 中", msg.String())
		}
	})

	// Reading the size must not cost an allocation — that is the point of
	// having it instead of len(msg.Bytes()).
	t.Run("allocates nothing", func(t *testing.T) {
		msg := Frames{{PayloadData: bytes.Repeat([]byte{'x'}, 1000)}}
		var got int
		if allocs := testing.AllocsPerRun(100, func() { got = msg.ByteLen() }); allocs != 0 {
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
	benchmarkFramesAssemblyByteSink []byte
	benchmarkFramesAssemblyStrSink  string
)

// Plain `go test` skips benchmarks. Run this one with:
//
//	go test -bench BenchmarkFramesAssembly .
//
// The B/op and allocs/op columns come from the b.ReportAllocs() calls below, so
// no -benchmem flag is needed here.
func BenchmarkFramesAssembly(b *testing.B) {
	// A 70000 byte message split the way the reader delivers it, mirroring the
	// large payload case Integration_test.go covers.
	frames := make(Frames, 0, 7)
	for i := 0; i < 7; i++ {
		frames = append(frames, &Frame{PayloadData: bytes.Repeat([]byte{'x'}, 10000)})
	}
	msg := frames

	// b.N is set by the framework, never by us: it is how many times to repeat
	// the operation this round. A single call is far too fast for the clock to
	// time, so Go calls each sub-benchmark again and again with a larger b.N
	// (1, 100, 10000, ...) until one round lasts about a second, then divides
	// that round's time by b.N. Only the setup above stays outside the loop —
	// everything inside it is what gets measured.
	b.Run("[]byte(GetStr())", func(b *testing.B) {
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			benchmarkFramesAssemblyByteSink = []byte(msg.String())
		}
	})
	b.Run("GetBytes()", func(b *testing.B) {
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			benchmarkFramesAssemblyByteSink = msg.Bytes()
		}
	})
	b.Run("GetStr()", func(b *testing.B) {
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			benchmarkFramesAssemblyStrSink = msg.String()
		}
	})
}
