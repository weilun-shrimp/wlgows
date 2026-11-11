package wlgows

import (
	"bytes"
	"errors"
	"io"
	"slices"
	"testing"
)

func TestNewSizeLimitReader(t *testing.T) {
	t.Run("reads reader, with max_size bytes remaining", func(t *testing.T) {
		inner := &fakeFuncReadCloser{}
		reader := NewSizeLimitReader(inner, 7).(*sizeLimitReader)
		if reader.reader != inner || reader.remainingSize != 7 || reader.exceeded {
			t.Errorf("got %+v, want reader, 7 remaining, not exceeded", reader)
		}
	})
}

func TestSizeLimitReaderRead(t *testing.T) {
	err_read := errors.New("read failed")
	var read_lengths []int // len(p) of each Read reader got
	// new_reader reads from a reader whose Reads give data and err.
	new_reader := func(remaining_size int64, data []byte, err error) *sizeLimitReader {
		read_lengths = nil
		return &sizeLimitReader{
			reader: &fakeFuncReadCloser{readFunc: func(p []byte) (int, error) {
				read_lengths = append(read_lengths, len(p))
				return copy(p, data), err
			}},
			remainingSize: remaining_size,
		}
	}

	t.Run("cuts p to the bytes remaining, and counts what reader read", func(t *testing.T) {
		reader := new_reader(3, []byte("ab"), nil)
		p := make([]byte, 16)
		n, err := reader.Read(p)
		if n != 2 || err != nil || string(p[:n]) != "ab" {
			t.Errorf("Read = %d %q, %v, want 2 \"ab\", nil", n, p[:n], err)
		}
		if len(read_lengths) != 1 || read_lengths[0] != 3 {
			t.Errorf("reader got p of lengths %v, want [3]", read_lengths)
		}
		if reader.remainingSize != 1 {
			t.Errorf("remainingSize = %d, want 1", reader.remainingSize)
		}
	})

	t.Run("p shorter than the bytes remaining goes to reader whole", func(t *testing.T) {
		reader := new_reader(10, []byte("ab"), nil)
		p := make([]byte, 4)
		n, err := reader.Read(p)
		if n != 2 || err != nil || string(p[:n]) != "ab" {
			t.Errorf("Read = %d %q, %v, want 2 \"ab\", nil", n, p[:n], err)
		}
		if len(read_lengths) != 1 || read_lengths[0] != 4 {
			t.Errorf("reader got p of lengths %v, want [4]", read_lengths)
		}
	})

	t.Run("reader's error returns as it is", func(t *testing.T) {
		reader := new_reader(10, []byte("ab"), err_read)
		n, err := reader.Read(make([]byte, 16))
		if n != 2 || err != err_read {
			t.Errorf("Read = %d, %v, want 2, %v", n, err, err_read)
		}
	})

	t.Run("an empty p reads nothing", func(t *testing.T) {
		for _, remaining_size := range []int64{0, 10} {
			reader := new_reader(remaining_size, []byte("a"), nil)
			n, err := reader.Read([]byte{})
			if n != 0 || err != nil || len(read_lengths) != 0 {
				t.Errorf("%d remaining: Read = %d, %v, reader read %v times, want 0, nil, never", remaining_size, n, err, len(read_lengths))
			}
		}
	})

	t.Run("none remaining reads one byte more; none there returns reader's error", func(t *testing.T) {
		for _, reader_err := range []error{io.EOF, err_read, nil} {
			n, err := new_reader(0, nil, reader_err).Read(make([]byte, 16))
			if n != 0 || err != reader_err {
				t.Errorf("reader error %v: Read = %d, %v, want 0, %v", reader_err, n, err, reader_err)
			}
			if len(read_lengths) != 1 || read_lengths[0] != 1 {
				t.Errorf("reader error %v: reader got p of lengths %v, want [1]", reader_err, read_lengths)
			}
		}
	})

	t.Run("none remaining and a byte more returns ErrSizeLimitExceeded, never the byte", func(t *testing.T) {
		for _, reader_err := range []error{nil, io.EOF, err_read} {
			reader := new_reader(0, []byte("a"), reader_err)
			p := make([]byte, 16)
			n, err := reader.Read(p)
			if n != 0 || err != ErrSizeLimitExceeded || p[0] != 0 {
				t.Errorf("reader error %v: Read = %d, %v, p[0] %q, want 0, ErrSizeLimitExceeded, p untouched", reader_err, n, err, p[0])
			}
		}
	})

	t.Run("a Read after ErrSizeLimitExceeded returns it again, reader not read", func(t *testing.T) {
		reader := new_reader(0, []byte("a"), nil)
		if _, err := reader.Read(make([]byte, 16)); err != ErrSizeLimitExceeded {
			t.Fatalf("first Read error %v, want ErrSizeLimitExceeded", err)
		}
		read_lengths = nil // forget the first Read's one byte more
		for _, p := range [][]byte{make([]byte, 16), {}} {
			n, err := reader.Read(p)
			if n != 0 || err != ErrSizeLimitExceeded || len(read_lengths) != 0 {
				t.Errorf("Read again with p of %d = %d, %v, reader read %v times, want 0, ErrSizeLimitExceeded, never", len(p), n, err, len(read_lengths))
			}
		}
	})

	// The reads a caller makes: io.ReadAll over messages around max_size.
	for _, tc := range []struct {
		name         string
		message_size int
		want_size    int
		want_err     error
	}{
		{
			name:         "an empty message is read whole",
			message_size: 0,
			want_size:    0,
			want_err:     nil,
		},
		{
			name:         "a message under max_size is read whole",
			message_size: 3,
			want_size:    3,
			want_err:     nil,
		},
		{
			name:         "a message of max_size is read whole",
			message_size: 5,
			want_size:    5,
			want_err:     nil,
		},
		{
			name:         "a message one byte over max_size returns ErrSizeLimitExceeded",
			message_size: 6,
			want_size:    5,
			want_err:     ErrSizeLimitExceeded,
		},
		{
			name:         "a message far over max_size returns ErrSizeLimitExceeded",
			message_size: 100,
			want_size:    5,
			want_err:     ErrSizeLimitExceeded,
		},
	} {
		t.Run("io.ReadAll: "+tc.name, func(t *testing.T) {
			message := bytes.Repeat([]byte("a"), tc.message_size)
			reader := NewSizeLimitReader(bytes.NewReader(message), 5)
			got, err := io.ReadAll(reader)
			if len(got) != tc.want_size || err != tc.want_err {
				t.Errorf("ReadAll = %d bytes, %v, want %d bytes, %v", len(got), err, tc.want_size, tc.want_err)
			}
		})
	}
}

func TestReadMessageInto(t *testing.T) {
	err_read := errors.New("read failed")

	t.Run("bytes with io.EOF are kept, and nil returns", func(t *testing.T) {
		var read_lengths []int // len(p) of each Read
		reader := &fakeFuncReadCloser{readFunc: func(p []byte) (int, error) {
			read_lengths = append(read_lengths, len(p))
			return copy(p, "ab"), io.EOF
		}}
		buf := make([]byte, 5)
		n, err := ReadMessageInto(reader, buf)
		if n != 2 || err != nil || string(buf[:n]) != "ab" {
			t.Errorf("ReadMessageInto = %d %q, %v, want 2 \"ab\", nil", n, buf[:n], err)
		}
		if !slices.Equal(read_lengths, []int{5}) {
			t.Errorf("reader got p of lengths %v, want [5]", read_lengths)
		}
	})

	t.Run("short reads go on into the rest of buf", func(t *testing.T) {
		var read_lengths []int // len(p) of each Read
		reader := &fakeFuncReadCloser{readFunc: func(p []byte) (int, error) {
			read_lengths = append(read_lengths, len(p))
			switch len(read_lengths) {
			case 1:
				return copy(p, "ab"), nil
			case 2:
				return copy(p, "cd"), nil
			default:
				return 0, io.EOF
			}
		}}
		buf := make([]byte, 5)
		n, err := ReadMessageInto(reader, buf)
		if n != 4 || err != nil || string(buf[:n]) != "abcd" {
			t.Errorf("ReadMessageInto = %d %q, %v, want 4 \"abcd\", nil", n, buf[:n], err)
		}
		if !slices.Equal(read_lengths, []int{5, 3, 1}) {
			t.Errorf("reader got p of lengths %v, want [5 3 1]", read_lengths)
		}
	})

	t.Run("a Read of 0 bytes and nil is read again", func(t *testing.T) {
		var read_lengths []int // len(p) of each Read
		reader := &fakeFuncReadCloser{readFunc: func(p []byte) (int, error) {
			read_lengths = append(read_lengths, len(p))
			switch len(read_lengths) {
			case 1:
				return 0, nil
			case 2:
				return copy(p, "ab"), nil
			default:
				return 0, io.EOF
			}
		}}
		buf := make([]byte, 5)
		n, err := ReadMessageInto(reader, buf)
		if n != 2 || err != nil || string(buf[:n]) != "ab" {
			t.Errorf("ReadMessageInto = %d %q, %v, want 2 \"ab\", nil", n, buf[:n], err)
		}
		if !slices.Equal(read_lengths, []int{5, 5, 3}) {
			t.Errorf("reader got p of lengths %v, want [5 5 3]", read_lengths)
		}
	})

	t.Run("reader's error returns as it is, with the bytes read", func(t *testing.T) {
		var read_lengths []int // len(p) of each Read
		reader := &fakeFuncReadCloser{readFunc: func(p []byte) (int, error) {
			read_lengths = append(read_lengths, len(p))
			return copy(p, "ab"), err_read
		}}
		buf := make([]byte, 5)
		n, err := ReadMessageInto(reader, buf)
		if n != 2 || err != err_read || string(buf[:n]) != "ab" {
			t.Errorf("ReadMessageInto = %d %q, %v, want 2 \"ab\", %v", n, buf[:n], err, err_read)
		}
		if !slices.Equal(read_lengths, []int{5}) {
			t.Errorf("reader got p of lengths %v, want [5]", read_lengths)
		}
	})

	t.Run("buf filled with io.EOF returns nil, no byte more read", func(t *testing.T) {
		var read_lengths []int // len(p) of each Read
		reader := &fakeFuncReadCloser{readFunc: func(p []byte) (int, error) {
			read_lengths = append(read_lengths, len(p))
			return copy(p, "abcde"), io.EOF
		}}
		buf := make([]byte, 5)
		n, err := ReadMessageInto(reader, buf)
		if n != 5 || err != nil || string(buf) != "abcde" {
			t.Errorf("ReadMessageInto = %d %q, %v, want 5 \"abcde\", nil", n, buf[:n], err)
		}
		if !slices.Equal(read_lengths, []int{5}) {
			t.Errorf("reader got p of lengths %v, want [5]", read_lengths)
		}
	})

	t.Run("buf full reads one byte more; io.EOF there returns nil", func(t *testing.T) {
		var read_lengths []int // len(p) of each Read
		reader := &fakeFuncReadCloser{readFunc: func(p []byte) (int, error) {
			read_lengths = append(read_lengths, len(p))
			switch len(read_lengths) {
			case 1:
				return copy(p, "abcde"), nil
			default:
				return 0, io.EOF
			}
		}}
		buf := make([]byte, 5)
		n, err := ReadMessageInto(reader, buf)
		if n != 5 || err != nil || string(buf) != "abcde" {
			t.Errorf("ReadMessageInto = %d %q, %v, want 5 \"abcde\", nil", n, buf[:n], err)
		}
		if !slices.Equal(read_lengths, []int{5, 1}) {
			t.Errorf("reader got p of lengths %v, want [5 1]", read_lengths)
		}
	})

	t.Run("buf full and a byte more returns ErrSizeLimitExceeded; the byte more is never written", func(t *testing.T) {
		for _, reader_err := range []error{nil, io.EOF, err_read} {
			var read_lengths []int // len(p) of each Read
			reader := &fakeFuncReadCloser{readFunc: func(p []byte) (int, error) {
				read_lengths = append(read_lengths, len(p))
				switch len(read_lengths) {
				case 1:
					return copy(p, "abcde"), nil
				default:
					return copy(p, "x"), reader_err
				}
			}}
			memory := make([]byte, 6) // memory[5] is past buf: the byte must not land there
			buf := memory[:5]
			n, err := ReadMessageInto(reader, buf)
			if n != 5 || err != ErrSizeLimitExceeded || string(buf) != "abcde" || memory[5] != 0 {
				t.Errorf("reader error %v: ReadMessageInto = %d %q, %v, byte past buf %q, want 5 \"abcde\", ErrSizeLimitExceeded, untouched", reader_err, n, buf, err, memory[5])
			}
			if !slices.Equal(read_lengths, []int{5, 1}) {
				t.Errorf("reader error %v: reader got p of lengths %v, want [5 1]", reader_err, read_lengths)
			}
		}
	})

	t.Run("buf full and reader's error at the byte more returns it", func(t *testing.T) {
		var read_lengths []int // len(p) of each Read
		reader := &fakeFuncReadCloser{readFunc: func(p []byte) (int, error) {
			read_lengths = append(read_lengths, len(p))
			switch len(read_lengths) {
			case 1:
				return copy(p, "abcde"), nil
			default:
				return 0, err_read
			}
		}}
		buf := make([]byte, 5)
		n, err := ReadMessageInto(reader, buf)
		if n != 5 || err != err_read || string(buf) != "abcde" {
			t.Errorf("ReadMessageInto = %d %q, %v, want 5 \"abcde\", %v", n, buf[:n], err, err_read)
		}
		if !slices.Equal(read_lengths, []int{5, 1}) {
			t.Errorf("reader got p of lengths %v, want [5 1]", read_lengths)
		}
	})

	t.Run("buf full and a byte more of 0 bytes and nil is read again", func(t *testing.T) {
		var read_lengths []int // len(p) of each Read
		reader := &fakeFuncReadCloser{readFunc: func(p []byte) (int, error) {
			read_lengths = append(read_lengths, len(p))
			switch len(read_lengths) {
			case 1:
				return copy(p, "abcde"), nil
			case 2:
				return 0, nil
			default:
				return 0, io.EOF
			}
		}}
		buf := make([]byte, 5)
		n, err := ReadMessageInto(reader, buf)
		if n != 5 || err != nil || string(buf) != "abcde" {
			t.Errorf("ReadMessageInto = %d %q, %v, want 5 \"abcde\", nil", n, buf[:n], err)
		}
		if !slices.Equal(read_lengths, []int{5, 1, 1}) {
			t.Errorf("reader got p of lengths %v, want [5 1 1]", read_lengths)
		}
	})

	for _, tc := range []struct {
		name         string
		buf_size     int
		message_size int
		want_size    int
		want_err     error
	}{
		{
			name:         "an empty message is read whole",
			buf_size:     5,
			message_size: 0,
			want_size:    0,
			want_err:     nil,
		},
		{
			name:         "a message under len(buf) is read whole",
			buf_size:     5,
			message_size: 3,
			want_size:    3,
			want_err:     nil,
		},
		{
			name:         "a message of len(buf) is read whole",
			buf_size:     5,
			message_size: 5,
			want_size:    5,
			want_err:     nil,
		},
		{
			name:         "a message one byte over len(buf) returns ErrSizeLimitExceeded",
			buf_size:     5,
			message_size: 6,
			want_size:    5,
			want_err:     ErrSizeLimitExceeded,
		},
		{
			name:         "a message far over len(buf) returns ErrSizeLimitExceeded",
			buf_size:     5,
			message_size: 100,
			want_size:    5,
			want_err:     ErrSizeLimitExceeded,
		},
		{
			name:         "an empty buf reads an empty message",
			buf_size:     0,
			message_size: 0,
			want_size:    0,
			want_err:     nil,
		},
		{
			name:         "an empty buf and one byte returns ErrSizeLimitExceeded",
			buf_size:     0,
			message_size: 1,
			want_size:    0,
			want_err:     ErrSizeLimitExceeded,
		},
	} {
		t.Run("bytes.Reader: "+tc.name, func(t *testing.T) {
			message := bytes.Repeat([]byte("a"), tc.message_size)
			buf := make([]byte, tc.buf_size)
			n, err := ReadMessageInto(bytes.NewReader(message), buf)
			if n != tc.want_size || err != tc.want_err || !bytes.Equal(buf[:n], message[:n]) {
				t.Errorf("ReadMessageInto = %d %q, %v, want %d bytes of message, %v", n, buf[:n], err, tc.want_size, tc.want_err)
			}
		})
	}
}
