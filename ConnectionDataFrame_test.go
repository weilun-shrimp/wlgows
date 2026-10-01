package wlgows

import (
	"errors"
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

/*
RFC 6455 5.4 through the long data transmission: every chunk but the last to
TransmitData, the last to End so it carries FIN itself and no empty terminator
follows it.
*/
func TestSendData(t *testing.T) {
	// base_di succeeds at every step; each case overrides what it looks at.
	base_di := func() sendDataDI {
		return sendDataDI{
			startLongDataTransmission:   func(opcode uint8) error { return nil },
			transmitData:                func(data []byte) error { return nil },
			endLongDataTransmission:     func(data []byte) error { return nil },
			releaseLongDataTransmission: func() {},
		}
	}

	t.Run("splits the payload into chunks of chunkSize", func(t *testing.T) {
		var transmitted, ended []string
		di := base_di()
		di.transmitData = func(data []byte) error {
			transmitted = append(transmitted, string(data))
			return nil
		}
		di.endLongDataTransmission = func(data []byte) error {
			ended = append(ended, string(data))
			return nil
		}

		if err := sendData(OpcodeText, []byte("hello world"), 4, di); err != nil {
			t.Fatalf("sendData: %v", err)
		}
		if !slices.Equal(transmitted, []string{"hell", "o wo"}) || !slices.Equal(ended, []string{"rld"}) {
			t.Errorf("transmitted %q, ended %q — want [hell o wo] then [rld]", transmitted, ended)
		}
	})

	t.Run("an exact multiple ends on its last chunk", func(t *testing.T) {
		var transmitted, ended []string
		di := base_di()
		di.transmitData = func(data []byte) error {
			transmitted = append(transmitted, string(data))
			return nil
		}
		di.endLongDataTransmission = func(data []byte) error {
			ended = append(ended, string(data))
			return nil
		}

		if err := sendData(OpcodeText, []byte("abcdefgh"), 4, di); err != nil {
			t.Fatalf("sendData: %v", err)
		}
		if !slices.Equal(transmitted, []string{"abcd"}) || !slices.Equal(ended, []string{"efgh"}) {
			t.Errorf("transmitted %q, ended %q — want [abcd] then [efgh]", transmitted, ended)
		}
	})

	// 0 or less, or a chunkSize the payload fits in, hands it all to End — one frame.
	t.Run("the whole payload to End when chunkSize does not split it", func(t *testing.T) {
		for _, size := range []int{-1, 0, 5, 100} {
			var ended []string
			di := base_di()
			di.transmitData = func([]byte) error {
				t.Errorf("size %d: transmitted, want it all to End", size)
				return nil
			}
			di.endLongDataTransmission = func(data []byte) error {
				ended = append(ended, string(data))
				return nil
			}

			if err := sendData(OpcodeText, []byte("hello"), size, di); err != nil {
				t.Fatalf("sendData: %v", err)
			}
			if !slices.Equal(ended, []string{"hello"}) {
				t.Errorf("size %d: ended %q, want [hello]", size, ended)
			}
		}
	})

	// End turns an empty payload into one empty frame; 5.6 allows the message.
	t.Run("an empty payload goes to End", func(t *testing.T) {
		for _, size := range []int{0, 4} {
			var ended []string
			di := base_di()
			di.transmitData = func([]byte) error {
				t.Errorf("size %d: transmitted an empty payload", size)
				return nil
			}
			di.endLongDataTransmission = func(data []byte) error {
				ended = append(ended, string(data))
				return nil
			}

			if err := sendData(OpcodeText, nil, size, di); err != nil {
				t.Fatalf("sendData: %v", err)
			}
			if !slices.Equal(ended, []string{""}) {
				t.Errorf("size %d: ended %q, want one empty", size, ended)
			}
		}
	})

	// By byte, not by rune: UTF-8 is judged on the message (8.1).
	t.Run("splits a rune across chunks", func(t *testing.T) {
		var transmitted, ended []string
		di := base_di()
		di.transmitData = func(data []byte) error {
			transmitted = append(transmitted, string(data))
			return nil
		}
		di.endLongDataTransmission = func(data []byte) error {
			ended = append(ended, string(data))
			return nil
		}

		if err := sendData(OpcodeText, []byte("中"), 1, di); err != nil {
			t.Fatalf("sendData: %v", err)
		}
		if !slices.Equal(transmitted, []string{"\xe4", "\xb8"}) || !slices.Equal(ended, []string{"\xad"}) {
			t.Errorf("transmitted %q, ended %q — want [e4 b8] then [ad]", transmitted, ended)
		}
	})

	t.Run("opens with the opcode it is given", func(t *testing.T) {
		for _, opcode := range []uint8{OpcodeText, OpcodeBinary} {
			var got []uint8
			di := base_di()
			di.startLongDataTransmission = func(opcode uint8) error {
				got = append(got, opcode)
				return nil
			}

			if err := sendData(opcode, []byte("hello"), 0, di); err != nil {
				t.Fatalf("sendData: %v", err)
			}
			if !slices.Equal(got, []uint8{opcode}) {
				t.Errorf("Start got %#x, want [%#x]", got, opcode)
			}
		}
	})

	// A refused start locked nothing, so there is nothing to transmit, end or
	// release — either of the last two would unlock what was never locked.
	t.Run("stops at a start error", func(t *testing.T) {
		wantErr := errors.New("cannot start")
		di := base_di()
		di.startLongDataTransmission = func(uint8) error { return wantErr }
		di.transmitData = func([]byte) error {
			t.Error("transmitted after a refused start")
			return nil
		}
		di.endLongDataTransmission = func([]byte) error {
			t.Error("ended after a refused start")
			return nil
		}
		di.releaseLongDataTransmission = func() { t.Error("released after a refused start") }

		if err := sendData(OpcodeText, []byte("hello world"), 4, di); !errors.Is(err, wantErr) {
			t.Errorf("sendData = %v, want %v", err, wantErr)
		}
	})

	/*
		End would put FIN on a message missing chunks, and the peer would take it
		as complete. Only released, the message is left unterminated.
	*/
	t.Run("releases without End on a transmit error", func(t *testing.T) {
		wantErr := errors.New("cannot transmit")
		var transmits, releases int
		di := base_di()
		di.transmitData = func([]byte) error {
			transmits++
			return wantErr
		}
		di.endLongDataTransmission = func([]byte) error {
			t.Error("ended a message missing chunks")
			return nil
		}
		di.releaseLongDataTransmission = func() { releases++ }

		if err := sendData(OpcodeText, []byte("hello world"), 4, di); !errors.Is(err, wantErr) {
			t.Errorf("sendData = %v, want %v", err, wantErr)
		}
		if transmits != 1 || releases != 1 {
			t.Errorf("transmitted %d, released %d — want 1 and 1", transmits, releases)
		}
	})

	// End only sends FIN; the connection is freed by Release, after it.
	t.Run("releases once after End", func(t *testing.T) {
		var steps []string
		di := base_di()
		di.endLongDataTransmission = func([]byte) error {
			steps = append(steps, "end")
			return nil
		}
		di.releaseLongDataTransmission = func() { steps = append(steps, "release") }

		if err := sendData(OpcodeText, []byte("hello world"), 4, di); err != nil {
			t.Fatalf("sendData: %v", err)
		}
		if !slices.Equal(steps, []string{"end", "release"}) {
			t.Errorf("steps %q, want [end release]", steps)
		}
	})

	t.Run("returns an end error and still releases", func(t *testing.T) {
		wantErr := errors.New("cannot end")
		releases := 0
		di := base_di()
		di.endLongDataTransmission = func([]byte) error { return wantErr }
		di.releaseLongDataTransmission = func() { releases++ }

		if err := sendData(OpcodeText, []byte("hello"), 0, di); !errors.Is(err, wantErr) {
			t.Errorf("sendData = %v, want %v", err, wantErr)
		}
		if releases != 1 {
			t.Errorf("released %d times, want 1", releases)
		}
	})
}
