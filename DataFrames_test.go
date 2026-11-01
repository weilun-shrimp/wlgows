package wlgows

import (
	"bytes"
	"io"
	"testing"
	"testing/iotest"
	"unicode/utf8"
)

// joinPayloads is every payload of data_frames joined, for checking what a hook
// got.
func joinPayloads(data_frames DataFrames) []byte {
	payload, _ := io.ReadAll(newDataFramesReader(data_frames))
	return payload
}

func TestDataFramesByteLen(t *testing.T) {
	t.Run("empty message", func(t *testing.T) {
		msg := DataFrames{}
		if got := msg.ByteLen(); got != 0 {
			t.Errorf("ByteLen() = %d, want 0", got)
		}
	})

	t.Run("sums every frame", func(t *testing.T) {
		msg := DataFrames{
			{PayloadData: []byte("hello ")},
			{PayloadData: []byte("wl")},
			{PayloadData: []byte("gows")},
		}
		if got := msg.ByteLen(); got != 12 {
			t.Errorf("ByteLen() = %d, want 12", got)
		}
	})

	// It has to be the length of the joined payloads.
	t.Run("matches the length of the joined payloads", func(t *testing.T) {
		msg := DataFrames{
			{PayloadData: bytes.Repeat([]byte{'x'}, 700)},
			{PayloadData: bytes.Repeat([]byte{'y'}, 300)},
		}
		if got, want := len(joinPayloads(msg)), msg.ByteLen(); got != want {
			t.Errorf("len(joinPayloads) = %d, ByteLen() = %d", got, want)
		}
	})

	// The exact claim the doc comment makes. Each CJK rune is 3 bytes in UTF-8,
	// so a 3 character payload is 9 bytes.
	t.Run("counts bytes rather than characters", func(t *testing.T) {
		msg := DataFrames{{PayloadData: []byte("中文字")}}
		if got := msg.ByteLen(); got != 9 {
			t.Errorf("ByteLen() = %d, want 9", got)
		}
		if got := utf8.RuneCountInString(string(joinPayloads(msg))); got != 3 {
			t.Errorf("the payload should still be 3 characters, got %d", got)
		}
	})

	// A rune straddling a frame boundary is counted once, not per fragment.
	t.Run("counts a rune split across frames once", func(t *testing.T) {
		msg := DataFrames{
			{PayloadData: []byte{0xE4, 0xB8}}, // first 2 bytes of 中
			{PayloadData: []byte{0xAD}},       // last byte of 中
		}
		if got := msg.ByteLen(); got != 3 {
			t.Errorf("ByteLen() = %d, want 3", got)
		}
		if got := string(joinPayloads(msg)); got != "中" {
			t.Errorf("joinPayloads = %q, want 中", got)
		}
	})

	// Reading the size must not cost an allocation.
	t.Run("allocates nothing", func(t *testing.T) {
		msg := DataFrames{{PayloadData: bytes.Repeat([]byte{'x'}, 1000)}}
		var got int
		if allocs := testing.AllocsPerRun(100, func() { got = msg.ByteLen() }); allocs != 0 {
			t.Errorf("ByteLen() allocated %v times, want 0", allocs)
		}
		if got != 1000 {
			t.Errorf("ByteLen() = %d, want 1000", got)
		}
	})
}

func TestCheckDataFramesFormat(t *testing.T) {
	for _, tc := range []struct {
		name   string
		frames DataFrames
	}{
		{"one frame", DataFrames{{Opcode: OpcodeText, FIN: true}}},
		{"a deflated message", DataFrames{
			{Opcode: OpcodeBinary, RSV1: true},
			{Opcode: OpcodeContinuation, FIN: true},
		}},
	} {
		t.Run(tc.name+" is one message", func(t *testing.T) {
			if err := checkDataFramesFormat(tc.frames); err != nil {
				t.Errorf("checkDataFramesFormat = %v, want nil", err)
			}
		})
	}

	// RFC 6455 5.4: one message is a text or binary frame, then continuations,
	// the last with FIN set.
	for _, tc := range []struct {
		name   string
		frames DataFrames
		want   error
	}{
		{"no frames", DataFrames{}, ErrDataFramesNotEnded},
		{"a continuation first", DataFrames{
			{Opcode: OpcodeContinuation, FIN: true},
		}, ErrContinuationFrameWithoutMsg},
		{"a text or binary frame after the first", DataFrames{
			{Opcode: OpcodeBinary},
			{Opcode: OpcodeText, FIN: true},
		}, ErrDataFrameDuringMsg},
		{"a frame after a FIN frame", DataFrames{
			{Opcode: OpcodeBinary, FIN: true},
			{Opcode: OpcodeContinuation, FIN: true},
		}, ErrContinuationFrameWithoutMsg},
		{"a last frame with FIN clear", DataFrames{
			{Opcode: OpcodeBinary},
			{Opcode: OpcodeContinuation},
		}, ErrDataFramesNotEnded},
	} {
		t.Run(tc.name+" is not one message", func(t *testing.T) {
			if err := checkDataFramesFormat(tc.frames); err != tc.want {
				t.Errorf("checkDataFramesFormat = %v, want %v", err, tc.want)
			}
		})
	}

	// RFC 7692 6.1: only the first frame carries RSV1.
	t.Run("RSV1 on a frame after the first returns ErrReservedBitsSet", func(t *testing.T) {
		frames := DataFrames{
			{Opcode: OpcodeBinary, PayloadData: []byte("x")},
			{Opcode: OpcodeContinuation, FIN: true, RSV1: true, PayloadData: []byte("y")},
		}
		if err := checkDataFramesFormat(frames); err != ErrReservedBitsSet {
			t.Errorf("checkDataFramesFormat = %v, want ErrReservedBitsSet", err)
		}
	})
}

func TestNewDataFramesReader(t *testing.T) {
	// iotest.TestReader checks order, small reads and io.EOF again after the
	// end. Empty and nil payloads between frames are skipped.
	t.Run("reads every payload in order", func(t *testing.T) {
		reader := newDataFramesReader(DataFrames{
			{PayloadData: []byte("ab")},
			{PayloadData: []byte{}},
			{PayloadData: nil},
			{PayloadData: []byte("cde")},
			{PayloadData: nil},
		})
		if err := iotest.TestReader(reader, []byte("abcde")); err != nil {
			t.Error(err)
		}
	})

	for _, tc := range []struct {
		name   string
		frames DataFrames
	}{
		{"no frames", nil},
		{"only empty payloads", DataFrames{{PayloadData: nil}, {PayloadData: []byte{}}}},
	} {
		t.Run(tc.name+" is an empty message", func(t *testing.T) {
			if got, err := io.ReadAll(newDataFramesReader(tc.frames)); len(got) != 0 || err != nil {
				t.Errorf("ReadAll = %q, %v, want empty, nil", got, err)
			}
		})
	}

	t.Run("reads the payloads in place: a change before the Read shows", func(t *testing.T) {
		payload := []byte("ab")
		reader := newDataFramesReader(DataFrames{{PayloadData: payload}})
		payload[0] = 'x'
		if got, _ := io.ReadAll(reader); string(got) != "xb" {
			t.Errorf("ReadAll = %q, want \"xb\"", got)
		}
	})

	t.Run("one Read fills p across frames", func(t *testing.T) {
		reader := newDataFramesReader(DataFrames{{PayloadData: []byte("ab")}, {PayloadData: []byte("cd")}})
		p := make([]byte, 3)
		if n, err := reader.Read(p); n != 3 || err != nil || string(p) != "abc" {
			t.Errorf("Read = %d, %v, %q, want 3, nil, \"abc\"", n, err, p[:n])
		}
	})

	t.Run("an empty p reads nothing, no error, before the end", func(t *testing.T) {
		reader := newDataFramesReader(DataFrames{{PayloadData: []byte("ab")}})
		if n, err := reader.Read(nil); n != 0 || err != nil {
			t.Errorf("Read(nil) = %d, %v, want 0, nil", n, err)
		}
	})

	t.Run("allocates once", func(t *testing.T) {
		frames := DataFrames{{PayloadData: []byte("ab")}, {PayloadData: []byte("cd")}, {PayloadData: []byte("e")}}
		var reader io.ReadCloser // kept outside the func, so the reader goes to the heap as in real use
		if allocs := testing.AllocsPerRun(100, func() { reader = newDataFramesReader(frames) }); allocs != 1 {
			t.Errorf("newDataFramesReader allocates %v times, want 1", allocs)
		}
		_ = reader
	})

	t.Run("Close lets go of the frames; a Read after returns io.ErrClosedPipe", func(t *testing.T) {
		reader := newDataFramesReader(DataFrames{{PayloadData: []byte("ab")}})
		if err := reader.Close(); err != nil {
			t.Errorf("Close = %v, want nil", err)
		}
		if frames := reader.(*dataFramesPayloadReader).dataFrames; frames != nil {
			t.Errorf("frames after Close %v, want nil", frames)
		}
		if n, err := reader.Read(make([]byte, 4)); n != 0 || err != io.ErrClosedPipe {
			t.Errorf("Read after Close = %d, %v, want 0, io.ErrClosedPipe", n, err)
		}
		if err := reader.Close(); err != nil {
			t.Errorf("second Close = %v, want nil", err)
		}
	})
}
