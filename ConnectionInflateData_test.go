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
	inflateDataFramesFunc  func(frames DataFrames, buf []byte) (int, error)
	startInflateFramesFunc func() int64
	putDeflatedFrameFunc   func(token int64, f *Frame) error
	pullInflatedBytesFunc  func(token int64, buf []byte) (int, error)
	endInflateFramesFunc   func(token int64)
	dropContextFunc        func(token int64)
}

func (i fakeFuncInflater) inflateDataFrames(frames DataFrames, buf []byte) (int, error) {
	return i.inflateDataFramesFunc(frames, buf)
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

func TestDataFramesBytesFunc(t *testing.T) {
	err_inflate := errors.New("inflate failed")
	var steps []string
	base_di := func() dataFramesBytesDI {
		steps = nil
		return dataFramesBytesDI{
			peerIsClient:        true,
			deflateAgreedConfig: &PermessageDeflateConfig{},
			contextInflater: fakeFuncInflater{inflateDataFramesFunc: func(frames DataFrames, buf []byte) (int, error) {
				steps = append(steps, fmt.Sprintf("context inflate %d frames, cap %d", len(frames), cap(buf)))
				return copy(buf[:cap(buf)], "abc"), nil
			}},
			noContextInflater: fakeFuncInflater{inflateDataFramesFunc: func(frames DataFrames, buf []byte) (int, error) {
				steps = append(steps, fmt.Sprintf("no context inflate %d frames, cap %d", len(frames), cap(buf)))
				return copy(buf[:cap(buf)], "abc"), nil
			}},
		}
	}
	inflate_if_agreed_config := DataFramesBytesConfig{InflateIfAgreed: true}
	deflated_message_frames := DataFrames{
		{Opcode: OpcodeBinary, RSV1: true, PayloadData: []byte("x")},
		{Opcode: OpcodeContinuation, FIN: true, PayloadData: []byte("y")},
	}

	t.Run("no frames, nothing written", func(t *testing.T) {
		n, err := dataFramesBytes(nil, make([]byte, 0, 8), inflate_if_agreed_config, base_di())
		if n != 0 || err != nil || steps != nil {
			t.Errorf("dataFramesBytes = %d, %v, steps %q, want 0, nil, nothing run", n, err, steps)
		}
	})

	// RFC 6455 5.4: one message is a text or binary frame, then continuations,
	// the last with FIN set.
	for _, tc := range []struct {
		name   string
		frames DataFrames
		want   error
	}{
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
			buf := make([]byte, 0, 8)
			n, err := dataFramesBytes(tc.frames, buf, inflate_if_agreed_config, base_di())
			if n != 0 || err != tc.want || steps != nil {
				t.Errorf("dataFramesBytes = %d, %v, steps %q, want 0, %v, nothing run", n, err, steps, tc.want)
			}
		})
	}

	// RFC 6455 5.2: RSV1 means nothing unless an extension was agreed.
	// RFC 7692 6.1: only the first frame carries RSV1, agreed or not.
	t.Run("RSV1 on a frame after the first returns ErrReservedBitsSet", func(t *testing.T) {
		frames := DataFrames{
			{Opcode: OpcodeBinary, PayloadData: []byte("x")},
			{Opcode: OpcodeContinuation, FIN: true, RSV1: true, PayloadData: []byte("y")},
		}
		n, err := dataFramesBytes(frames, make([]byte, 0, 8), inflate_if_agreed_config, base_di())
		if n != 0 || err != ErrReservedBitsSet || steps != nil {
			t.Errorf("dataFramesBytes = %d, %v, steps %q, want 0, ErrReservedBitsSet, nothing run", n, err, steps)
		}
	})

	t.Run("RSV1 with nothing agreed returns ErrReservedBitsSet", func(t *testing.T) {
		di := base_di()
		di.deflateAgreedConfig = nil
		n, err := dataFramesBytes(deflated_message_frames, make([]byte, 0, 8), DataFramesBytesConfig{}, di)
		if n != 0 || err != ErrReservedBitsSet || steps != nil {
			t.Errorf("dataFramesBytes = %d, %v, steps %q, want 0, ErrReservedBitsSet, nothing run", n, err, steps)
		}
	})

	for _, tc := range []struct {
		name   string
		frames DataFrames
		config DataFramesBytesConfig
		agreed *PermessageDeflateConfig
	}{
		{"without InflateIfAgreed", deflated_message_frames, DataFramesBytesConfig{}, &PermessageDeflateConfig{}},
		{"with RSV1 clear", DataFrames{
			{Opcode: OpcodeBinary, PayloadData: []byte("x")},
			{Opcode: OpcodeContinuation, FIN: true, PayloadData: []byte("y")},
		}, inflate_if_agreed_config, &PermessageDeflateConfig{}},
		{"with nothing agreed", DataFrames{
			{Opcode: OpcodeBinary, PayloadData: []byte("x")},
			{Opcode: OpcodeContinuation, FIN: true, PayloadData: []byte("y")},
		}, inflate_if_agreed_config, nil},
	} {
		t.Run("copies the payloads as they are into buf from its start, "+tc.name, func(t *testing.T) {
			di := base_di()
			di.deflateAgreedConfig = tc.agreed
			buf := make([]byte, 1, 8)
			n, err := dataFramesBytes(tc.frames, buf, tc.config, di)
			if n != 2 || err != nil || string(buf[:n]) != "xy" || steps != nil {
				t.Errorf("dataFramesBytes = %d, %v, %q, steps %q, want 2, nil, \"xy\", no inflate", n, err, buf[:n], steps)
			}
		})
	}

	t.Run("copies payloads that fill buf exactly", func(t *testing.T) {
		buf := make([]byte, 0, 2)
		n, err := dataFramesBytes(deflated_message_frames, buf, DataFramesBytesConfig{}, base_di())
		if n != 2 || err != nil || string(buf[:n]) != "xy" {
			t.Errorf("dataFramesBytes = %d, %v, %q, want 2, nil, \"xy\"", n, err, buf[:n])
		}
	})

	t.Run("payloads past cap(buf) return ErrDataFramesBytesSizeExceeded, nothing written", func(t *testing.T) {
		buf := []byte{'-'}[:0]
		n, err := dataFramesBytes(deflated_message_frames, buf, DataFramesBytesConfig{}, base_di())
		if n != 0 || err != ErrDataFramesBytesSizeExceeded || buf[:1][0] != '-' {
			t.Errorf("dataFramesBytes = %d, %v, buf %q, want 0, ErrDataFramesBytesSizeExceeded, buf untouched", n, err, buf[:1])
		}
	})

	// RFC 7692 7.1.1: the peer's own no context takeover param says how it
	// sends.
	for _, tc := range []struct {
		name           string
		peer_is_client bool
		agreed         PermessageDeflateConfig
		want           string
	}{
		{
			name:           "a client that agreed to no context takeover",
			peer_is_client: true,
			agreed:         PermessageDeflateConfig{ClientNoContextTakeover: true},
			want:           "no context inflate 2 frames, cap 8",
		},
		{
			name:           "a server that agreed to no context takeover",
			peer_is_client: false,
			agreed:         PermessageDeflateConfig{ServerNoContextTakeover: true},
			want:           "no context inflate 2 frames, cap 8",
		},
		{
			name:           "a client that keeps its context",
			peer_is_client: true,
			agreed:         PermessageDeflateConfig{ServerNoContextTakeover: true},
			want:           "context inflate 2 frames, cap 8",
		},
		{
			name:           "a server that keeps its context",
			peer_is_client: false,
			agreed:         PermessageDeflateConfig{ClientNoContextTakeover: true},
			want:           "context inflate 2 frames, cap 8",
		},
	} {
		t.Run("inflates the message of "+tc.name, func(t *testing.T) {
			di := base_di()
			di.peerIsClient, di.deflateAgreedConfig = tc.peer_is_client, &tc.agreed
			buf := make([]byte, 0, 8)
			n, err := dataFramesBytes(deflated_message_frames, buf, inflate_if_agreed_config, di)
			if n != 3 || err != nil || string(buf[:n]) != "abc" {
				t.Errorf("dataFramesBytes = %d, %v, %q, want 3, nil, \"abc\"", n, err, buf[:n])
			}
			if want := []string{tc.want}; !slices.Equal(steps, want) {
				t.Errorf("steps %q, want %q", steps, want)
			}
		})
	}

	t.Run("an inflate error comes wrapped in ErrInflateDataInvalid", func(t *testing.T) {
		di := base_di()
		di.contextInflater = fakeFuncInflater{inflateDataFramesFunc: func(frames DataFrames, buf []byte) (int, error) {
			return 2, err_inflate
		}}
		n, err := dataFramesBytes(deflated_message_frames, make([]byte, 0, 8), inflate_if_agreed_config, di)
		if n != 2 || !errors.Is(err, ErrInflateDataInvalid) || !errors.Is(err, err_inflate) {
			t.Errorf("dataFramesBytes = %d, %v, want 2, ErrInflateDataInvalid wrapping %v", n, err, err_inflate)
		}
	})

	t.Run("ErrInflatedSizeExceeded comes as it is", func(t *testing.T) {
		di := base_di()
		di.contextInflater = fakeFuncInflater{inflateDataFramesFunc: func(frames DataFrames, buf []byte) (int, error) {
			return 8, ErrInflatedSizeExceeded
		}}
		n, err := dataFramesBytes(deflated_message_frames, make([]byte, 0, 8), inflate_if_agreed_config, di)
		if n != 8 || err != ErrInflatedSizeExceeded {
			t.Errorf("dataFramesBytes = %d, %v, want 8, ErrInflatedSizeExceeded", n, err)
		}
	})

	// RFC 6455 8.1: a text message must be UTF-8 once it is what the sender
	// wrote.
	for _, tc := range []struct {
		name     string
		frames   DataFrames
		config   DataFramesBytesConfig
		want_err error
	}{
		{
			name: "copied text that is not UTF-8 returns ErrInvalidUTF8",
			frames: DataFrames{
				{Opcode: OpcodeText, FIN: true, PayloadData: []byte{0xff}},
			},
			config:   inflate_if_agreed_config,
			want_err: ErrInvalidUTF8,
		},
		{
			name: "inflated text that is not UTF-8 returns ErrInvalidUTF8",
			frames: DataFrames{
				{Opcode: OpcodeText, FIN: true, RSV1: true, PayloadData: []byte("x")},
			},
			config:   inflate_if_agreed_config,
			want_err: ErrInvalidUTF8,
		},
		{
			name: "deflated text copied unchanged is not checked",
			frames: DataFrames{
				{Opcode: OpcodeText, FIN: true, RSV1: true, PayloadData: []byte{0xff}},
			},
			config:   DataFramesBytesConfig{},
			want_err: nil,
		},
		{
			name: "binary is not checked",
			frames: DataFrames{
				{Opcode: OpcodeBinary, FIN: true, RSV1: true, PayloadData: []byte("x")},
			},
			config:   inflate_if_agreed_config,
			want_err: nil,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			di := base_di()
			di.contextInflater = fakeFuncInflater{inflateDataFramesFunc: func(frames DataFrames, buf []byte) (int, error) {
				return copy(buf[:cap(buf)], []byte{0xff}), nil
			}}
			buf := make([]byte, 0, 8)
			n, err := dataFramesBytes(tc.frames, buf, tc.config, di)
			if n != 1 || err != tc.want_err || buf[:n][0] != 0xff {
				t.Errorf("dataFramesBytes = %d, %v, % x, want 1, %v, ff", n, err, buf[:n], tc.want_err)
			}
		})
	}

	// Only the message is checked: the room past it holds bytes that are not
	// UTF-8.
	t.Run("copied UTF-8 text passes", func(t *testing.T) {
		buf := []byte{0, 0, 0, 0xff, 0xff}[:0]
		n, err := dataFramesBytes(DataFrames{{Opcode: OpcodeText, FIN: true, PayloadData: []byte("hé")}}, buf, inflate_if_agreed_config, base_di())
		if n != 3 || err != nil || string(buf[:n]) != "hé" {
			t.Errorf("dataFramesBytes = %d, %v, %q, want 3, nil, \"hé\"", n, err, buf[:n])
		}
	})

	t.Run("inflated UTF-8 text passes", func(t *testing.T) {
		buf := []byte{0, 0, 0, 0xff, 0xff}[:0]
		n, err := dataFramesBytes(DataFrames{{Opcode: OpcodeText, FIN: true, RSV1: true, PayloadData: []byte("x")}}, buf, inflate_if_agreed_config, base_di())
		if n != 3 || err != nil || string(buf[:n]) != "abc" {
			t.Errorf("dataFramesBytes = %d, %v, %q, want 3, nil, \"abc\"", n, err, buf[:n])
		}
	})
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
		{"ErrInflatedSizeExceeded as it is", ErrInflatedSizeExceeded, ErrInflatedSizeExceeded},
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
	t.Run("nil, ErrInflatedSizeExceeded and ErrInflateFramesTokenStale come as they are", func(t *testing.T) {
		if err := inflateError(nil); err != nil {
			t.Errorf("inflateError(nil) = %v, want nil", err)
		}
		for _, want := range []error{ErrInflatedSizeExceeded, ErrInflateFramesTokenStale} {
			if err := inflateError(want); err != want {
				t.Errorf("inflateError(%v) = %v, want it as it is", want, err)
			}
		}
	})

	t.Run("another error comes wrapped in ErrInflateDataInvalid", func(t *testing.T) {
		err := inflateError(io.ErrUnexpectedEOF)
		if !errors.Is(err, ErrInflateDataInvalid) || !errors.Is(err, io.ErrUnexpectedEOF) {
			t.Errorf("inflateError = %v, want ErrInflateDataInvalid wrapping io.ErrUnexpectedEOF", err)
		}
	})
}
