package wlgows

import (
	"errors"
	"fmt"
	"io"
	"slices"
	"testing"
)

// fakeFuncInflater runs its funcs, so a test can record each call. It is a
// connContextInflater, and so a connNoContextInflater too. Set the ones the
// code under test calls.
type fakeFuncInflater struct {
	inflateDataFramesReaderFunc func(frames DataFrames) io.ReadCloser
	startInflateFramesFunc      func() int64
	putDeflatedFrameFunc        func(token int64, f *Frame) error
	pullInflatedBytesFunc       func(token int64, buf []byte) (int, error)
	endInflateFramesFunc        func(token int64)
	dropContextFunc             func(token int64)
}

func (i fakeFuncInflater) inflateDataFramesReader(frames DataFrames) io.ReadCloser {
	return i.inflateDataFramesReaderFunc(frames)
}
func (i fakeFuncInflater) startInflateFrames() int64 { return i.startInflateFramesFunc() }
func (i fakeFuncInflater) putDeflatedFrame(token int64, f *Frame) error {
	return i.putDeflatedFrameFunc(token, f)
}
func (i fakeFuncInflater) pullInflatedBytes(token int64, buf []byte) (int, error) {
	return i.pullInflatedBytesFunc(token, buf)
}
func (i fakeFuncInflater) endInflateFrames(token int64) { i.endInflateFramesFunc(token) }
func (i fakeFuncInflater) dropContext(token int64)      { i.dropContextFunc(token) }

func TestDataFramesReaderFunc(t *testing.T) {
	plain_reader := &fakeFuncReadCloser{}
	context_reader := &fakeFuncReadCloser{}
	no_context_reader := &fakeFuncReadCloser{}
	var steps []string
	base_di := func() dataFramesReaderDI {
		steps = nil
		return dataFramesReaderDI{
			peerIsClient:        true,
			deflateAgreedConfig: &PermessageDeflateConfig{},
			contextInflater: fakeFuncInflater{inflateDataFramesReaderFunc: func(frames DataFrames) io.ReadCloser {
				steps = append(steps, fmt.Sprintf("context inflate %d frames", len(frames)))
				return context_reader
			}},
			noContextInflater: fakeFuncInflater{inflateDataFramesReaderFunc: func(frames DataFrames) io.ReadCloser {
				steps = append(steps, fmt.Sprintf("no context inflate %d frames", len(frames)))
				return no_context_reader
			}},
			checkDataFramesFormat: func(data_frames DataFrames) error { return nil },
			newDataFramesReader: func(data_frames DataFrames) io.ReadCloser {
				steps = append(steps, fmt.Sprintf("plain %d frames", len(data_frames)))
				return plain_reader
			},
		}
	}
	deflated_message_frames := DataFrames{
		{Opcode: OpcodeBinary, RSV1: true, PayloadData: []byte("x")},
		{Opcode: OpcodeContinuation, FIN: true, PayloadData: []byte("y")},
	}
	message_frames := DataFrames{
		{Opcode: OpcodeBinary, PayloadData: []byte("x")},
		{Opcode: OpcodeContinuation, FIN: true, PayloadData: []byte("y")},
	}

	t.Run("checks the frames' format first; an error returns as it is, nothing run", func(t *testing.T) {
		err_check := errors.New("check failed")
		di := base_di()
		di.deflateAgreedConfig = nil // RSV1 with nothing agreed would fail next: the check goes first
		di.checkDataFramesFormat = func(data_frames DataFrames) error {
			if len(data_frames) != 2 {
				t.Errorf("check %d frames, want the 2 frames", len(data_frames))
			}
			return err_check
		}
		reader, err := dataFramesReader(deflated_message_frames, di)
		if reader != nil || err != err_check || steps != nil {
			t.Errorf("dataFramesReader = %v, %v, steps %q, want nil, %v, nothing run", reader, err, steps, err_check)
		}
	})

	// RFC 6455 5.2: RSV1 means nothing unless an extension was agreed.
	t.Run("RSV1 with nothing agreed returns ErrReservedBitsSet", func(t *testing.T) {
		di := base_di()
		di.deflateAgreedConfig = nil
		reader, err := dataFramesReader(deflated_message_frames, di)
		if reader != nil || err != ErrReservedBitsSet || steps != nil {
			t.Errorf("dataFramesReader = %v, %v, steps %q, want nil, ErrReservedBitsSet, nothing run", reader, err, steps)
		}
	})

	for _, tc := range []struct {
		name   string
		agreed *PermessageDeflateConfig
	}{
		{"with permessage-deflate agreed", &PermessageDeflateConfig{}},
		{"with nothing agreed", nil},
	} {
		t.Run("RSV1 clear reads the payloads as they are, "+tc.name, func(t *testing.T) {
			di := base_di()
			di.deflateAgreedConfig = tc.agreed
			reader, err := dataFramesReader(message_frames, di)
			if reader != io.ReadCloser(plain_reader) || err != nil {
				t.Errorf("dataFramesReader = %v, %v, want the plain reader, nil", reader, err)
			}
			if want := []string{"plain 2 frames"}; !slices.Equal(steps, want) {
				t.Errorf("steps %q, want %q", steps, want)
			}
		})
	}

	// RFC 7692 7.1.1: the peer's own no context takeover param says how it
	// sends.
	for _, tc := range []struct {
		name           string
		peer_is_client bool
		agreed         PermessageDeflateConfig
		want_reader    io.ReadCloser
		want_step      string
	}{
		{
			name:           "a client that agreed to no context takeover",
			peer_is_client: true,
			agreed:         PermessageDeflateConfig{ClientNoContextTakeover: true},
			want_reader:    no_context_reader,
			want_step:      "no context inflate 2 frames",
		},
		{
			name:           "a server that agreed to no context takeover",
			peer_is_client: false,
			agreed:         PermessageDeflateConfig{ServerNoContextTakeover: true},
			want_reader:    no_context_reader,
			want_step:      "no context inflate 2 frames",
		},
		{
			name:           "a client that keeps its context",
			peer_is_client: true,
			agreed:         PermessageDeflateConfig{ServerNoContextTakeover: true},
			want_reader:    context_reader,
			want_step:      "context inflate 2 frames",
		},
		{
			name:           "a server that keeps its context",
			peer_is_client: false,
			agreed:         PermessageDeflateConfig{ClientNoContextTakeover: true},
			want_reader:    context_reader,
			want_step:      "context inflate 2 frames",
		},
	} {
		t.Run("reads the message of "+tc.name+" inflated", func(t *testing.T) {
			di := base_di()
			di.peerIsClient, di.deflateAgreedConfig = tc.peer_is_client, &tc.agreed
			reader, err := dataFramesReader(deflated_message_frames, di)
			if reader != tc.want_reader || err != nil {
				t.Errorf("dataFramesReader = %v, %v, want the inflate reader, nil", reader, err)
			}
			if want := []string{tc.want_step}; !slices.Equal(steps, want) {
				t.Errorf("steps %q, want %q", steps, want)
			}
		})
	}

	// RFC 6455 8.1: a text message must be UTF-8 once it is what the sender
	// wrote.
	for _, tc := range []struct {
		name        string
		rsv1        bool
		agreed      *PermessageDeflateConfig
		want_reader io.ReadCloser
		want_step   string
	}{
		{
			name:        "not deflated, nothing agreed",
			rsv1:        false,
			agreed:      nil,
			want_reader: plain_reader,
			want_step:   "plain 1 frames",
		},
		{
			name:        "not deflated, permessage-deflate agreed",
			rsv1:        false,
			agreed:      &PermessageDeflateConfig{},
			want_reader: plain_reader,
			want_step:   "plain 1 frames",
		},
		{
			name:        "inflated, no context takeover",
			rsv1:        true,
			agreed:      &PermessageDeflateConfig{ClientNoContextTakeover: true},
			want_reader: no_context_reader,
			want_step:   "no context inflate 1 frames",
		},
		{
			name:        "inflated, context takeover",
			rsv1:        true,
			agreed:      &PermessageDeflateConfig{},
			want_reader: context_reader,
			want_step:   "context inflate 1 frames",
		},
	} {
		t.Run(tc.name+": text is read through the UTF-8 check", func(t *testing.T) {
			di := base_di()
			di.deflateAgreedConfig = tc.agreed
			reader, err := dataFramesReader(DataFrames{{Opcode: OpcodeText, FIN: true, RSV1: tc.rsv1}}, di)
			check_reader, is_check_reader := reader.(*utf8CheckReader)
			if err != nil || !is_check_reader || check_reader.reader != tc.want_reader {
				t.Errorf("dataFramesReader = %v, %v, want a utf8CheckReader of the %s reader, nil", reader, err, tc.name)
			}
			if want := []string{tc.want_step}; !slices.Equal(steps, want) {
				t.Errorf("steps %q, want %q", steps, want)
			}
		})
	}
}

func TestStartInflateDataFrames(t *testing.T) {
	t.Run("returns start's token", func(t *testing.T) {
		conn := &Conn{contextInflater: fakeFuncInflater{startInflateFramesFunc: func() int64 { return 7 }}}
		if token := conn.StartInflateDataFrames(); token != 7 {
			t.Errorf("StartInflateDataFrames = %d, want 7", token)
		}
	})
}

func TestPullInflatedBytes(t *testing.T) {
	err_inflate := errors.New("inflate failed")
	for _, tc := range []struct {
		name     string
		err      error
		want_err error
	}{
		{"nil", nil, nil},
		{"io.EOF as it is", io.EOF, io.EOF},
		{"ErrInflateFramesTokenStale as it is", ErrInflateFramesTokenStale, ErrInflateFramesTokenStale},
	} {
		t.Run("returns what it pulled, "+tc.name, func(t *testing.T) {
			buf := make([]byte, 0, 8)
			conn := &Conn{contextInflater: fakeFuncInflater{pullInflatedBytesFunc: func(token int64, got []byte) (int, error) {
				if token != 7 || cap(got) != 8 {
					t.Errorf("pull token %d, cap %d, want 7, buf's 8", token, cap(got))
				}
				return 3, tc.err
			}}}
			if n, err := conn.PullInflatedBytes(7, buf); n != 3 || err != tc.want_err {
				t.Errorf("PullInflatedBytes = %d, %v, want 3, %v", n, err, tc.want_err)
			}
		})
	}

	t.Run("wraps another error in ErrInflateDataInvalid", func(t *testing.T) {
		conn := &Conn{contextInflater: fakeFuncInflater{pullInflatedBytesFunc: func(token int64, buf []byte) (int, error) {
			return 3, err_inflate
		}}}
		n, err := conn.PullInflatedBytes(7, make([]byte, 0, 8))
		if n != 3 || !errors.Is(err, ErrInflateDataInvalid) || !errors.Is(err, err_inflate) {
			t.Errorf("PullInflatedBytes = %d, %v, want 3, ErrInflateDataInvalid wrapping %v", n, err, err_inflate)
		}
	})
}

func TestEndInflateDataFrames(t *testing.T) {
	// RFC 7692 7.1.1: the context goes before the next message can start.
	for _, tc := range []struct {
		name   string
		agreed *PermessageDeflateConfig
		want   []string
	}{
		{
			name:   "a peer that agreed to no context takeover drops the context, then ends",
			agreed: &PermessageDeflateConfig{ClientNoContextTakeover: true},
			want:   []string{"drop context 7", "end 7"},
		},
		{
			name:   "a peer that keeps its context only ends",
			agreed: &PermessageDeflateConfig{ServerNoContextTakeover: true},
			want:   []string{"end 7"},
		},
		{
			name:   "permessage-deflate not agreed: ends without dropping the context",
			agreed: nil,
			want:   []string{"end 7"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var steps []string
			conn := &Conn{
				peerIsClient:                  true,
				permessageDeflateAgreedConfig: tc.agreed,
				contextInflater: fakeFuncInflater{
					dropContextFunc:      func(token int64) { steps = append(steps, fmt.Sprintf("drop context %d", token)) },
					endInflateFramesFunc: func(token int64) { steps = append(steps, fmt.Sprintf("end %d", token)) },
				},
			}
			conn.EndInflateDataFrames(7)
			if !slices.Equal(steps, tc.want) {
				t.Errorf("steps %q, want %q", steps, tc.want)
			}
		})
	}
}

func TestPeerNoContextTakeover(t *testing.T) {
	// RFC 7692 7.1.1: client_no_context_takeover binds a client,
	// server_no_context_takeover a server.
	for _, tc := range []struct {
		name           string
		peer_is_client bool
		agreed         *PermessageDeflateConfig
		want           bool
	}{
		{"nothing agreed", true, nil, false},
		{"a client with client_no_context_takeover", true, &PermessageDeflateConfig{ClientNoContextTakeover: true}, true},
		{"a client with server_no_context_takeover", true, &PermessageDeflateConfig{ServerNoContextTakeover: true}, false},
		{"a server with server_no_context_takeover", false, &PermessageDeflateConfig{ServerNoContextTakeover: true}, true},
		{"a server with client_no_context_takeover", false, &PermessageDeflateConfig{ClientNoContextTakeover: true}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := peerNoContextTakeover(tc.peer_is_client, tc.agreed); got != tc.want {
				t.Errorf("peerNoContextTakeover = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestInflateError(t *testing.T) {
	t.Run("nil and ErrInflateFramesTokenStale come as they are", func(t *testing.T) {
		if err := inflateError(nil); err != nil {
			t.Errorf("inflateError(nil) = %v, want nil", err)
		}
		if err := inflateError(ErrInflateFramesTokenStale); err != ErrInflateFramesTokenStale {
			t.Errorf("inflateError(ErrInflateFramesTokenStale) = %v, want it as it is", err)
		}
	})

	t.Run("another error comes wrapped in ErrInflateDataInvalid", func(t *testing.T) {
		err := inflateError(io.ErrUnexpectedEOF)
		if !errors.Is(err, ErrInflateDataInvalid) || !errors.Is(err, io.ErrUnexpectedEOF) {
			t.Errorf("inflateError = %v, want ErrInflateDataInvalid wrapping io.ErrUnexpectedEOF", err)
		}
	})
}
