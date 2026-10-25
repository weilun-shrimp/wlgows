package wlgows

import (
	"bytes"
	"compress/flate"
	"errors"
	"fmt"
	"io"
	"slices"
	"sync"
	"testing"
	"time"
)

// fakeFuncPipeSource runs its funcs, so a test can record each call. It is an
// inflaterPipeSource, and a pointer so a test can tell which one it got. Set
// the ones the code under test calls.
type fakeFuncPipeSource struct {
	readFunc     func(p []byte) (int, error)
	readByteFunc func() (byte, error)
	putFunc      func(data []byte) error
	closeFunc    func(err error)
	dropFunc     func()
}

func (s *fakeFuncPipeSource) Read(p []byte) (int, error) { return s.readFunc(p) }
func (s *fakeFuncPipeSource) ReadByte() (byte, error)    { return s.readByteFunc() }
func (s *fakeFuncPipeSource) put(data []byte) error      { return s.putFunc(data) }
func (s *fakeFuncPipeSource) close(err error)            { s.closeFunc(err) }
func (s *fakeFuncPipeSource) drop()                      { s.dropFunc() }

// fakeFuncFlateReaderPool runs its funcs, so a test can record each call. It is
// a flateReaderPool. Set the ones the code under test calls.
type fakeFuncFlateReaderPool struct {
	getFunc func() any
	putFunc func(x any)
}

func (p fakeFuncFlateReaderPool) Get() any  { return p.getFunc() }
func (p fakeFuncFlateReaderPool) Put(x any) { p.putFunc(x) }

// fakeFuncFlateReader runs its funcs, so a test can record each call. It is an
// io.ReadCloser and a flate.Resetter, as compress/flate's readers are, and a
// pointer so a test can tell which one it got. Set the ones the code under
// test calls.
type fakeFuncFlateReader struct {
	readFunc  func(p []byte) (int, error)
	closeFunc func() error
	resetFunc func(source io.Reader, dict []byte) error
}

func (r *fakeFuncFlateReader) Read(p []byte) (int, error) { return r.readFunc(p) }
func (r *fakeFuncFlateReader) Close() error               { return r.closeFunc() }
func (r *fakeFuncFlateReader) Reset(source io.Reader, dict []byte) error {
	return r.resetFunc(source, dict)
}

func TestInflateTail(t *testing.T) {
	// RFC 7692 7.2.2: the sender's last flush ends in 00 00 ff ff, which it
	// strips (7.2.1). With inflateTail after it, the message ends with io.EOF.
	t.Run("ends a stripped message with io.EOF", func(t *testing.T) {
		var deflated_buffer bytes.Buffer
		writer, _ := flate.NewWriter(&deflated_buffer, flate.BestCompression)
		writer.Write([]byte("hello hello hello"))
		writer.Flush()
		message := bytes.TrimSuffix(deflated_buffer.Bytes(), []byte{0x00, 0x00, 0xff, 0xff})

		reader := flate.NewReader(bytes.NewReader(append(message, inflateTail...)))
		got, err := io.ReadAll(reader)
		if string(got) != "hello hello hello" || err != nil {
			t.Errorf("ReadAll = %q, %v, want the text, then io.EOF", got, err)
		}
	})
}

func TestNoContextInflateDataFrames(t *testing.T) {
	err_read := errors.New("read failed")
	var steps []string
	source := &fakeFuncPipeSource{}
	reader := &fakeFuncFlateReader{}
	base_di := func() noContextInflateDataFramesDI {
		steps = nil
		*source = fakeFuncPipeSource{
			putFunc: func(data []byte) error {
				steps = append(steps, fmt.Sprintf("put % x", data))
				return nil
			},
			closeFunc: func(err error) { steps = append(steps, fmt.Sprintf("close %v", err)) },
			dropFunc:  func() { steps = append(steps, "drop") },
		}
		*reader = fakeFuncFlateReader{readFunc: func(p []byte) (int, error) {
			steps = append(steps, fmt.Sprintf("reader read %d", cap(p)))
			return 0, nil
		}}
		return noContextInflateDataFramesDI{
			newPipeSource: func() inflaterPipeSource {
				steps = append(steps, "new source")
				return source
			},
			getFlateReader: func(got io.Reader) io.ReadCloser {
				if got != io.Reader(source) {
					t.Errorf("getFlateReader got %v, want the new source", got)
				}
				steps = append(steps, "get reader")
				return reader
			},
			noContextFlateReaders: fakeFuncFlateReaderPool{putFunc: func(got any) {
				if got != any(reader) {
					t.Errorf("Put got %v, want the reader it got", got)
				}
				steps = append(steps, "put reader")
			}},
			readInflated: func(inflate_read func(buf []byte) (int, error), buf []byte) (int, error) {
				steps = append(steps, fmt.Sprintf("read inflated %d", cap(buf)))
				inflate_read(buf)
				return 3, err_read
			},
		}
	}

	// RFC 7692 7.2.2: the tail goes after the payloads, and io.EOF closes the
	// source once the whole message is in. The source is dropped before the
	// reader, which still points at it, goes back to the pool.
	t.Run("puts the payloads and the tail, reads them with the reader, drops the source, then pools the reader", func(t *testing.T) {
		di := base_di()
		frames := DataFrames{
			{PayloadData: []byte{0x01, 0x02}},
			{PayloadData: []byte{}},
			{PayloadData: []byte{0x03}},
		}
		buf := make([]byte, 0, 8)
		n, err := noContextInflateDataFrames(frames, buf, di)
		if n != 3 || err != err_read {
			t.Errorf("noContextInflateDataFrames = %d, %v, want readInflated's 3, %v", n, err, err_read)
		}
		want := []string{
			"new source",
			"put 01 02",
			"put 03",
			fmt.Sprintf("put % x", inflateTail),
			"close EOF",
			"get reader",
			"read inflated 8",
			"reader read 8",
			"drop",
			"put reader",
		}
		if !slices.Equal(steps, want) {
			t.Errorf("steps %q, want %q", steps, want)
		}
	})

	t.Run("io.EOF from readInflated returns nil: the message ended", func(t *testing.T) {
		di := base_di()
		di.readInflated = func(inflate_read func(buf []byte) (int, error), buf []byte) (int, error) {
			return 3, io.EOF
		}
		n, err := noContextInflateDataFrames(DataFrames{}, make([]byte, 0, 8), di)
		if n != 3 || err != nil {
			t.Errorf("noContextInflateDataFrames = %d, %v, want 3, nil", n, err)
		}
	})
}

func TestGetFlateReader(t *testing.T) {
	var steps []string
	source := &fakeIOReader{}
	new_flate_reader := &fakeFuncFlateReader{}
	base_di := func() getFlateReaderDI {
		steps = nil
		return getFlateReaderDI{
			noContextFlateReaders: fakeFuncFlateReaderPool{getFunc: func() any {
				steps = append(steps, "get from pool")
				return nil
			}},
			newFlateReader: func(got io.Reader) io.ReadCloser {
				if got != io.Reader(source) {
					t.Errorf("newFlateReader got %v, want source", got)
				}
				steps = append(steps, "new reader")
				return new_flate_reader
			},
		}
	}

	t.Run("an empty pool makes a new reader on source", func(t *testing.T) {
		reader := getFlateReader(source, base_di())
		if reader != io.ReadCloser(new_flate_reader) {
			t.Errorf("getFlateReader = %v, want the new reader", reader)
		}
		if want := []string{"get from pool", "new reader"}; !slices.Equal(steps, want) {
			t.Errorf("steps %q, want %q", steps, want)
		}
	})

	// The pooled reader still points at the last message: Reset points it at
	// source, from an empty window.
	t.Run("a pooled reader is reset to read source from an empty window", func(t *testing.T) {
		di := base_di()
		var reset_source io.Reader
		var reset_dict []byte
		reader_in_pool := &fakeFuncFlateReader{resetFunc: func(got io.Reader, dict []byte) error {
			steps = append(steps, "reset")
			reset_source, reset_dict = got, dict
			return nil
		}}
		di.noContextFlateReaders = fakeFuncFlateReaderPool{getFunc: func() any {
			steps = append(steps, "get from pool")
			return reader_in_pool
		}}
		reader := getFlateReader(source, di)
		if reader != io.ReadCloser(reader_in_pool) {
			t.Errorf("getFlateReader = %v, want the pooled reader", reader)
		}
		if reset_source != io.Reader(source) || reset_dict != nil {
			t.Errorf("Reset(%v, %q), want source and no dict", reset_source, reset_dict)
		}
		if want := []string{"get from pool", "reset"}; !slices.Equal(steps, want) {
			t.Errorf("steps %q, want %q", steps, want)
		}
	})
}

func TestReadInflated(t *testing.T) {
	err_read := errors.New("read failed")

	t.Run("reads into buf from its start until io.EOF", func(t *testing.T) {
		var caps []int
		results := []struct {
			data string
			err  error
		}{{"abc", nil}, {"de", io.EOF}}
		inflate_read := func(buf []byte) (int, error) {
			caps = append(caps, cap(buf))
			result := results[0]
			results = results[1:]
			return copy(buf, result.data), result.err
		}
		buf := make([]byte, 2, 10)
		n, err := readInflated(inflate_read, buf)
		if n != 5 || err != io.EOF || string(buf[:n]) != "abcde" {
			t.Errorf("readInflated = %d, %v, %q, want 5, io.EOF, \"abcde\"", n, err, buf[:n])
		}
		if want := []int{10, 7}; !slices.Equal(caps, want) {
			t.Errorf("read with caps %v, want %v", caps, want)
		}
	})

	t.Run("another error returns it with what was read, its own bytes too", func(t *testing.T) {
		results := []struct {
			data string
			err  error
		}{
			{"abc", nil},
			{"de", err_read},
		}
		inflate_read := func(buf []byte) (int, error) {
			result := results[0]
			results = results[1:]
			return copy(buf, result.data), result.err
		}
		buf := make([]byte, 0, 10)
		n, err := readInflated(inflate_read, buf)
		if n != 5 || err != err_read || string(buf[:n]) != "abcde" {
			t.Errorf("readInflated = %d, %v, %q, want 5, %v, \"abcde\"", n, err, buf[:n], err_read)
		}
	})

	t.Run("a full buf then io.EOF returns io.EOF", func(t *testing.T) {
		var caps []int
		inflate_read := func(buf []byte) (int, error) {
			caps = append(caps, cap(buf))
			if len(caps) == 1 {
				return copy(buf, "abcd"), nil
			}
			return 0, io.EOF
		}
		buf := make([]byte, 0, 4)
		n, err := readInflated(inflate_read, buf)
		if n != 4 || err != io.EOF || string(buf[:n]) != "abcd" {
			t.Errorf("readInflated = %d, %v, %q, want 4, io.EOF, \"abcd\"", n, err, buf[:n])
		}
		if want := []int{4, 1}; !slices.Equal(caps, want) {
			t.Errorf("read with caps %v, want %v: buf, then a 1 byte probe", caps, want)
		}
	})

	t.Run("a byte past a full buf returns ErrInflatedSizeExceeded", func(t *testing.T) {
		calls := 0
		inflate_read := func(buf []byte) (int, error) {
			calls++
			if calls == 1 {
				return copy(buf, "abcd"), nil
			}
			return copy(buf, "e"), nil
		}
		buf := make([]byte, 0, 4)
		n, err := readInflated(inflate_read, buf)
		if n != 4 || err != ErrInflatedSizeExceeded || string(buf[:n]) != "abcd" {
			t.Errorf("readInflated = %d, %v, %q, want 4, ErrInflatedSizeExceeded, \"abcd\"", n, err, buf[:n])
		}
	})

	// io.Reader allows a byte together with io.EOF: it is still a byte past
	// the room.
	t.Run("a byte with io.EOF past a full buf returns ErrInflatedSizeExceeded", func(t *testing.T) {
		calls := 0
		inflate_read := func(buf []byte) (int, error) {
			calls++
			if calls == 1 {
				return copy(buf, "abcd"), nil
			}
			return copy(buf, "e"), io.EOF
		}
		n, err := readInflated(inflate_read, make([]byte, 0, 4))
		if n != 4 || err != ErrInflatedSizeExceeded {
			t.Errorf("readInflated = %d, %v, want 4, ErrInflatedSizeExceeded", n, err)
		}
	})

	t.Run("a full buf then another error returns it", func(t *testing.T) {
		calls := 0
		inflate_read := func(buf []byte) (int, error) {
			calls++
			if calls == 1 {
				return copy(buf, "abcd"), nil
			}
			return 0, err_read
		}
		n, err := readInflated(inflate_read, make([]byte, 0, 4))
		if n != 4 || err != err_read {
			t.Errorf("readInflated = %d, %v, want 4, %v", n, err, err_read)
		}
	})

	t.Run("no room probes at once", func(t *testing.T) {
		var caps []int
		inflate_read := func(buf []byte) (int, error) {
			caps = append(caps, cap(buf))
			return 0, io.EOF
		}
		n, err := readInflated(inflate_read, nil)
		if n != 0 || err != io.EOF {
			t.Errorf("readInflated = %d, %v, want 0, io.EOF", n, err)
		}
		if want := []int{1}; !slices.Equal(caps, want) {
			t.Errorf("read with caps %v, want %v", caps, want)
		}
	})
}

func TestContextInflateDataFrames(t *testing.T) {
	err_put := errors.New("put failed")
	err_read := errors.New("read failed")
	var steps []string
	base_di := func() contextInflateDataFramesDI {
		steps = nil
		return contextInflateDataFramesDI{
			startInflateFrames: func() { steps = append(steps, "start") },
			putDeflatedFrame: func(f *Frame) error {
				steps = append(steps, fmt.Sprintf("put %s", f.PayloadData))
				return nil
			},
			pullInflatedBytes: func(buf []byte) (int, error) {
				steps = append(steps, fmt.Sprintf("pull %d", cap(buf)))
				return 0, nil
			},
			endInflateFrames: func() { steps = append(steps, "end") },
			readInflated: func(inflate_read func(buf []byte) (int, error), buf []byte) (int, error) {
				steps = append(steps, fmt.Sprintf("read inflated %d", cap(buf)))
				inflate_read(buf)
				return 3, err_read
			},
		}
	}
	frames := DataFrames{{PayloadData: []byte("a")}, {PayloadData: []byte("b"), FIN: true}}

	t.Run("starts, puts every frame, reads them with pull, ends", func(t *testing.T) {
		di := base_di()
		n, err := contextInflateDataFrames(frames, make([]byte, 0, 8), di)
		if n != 3 || err != err_read {
			t.Errorf("contextInflateDataFrames = %d, %v, want readInflated's 3, %v", n, err, err_read)
		}
		want := []string{"start", "put a", "put b", "read inflated 8", "pull 8", "end"}
		if !slices.Equal(steps, want) {
			t.Errorf("steps %q, want %q", steps, want)
		}
	})

	t.Run("a put error stops there, ends, and returns it", func(t *testing.T) {
		di := base_di()
		di.putDeflatedFrame = func(f *Frame) error {
			steps = append(steps, fmt.Sprintf("put %s", f.PayloadData))
			return err_put
		}
		n, err := contextInflateDataFrames(frames, make([]byte, 0, 8), di)
		if n != 0 || err != err_put {
			t.Errorf("contextInflateDataFrames = %d, %v, want 0, %v", n, err, err_put)
		}
		if want := []string{"start", "put a", "end"}; !slices.Equal(steps, want) {
			t.Errorf("steps %q, want %q", steps, want)
		}
	})

	t.Run("io.EOF from readInflated returns nil: the message ended", func(t *testing.T) {
		di := base_di()
		di.readInflated = func(inflate_read func(buf []byte) (int, error), buf []byte) (int, error) {
			return 3, io.EOF
		}
		n, err := contextInflateDataFrames(frames, make([]byte, 0, 8), di)
		if n != 3 || err != nil {
			t.Errorf("contextInflateDataFrames = %d, %v, want 3, nil", n, err)
		}
	})
}

func TestStartInflateFrames(t *testing.T) {
	var steps []string
	var new_dict []byte
	source := &fakeFuncPipeSource{}
	reader := &fakeFuncFlateReader{}
	base_di := func() startInflateFramesDI {
		steps, new_dict = nil, nil
		return startInflateFramesDI{
			inflater: &contextInflater{
				framesLocker: fakeFuncLocker{
					lock:   func() { steps = append(steps, "frames lock") },
					unlock: func() { steps = append(steps, "frames unlock") },
				},
				history: []byte("abc"),
			},
			newPipeSource: func() inflaterPipeSource {
				steps = append(steps, "new source")
				return source
			},
			newFlateReader: func(got io.Reader, dict []byte) io.ReadCloser {
				if got != io.Reader(source) {
					t.Errorf("newFlateReader got %v, want the new source", got)
				}
				steps = append(steps, "new reader")
				new_dict = dict
				return reader
			},
		}
	}

	t.Run("locks, then builds the reader on a new source from history", func(t *testing.T) {
		di := base_di()
		startInflateFrames(di)
		if want := []string{"frames lock", "new source", "new reader"}; !slices.Equal(steps, want) {
			t.Errorf("steps %q, want %q", steps, want)
		}
		if string(new_dict) != "abc" {
			t.Errorf("dict %q, want the history \"abc\"", new_dict)
		}
		if di.inflater.pipeSource != inflaterPipeSource(source) || di.inflater.flateReader != io.ReadCloser(reader) {
			t.Errorf("pipeSource, flateReader = %v, %v, want the new ones", di.inflater.pipeSource, di.inflater.flateReader)
		}
	})

	t.Run("the dict is the last 32 KB of history", func(t *testing.T) {
		di := base_di()
		di.inflater.history = make([]byte, 40*1024) // 40 KB
		for i := range di.inflater.history {
			di.inflater.history[i] = byte(i)
		}
		startInflateFrames(di)
		if !bytes.Equal(new_dict, di.inflater.history[8*1024:]) {
			t.Errorf("dict is %d bytes, want the last 32 KB of history", len(new_dict))
		}
	})

	t.Run("resets the reader it has", func(t *testing.T) {
		di := base_di()
		var reset_source io.Reader
		var reset_dict []byte
		reader_it_had := &fakeFuncFlateReader{resetFunc: func(got io.Reader, dict []byte) error {
			steps = append(steps, "reset reader")
			reset_source, reset_dict = got, dict
			return nil
		}}
		di.inflater.flateReader = reader_it_had
		startInflateFrames(di)
		if want := []string{"frames lock", "new source", "reset reader"}; !slices.Equal(steps, want) {
			t.Errorf("steps %q, want %q", steps, want)
		}
		if reset_source != io.Reader(source) || string(reset_dict) != "abc" {
			t.Errorf("Reset(%v, %q), want the new source and the history \"abc\"", reset_source, reset_dict)
		}
		if di.inflater.flateReader != io.ReadCloser(reader_it_had) {
			t.Errorf("flateReader = %v, want the one it had", di.inflater.flateReader)
		}
	})
}

func TestContextInflaterPutDeflatedFrame(t *testing.T) {
	err_put := errors.New("put failed")
	var steps []string
	source := &fakeFuncPipeSource{}
	base_inflater := func() *contextInflater {
		steps = nil
		*source = fakeFuncPipeSource{
			putFunc: func(data []byte) error {
				steps = append(steps, fmt.Sprintf("put % x", data))
				return nil
			},
			closeFunc: func(err error) { steps = append(steps, fmt.Sprintf("close %v", err)) },
		}
		return &contextInflater{
			pipeSourceLocker: fakeFuncLocker{
				lock:   func() { steps = append(steps, "lock") },
				unlock: func() { steps = append(steps, "unlock") },
			},
			pipeSource: source,
		}
	}
	tail_put_step := fmt.Sprintf("put % x", inflateTail)

	t.Run("puts the payload under the lock", func(t *testing.T) {
		err := base_inflater().putDeflatedFrame(&Frame{PayloadData: []byte{0x01}})
		if err != nil {
			t.Errorf("putDeflatedFrame = %v, want nil", err)
		}
		if want := []string{"lock", "put 01", "unlock"}; !slices.Equal(steps, want) {
			t.Errorf("steps %q, want %q", steps, want)
		}
	})

	t.Run("puts nothing for an empty payload", func(t *testing.T) {
		err := base_inflater().putDeflatedFrame(&Frame{PayloadData: []byte{}})
		if err != nil {
			t.Errorf("putDeflatedFrame = %v, want nil", err)
		}
		if want := []string{"lock", "unlock"}; !slices.Equal(steps, want) {
			t.Errorf("steps %q, want %q", steps, want)
		}
	})

	// RFC 7692 7.2.2: the tail goes after the last payload.
	t.Run("after the FIN frame's payload, puts the tail and closes with io.EOF", func(t *testing.T) {
		err := base_inflater().putDeflatedFrame(&Frame{FIN: true, PayloadData: []byte{0x01}})
		if err != nil {
			t.Errorf("putDeflatedFrame = %v, want nil", err)
		}
		if want := []string{"lock", "put 01", tail_put_step, "close EOF", "unlock"}; !slices.Equal(steps, want) {
			t.Errorf("steps %q, want %q", steps, want)
		}
	})

	t.Run("a FIN frame with an empty payload puts only the tail", func(t *testing.T) {
		err := base_inflater().putDeflatedFrame(&Frame{FIN: true, PayloadData: []byte{}})
		if err != nil {
			t.Errorf("putDeflatedFrame = %v, want nil", err)
		}
		if want := []string{"lock", tail_put_step, "close EOF", "unlock"}; !slices.Equal(steps, want) {
			t.Errorf("steps %q, want %q", steps, want)
		}
	})

	t.Run("a payload put error returns it, puts nothing more", func(t *testing.T) {
		inflater := base_inflater()
		source.putFunc = func(data []byte) error {
			steps = append(steps, fmt.Sprintf("put % x", data))
			return err_put
		}
		err := inflater.putDeflatedFrame(&Frame{FIN: true, PayloadData: []byte{0x01}})
		if err != err_put {
			t.Errorf("putDeflatedFrame = %v, want %v", err, err_put)
		}
		if want := []string{"lock", "put 01", "unlock"}; !slices.Equal(steps, want) {
			t.Errorf("steps %q, want %q", steps, want)
		}
	})

	t.Run("a tail put error returns it, does not close", func(t *testing.T) {
		inflater := base_inflater()
		source.putFunc = func(data []byte) error {
			steps = append(steps, fmt.Sprintf("put % x", data))
			if bytes.Equal(data, inflateTail) {
				return err_put
			}
			return nil
		}
		err := inflater.putDeflatedFrame(&Frame{FIN: true, PayloadData: []byte{0x01}})
		if err != err_put {
			t.Errorf("putDeflatedFrame = %v, want %v", err, err_put)
		}
		if want := []string{"lock", "put 01", tail_put_step, "unlock"}; !slices.Equal(steps, want) {
			t.Errorf("steps %q, want %q", steps, want)
		}
	})
}

func TestContextInflaterPullInflatedBytes(t *testing.T) {
	err_read := errors.New("read failed")

	t.Run("reads into buf from its start and adds it to history", func(t *testing.T) {
		var read_len int
		inflater := &contextInflater{
			history: []byte("ab"),
			flateReader: &fakeFuncFlateReader{readFunc: func(p []byte) (int, error) {
				read_len = len(p)
				return copy(p, "cd"), err_read
			}},
		}
		buf := make([]byte, 0, 8)
		n, err := inflater.pullInflatedBytes(buf)
		if n != 2 || err != err_read || string(buf[:n]) != "cd" {
			t.Errorf("pullInflatedBytes = %d, %v, %q, want 2, %v, \"cd\"", n, err, buf[:n], err_read)
		}
		if read_len != 8 {
			t.Errorf("read %d bytes of room, want buf[:cap(buf)], 8", read_len)
		}
		if string(inflater.history) != "abcd" {
			t.Errorf("history %q, want \"abcd\"", inflater.history)
		}
	})

	t.Run("no room reads nothing", func(t *testing.T) {
		inflater := &contextInflater{
			history: []byte("ab"),
			flateReader: &fakeFuncFlateReader{readFunc: func(p []byte) (int, error) {
				t.Errorf("read, want no read")
				return 0, nil
			}},
		}
		n, err := inflater.pullInflatedBytes(nil)
		if n != 0 || err != nil {
			t.Errorf("pullInflatedBytes = %d, %v, want 0, nil", n, err)
		}
		if string(inflater.history) != "ab" {
			t.Errorf("history %q, want it unchanged, \"ab\"", inflater.history)
		}
	})
}

func TestContextInflaterEndInflateFrames(t *testing.T) {
	t.Run("drops the source under the lock, then lets the next message start", func(t *testing.T) {
		var steps []string
		inflater := &contextInflater{
			framesLocker: fakeFuncLocker{
				lock:   func() { steps = append(steps, "frames lock") },
				unlock: func() { steps = append(steps, "frames unlock") },
			},
			pipeSourceLocker: fakeFuncLocker{
				lock:   func() { steps = append(steps, "put lock") },
				unlock: func() { steps = append(steps, "put unlock") },
			},
			pipeSource: &fakeFuncPipeSource{dropFunc: func() { steps = append(steps, "drop") }},
		}
		inflater.endInflateFrames()
		if want := []string{"put lock", "drop", "put unlock", "frames unlock"}; !slices.Equal(steps, want) {
			t.Errorf("steps %q, want %q", steps, want)
		}
		if inflater.pipeSource != nil {
			t.Errorf("pipeSource = %v, want nil", inflater.pipeSource)
		}
	})
}

func TestContextInflaterDropContext(t *testing.T) {
	t.Run("frees the history", func(t *testing.T) {
		inflater := &contextInflater{history: []byte("abc")}
		inflater.dropContext()
		if inflater.history != nil {
			t.Errorf("history %q, want nil", inflater.history)
		}
	})
}

func TestNewPipeSource(t *testing.T) {
	t.Run("is empty and open, its cond on its locker", func(t *testing.T) {
		source := newPipeSource().(*pipeSource)
		if source.pipe != nil || source.err != nil {
			t.Errorf("pipe, err = %q, %v, want nil, nil", source.pipe, source.err)
		}
		if source.locker == nil || source.cond == nil || source.cond.L != source.locker {
			t.Errorf("locker %v, cond %v, want a cond on the locker", source.locker, source.cond)
		}
	})
}

func TestPipeSourcePut(t *testing.T) {
	t.Run("adds data after what pipe holds, in place", func(t *testing.T) {
		source := newPipeSource().(*pipeSource)
		first, second := []byte("ab"), []byte("c")
		source.put(first)
		err := source.put(second)
		if err != nil {
			t.Errorf("put = %v, want nil", err)
		}
		if len(source.pipe) != 2 || &source.pipe[0][0] != &first[0] || &source.pipe[1][0] != &second[0] {
			t.Errorf("pipe %q, want the two payloads in order, not copied", source.pipe)
		}
	})

	t.Run("closed, returns its err and adds nothing", func(t *testing.T) {
		source := newPipeSource().(*pipeSource)
		source.err = io.EOF
		if err := source.put([]byte("ab")); err != io.EOF {
			t.Errorf("put = %v, want io.EOF", err)
		}
		if source.pipe != nil {
			t.Errorf("pipe %q, want nothing added", source.pipe)
		}
	})

	t.Run("wakes a waiting Read", func(t *testing.T) {
		mutex := &sync.Mutex{}
		waiting := make(chan struct{}, 1)
		locker := fakeFuncLocker{lock: mutex.Lock, unlock: func() {
			mutex.Unlock()
			select {
			case waiting <- struct{}{}:
			default:
			}
		}}
		source := &pipeSource{locker: locker, cond: sync.NewCond(locker)}
		done := make(chan string)
		go func() {
			p := make([]byte, 4)
			n, err := source.Read(p)
			done <- fmt.Sprintf("%d %v %q", n, err, p[:n])
		}()
		<-waiting // the Read unlocks first inside cond.Wait
		source.put([]byte("ab"))
		select {
		case got := <-done:
			if got != `2 <nil> "ab"` {
				t.Errorf("Read = %s, want 2 <nil> \"ab\"", got)
			}
		case <-time.After(time.Second):
			t.Fatalf("Read still waits after put")
		}
	})

	t.Run("locks", func(t *testing.T) {
		var steps []string
		locker := fakeFuncLocker{
			lock:   func() { steps = append(steps, "lock") },
			unlock: func() { steps = append(steps, "unlock") },
		}
		source := &pipeSource{locker: locker, cond: sync.NewCond(locker)}
		source.put([]byte("ab"))
		if want := []string{"lock", "unlock"}; !slices.Equal(steps, want) {
			t.Errorf("steps %q, want %q", steps, want)
		}
	})
}

func TestPipeSourceClose(t *testing.T) {
	t.Run("closes with err, keeps what pipe holds", func(t *testing.T) {
		source := newPipeSource().(*pipeSource)
		source.put([]byte("ab"))
		source.close(io.EOF)
		if source.err != io.EOF || len(source.pipe) != 1 {
			t.Errorf("err, pipe = %v, %q, want io.EOF, the payload kept", source.err, source.pipe)
		}
	})

	t.Run("the first err stays", func(t *testing.T) {
		source := newPipeSource().(*pipeSource)
		source.close(io.EOF)
		source.close(io.ErrClosedPipe)
		if source.err != io.EOF {
			t.Errorf("err = %v, want the first, io.EOF", source.err)
		}
	})

	t.Run("wakes a waiting Read", func(t *testing.T) {
		mutex := &sync.Mutex{}
		waiting := make(chan struct{}, 1)
		locker := fakeFuncLocker{lock: mutex.Lock, unlock: func() {
			mutex.Unlock()
			select {
			case waiting <- struct{}{}:
			default:
			}
		}}
		source := &pipeSource{locker: locker, cond: sync.NewCond(locker)}
		done := make(chan string)
		go func() {
			n, err := source.Read(make([]byte, 4))
			done <- fmt.Sprintf("%d %v", n, err)
		}()
		<-waiting // the Read unlocks first inside cond.Wait
		source.close(io.EOF)
		select {
		case got := <-done:
			if got != "0 EOF" {
				t.Errorf("Read = %s, want 0 EOF", got)
			}
		case <-time.After(time.Second):
			t.Fatalf("Read still waits after close")
		}
	})

	t.Run("locks", func(t *testing.T) {
		var steps []string
		locker := fakeFuncLocker{
			lock:   func() { steps = append(steps, "lock") },
			unlock: func() { steps = append(steps, "unlock") },
		}
		source := &pipeSource{locker: locker, cond: sync.NewCond(locker)}
		source.close(io.EOF)
		if want := []string{"lock", "unlock"}; !slices.Equal(steps, want) {
			t.Errorf("steps %q, want %q", steps, want)
		}
	})
}

func TestPipeSourceDrop(t *testing.T) {
	t.Run("frees pipe and closes an open one with io.ErrClosedPipe", func(t *testing.T) {
		source := newPipeSource().(*pipeSource)
		source.put([]byte("ab"))
		source.drop()
		if source.pipe != nil || source.err != io.ErrClosedPipe {
			t.Errorf("pipe, err = %q, %v, want nil, io.ErrClosedPipe", source.pipe, source.err)
		}
	})

	t.Run("frees pipe and keeps the err of a closed one", func(t *testing.T) {
		source := newPipeSource().(*pipeSource)
		source.put([]byte("ab"))
		source.close(io.EOF)
		source.drop()
		if source.pipe != nil || source.err != io.EOF {
			t.Errorf("pipe, err = %q, %v, want nil, io.EOF", source.pipe, source.err)
		}
	})

	t.Run("wakes a waiting Read", func(t *testing.T) {
		mutex := &sync.Mutex{}
		waiting := make(chan struct{}, 1)
		locker := fakeFuncLocker{lock: mutex.Lock, unlock: func() {
			mutex.Unlock()
			select {
			case waiting <- struct{}{}:
			default:
			}
		}}
		source := &pipeSource{locker: locker, cond: sync.NewCond(locker)}
		done := make(chan string)
		go func() {
			n, err := source.Read(make([]byte, 4))
			done <- fmt.Sprintf("%d %v", n, err)
		}()
		<-waiting // the Read unlocks first inside cond.Wait
		source.drop()
		select {
		case got := <-done:
			if got != "0 io: read/write on closed pipe" {
				t.Errorf("Read = %s, want 0 io.ErrClosedPipe", got)
			}
		case <-time.After(time.Second):
			t.Fatalf("Read still waits after drop")
		}
	})

	t.Run("locks", func(t *testing.T) {
		var steps []string
		locker := fakeFuncLocker{
			lock:   func() { steps = append(steps, "lock") },
			unlock: func() { steps = append(steps, "unlock") },
		}
		source := &pipeSource{locker: locker, cond: sync.NewCond(locker)}
		source.drop()
		if want := []string{"lock", "unlock"}; !slices.Equal(steps, want) {
			t.Errorf("steps %q, want %q", steps, want)
		}
	})
}

func TestPipeSourceIsClosed(t *testing.T) {
	t.Run("is whether err is set", func(t *testing.T) {
		source := &pipeSource{}
		if source.isClosed() {
			t.Errorf("isClosed = true with no err, want false")
		}
		source.err = io.EOF
		if !source.isClosed() {
			t.Errorf("isClosed = false with err set, want true")
		}
	})
}

func TestPipeSourceRead(t *testing.T) {
	t.Run("moves the front payload's bytes into p", func(t *testing.T) {
		source := newPipeSource().(*pipeSource)
		source.put([]byte("abc"))
		source.put([]byte("de"))
		p := make([]byte, 2)
		n, err := source.Read(p)
		if n != 2 || err != nil || string(p) != "ab" {
			t.Errorf("Read = %d, %v, %q, want 2, nil, \"ab\"", n, err, p[:n])
		}
		if len(source.pipe) != 2 || string(source.pipe[0]) != "c" || string(source.pipe[1]) != "de" {
			t.Errorf("pipe %q, want [\"c\" \"de\"]", source.pipe)
		}
	})

	t.Run("reads one payload at most, and lets it go once read", func(t *testing.T) {
		source := newPipeSource().(*pipeSource)
		source.put([]byte("abc"))
		source.put([]byte("de"))
		pipe := source.pipe
		p := make([]byte, 10)
		n, err := source.Read(p)
		if n != 3 || err != nil || string(p[:n]) != "abc" {
			t.Errorf("Read = %d, %v, %q, want 3, nil, \"abc\"", n, err, p[:n])
		}
		if len(source.pipe) != 1 || string(source.pipe[0]) != "de" || pipe[0] != nil {
			t.Errorf("pipe %q, its old front %q, want [\"de\"] and nil", source.pipe, pipe[0])
		}
	})

	t.Run("closed, still reads what pipe holds, then returns err", func(t *testing.T) {
		source := newPipeSource().(*pipeSource)
		source.put([]byte("ab"))
		source.close(io.EOF)
		p := make([]byte, 4)
		if n, err := source.Read(p); n != 2 || err != nil || string(p[:n]) != "ab" {
			t.Errorf("Read = %d, %v, %q, want 2, nil, \"ab\"", n, err, p[:n])
		}
		if n, err := source.Read(p); n != 0 || err != io.EOF {
			t.Errorf("Read once empty = %d, %v, want 0, io.EOF", n, err)
		}
	})

	t.Run("pipe empty and not closed, waits for a put", func(t *testing.T) {
		mutex := &sync.Mutex{}
		waiting := make(chan struct{}, 1)
		locker := fakeFuncLocker{lock: mutex.Lock, unlock: func() {
			mutex.Unlock()
			select {
			case waiting <- struct{}{}:
			default:
			}
		}}
		source := &pipeSource{locker: locker, cond: sync.NewCond(locker)}
		done := make(chan string)
		go func() {
			p := make([]byte, 4)
			n, err := source.Read(p)
			done <- fmt.Sprintf("%d %v %q", n, err, p[:n])
		}()
		<-waiting // the Read unlocks first inside cond.Wait
		select {
		case got := <-done:
			t.Fatalf("Read = %s before a put, want it waiting", got)
		default:
		}
		source.put([]byte("ab"))
		select {
		case got := <-done:
			if got != `2 <nil> "ab"` {
				t.Errorf("Read = %s, want 2 <nil> \"ab\"", got)
			}
		case <-time.After(time.Second):
			t.Fatalf("Read still waits after put")
		}
	})

	t.Run("locks", func(t *testing.T) {
		var steps []string
		locker := fakeFuncLocker{
			lock:   func() { steps = append(steps, "lock") },
			unlock: func() { steps = append(steps, "unlock") },
		}
		source := &pipeSource{locker: locker, cond: sync.NewCond(locker), pipe: [][]byte{[]byte("ab")}}
		source.Read(make([]byte, 4))
		if want := []string{"lock", "unlock"}; !slices.Equal(steps, want) {
			t.Errorf("steps %q, want %q", steps, want)
		}
	})
}

func TestPipeSourceReadByte(t *testing.T) {
	t.Run("takes the front payload's first byte", func(t *testing.T) {
		source := newPipeSource().(*pipeSource)
		source.put([]byte("ab"))
		source.put([]byte("c"))
		b, err := source.ReadByte()
		if b != 'a' || err != nil {
			t.Errorf("ReadByte = %q, %v, want 'a', nil", b, err)
		}
		if len(source.pipe) != 2 || string(source.pipe[0]) != "b" || string(source.pipe[1]) != "c" {
			t.Errorf("pipe %q, want [\"b\" \"c\"]", source.pipe)
		}
	})

	t.Run("lets a payload go once read", func(t *testing.T) {
		source := newPipeSource().(*pipeSource)
		source.put([]byte("a"))
		source.put([]byte("b"))
		pipe := source.pipe
		b, err := source.ReadByte()
		if b != 'a' || err != nil {
			t.Errorf("ReadByte = %q, %v, want 'a', nil", b, err)
		}
		if len(source.pipe) != 1 || string(source.pipe[0]) != "b" || pipe[0] != nil {
			t.Errorf("pipe %q, its old front %q, want [\"b\"] and nil", source.pipe, pipe[0])
		}
	})

	t.Run("closed, still reads what pipe holds, then returns err", func(t *testing.T) {
		source := newPipeSource().(*pipeSource)
		source.put([]byte("a"))
		source.close(io.EOF)
		if b, err := source.ReadByte(); b != 'a' || err != nil {
			t.Errorf("ReadByte = %q, %v, want 'a', nil", b, err)
		}
		if b, err := source.ReadByte(); b != 0 || err != io.EOF {
			t.Errorf("ReadByte once empty = %q, %v, want 0, io.EOF", b, err)
		}
	})

	t.Run("pipe empty and not closed, waits for a put", func(t *testing.T) {
		mutex := &sync.Mutex{}
		waiting := make(chan struct{}, 1)
		locker := fakeFuncLocker{lock: mutex.Lock, unlock: func() {
			mutex.Unlock()
			select {
			case waiting <- struct{}{}:
			default:
			}
		}}
		source := &pipeSource{locker: locker, cond: sync.NewCond(locker)}
		done := make(chan string)
		go func() {
			b, err := source.ReadByte()
			done <- fmt.Sprintf("%q %v", b, err)
		}()
		<-waiting // the ReadByte unlocks first inside cond.Wait
		select {
		case got := <-done:
			t.Fatalf("ReadByte = %s before a put, want it waiting", got)
		default:
		}
		source.put([]byte("a"))
		select {
		case got := <-done:
			if got != "'a' <nil>" {
				t.Errorf("ReadByte = %s, want 'a' <nil>", got)
			}
		case <-time.After(time.Second):
			t.Fatalf("ReadByte still waits after put")
		}
	})

	t.Run("locks", func(t *testing.T) {
		var steps []string
		locker := fakeFuncLocker{
			lock:   func() { steps = append(steps, "lock") },
			unlock: func() { steps = append(steps, "unlock") },
		}
		source := &pipeSource{locker: locker, cond: sync.NewCond(locker), pipe: [][]byte{[]byte("ab")}}
		source.ReadByte()
		if want := []string{"lock", "unlock"}; !slices.Equal(steps, want) {
			t.Errorf("steps %q, want %q", steps, want)
		}
	})
}
