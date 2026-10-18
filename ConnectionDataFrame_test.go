package wlgows

import (
	"compress/flate"
	"errors"
	"fmt"
	"slices"
	"testing"
)

func TestSendText(t *testing.T) {
	t.Run("hands the text on as a text message with its config", func(t *testing.T) {
		calls := 0
		config := SendDataConfig{ChunkSize: 4, DeflateIfAgreed: true, DeflateLevel: 9}
		err := sendText([]byte("hello"), config, sendTextDI{
			sendData: func(opcode uint8, payload []byte, givenConfig SendDataConfig) error {
				calls++
				if opcode != OpcodeText || string(payload) != "hello" || givenConfig != config {
					t.Errorf("sendData(%#x, %q, %+v), want (%#x, %q, %+v)",
						opcode, payload, givenConfig, OpcodeText, "hello", config)
				}
				return nil
			},
		})
		if err != nil {
			t.Fatalf("sendText: %v", err)
		}
		if calls != 1 {
			t.Errorf("sendData called %d times, want 1", calls)
		}
	})

	/*
		5.6 defines a text payload as UTF-8 and 8.1 has the peer fail the
		connection over it, so an invalid message costs a close 1007 and the
		connection. The Listener refuses these on the way in; refusing them on
		the way out is the same rule.

		The bytes are a truncated 3 byte rune — the shape a caller hits by
		slicing a string at a byte offset.
	*/
	t.Run("refuses a payload that is not valid UTF-8", func(t *testing.T) {
		err := sendText([]byte{0xe4, 0xb8}, SendDataConfig{}, sendTextDI{
			sendData: func(uint8, []byte, SendDataConfig) error {
				t.Error("an invalid payload still reached sendData")
				return nil
			},
		})
		if !errors.Is(err, ErrInvalidUTF8) {
			t.Errorf("sendText = %v, want ErrInvalidUTF8", err)
		}
	})

	// The same rune whole, to show the check passes what 5.6 allows rather than
	// just refusing anything non ASCII.
	t.Run("accepts multi byte UTF-8", func(t *testing.T) {
		calls := 0
		err := sendText([]byte("中文字"), SendDataConfig{}, sendTextDI{
			sendData: func(uint8, []byte, SendDataConfig) error { calls++; return nil },
		})
		if err != nil {
			t.Fatalf("sendText: %v", err)
		}
		if calls != 1 {
			t.Errorf("sendData called %d times, want 1", calls)
		}
	})

	t.Run("returns what sendData returns", func(t *testing.T) {
		wantErr := errors.New("cannot send")
		err := sendText([]byte("hello"), SendDataConfig{}, sendTextDI{
			sendData: func(uint8, []byte, SendDataConfig) error { return wantErr },
		})
		if !errors.Is(err, wantErr) {
			t.Errorf("sendText = %v, want %v", err, wantErr)
		}
	})
}

func TestSendBinary(t *testing.T) {
	/*
		The payload is the one sendText refuses. RFC 6455 5.6 gives binary no
		encoding, so it goes through untouched — a truncated rune is only
		malformed if something claims it is text.
	*/
	t.Run("hands the data on as a binary message with its config", func(t *testing.T) {
		calls := 0
		config := SendDataConfig{ChunkSize: 2, DeflateIfAgreed: true, DeflateLevel: 9}
		err := sendBinary([]byte{0xe4, 0xb8}, config, sendBinaryDI{
			sendData: func(opcode uint8, payload []byte, givenConfig SendDataConfig) error {
				calls++
				if opcode != OpcodeBinary || string(payload) != "\xe4\xb8" || givenConfig != config {
					t.Errorf("sendData(%#x, % x, %+v), want (%#x, e4 b8, %+v)",
						opcode, payload, givenConfig, OpcodeBinary, config)
				}
				return nil
			},
		})
		if err != nil {
			t.Fatalf("sendBinary: %v", err)
		}
		if calls != 1 {
			t.Errorf("sendData called %d times, want 1", calls)
		}
	})

	t.Run("returns what sendData returns", func(t *testing.T) {
		wantErr := errors.New("cannot send")
		err := sendBinary([]byte{0x01}, SendDataConfig{}, sendBinaryDI{
			sendData: func(uint8, []byte, SendDataConfig) error { return wantErr },
		})
		if !errors.Is(err, wantErr) {
			t.Errorf("sendBinary = %v, want %v", err, wantErr)
		}
	})
}

func TestSendData(t *testing.T) {
	// base_di succeeds at every step; each case overrides what it looks at.
	// Nothing is agreed, so nothing is deflated unless a case agrees.
	base_di := func() sendDataDI {
		fake_deflate := func(data []byte, level int) ([]byte, error) {
			return append([]byte("z:"), append(data, 0x00, 0x00, 0xff, 0xff)...), nil
		}
		return sendDataDI{
			dataFramesWriteLocker: fakeFuncLocker{lock: func() {}, unlock: func() {}},
			writeLocker:           fakeFuncLocker{lock: func() {}, unlock: func() {}},
			newDataFrame: func(config NewFrameConfig) (*Frame, error) {
				return &Frame{Opcode: config.Opcode}, nil
			},
			bufferedWriteFrame: func(f *Frame) error { return nil },
			flushWriter:        func() error { return nil },
			contextDeflater:    fakeFuncDeflater{deflateFunc: fake_deflate},
			noContextDeflater:  fakeFuncDeflater{deflateFunc: fake_deflate},
		}
	}

	// 5.4: there is no message for it to continue.
	t.Run("refuses a continuation opcode", func(t *testing.T) {
		var steps []string
		di := base_di()
		di.dataFramesWriteLocker = fakeFuncLocker{
			lock:   func() { steps = append(steps, "data lock") },
			unlock: func() { steps = append(steps, "data unlock") },
		}
		di.writeLocker = fakeFuncLocker{
			lock:   func() { steps = append(steps, "write lock") },
			unlock: func() { steps = append(steps, "write unlock") },
		}
		di.newDataFrame = func(NewFrameConfig) (*Frame, error) {
			t.Error("built a frame for a continuation")
			return nil, nil
		}

		if err := sendData(OpcodeContinuation, []byte("hello"), SendDataConfig{}, di); !errors.Is(err, ErrContinuationFrameWithoutMsg) {
			t.Errorf("sendData = %v, want ErrContinuationFrameWithoutMsg", err)
		}
		if len(steps) != 0 {
			t.Errorf("steps %q, want no lock taken", steps)
		}
	})

	t.Run("builds one frame from the opcode", func(t *testing.T) {
		var configs []NewFrameConfig
		di := base_di()
		di.newDataFrame = func(config NewFrameConfig) (*Frame, error) {
			configs = append(configs, config)
			return &Frame{Opcode: config.Opcode}, nil
		}

		if err := sendData(OpcodeBinary, []byte("hello world"), SendDataConfig{ChunkSize: 4}, di); err != nil {
			t.Fatalf("sendData: %v", err)
		}
		if len(configs) != 1 || configs[0].Opcode != OpcodeBinary || configs[0].PayloadData != nil || configs[0].FIN {
			t.Errorf("configs %+v, want one {Opcode: binary}", configs)
		}
	})

	// A message that fails to build is never deflated: its bytes would enter
	// the context, and the next message could refer to what the peer never saw.
	t.Run("returns a build error with nothing written or deflated", func(t *testing.T) {
		wantErr := errors.New("cannot build")
		var steps []string
		di := base_di()
		di.deflateAgreedConfig = &PermessageDeflateConfig{}
		for _, deflater := range []*sendDataDeflater{&di.contextDeflater, &di.noContextDeflater} {
			*deflater = fakeFuncDeflater{deflateFunc: func([]byte, int) ([]byte, error) {
				steps = append(steps, "deflate")
				return []byte{0x00, 0x00, 0xff, 0xff}, nil
			}}
		}
		di.dataFramesWriteLocker = fakeFuncLocker{
			lock:   func() { steps = append(steps, "data lock") },
			unlock: func() { steps = append(steps, "data unlock") },
		}
		di.writeLocker = fakeFuncLocker{
			lock:   func() { steps = append(steps, "write lock") },
			unlock: func() { steps = append(steps, "write unlock") },
		}
		di.newDataFrame = func(NewFrameConfig) (*Frame, error) { return nil, wantErr }
		di.bufferedWriteFrame = func(*Frame) error {
			t.Error("wrote after a build error")
			return nil
		}
		di.flushWriter = func() error {
			t.Error("flushed after a build error")
			return nil
		}

		if err := sendData(OpcodeText, []byte("hello"), SendDataConfig{DeflateIfAgreed: true}, di); !errors.Is(err, wantErr) {
			t.Errorf("sendData = %v, want %v", err, wantErr)
		}
		if want := []string{"data lock", "data unlock"}; !slices.Equal(steps, want) {
			t.Errorf("steps %q, want %q", steps, want)
		}
	})

	// 5.4: the first frame carries the opcode, the rest 0, only the last FIN.
	// The frame is reused, so each write is recorded as it happens.
	t.Run("writes one frame per chunk", func(t *testing.T) {
		for _, testCase := range []struct {
			name      string
			payload   string
			chunkSize int
			want      []string
		}{
			{"0 sends one frame", "hello", 0, []string{"1 true false hello"}},
			{"negative sends one frame", "hello", -1, []string{"1 true false hello"}},
			{"a payload that fits is one frame", "hello", 5, []string{"1 true false hello"}},
			{"an empty payload is one empty frame", "", 4, []string{"1 true false "}},
			{"splits by chunkSize", "hello world", 4, []string{"1 false false hell", "0 false false o wo", "0 true false rld"}},
			{"an exact multiple ends on its last chunk", "abcdefgh", 4, []string{"1 false false abcd", "0 true false efgh"}},
		} {
			var got []string
			di := base_di()
			di.bufferedWriteFrame = func(f *Frame) error {
				got = append(got, fmt.Sprintf("%d %v %v %s", f.Opcode, f.FIN, f.RSV1, f.PayloadData))
				return nil
			}

			if err := sendData(OpcodeText, []byte(testCase.payload), SendDataConfig{ChunkSize: testCase.chunkSize}, di); err != nil {
				t.Fatalf("%s: sendData: %v", testCase.name, err)
			}
			if !slices.Equal(got, testCase.want) {
				t.Errorf("%s: wrote %q, want %q", testCase.name, got, testCase.want)
			}
		}
	})

	/*
		dataFramesWriteLocker is held throughout. writeLocker is taken for each
		frame and for the flush, and released in between, so a control frame can
		get out mid message (5.4).
	*/
	t.Run("locks", func(t *testing.T) {
		var steps []string
		di := base_di()
		di.dataFramesWriteLocker = fakeFuncLocker{
			lock:   func() { steps = append(steps, "data lock") },
			unlock: func() { steps = append(steps, "data unlock") },
		}
		di.writeLocker = fakeFuncLocker{
			lock:   func() { steps = append(steps, "write lock") },
			unlock: func() { steps = append(steps, "write unlock") },
		}
		di.bufferedWriteFrame = func(*Frame) error {
			steps = append(steps, "write")
			return nil
		}
		di.flushWriter = func() error {
			steps = append(steps, "flush")
			return nil
		}

		if err := sendData(OpcodeText, []byte("hello world"), SendDataConfig{ChunkSize: 4}, di); err != nil {
			t.Fatalf("sendData: %v", err)
		}
		want := []string{
			"data lock",
			"write lock", "write", "write unlock",
			"write lock", "write", "write unlock",
			"write lock", "write", "write unlock",
			"write lock", "flush", "write unlock",
			"data unlock",
		}
		if !slices.Equal(steps, want) {
			t.Errorf("steps %q, want %q", steps, want)
		}
	})

	// A message missing chunks must not be flushed as if it were whole.
	t.Run("stops at a write error", func(t *testing.T) {
		wantErr := errors.New("refused")
		var steps []string
		di := base_di()
		di.dataFramesWriteLocker = fakeFuncLocker{
			lock:   func() { steps = append(steps, "data lock") },
			unlock: func() { steps = append(steps, "data unlock") },
		}
		di.writeLocker = fakeFuncLocker{
			lock:   func() { steps = append(steps, "write lock") },
			unlock: func() { steps = append(steps, "write unlock") },
		}
		di.bufferedWriteFrame = func(*Frame) error {
			steps = append(steps, "write")
			return wantErr
		}
		di.flushWriter = func() error {
			steps = append(steps, "flush")
			return nil
		}

		if err := sendData(OpcodeText, []byte("hello world"), SendDataConfig{ChunkSize: 4}, di); !errors.Is(err, wantErr) {
			t.Errorf("sendData = %v, want %v", err, wantErr)
		}
		want := []string{"data lock", "write lock", "write", "write unlock", "data unlock"}
		if !slices.Equal(steps, want) {
			t.Errorf("steps %q, want %q", steps, want)
		}
	})

	t.Run("returns a flush error", func(t *testing.T) {
		wantErr := errors.New("socket gone")
		var steps []string
		di := base_di()
		di.dataFramesWriteLocker = fakeFuncLocker{
			lock:   func() { steps = append(steps, "data lock") },
			unlock: func() { steps = append(steps, "data unlock") },
		}
		di.writeLocker = fakeFuncLocker{
			lock:   func() { steps = append(steps, "write lock") },
			unlock: func() { steps = append(steps, "write unlock") },
		}
		di.bufferedWriteFrame = func(*Frame) error {
			steps = append(steps, "write")
			return nil
		}
		di.flushWriter = func() error {
			steps = append(steps, "flush")
			return wantErr
		}

		if err := sendData(OpcodeText, []byte("hello"), SendDataConfig{}, di); !errors.Is(err, wantErr) {
			t.Errorf("sendData = %v, want %v", err, wantErr)
		}
		want := []string{
			"data lock",
			"write lock", "write", "write unlock",
			"write lock", "flush", "write unlock",
			"data unlock",
		}
		if !slices.Equal(steps, want) {
			t.Errorf("steps %q, want %q", steps, want)
		}
	})

	// RFC 7692 6: RSV1 on the first frame marks the message compressed. 7.2.1:
	// the flush's trailing 00 00 ff ff is not sent. The deflated payload is
	// what gets cut into chunks.
	t.Run("deflates when agreed and asked, then cuts the result", func(t *testing.T) {
		var deflated []string
		var got []string
		di := base_di()
		di.deflateAgreedConfig = &PermessageDeflateConfig{}
		di.contextDeflater = fakeFuncDeflater{deflateFunc: func(data []byte, level int) ([]byte, error) {
			deflated = append(deflated, fmt.Sprintf("%s %d", data, level))
			return append([]byte("z:"), append(data, 0x00, 0x00, 0xff, 0xff)...), nil
		}}
		di.bufferedWriteFrame = func(f *Frame) error {
			got = append(got, fmt.Sprintf("%d %v %v %s", f.Opcode, f.FIN, f.RSV1, f.PayloadData))
			return nil
		}

		config := SendDataConfig{ChunkSize: 4, DeflateIfAgreed: true, DeflateLevel: 9}
		if err := sendData(OpcodeText, []byte("hello"), config, di); err != nil {
			t.Fatalf("sendData: %v", err)
		}
		if want := []string{"hello 9"}; !slices.Equal(deflated, want) {
			t.Errorf("deflate got %q, want %q", deflated, want)
		}
		if want := []string{"1 false true z:he", "0 true false llo"}; !slices.Equal(got, want) {
			t.Errorf("wrote %q, want %q", got, want)
		}
	})

	// 5.4 with RFC 7692 6: the first frame carries the opcode and RSV1, the
	// rest 0 and no RSV1, only the last FIN. base_di's deflate gives "z:" and
	// the message.
	t.Run("writes one frame per chunk of the deflated payload", func(t *testing.T) {
		for _, testCase := range []struct {
			name      string
			payload   string
			chunkSize int
			want      []string
		}{
			{
				name:      "0 sends one frame",
				payload:   "hello",
				chunkSize: 0,
				want:      []string{"1 true true z:hello"},
			},
			{
				name:      "a result that fits is one frame",
				payload:   "hello",
				chunkSize: 7,
				want:      []string{"1 true true z:hello"},
			},
			{
				name:      "an empty payload is one frame",
				payload:   "",
				chunkSize: 4,
				want:      []string{"1 true true z:"},
			},
			{
				name:      "splits the result by chunkSize",
				payload:   "hello world",
				chunkSize: 4,
				want: []string{
					"1 false true z:he",
					"0 false false llo ",
					"0 false false worl",
					"0 true false d",
				},
			},
			{
				name:      "an exact multiple ends on its last chunk",
				payload:   "abcdef",
				chunkSize: 4,
				want: []string{
					"1 false true z:ab",
					"0 true false cdef",
				},
			},
		} {
			var got []string
			di := base_di()
			di.deflateAgreedConfig = &PermessageDeflateConfig{}
			di.bufferedWriteFrame = func(f *Frame) error {
				got = append(got, fmt.Sprintf("%d %v %v %s", f.Opcode, f.FIN, f.RSV1, f.PayloadData))
				return nil
			}

			config := SendDataConfig{ChunkSize: testCase.chunkSize, DeflateIfAgreed: true}
			if err := sendData(OpcodeText, []byte(testCase.payload), config, di); err != nil {
				t.Fatalf("%s: sendData: %v", testCase.name, err)
			}
			if !slices.Equal(got, testCase.want) {
				t.Errorf("%s: wrote %q, want %q", testCase.name, got, testCase.want)
			}
		}
	})

	t.Run("deflates at flate.NoCompression for level 0", func(t *testing.T) {
		var levels []int
		di := base_di()
		di.deflateAgreedConfig = &PermessageDeflateConfig{}
		di.contextDeflater = fakeFuncDeflater{deflateFunc: func(data []byte, level int) ([]byte, error) {
			levels = append(levels, level)
			return []byte{0x00, 0x00, 0xff, 0xff}, nil
		}}

		if err := sendData(OpcodeText, []byte("hello"), SendDataConfig{DeflateIfAgreed: true}, di); err != nil {
			t.Fatalf("sendData: %v", err)
		}
		if want := []int{flate.NoCompression}; !slices.Equal(levels, want) {
			t.Errorf("deflate got levels %v, want %v", levels, want)
		}
	})

	t.Run("sends as is unless agreed and asked", func(t *testing.T) {
		for _, testCase := range []struct {
			name            string
			agreed          *PermessageDeflateConfig
			deflateIfAgreed bool
		}{
			{"asked, not agreed", nil, true},
			{"agreed, not asked", &PermessageDeflateConfig{}, false},
			{"neither", nil, false},
		} {
			var got []string
			di := base_di()
			di.deflateAgreedConfig = testCase.agreed
			for _, deflater := range []*sendDataDeflater{&di.contextDeflater, &di.noContextDeflater} {
				*deflater = fakeFuncDeflater{deflateFunc: func([]byte, int) ([]byte, error) {
					t.Errorf("%s: deflated", testCase.name)
					return nil, nil
				}}
			}
			di.bufferedWriteFrame = func(f *Frame) error {
				got = append(got, fmt.Sprintf("%d %v %v %s", f.Opcode, f.FIN, f.RSV1, f.PayloadData))
				return nil
			}

			config := SendDataConfig{ChunkSize: 4, DeflateIfAgreed: testCase.deflateIfAgreed}
			if err := sendData(OpcodeText, []byte("hello"), config, di); err != nil {
				t.Fatalf("%s: sendData: %v", testCase.name, err)
			}
			if want := []string{"1 false false hell", "0 true false o"}; !slices.Equal(got, want) {
				t.Errorf("%s: wrote %q, want %q", testCase.name, got, want)
			}
		}
	})

	// RFC 7692 7.1.1: server_no_context_takeover binds a server,
	// client_no_context_takeover a client. A side bound by it starts every
	// message from an empty window, which noContextDeflater does.
	t.Run("picks the deflater by this side's context takeover", func(t *testing.T) {
		for _, testCase := range []struct {
			name         string
			peerIsClient bool
			agreed       PermessageDeflateConfig
			want         string
		}{
			{
				name:         "a server with server_no_context_takeover",
				peerIsClient: true,
				agreed:       PermessageDeflateConfig{ServerNoContextTakeover: true},
				want:         "no context",
			},
			{
				name:         "a server with client_no_context_takeover only",
				peerIsClient: true,
				agreed:       PermessageDeflateConfig{ClientNoContextTakeover: true},
				want:         "context",
			},
			{
				name:         "a client with client_no_context_takeover",
				peerIsClient: false,
				agreed:       PermessageDeflateConfig{ClientNoContextTakeover: true},
				want:         "no context",
			},
			{
				name:         "a client with server_no_context_takeover only",
				peerIsClient: false,
				agreed:       PermessageDeflateConfig{ServerNoContextTakeover: true},
				want:         "context",
			},
			{
				name:         "a server with neither",
				peerIsClient: true,
				agreed:       PermessageDeflateConfig{},
				want:         "context",
			},
			{
				name:         "a client with neither",
				peerIsClient: false,
				agreed:       PermessageDeflateConfig{},
				want:         "context",
			},
		} {
			var used []string
			di := base_di()
			di.peerIsClient = testCase.peerIsClient
			di.deflateAgreedConfig = &testCase.agreed
			di.contextDeflater = fakeFuncDeflater{deflateFunc: func([]byte, int) ([]byte, error) {
				used = append(used, "context")
				return []byte{0x00, 0x00, 0xff, 0xff}, nil
			}}
			di.noContextDeflater = fakeFuncDeflater{deflateFunc: func([]byte, int) ([]byte, error) {
				used = append(used, "no context")
				return []byte{0x00, 0x00, 0xff, 0xff}, nil
			}}

			if err := sendData(OpcodeText, []byte("hello"), SendDataConfig{DeflateIfAgreed: true}, di); err != nil {
				t.Fatalf("%s: sendData: %v", testCase.name, err)
			}
			if want := []string{testCase.want}; !slices.Equal(used, want) {
				t.Errorf("%s: used %q, want %q", testCase.name, used, want)
			}
		}
	})

	// The result stays in the deflater's buffer for the next message: sendData
	// never releases it, see ReleaseDeflaterResultBuffer.
	t.Run("deflates under the data lock, before any write", func(t *testing.T) {
		var steps []string
		di := base_di()
		di.deflateAgreedConfig = &PermessageDeflateConfig{}
		di.contextDeflater = fakeFuncDeflater{deflateFunc: func([]byte, int) ([]byte, error) {
			steps = append(steps, "deflate")
			return []byte{0x00, 0x00, 0xff, 0xff}, nil
		}}
		di.dataFramesWriteLocker = fakeFuncLocker{
			lock:   func() { steps = append(steps, "data lock") },
			unlock: func() { steps = append(steps, "data unlock") },
		}
		di.bufferedWriteFrame = func(*Frame) error {
			steps = append(steps, "write")
			return nil
		}
		di.flushWriter = func() error {
			steps = append(steps, "flush")
			return nil
		}

		if err := sendData(OpcodeText, []byte("hello"), SendDataConfig{DeflateIfAgreed: true}, di); err != nil {
			t.Fatalf("sendData: %v", err)
		}
		if want := []string{"data lock", "deflate", "write", "flush", "data unlock"}; !slices.Equal(steps, want) {
			t.Errorf("steps %q, want %q", steps, want)
		}
	})

	t.Run("refuses a level compress/flate has not before anything runs", func(t *testing.T) {
		for _, testCase := range []struct {
			name   string
			config SendDataConfig
		}{
			{"above flate.BestCompression", SendDataConfig{DeflateIfAgreed: true, DeflateLevel: 10}},
			{"below flate.HuffmanOnly", SendDataConfig{DeflateIfAgreed: true, DeflateLevel: -3}},
			{"not asked to deflate", SendDataConfig{DeflateLevel: 10}},
		} {
			var steps []string
			di := base_di()
			di.deflateAgreedConfig = &PermessageDeflateConfig{}
			di.dataFramesWriteLocker = fakeFuncLocker{
				lock:   func() { steps = append(steps, "data lock") },
				unlock: func() { steps = append(steps, "data unlock") },
			}
			di.contextDeflater = fakeFuncDeflater{deflateFunc: func([]byte, int) ([]byte, error) {
				steps = append(steps, "deflate")
				return nil, nil
			}}

			if err := sendData(OpcodeText, []byte("hello"), testCase.config, di); !errors.Is(err, ErrDeflateLevelInvalid) {
				t.Errorf("%s: sendData = %v, want ErrDeflateLevelInvalid", testCase.name, err)
			}
			if len(steps) != 0 {
				t.Errorf("%s: steps %q, want nothing run", testCase.name, steps)
			}
		}
	})

	t.Run("returns a deflate error with nothing written", func(t *testing.T) {
		wantErr := errors.New("cannot deflate")
		var steps []string
		di := base_di()
		di.deflateAgreedConfig = &PermessageDeflateConfig{}
		di.dataFramesWriteLocker = fakeFuncLocker{
			lock:   func() { steps = append(steps, "data lock") },
			unlock: func() { steps = append(steps, "data unlock") },
		}
		di.contextDeflater = fakeFuncDeflater{deflateFunc: func([]byte, int) ([]byte, error) { return nil, wantErr }}
		di.bufferedWriteFrame = func(*Frame) error {
			steps = append(steps, "write")
			return nil
		}
		di.flushWriter = func() error {
			steps = append(steps, "flush")
			return nil
		}

		if err := sendData(OpcodeText, []byte("hello"), SendDataConfig{DeflateIfAgreed: true}, di); !errors.Is(err, wantErr) {
			t.Errorf("sendData = %v, want %v", err, wantErr)
		}
		if want := []string{"data lock", "data unlock"}; !slices.Equal(steps, want) {
			t.Errorf("steps %q, want %q", steps, want)
		}
	})
}
