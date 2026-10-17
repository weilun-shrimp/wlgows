package wlgows

import (
	"bytes"
	"errors"
	"io"
	"testing"
)

func TestUTF8Checker(t *testing.T) {
	// 中 is E4 B8 AD, 😀 is F0 9F 98 80
	tests := []struct {
		name string
		puts [][]byte
		want bool
	}{
		{"nothing put", nil, true},
		{"empty puts", [][]byte{{}, {}}, true},
		{"ascii", [][]byte{[]byte("hello")}, true},
		{"whole runes", [][]byte{[]byte("中"), []byte("😀")}, true},
		{"rune split 1 + 2", [][]byte{{'a', 0xE4}, {0xB8, 0xAD, 'b'}}, true},
		{"rune split 2 + 1", [][]byte{{0xE4, 0xB8}, {0xAD}}, true},
		{"rune split over 4 puts", [][]byte{{0xF0}, {0x9F}, {}, {0x98}, {0x80}}, true},
		{"cut by 1 byte at the end", [][]byte{{'a', 0xF0, 0x9F, 0x98}}, false},
		{"cut by 2 bytes at the end", [][]byte{{0xF0, 0x9F}}, false},
		{"cut by 3 bytes at the end", [][]byte{{0xF0}}, false},
		{"cut across puts at the end", [][]byte{{0xF0}, {0x9F, 0x98}}, false},
		{"invalid byte", [][]byte{{'a', 0xFF, 'b'}}, false},
		{"stays invalid after valid puts", [][]byte{{0xFF}, []byte("abc")}, false},
		{"continuation byte alone", [][]byte{{0x80}}, false},
		{"continuation bytes only at the end", [][]byte{{'a', 0x80, 0x80, 0x80}}, false},
		{"split rune completed wrong", [][]byte{{0xE4}, {'a', 'b'}}, false},
		{"overlong", [][]byte{{0xC0}, {0x80}}, false},
		{"surrogate", [][]byte{{0xED, 0xA0}, {0x80}}, false},
		{"surrogate in one put", [][]byte{{0xED, 0xA0, 0x80}}, false},
		{"split rune invalid before it is full", [][]byte{{0xE0}, {0x80, 0x80}}, false},
		{"invalid start at the end is not kept", [][]byte{{'a', 0xE0, 0x80}}, false},
		{"above U+10FFFF", [][]byte{{0xF4, 0x90}, {0x80, 0x80}}, false},
		{"U+10FFFF", [][]byte{{0xF4, 0x8F}, {0xBF, 0xBF}}, true},
		{"U+FFFD", [][]byte{{0xEF, 0xBF}, {0xBD}}, true},
		{"rune completed, then a new one cut", [][]byte{{0xF0, 0x9F}, {0x98, 0x80, 0xE4}}, false},
		{"4 continuation bytes at the end", [][]byte{{0xF0, 0x9F, 0x98, 0x80, 0x80}}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var checker utf8Checker
			for _, data := range tt.puts {
				checker.put(data)
			}
			if got := checker.isValid(); got != tt.want {
				t.Errorf("isValid() = %v, want %v", got, tt.want)
			}
		})
	}

	t.Run("asked between puts", func(t *testing.T) {
		var checker utf8Checker
		checker.put([]byte{0xE4, 0xB8})
		if checker.isValid() {
			t.Errorf("isValid() with a rune cut = true, want false")
		}
		checker.put([]byte{0xAD})
		if !checker.isValid() {
			t.Errorf("isValid() with the rune completed = false, want true")
		}
	})

	t.Run("keeps no reference to the data put", func(t *testing.T) {
		var checker utf8Checker
		data := []byte{'a', 0xE4, 0xB8}
		checker.put(data)
		data[1], data[2] = 0xFF, 0xFF // the caller reuses its buffer
		checker.put([]byte{0xAD})
		if !checker.isValid() {
			t.Errorf("isValid() = false, want true")
		}
	})
}

func TestUTF8CheckReader(t *testing.T) {
	err_read := errors.New("read failed")
	err_close := errors.New("close failed")
	// read is one Read's bytes and error.
	type read = struct {
		data []byte
		err  error
	}
	new_reader := func(reads []read) *utf8CheckReader {
		return &utf8CheckReader{reader: &fakeFuncReadCloser{
			readFunc: func(p []byte) (int, error) {
				if len(reads) == 0 {
					return 0, io.EOF
				}
				next := reads[0]
				reads = reads[1:]
				return copy(p, next.data), next.err
			},
			closeFunc: func() error { return err_close },
		}}
	}

	// RFC 6455 8.1: a text message must be UTF-8. It is known only once the
	// whole message is read.
	for _, tc := range []struct {
		name     string
		reads    []read
		want_err error
	}{
		{"UTF-8 passes io.EOF", []read{{[]byte("中"), nil}, {[]byte("a"), io.EOF}}, io.EOF},
		{"nothing read passes io.EOF", nil, io.EOF},
		{"a rune split across Reads passes", []read{{[]byte{0xE4}, nil}, {[]byte{0xB8, 0xAD}, io.EOF}}, io.EOF},
		{"not UTF-8 returns ErrInvalidUTF8 at io.EOF", []read{{[]byte{0xFF}, nil}, {nil, io.EOF}}, ErrInvalidUTF8},
		{"a rune cut at the end returns ErrInvalidUTF8", []read{{[]byte{0xE4, 0xB8}, io.EOF}}, ErrInvalidUTF8},
		{"another error returns as it is", []read{{[]byte{0xFF}, err_read}}, err_read},
	} {
		t.Run(tc.name+", every byte read handed out", func(t *testing.T) {
			var want_bytes []byte
			for _, r := range tc.reads {
				want_bytes = append(want_bytes, r.data...)
			}
			reader := new_reader(tc.reads)
			var got_bytes []byte
			var err error
			// the fake returns io.EOF once its reads run out
			for i := 0; i <= len(tc.reads) && err == nil; i++ {
				p := make([]byte, 16)
				var n int
				n, err = reader.Read(p)
				got_bytes = append(got_bytes, p[:n]...)
			}
			if err != tc.want_err || !bytes.Equal(got_bytes, want_bytes) {
				t.Errorf("Reads gave % x, %v, want % x, %v", got_bytes, err, want_bytes, tc.want_err)
			}
		})
	}

	t.Run("a Read after ErrInvalidUTF8 returns it again", func(t *testing.T) {
		reader := new_reader([]read{{[]byte{0xFF}, io.EOF}})
		reader.Read(make([]byte, 16))
		if _, err := reader.Read(make([]byte, 16)); err != ErrInvalidUTF8 {
			t.Errorf("Read again error %v, want ErrInvalidUTF8", err)
		}
	})

	t.Run("Close closes reader", func(t *testing.T) {
		if err := new_reader(nil).Close(); err != err_close {
			t.Errorf("Close = %v, want reader's %v", err, err_close)
		}
	})
}
