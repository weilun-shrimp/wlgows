package wlgows

import (
	"errors"
	"fmt"
	"slices"
	"testing"
)

func TestSendText(t *testing.T) {
	t.Run("hands the text on as a text message with its chunk size", func(t *testing.T) {
		calls := 0
		err := sendText([]byte("hello"), 4, sendTextDI{
			sendData: func(opcode uint8, payload []byte, chunkSize int) error {
				calls++
				if opcode != OpcodeText || string(payload) != "hello" || chunkSize != 4 {
					t.Errorf("sendData(%#x, %q, %d), want (%#x, %q, 4)",
						opcode, payload, chunkSize, OpcodeText, "hello")
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
		err := sendText([]byte{0xe4, 0xb8}, 0, sendTextDI{
			sendData: func(uint8, []byte, int) error {
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
		err := sendText([]byte("中文字"), 0, sendTextDI{
			sendData: func(uint8, []byte, int) error { calls++; return nil },
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
		err := sendText([]byte("hello"), 0, sendTextDI{
			sendData: func(uint8, []byte, int) error { return wantErr },
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
	t.Run("hands the data on as a binary message with its chunk size", func(t *testing.T) {
		calls := 0
		err := sendBinary([]byte{0xe4, 0xb8}, 2, sendBinaryDI{
			sendData: func(opcode uint8, payload []byte, chunkSize int) error {
				calls++
				if opcode != OpcodeBinary || string(payload) != "\xe4\xb8" || chunkSize != 2 {
					t.Errorf("sendData(%#x, % x, %d), want (%#x, e4 b8, 2)",
						opcode, payload, chunkSize, OpcodeBinary)
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
		err := sendBinary([]byte{0x01}, 0, sendBinaryDI{
			sendData: func(uint8, []byte, int) error { return wantErr },
		})
		if !errors.Is(err, wantErr) {
			t.Errorf("sendBinary = %v, want %v", err, wantErr)
		}
	})
}

func TestSendData(t *testing.T) {
	// base_di succeeds at every step; each case overrides what it looks at.
	base_di := func() sendDataDI {
		return sendDataDI{
			dataFramesWriteLocker: fakeFuncLocker{lock: func() {}, unlock: func() {}},
			writeLocker:           fakeFuncLocker{lock: func() {}, unlock: func() {}},
			newDataFrame: func(config NewFrameConfig) (*Frame, error) {
				return &Frame{Opcode: config.Opcode}, nil
			},
			bufferedWriteFrame: func(f *Frame) error { return nil },
			flushWriter:        func() error { return nil },
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

		if err := sendData(OpcodeContinuation, []byte("hello"), 0, di); !errors.Is(err, ErrContinuationFrameWithoutMsg) {
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

		if err := sendData(OpcodeBinary, []byte("hello world"), 4, di); err != nil {
			t.Fatalf("sendData: %v", err)
		}
		if len(configs) != 1 || configs[0].Opcode != OpcodeBinary || configs[0].PayloadData != nil || configs[0].FIN {
			t.Errorf("configs %+v, want one {Opcode: binary}", configs)
		}
	})

	t.Run("returns a build error with nothing written", func(t *testing.T) {
		wantErr := errors.New("cannot build")
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
		di.newDataFrame = func(NewFrameConfig) (*Frame, error) { return nil, wantErr }
		di.bufferedWriteFrame = func(*Frame) error {
			t.Error("wrote after a build error")
			return nil
		}
		di.flushWriter = func() error {
			t.Error("flushed after a build error")
			return nil
		}

		if err := sendData(OpcodeText, []byte("hello"), 0, di); !errors.Is(err, wantErr) {
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
			{"0 sends one frame", "hello", 0, []string{"1 true hello"}},
			{"negative sends one frame", "hello", -1, []string{"1 true hello"}},
			{"a payload that fits is one frame", "hello", 5, []string{"1 true hello"}},
			{"an empty payload is one empty frame", "", 4, []string{"1 true "}},
			{"splits by chunkSize", "hello world", 4, []string{"1 false hell", "0 false o wo", "0 true rld"}},
			{"an exact multiple ends on its last chunk", "abcdefgh", 4, []string{"1 false abcd", "0 true efgh"}},
		} {
			var got []string
			di := base_di()
			di.bufferedWriteFrame = func(f *Frame) error {
				got = append(got, fmt.Sprintf("%d %v %s", f.Opcode, f.FIN, f.PayloadData))
				return nil
			}

			if err := sendData(OpcodeText, []byte(testCase.payload), testCase.chunkSize, di); err != nil {
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
		get out mid message (5.5.2).
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

		if err := sendData(OpcodeText, []byte("hello world"), 4, di); err != nil {
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

		if err := sendData(OpcodeText, []byte("hello world"), 4, di); !errors.Is(err, wantErr) {
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

		if err := sendData(OpcodeText, []byte("hello"), 0, di); !errors.Is(err, wantErr) {
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
}
