package wlgows

import (
	"bytes"
	"compress/flate"
	"errors"
	"fmt"
	"io"
	"slices"
	"strings"
	"sync"
	"testing"
)

func TestDeflaterResultBufferCap(t *testing.T) {
	// It does not wait for a message being sent: the data lock is not taken.
	t.Run("sums both deflaters", func(t *testing.T) {
		var steps []string
		conn := &Conn{
			contextDeflater: fakeFuncDeflater{resultBufferCapFunc: func() int {
				steps = append(steps, "context cap")
				return 3
			}},
			noContextDeflater: fakeFuncDeflater{resultBufferCapFunc: func() int {
				steps = append(steps, "no context cap")
				return 4
			}},
			di: connDI{dataFramesWriteLocker: fakeFuncLocker{
				lock:   func() { steps = append(steps, "data lock") },
				unlock: func() { steps = append(steps, "data unlock") },
			}},
		}
		if got := conn.DeflaterResultBufferCap(); got != 7 {
			t.Errorf("DeflaterResultBufferCap = %d, want 7", got)
		}
		if want := []string{"no context cap", "context cap"}; !slices.Equal(steps, want) {
			t.Errorf("steps %q, want %q", steps, want)
		}
	})
}

func TestReleaseDeflaterResultBuffer(t *testing.T) {
	t.Run("releases both deflaters under the data lock", func(t *testing.T) {
		var steps []string
		conn := &Conn{
			contextDeflater:   fakeFuncDeflater{releaseResultBufferFunc: func() { steps = append(steps, "context release") }},
			noContextDeflater: fakeFuncDeflater{releaseResultBufferFunc: func() { steps = append(steps, "no context release") }},
			di: connDI{dataFramesWriteLocker: fakeFuncLocker{
				lock:   func() { steps = append(steps, "data lock") },
				unlock: func() { steps = append(steps, "data unlock") },
			}},
		}
		conn.ReleaseDeflaterResultBuffer()
		if want := []string{"data lock", "no context release", "context release", "data unlock"}; !slices.Equal(steps, want) {
			t.Errorf("steps %q, want %q", steps, want)
		}
	})
}

func TestNoContextDeflaterDeflate(t *testing.T) {
	// inflate reads one deflate stream made of the given pieces, the way the
	// peer's inflater does. A sync flush ends with no final block, so the stream
	// ends with io.ErrUnexpectedEOF once everything is read.
	inflate := func(pieces ...[]byte) (string, error) {
		reader := flate.NewReader(bytes.NewReader(bytes.Join(pieces, nil)))
		got, err := io.ReadAll(reader)
		if errors.Is(err, io.ErrUnexpectedEOF) {
			err = nil
		}
		return string(got), err
	}
	var builder strings.Builder
	for i := range 3000 {
		fmt.Fprintf(&builder, "%d ", i)
	}
	text := builder.String()

	// RFC 7692 7.1.1: without context takeover, every message must inflate
	// alone, at the same level as the last and at a new one. 7.2.1: each ends
	// with the sync flush's 00 00 ff ff.
	t.Run("every deflate stands alone", func(t *testing.T) {
		deflater := noContextDeflater{locker: &sync.Mutex{}}
		for index, level := range []int{flate.BestCompression, flate.BestCompression, flate.BestSpeed, flate.HuffmanOnly, flate.BestSpeed} {
			out, err := deflater.deflate([]byte(text), level)
			if err != nil {
				t.Fatalf("deflate %d: %v", index, err)
			}
			if !bytes.HasSuffix(out, []byte{0x00, 0x00, 0xff, 0xff}) {
				t.Errorf("deflate %d = ...% x, want it to end with 00 00 ff ff", index, out[max(len(out)-8, 0):])
			}
			if got, err := inflate(out); err != nil || got != text {
				t.Errorf("deflate %d inflates alone to %d bytes, %v, want the text", index, len(got), err)
			}
		}
	})

	/*
		A writer in noContextFlateWriters was last used by another Conn: it still
		points at that Conn's buffer, with that message in its window. deflate
		must point it at its own buffer and start it empty.
	*/
	t.Run("a pooled writer writes to this buffer, from an empty window", func(t *testing.T) {
		var other bytes.Buffer
		used, _ := flate.NewWriter(&other, flate.BestCompression)
		used.Write([]byte(text))
		used.Flush()
		other_len := other.Len()
		noContextFlateWriters[flate.BestCompression-flate.HuffmanOnly].Put(used)

		deflater := noContextDeflater{locker: &sync.Mutex{}}
		out, err := deflater.deflate([]byte(text), flate.BestCompression)
		if err != nil {
			t.Fatalf("deflate: %v", err)
		}
		if other.Len() != other_len {
			t.Errorf("the other buffer grew from %d to %d bytes, want it untouched", other_len, other.Len())
		}
		if got, err := inflate(out); err != nil || got != text {
			t.Errorf("inflate alone = %d bytes, %v, want the text", len(got), err)
		}
	})

	t.Run("refuses an invalid level before taking the lock", func(t *testing.T) {
		var steps []string
		deflater := noContextDeflater{locker: fakeFuncLocker{
			lock:   func() { steps = append(steps, "lock") },
			unlock: func() { steps = append(steps, "unlock") },
		}}
		if _, err := deflater.deflate([]byte(text), flate.HuffmanOnly-1); !errors.Is(err, ErrDeflateLevelInvalid) {
			t.Errorf("deflate = %v, want ErrDeflateLevelInvalid", err)
		}
		if len(steps) != 0 {
			t.Errorf("steps %q, want nothing run", steps)
		}
	})

	t.Run("locks", func(t *testing.T) {
		var steps []string
		deflater := noContextDeflater{locker: fakeFuncLocker{
			lock:   func() { steps = append(steps, "lock") },
			unlock: func() { steps = append(steps, "unlock") },
		}}
		deflater.deflate([]byte("hello"), flate.BestSpeed)
		if want := []string{"lock", "unlock"}; !slices.Equal(steps, want) {
			t.Errorf("steps %q, want %q", steps, want)
		}
	})
}

func TestNoContextDeflaterReleaseResultBuffer(t *testing.T) {
	t.Run("lets go of the buffer", func(t *testing.T) {
		deflater := noContextDeflater{locker: &sync.Mutex{}}
		deflater.result_buffer.Write(make([]byte, 1024))
		deflater.releaseResultBuffer()
		if deflater.result_buffer.Cap() != 0 {
			t.Errorf("result_buffer Cap = %d after release, want 0", deflater.result_buffer.Cap())
		}
	})

	t.Run("locks", func(t *testing.T) {
		var steps []string
		deflater := noContextDeflater{locker: fakeFuncLocker{
			lock:   func() { steps = append(steps, "lock") },
			unlock: func() { steps = append(steps, "unlock") },
		}}
		deflater.releaseResultBuffer()
		if want := []string{"lock", "unlock"}; !slices.Equal(steps, want) {
			t.Errorf("steps %q, want %q", steps, want)
		}
	})
}

func TestNoContextDeflaterResultBufferCap(t *testing.T) {
	t.Run("is the buffer's Cap", func(t *testing.T) {
		deflater := noContextDeflater{locker: &sync.Mutex{}}
		deflater.result_buffer.Write(make([]byte, 1024))
		if got, want := deflater.resultBufferCap(), deflater.result_buffer.Cap(); got != want {
			t.Errorf("resultBufferCap = %d, want %d", got, want)
		}
	})

	t.Run("locks", func(t *testing.T) {
		var steps []string
		deflater := noContextDeflater{locker: fakeFuncLocker{
			lock:   func() { steps = append(steps, "lock") },
			unlock: func() { steps = append(steps, "unlock") },
		}}
		deflater.resultBufferCap()
		if want := []string{"lock", "unlock"}; !slices.Equal(steps, want) {
			t.Errorf("steps %q, want %q", steps, want)
		}
	})
}

func TestContextDeflaterDeflate(t *testing.T) {
	// inflate reads one deflate stream made of the given pieces, the way the
	// peer's inflater does. A sync flush ends with no final block, so the stream
	// ends with io.ErrUnexpectedEOF once everything is read.
	inflate := func(pieces ...[]byte) (string, error) {
		reader := flate.NewReader(bytes.NewReader(bytes.Join(pieces, nil)))
		got, err := io.ReadAll(reader)
		if errors.Is(err, io.ErrUnexpectedEOF) {
			err = nil
		}
		return string(got), err
	}
	var builder strings.Builder
	for i := range 3000 {
		fmt.Fprintf(&builder, "%d ", i)
	}
	text := builder.String()

	// RFC 7692 7.2.1: the sync flush ends with 00 00 ff ff, and the result
	// holds all of data.
	t.Run("flushes all of data", func(t *testing.T) {
		deflater := contextDeflater{locker: &sync.Mutex{}}
		out, err := deflater.deflate([]byte(text), flate.BestSpeed)
		if err != nil {
			t.Fatalf("deflate: %v", err)
		}
		if !bytes.HasSuffix(out, []byte{0x00, 0x00, 0xff, 0xff}) {
			t.Errorf("deflate = ...% x, want it to end with 00 00 ff ff", out[max(len(out)-8, 0):])
		}
		if got, err := inflate(out); err != nil || got != text {
			t.Errorf("inflate = %d bytes, %v, want the %d bytes of text", len(got), err, len(text))
		}
	})

	// RFC 7692 7.2.1: with context takeover, the next message may refer back.
	t.Run("the next deflate continues the stream", func(t *testing.T) {
		deflater := contextDeflater{locker: &sync.Mutex{}}
		first, _ := deflater.deflate([]byte(text), flate.BestCompression)
		first = bytes.Clone(first)
		second, err := deflater.deflate([]byte(text), flate.BestCompression)
		if err != nil {
			t.Fatalf("deflate: %v", err)
		}
		if got, err := inflate(first, second); err != nil || got != text+text {
			t.Errorf("inflate = %d bytes, %v, want the text twice", len(got), err)
		}
		if len(second) >= len(first)/10 {
			t.Errorf("second deflate is %d bytes, first %d: want it to refer back to the first", len(second), len(first))
		}
	})

	// compress/flate cannot change a writer's level. The writer for the new
	// level starts from the history, so it still refers back.
	t.Run("a new level continues the stream", func(t *testing.T) {
		deflater := contextDeflater{locker: &sync.Mutex{}}
		first, _ := deflater.deflate([]byte(text), flate.BestSpeed)
		first = bytes.Clone(first)
		second, err := deflater.deflate([]byte(text), flate.BestCompression)
		if err != nil {
			t.Fatalf("deflate: %v", err)
		}
		if got, err := inflate(first, second); err != nil || got != text+text {
			t.Errorf("inflate = %d bytes, %v, want the text twice", len(got), err)
		}
		if len(second) >= len(first)/10 {
			t.Errorf("second deflate is %d bytes, first %d: want it to refer back to the first", len(second), len(first))
		}
	})

	// A new writer costs about 1 MB, so one is built only when the level
	// changes.
	t.Run("keeps its writer while the level stays", func(t *testing.T) {
		deflater := contextDeflater{locker: &sync.Mutex{}}
		deflater.deflate([]byte(text), flate.BestCompression)
		writer := deflater.flate_writer
		deflater.deflate([]byte(text), flate.BestCompression)
		if deflater.flate_writer != writer {
			t.Error("deflate built a new writer for the same level")
		}
		deflater.deflate([]byte(text), flate.BestSpeed)
		if deflater.flate_writer == writer {
			t.Error("deflate kept the writer for a new level")
		}
	})

	// Past 32 KB, the history holds more than the window, and only its last
	// 32 KB is where the peer's window is. compress/flate trims a longer dict
	// itself, so this shows only that the stream still inflates.
	t.Run("a new level past 32 KB of history still inflates", func(t *testing.T) {
		deflater := contextDeflater{locker: &sync.Mutex{}}
		var pieces [][]byte
		for _, level := range []int{flate.BestSpeed, flate.BestSpeed, flate.BestSpeed, flate.BestCompression} {
			out, err := deflater.deflate([]byte(text), level)
			if err != nil {
				t.Fatalf("deflate: %v", err)
			}
			pieces = append(pieces, bytes.Clone(out))
		}
		if history := 3 * len(text); history <= deflateWindowSize || history >= 2*deflateWindowSize {
			t.Fatalf("history %d bytes before the new level, want between 1 and 2 windows", history)
		}
		if got, err := inflate(pieces...); err != nil || got != strings.Repeat(text, 4) {
			t.Errorf("inflate = %d bytes, %v, want the text 4 times", len(got), err)
		}
	})

	t.Run("refuses an invalid level before taking the lock", func(t *testing.T) {
		var steps []string
		deflater := contextDeflater{locker: fakeFuncLocker{
			lock:   func() { steps = append(steps, "lock") },
			unlock: func() { steps = append(steps, "unlock") },
		}}
		if _, err := deflater.deflate([]byte(text), flate.BestCompression+1); !errors.Is(err, ErrDeflateLevelInvalid) {
			t.Errorf("deflate = %v, want ErrDeflateLevelInvalid", err)
		}
		if len(steps) != 0 || deflater.history != nil {
			t.Errorf("steps %q, history %d bytes, want nothing run", steps, len(deflater.history))
		}
	})

	t.Run("locks", func(t *testing.T) {
		var steps []string
		deflater := contextDeflater{locker: fakeFuncLocker{
			lock:   func() { steps = append(steps, "lock") },
			unlock: func() { steps = append(steps, "unlock") },
		}}
		deflater.deflate([]byte("hello"), flate.BestSpeed)
		if want := []string{"lock", "unlock"}; !slices.Equal(steps, want) {
			t.Errorf("steps %q, want %q", steps, want)
		}
	})
}

func TestContextDeflaterDropContext(t *testing.T) {
	// inflate reads one deflate stream made of the given pieces, the way the
	// peer's inflater does. A sync flush ends with no final block, so the stream
	// ends with io.ErrUnexpectedEOF once everything is read.
	inflate := func(pieces ...[]byte) (string, error) {
		reader := flate.NewReader(bytes.NewReader(bytes.Join(pieces, nil)))
		got, err := io.ReadAll(reader)
		if errors.Is(err, io.ErrUnexpectedEOF) {
			err = nil
		}
		return string(got), err
	}
	var builder strings.Builder
	for i := range 3000 {
		fmt.Fprintf(&builder, "%d ", i)
	}
	text := builder.String()

	// RFC 7692 7.1.1: after dropContext the peer's window is empty, so the next
	// message must inflate alone. A writer that started from history is the
	// case Reset would get wrong: it would put that history back.
	t.Run("the next deflate stands alone", func(t *testing.T) {
		for _, testCase := range []struct {
			name   string
			levels []int // deflated at, in order, before dropContext
		}{
			{"a writer started empty", []int{flate.BestCompression}},
			{"a writer started from history", []int{flate.BestSpeed, flate.BestCompression}},
		} {
			deflater := contextDeflater{locker: &sync.Mutex{}}
			for _, level := range testCase.levels {
				deflater.deflate([]byte(text), level)
			}
			deflater.dropContext()
			if deflater.flate_writer != nil || deflater.history != nil {
				t.Errorf("%s: dropContext kept writer %v, history %d bytes, want both freed",
					testCase.name, deflater.flate_writer != nil, len(deflater.history))
			}
			out, err := deflater.deflate([]byte(text), flate.BestCompression)
			if err != nil {
				t.Fatalf("%s: deflate: %v", testCase.name, err)
			}
			if got, err := inflate(out); err != nil || got != text {
				t.Errorf("%s: inflate alone = %d bytes, %v, want the text", testCase.name, len(got), err)
			}
		}
	})

	t.Run("before any deflate is fine", func(t *testing.T) {
		deflater := contextDeflater{locker: &sync.Mutex{}}
		deflater.dropContext()
		out, err := deflater.deflate([]byte(text), flate.BestSpeed)
		if err != nil {
			t.Fatalf("deflate: %v", err)
		}
		if got, err := inflate(out); err != nil || got != text {
			t.Errorf("inflate = %d bytes, %v, want the text", len(got), err)
		}
	})

	t.Run("locks", func(t *testing.T) {
		var steps []string
		deflater := contextDeflater{locker: fakeFuncLocker{
			lock:   func() { steps = append(steps, "lock") },
			unlock: func() { steps = append(steps, "unlock") },
		}}
		deflater.dropContext()
		if want := []string{"lock", "unlock"}; !slices.Equal(steps, want) {
			t.Errorf("steps %q, want %q", steps, want)
		}
	})
}

func TestContextDeflaterReleaseResultBuffer(t *testing.T) {
	// inflate reads one deflate stream made of the given pieces, the way the
	// peer's inflater does. A sync flush ends with no final block, so the stream
	// ends with io.ErrUnexpectedEOF once everything is read.
	inflate := func(pieces ...[]byte) (string, error) {
		reader := flate.NewReader(bytes.NewReader(bytes.Join(pieces, nil)))
		got, err := io.ReadAll(reader)
		if errors.Is(err, io.ErrUnexpectedEOF) {
			err = nil
		}
		return string(got), err
	}
	var builder strings.Builder
	for i := range 3000 {
		fmt.Fprintf(&builder, "%d ", i)
	}
	text := builder.String()

	// The stream lives in the writer, not the buffer, so it goes on.
	t.Run("lets go of the buffer, and the stream goes on", func(t *testing.T) {
		deflater := contextDeflater{locker: &sync.Mutex{}}
		first, _ := deflater.deflate([]byte(text), flate.BestSpeed)
		first = bytes.Clone(first)
		deflater.releaseResultBuffer()
		if deflater.result_buffer.Cap() != 0 {
			t.Errorf("result_buffer Cap = %d after release, want 0", deflater.result_buffer.Cap())
		}
		second, err := deflater.deflate([]byte(text), flate.BestSpeed)
		if err != nil {
			t.Fatalf("deflate: %v", err)
		}
		if got, err := inflate(first, second); err != nil || got != text+text {
			t.Errorf("inflate = %d bytes, %v, want the text twice", len(got), err)
		}
	})

	t.Run("locks", func(t *testing.T) {
		var steps []string
		deflater := contextDeflater{locker: fakeFuncLocker{
			lock:   func() { steps = append(steps, "lock") },
			unlock: func() { steps = append(steps, "unlock") },
		}}
		deflater.releaseResultBuffer()
		if want := []string{"lock", "unlock"}; !slices.Equal(steps, want) {
			t.Errorf("steps %q, want %q", steps, want)
		}
	})
}

func TestContextDeflaterResultBufferCap(t *testing.T) {
	t.Run("is the buffer's Cap", func(t *testing.T) {
		deflater := contextDeflater{locker: &sync.Mutex{}}
		deflater.result_buffer.Write(make([]byte, 1024))
		if got, want := deflater.resultBufferCap(), deflater.result_buffer.Cap(); got != want {
			t.Errorf("resultBufferCap = %d, want %d", got, want)
		}
	})

	t.Run("locks", func(t *testing.T) {
		var steps []string
		deflater := contextDeflater{locker: fakeFuncLocker{
			lock:   func() { steps = append(steps, "lock") },
			unlock: func() { steps = append(steps, "unlock") },
		}}
		deflater.resultBufferCap()
		if want := []string{"lock", "unlock"}; !slices.Equal(steps, want) {
			t.Errorf("steps %q, want %q", steps, want)
		}
	})
}

func TestAppendWindow(t *testing.T) {
	a := bytes.Repeat([]byte("a"), deflateWindowSize)
	b := bytes.Repeat([]byte("b"), deflateWindowSize)
	twice := append(bytes.Clone(a), b...) // 2 windows, the most it holds
	for _, testCase := range []struct {
		name    string
		history []byte
		data    []byte
		want    []byte
	}{
		{"appends under the window", []byte("ab"), []byte("cd"), []byte("abcd")},
		{"appends to nil", nil, []byte("cd"), []byte("cd")},
		{"appends past the window without sliding", a, []byte("cd"), append(bytes.Clone(a), "cd"...)},
		{"appends up to twice the window without sliding", twice[2:], []byte("cd"), append(bytes.Clone(twice[2:]), "cd"...)},
		{"past twice the window keeps the last window", twice, []byte("cd"), append(bytes.Clone(b[2:]), "cd"...)},
		{"keeps the last of data past the window", []byte("ab"), append(bytes.Clone(b), "cd"...), append(bytes.Clone(b[2:]), "cd"...)},
		{"data of exactly the window replaces it", []byte("ab"), b, b},
	} {
		got := appendWindow(bytes.Clone(testCase.history), testCase.data)
		if !bytes.Equal(got, testCase.want) {
			t.Errorf("%s: appendWindow = %d bytes, want %d", testCase.name, len(got), len(testCase.want))
		}
	}
}

func TestValidateDeflateLevel(t *testing.T) {
	for _, testCase := range []struct {
		level   int
		wantErr error
	}{
		{flate.HuffmanOnly, nil},
		{flate.DefaultCompression, nil},
		{flate.NoCompression, nil},
		{flate.BestSpeed, nil},
		{flate.BestCompression, nil},
		{flate.HuffmanOnly - 1, ErrDeflateLevelInvalid},
		{flate.BestCompression + 1, ErrDeflateLevelInvalid},
	} {
		if err := validateDeflateLevel(testCase.level); !errors.Is(err, testCase.wantErr) {
			t.Errorf("validateDeflateLevel(%d) = %v, want %v", testCase.level, err, testCase.wantErr)
		}
	}
}

func TestSendWindowBits(t *testing.T) {
	for _, testCase := range []struct {
		name         string
		agreed       PermessageDeflateConfig
		peerIsClient bool
		want         uint
	}{
		// RFC 7692 7.1.2.1: a server sends within server_max_window_bits.
		{
			name:         "server, no server_max_window_bits, sends within 15 bits",
			agreed:       PermessageDeflateConfig{},
			peerIsClient: true,
			want:         15,
		},
		{
			name:         "server, server_max_window_bits=8, sends within 8 bits",
			agreed:       PermessageDeflateConfig{ServerMaxWindowBits: 8},
			peerIsClient: true,
			want:         8,
		},
		{
			name:         "server, server_max_window_bits=14, sends within 14 bits",
			agreed:       PermessageDeflateConfig{ServerMaxWindowBits: 14},
			peerIsClient: true,
			want:         14,
		},
		{
			name:         "server, server_max_window_bits=15, sends within 15 bits",
			agreed:       PermessageDeflateConfig{ServerMaxWindowBits: 15},
			peerIsClient: true,
			want:         15,
		},
		{
			name:         "server, the client's window does not count, sends within 15 bits",
			agreed:       PermessageDeflateConfig{ClientMaxWindowBitsEnabled: true, ClientMaxWindowBits: 9},
			peerIsClient: true,
			want:         15,
		},
		// RFC 7692 7.1.2.2: a client sends within client_max_window_bits.
		{
			name:         "client, no client_max_window_bits, sends within 15 bits",
			agreed:       PermessageDeflateConfig{},
			peerIsClient: false,
			want:         15,
		},
		{
			name:         "client, ClientMaxWindowBits set without ClientMaxWindowBitsEnabled, sends within 15 bits",
			agreed:       PermessageDeflateConfig{ClientMaxWindowBits: 9},
			peerIsClient: false,
			want:         15,
		},
		{
			name:         "client, client_max_window_bits with no value, sends within 15 bits",
			agreed:       PermessageDeflateConfig{ClientMaxWindowBitsEnabled: true},
			peerIsClient: false,
			want:         15,
		},
		{
			name:         "client, client_max_window_bits=8, sends within 8 bits",
			agreed:       PermessageDeflateConfig{ClientMaxWindowBitsEnabled: true, ClientMaxWindowBits: 8},
			peerIsClient: false,
			want:         8,
		},
		{
			name:         "client, client_max_window_bits=14, sends within 14 bits",
			agreed:       PermessageDeflateConfig{ClientMaxWindowBitsEnabled: true, ClientMaxWindowBits: 14},
			peerIsClient: false,
			want:         14,
		},
		{
			name:         "client, client_max_window_bits=15, sends within 15 bits",
			agreed:       PermessageDeflateConfig{ClientMaxWindowBitsEnabled: true, ClientMaxWindowBits: 15},
			peerIsClient: false,
			want:         15,
		},
		{
			name:         "client, the server's window does not count, sends within 15 bits",
			agreed:       PermessageDeflateConfig{ServerMaxWindowBits: 9},
			peerIsClient: false,
			want:         15,
		},
	} {
		if got := sendWindowBits(&testCase.agreed, testCase.peerIsClient); got != testCase.want {
			t.Errorf("%s: sendWindowBits = %d, want %d", testCase.name, got, testCase.want)
		}
	}
}

func TestValidateDeflateLevelForWindow(t *testing.T) {
	// Every compress/flate level at 15, 14 and 8 bits. Not tried, as they never
	// reach validateDeflateLevelForWindow:
	//   - levels compress/flate has not: validateDeflateLevel refuses them
	//     first.
	//   - window bits out of 8 to 15, see its comment.

	// At 15 bits every level fits.
	for level := flate.HuffmanOnly; level <= flate.BestCompression; level++ {
		if err := validateDeflateLevelForWindow(level, 15); err != nil {
			t.Errorf("validateDeflateLevelForWindow(%d, 15) = %v, want nil", level, err)
		}
	}
	for _, window_bits := range []uint{14, 8} {
		// Only these two write no references back, so they fit any window.
		for _, level := range []int{flate.HuffmanOnly, flate.NoCompression} {
			if err := validateDeflateLevelForWindow(level, window_bits); err != nil {
				t.Errorf("validateDeflateLevelForWindow(%d, %d) = %v, want nil", level, window_bits, err)
			}
		}
		for _, level := range []int{flate.DefaultCompression, 1, 2, 3, 4, 5, 6, 7, 8, 9} {
			if err := validateDeflateLevelForWindow(level, window_bits); !errors.Is(err, ErrDeflateLevelUnsupportedForWindow) {
				t.Errorf(
					"validateDeflateLevelForWindow(%d, %d) = %v, want ErrDeflateLevelUnsupportedForWindow",
					level, window_bits, err,
				)
			}
		}
	}
}
