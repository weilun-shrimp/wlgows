package wlgows

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"io"
	"net"
	"reflect"
	"slices"
	"testing"
)

func TestBufferedWriteFrame(t *testing.T) {
	// base_di has every step succeed; each case overrides what it looks at.
	base_di := func() bufferedWriteFrameDI {
		return bufferedWriteFrameDI{
			writer:             bufio.NewWriter(&fakeIOWriter{}),
			validateSendFrame:  func(f *Frame, closeSent bool) error { return nil },
			prepareSendFrame:   func(f *Frame, maskSendFrame bool, di prepareSendFrameDI) error { return nil },
			prepareSendFrameDI: prepareSendFrameDI{fillMaskingKey: func(key *[4]byte) error { return nil }},
			writeFrameHeader:   func(f *Frame, w *bufio.Writer) error { return nil },
			writeFramePayload:  func(f *Frame, w *bufio.Writer) error { return nil },
		}
	}

	t.Run("validates, prepares, writes the header, then the payload", func(t *testing.T) {
		f := &Frame{}
		var steps []string
		di := base_di()
		di.closeSent, di.maskSendFrame = true, true
		di.validateSendFrame = func(got *Frame, closeSent bool) error {
			steps = append(steps, fmt.Sprintf("validate same=%v closeSent=%v", got == f, closeSent))
			return nil
		}
		di.prepareSendFrameDI.fillMaskingKey = func(*[4]byte) error {
			steps = append(steps, "fillMaskingKey")
			return nil
		}
		di.prepareSendFrame = func(got *Frame, maskSendFrame bool, prepareDI prepareSendFrameDI) error {
			steps = append(steps, fmt.Sprintf("prepare same=%v maskSendFrame=%v", got == f, maskSendFrame))
			return prepareDI.fillMaskingKey(nil)
		}
		di.writeFrameHeader = func(got *Frame, w *bufio.Writer) error {
			steps = append(steps, fmt.Sprintf("header same=%v writer=%v", got == f, w == di.writer))
			return nil
		}
		di.writeFramePayload = func(got *Frame, w *bufio.Writer) error {
			steps = append(steps, fmt.Sprintf("payload same=%v writer=%v", got == f, w == di.writer))
			return nil
		}

		if err := bufferedWriteFrame(f, di); err != nil {
			t.Fatalf("bufferedWriteFrame: %v", err)
		}
		want := []string{
			"validate same=true closeSent=true",
			"prepare same=true maskSendFrame=true", "fillMaskingKey",
			"header same=true writer=true",
			"payload same=true writer=true",
		}
		if !slices.Equal(steps, want) {
			t.Errorf("steps %q, want %q", steps, want)
		}
	})

	t.Run("stops at a validate error", func(t *testing.T) {
		wantErr := errors.New("refused")
		di := base_di()
		di.validateSendFrame = func(*Frame, bool) error { return wantErr }
		di.prepareSendFrame = func(*Frame, bool, prepareSendFrameDI) error {
			t.Error("prepared after a validate error")
			return nil
		}

		if err := bufferedWriteFrame(&Frame{}, di); !errors.Is(err, wantErr) {
			t.Errorf("bufferedWriteFrame = %v, want %v", err, wantErr)
		}
	})

	t.Run("stops at a prepare error", func(t *testing.T) {
		wantErr := errors.New("no entropy")
		di := base_di()
		di.prepareSendFrame = func(*Frame, bool, prepareSendFrameDI) error { return wantErr }
		di.writeFrameHeader = func(*Frame, *bufio.Writer) error {
			t.Error("wrote the header after a prepare error")
			return nil
		}

		if err := bufferedWriteFrame(&Frame{}, di); !errors.Is(err, wantErr) {
			t.Errorf("bufferedWriteFrame = %v, want %v", err, wantErr)
		}
	})

	t.Run("stops at a write header error", func(t *testing.T) {
		wantErr := errors.New("socket gone")
		di := base_di()
		di.writeFrameHeader = func(*Frame, *bufio.Writer) error { return wantErr }
		di.writeFramePayload = func(*Frame, *bufio.Writer) error {
			t.Error("wrote the payload after a write header error")
			return nil
		}

		if err := bufferedWriteFrame(&Frame{}, di); !errors.Is(err, wantErr) {
			t.Errorf("bufferedWriteFrame = %v, want %v", err, wantErr)
		}
	})

	t.Run("returns a write payload error", func(t *testing.T) {
		wantErr := errors.New("socket gone")
		di := base_di()
		di.writeFramePayload = func(*Frame, *bufio.Writer) error { return wantErr }

		if err := bufferedWriteFrame(&Frame{}, di); !errors.Is(err, wantErr) {
			t.Errorf("bufferedWriteFrame = %v, want %v", err, wantErr)
		}
	})
}

func TestValidateSendFrame(t *testing.T) {
	for _, testCase := range []struct {
		name          string
		opcode        byte
		payloadLength int
		closeSent     bool
		want          error
	}{
		// Before a close, anything within the control cap goes.
		{"text", OpcodeText, 5, false, nil},
		{"binary", OpcodeBinary, 5, false, nil},
		{"continuation", OpcodeContinuation, 5, false, nil},
		{"close", OpcodeClose, 5, false, nil},
		{"ping", OpcodePing, 5, false, nil},
		{"pong", OpcodePong, 5, false, nil},
		// 5.5: 125 bytes is the control cap; data frames have none.
		{"ping at 125 bytes", OpcodePing, 125, false, nil},
		{"close over 125 bytes", OpcodeClose, 126, false, ErrControlFramePayloadTooLong},
		{"ping over 125 bytes", OpcodePing, 126, false, ErrControlFramePayloadTooLong},
		{"pong over 125 bytes", OpcodePong, 126, false, ErrControlFramePayloadTooLong},
		{"binary over 125 bytes", OpcodeBinary, 126, false, nil},
		// 5.5.1: after this side's close, no data and no second close.
		{"text after a close", OpcodeText, 5, true, ErrCloseAlreadySent},
		{"binary after a close", OpcodeBinary, 5, true, ErrCloseAlreadySent},
		{"continuation after a close", OpcodeContinuation, 5, true, ErrCloseAlreadySent},
		{"second close", OpcodeClose, 5, true, ErrCloseAlreadySent},
		{"second close over 125 bytes", OpcodeClose, 126, true, ErrCloseAlreadySent},
		// 5.5.2: a ping is answered until the peer's close arrives.
		{"ping after a close", OpcodePing, 5, true, nil},
		{"pong after a close", OpcodePong, 5, true, nil},
		{"pong over 125 bytes after a close", OpcodePong, 126, true, ErrControlFramePayloadTooLong},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			f := &Frame{Opcode: testCase.opcode, PayloadData: make([]byte, testCase.payloadLength)}
			err := validateSendFrame(f, testCase.closeSent)
			if !errors.Is(err, testCase.want) {
				t.Errorf("validateSendFrame = %v, want %v", err, testCase.want)
			}
		})
	}
}

func TestPrepareSendFrame(t *testing.T) {
	// base_di hands out a fixed key; each case overrides what it looks at.
	base_di := func() prepareSendFrameDI {
		return prepareSendFrameDI{
			fillMaskingKey: func(key *[4]byte) error { *key = [4]byte{1, 2, 3, 4}; return nil },
		}
	}

	// 5.3: a client draws a new key for every frame, replacing any it carries.
	t.Run("a client masks with a fresh key", func(t *testing.T) {
		f := &Frame{Opcode: OpcodeText, Mask: false, MaskingKey: [4]byte{9, 9, 9, 9}, PayloadData: []byte("hello")}

		if err := prepareSendFrame(f, true, base_di()); err != nil {
			t.Fatalf("prepareSendFrame: %v", err)
		}
		want := Frame{
			Opcode: OpcodeText, Mask: true, MaskingKey: [4]byte{1, 2, 3, 4},
			PayloadLength: 5, PayloadData: []byte("hello"),
		}
		if !reflect.DeepEqual(*f, want) {
			t.Errorf("frame = %+v, want %+v", *f, want)
		}
	})

	t.Run("a server sends unmasked, with no key", func(t *testing.T) {
		f := &Frame{Opcode: OpcodeText, Mask: true, MaskingKey: [4]byte{9, 9, 9, 9}, PayloadData: []byte("hello")}
		di := base_di()
		di.fillMaskingKey = func(*[4]byte) error {
			t.Error("a server drew a key")
			return nil
		}

		if err := prepareSendFrame(f, false, di); err != nil {
			t.Fatalf("prepareSendFrame: %v", err)
		}
		want := Frame{Opcode: OpcodeText, PayloadLength: 5, PayloadData: []byte("hello")}
		if !reflect.DeepEqual(*f, want) {
			t.Errorf("frame = %+v, want %+v", *f, want)
		}
	})

	// The length fields follow PayloadData, whatever they said before.
	t.Run("rewrites the length fields", func(t *testing.T) {
		f := &Frame{Opcode: OpcodeBinary, PayloadLength: 3, ExtendedPayloadLength: 3, PayloadData: make([]byte, 300)}

		if err := prepareSendFrame(f, false, base_di()); err != nil {
			t.Fatalf("prepareSendFrame: %v", err)
		}
		if f.PayloadLength != 126 || f.ExtendedPayloadLength != 300 {
			t.Errorf("PayloadLength=%d ExtendedPayloadLength=%d, want 126 and 300", f.PayloadLength, f.ExtendedPayloadLength)
		}
	})

	// 5.5: a control frame is never fragmented; a data frame keeps its FIN.
	t.Run("sets FIN on a control frame only", func(t *testing.T) {
		for _, testCase := range []struct {
			opcode  byte
			wantFIN bool
		}{
			{OpcodeClose, true}, {OpcodePing, true}, {OpcodePong, true},
			{OpcodeContinuation, false}, {OpcodeText, false}, {OpcodeBinary, false},
		} {
			f := &Frame{Opcode: testCase.opcode, FIN: false}

			if err := prepareSendFrame(f, false, base_di()); err != nil {
				t.Fatalf("opcode %#x: prepareSendFrame: %v", testCase.opcode, err)
			}
			if f.FIN != testCase.wantFIN {
				t.Errorf("opcode %#x: FIN = %v, want %v", testCase.opcode, f.FIN, testCase.wantFIN)
			}
		}
	})

	// A frame with no key must not claim to be masked.
	t.Run("returns a key error and leaves Mask unset", func(t *testing.T) {
		wantErr := errors.New("no entropy")
		f := &Frame{Opcode: OpcodeText, PayloadData: []byte("hello")}
		di := base_di()
		di.fillMaskingKey = func(*[4]byte) error { return wantErr }

		if err := prepareSendFrame(f, true, di); !errors.Is(err, wantErr) {
			t.Errorf("prepareSendFrame = %v, want %v", err, wantErr)
		}
		if f.Mask {
			t.Error("Mask was set without a key")
		}
	})
}

func TestWriteFrameHeader(t *testing.T) {
	// The cases below seal a masked 7 bit length header: 6 bytes.
	wantHeader := []byte{0x81, 0x85, 1, 2, 3, 4}

	t.Run("seals the header into the buffer", func(t *testing.T) {
		out := &fakeIOWriter{}
		w := bufio.NewWriterSize(out, 16)
		w.WriteString("ab")

		f := &Frame{FIN: true, Opcode: OpcodeText, Mask: true, PayloadLength: 5, MaskingKey: [4]byte{1, 2, 3, 4}}
		if err := writeFrameHeader(f, w); err != nil {
			t.Fatalf("writeFrameHeader: %v", err)
		}
		if out.Len() != 0 {
			t.Errorf("wrote %d bytes, want the header kept in the buffer", out.Len())
		}
		w.Flush()
		if want := append([]byte("ab"), wantHeader...); !bytes.Equal(out.Bytes(), want) {
			t.Errorf("flushed % x, want % x", out.Bytes(), want)
		}
	})

	t.Run("an exact fit does not flush", func(t *testing.T) {
		out := &fakeIOWriter{}
		w := bufio.NewWriterSize(out, 16)
		w.WriteString("0123456789") // 6 bytes left

		f := &Frame{FIN: true, Opcode: OpcodeText, Mask: true, PayloadLength: 5, MaskingKey: [4]byte{1, 2, 3, 4}}
		if err := writeFrameHeader(f, w); err != nil {
			t.Fatalf("writeFrameHeader: %v", err)
		}
		if out.Len() != 0 || w.Buffered() != 16 {
			t.Errorf("wrote %d, buffered %d — want 0 and 16", out.Len(), w.Buffered())
		}
	})

	t.Run("flushes first when the header does not fit", func(t *testing.T) {
		out := &fakeIOWriter{}
		w := bufio.NewWriterSize(out, 16)
		w.WriteString("01234567890") // 5 bytes left

		f := &Frame{FIN: true, Opcode: OpcodeText, Mask: true, PayloadLength: 5, MaskingKey: [4]byte{1, 2, 3, 4}}
		if err := writeFrameHeader(f, w); err != nil {
			t.Fatalf("writeFrameHeader: %v", err)
		}
		if string(out.Bytes()) != "01234567890" {
			t.Errorf("wrote %q, want what was buffered before the header", out.Bytes())
		}
		w.Flush()
		if want := append([]byte("01234567890"), wantHeader...); !bytes.Equal(out.Bytes(), want) {
			t.Errorf("flushed % x, want % x", out.Bytes(), want)
		}
	})

	t.Run("returns a flush error", func(t *testing.T) {
		wantErr := errors.New("socket gone")
		out := &fakeIOWriter{err: wantErr}
		w := bufio.NewWriterSize(out, 16)
		w.WriteString("01234567890")

		f := &Frame{FIN: true, Opcode: OpcodeText, Mask: true, PayloadLength: 5, MaskingKey: [4]byte{1, 2, 3, 4}}
		if err := writeFrameHeader(f, w); !errors.Is(err, wantErr) {
			t.Errorf("writeFrameHeader = %v, want %v", err, wantErr)
		}
	})

	// bufio keeps the first error, so a writer whose flush failed refuses every
	// later Write, even one that fits.
	t.Run("returns a write error", func(t *testing.T) {
		wantErr := errors.New("socket gone")
		w := bufio.NewWriterSize(&fakeIOWriter{err: wantErr}, 16)
		w.WriteString("ab")
		w.Flush() // fails, and the writer keeps wantErr

		f := &Frame{FIN: true, Opcode: OpcodeText, Mask: true, PayloadLength: 5, MaskingKey: [4]byte{1, 2, 3, 4}}
		if err := writeFrameHeader(f, w); !errors.Is(err, wantErr) {
			t.Errorf("writeFrameHeader = %v, want %v", err, wantErr)
		}
	})

	t.Run("allocates nothing", func(t *testing.T) {
		w := bufio.NewWriterSize(io.Discard, 16)
		f := &Frame{FIN: true, Opcode: OpcodeBinary, Mask: true, PayloadLength: 127, ExtendedPayloadLength: 1 << 20}
		if allocs := testing.AllocsPerRun(10, func() { writeFrameHeader(f, w) }); allocs != 0 {
			t.Errorf("allocs = %v, want 0", allocs)
		}
	})
}

func TestWriteFramePayload(t *testing.T) {
	key := [4]byte{0x11, 0x22, 0x44, 0x88}

	t.Run("an unmasked payload goes as it is", func(t *testing.T) {
		for _, payloadLength := range []int{5, 40} { // within the 16 byte buffer, and past it
			out := &fakeIOWriter{}
			w := bufio.NewWriterSize(out, 16)
			payload := bytes.Repeat([]byte("abcdefg"), payloadLength/7+1)[:payloadLength]

			if err := writeFramePayload(&Frame{PayloadData: payload}, w); err != nil {
				t.Fatalf("payloadLength %d: writeFramePayload: %v", payloadLength, err)
			}
			w.Flush()
			if !bytes.Equal(out.Bytes(), payload) {
				t.Errorf("payloadLength %d: wrote % x, want % x", payloadLength, out.Bytes(), payload)
			}
		}
	})

	t.Run("returns an unmasked write error", func(t *testing.T) {
		wantErr := errors.New("socket gone")
		w := bufio.NewWriterSize(&fakeIOWriter{err: wantErr}, 16)

		if err := writeFramePayload(&Frame{PayloadData: make([]byte, 40)}, w); !errors.Is(err, wantErr) {
			t.Errorf("writeFramePayload = %v, want %v", err, wantErr)
		}
	})

	/*
		Masked, the payload goes through the buffer's free space in pieces. Each
		piece but the last is whole keys, so the next starts on key[0]; less than
		one key of room is flushed first. The want is masked with a byte loop.
	*/
	t.Run("a masked payload is masked piece by piece", func(t *testing.T) {
		for _, testCase := range []struct {
			name          string
			buffered      int // bytes in the 16 byte buffer before the payload
			payloadLength int
		}{
			{"fits whole, not a multiple of 4", 0, 7},
			{"room for whole keys", 0, 40},
			{"room is not a multiple of 4", 6, 40},
			{"room is under one key", 14, 40},
			{"empty", 0, 0},
		} {
			out := &fakeIOWriter{}
			w := bufio.NewWriterSize(out, 16)
			prefix := bytes.Repeat([]byte{'.'}, testCase.buffered)
			w.Write(prefix)
			payload := make([]byte, testCase.payloadLength)
			for i := range payload {
				payload[i] = byte(i*7 + 1)
			}
			plain := bytes.Clone(payload)

			if err := writeFramePayload(&Frame{Mask: true, MaskingKey: key, PayloadData: payload}, w); err != nil {
				t.Fatalf("%s: writeFramePayload: %v", testCase.name, err)
			}
			w.Flush()
			want := bytes.Clone(prefix)
			for i := range plain {
				want = append(want, plain[i]^key[i&3])
			}
			if !bytes.Equal(out.Bytes(), want) {
				t.Errorf("%s: wrote % x, want % x", testCase.name, out.Bytes(), want)
			}
			if !bytes.Equal(payload, plain) {
				t.Errorf("%s: PayloadData was modified", testCase.name)
			}
		}
	})

	t.Run("returns a masked flush error", func(t *testing.T) {
		wantErr := errors.New("socket gone")
		w := bufio.NewWriterSize(&fakeIOWriter{err: wantErr}, 16)
		w.WriteString("..............") // 2 bytes left: under one key, so it flushes

		f := &Frame{Mask: true, MaskingKey: key, PayloadData: make([]byte, 40)}
		if err := writeFramePayload(f, w); !errors.Is(err, wantErr) {
			t.Errorf("writeFramePayload = %v, want %v", err, wantErr)
		}
	})

	// bufio keeps the first error, so a writer whose flush failed refuses every
	// later Write, even one that fits.
	t.Run("returns a masked write error", func(t *testing.T) {
		wantErr := errors.New("socket gone")
		w := bufio.NewWriterSize(&fakeIOWriter{err: wantErr}, 16)
		w.WriteString("ab")
		w.Flush() // fails, and the writer keeps wantErr

		f := &Frame{Mask: true, MaskingKey: key, PayloadData: make([]byte, 4)} // fits the free space
		if err := writeFramePayload(f, w); !errors.Is(err, wantErr) {
			t.Errorf("writeFramePayload = %v, want %v", err, wantErr)
		}
	})

	t.Run("a masked payload allocates nothing", func(t *testing.T) {
		w := bufio.NewWriterSize(io.Discard, 16)
		f := &Frame{Mask: true, MaskingKey: key, PayloadData: make([]byte, 100)}
		if allocs := testing.AllocsPerRun(10, func() { writeFramePayload(f, w) }); allocs != 0 {
			t.Errorf("allocs = %v, want 0", allocs)
		}
	})
}

func TestConnSendFrame(t *testing.T) {
	var steps []string
	netConn := newFakeConn(nil)
	netConn.onWrite = func() { steps = append(steps, "write") }
	wsConn, _ := NewConn(netConn, bufio.NewReader(netConn), bufio.NewWriter(netConn), false)
	wsConn.di.writeLocker = fakeFuncLocker{
		lock:   func() { steps = append(steps, "write lock") },
		unlock: func() { steps = append(steps, "write unlock") },
	}

	if err := wsConn.SendFrame(NewFrame(NewFrameConfig{Opcode: OpcodeText, FIN: true})); err != nil {
		t.Fatalf("SendFrame: %v", err)
	}
	if want := []string{"write lock", "write", "write unlock"}; !slices.Equal(steps, want) {
		t.Errorf("steps %q, want %q", steps, want)
	}
}

func TestSendFrame(t *testing.T) {
	// base_di has a real Conn with no close sent, and steps that succeed; each
	// case overrides what it looks at.
	base_di := func() sendFrameDI {
		netConn := newFakeConn(nil)
		conn, _ := NewConn(netConn, bufio.NewReader(netConn), bufio.NewWriter(netConn), false)
		return sendFrameDI{
			conn:               conn,
			bufferedWriteFrame: func(f *Frame) error { return nil },
			flushWriter:        func() error { return nil },
		}
	}

	t.Run("writes the frame, then flushes", func(t *testing.T) {
		f := &Frame{Opcode: OpcodeText, FIN: true}
		var steps []string
		di := base_di()
		di.bufferedWriteFrame = func(got *Frame) error {
			if got != f {
				t.Error("bufferedWriteFrame was given another frame")
			}
			steps = append(steps, "write")
			return nil
		}
		di.flushWriter = func() error {
			steps = append(steps, "flush")
			return nil
		}

		if err := sendFrame(f, di); err != nil {
			t.Fatalf("sendFrame: %v", err)
		}
		if !slices.Equal(steps, []string{"write", "flush"}) {
			t.Errorf("steps %q, want [write flush]", steps)
		}
	})

	// 5.5.1: one close each way, counted once it is on the wire.
	t.Run("a close sets closeSent after the flush", func(t *testing.T) {
		di := base_di()
		di.flushWriter = func() error {
			if di.conn.closeSent {
				t.Error("closeSent was set before the flush")
			}
			return nil
		}

		if err := sendFrame(&Frame{Opcode: OpcodeClose, FIN: true}, di); err != nil {
			t.Fatalf("sendFrame: %v", err)
		}
		if !di.conn.closeSent {
			t.Error("closeSent was not set")
		}
	})

	t.Run("no other opcode sets closeSent", func(t *testing.T) {
		for _, opcode := range []uint8{OpcodeContinuation, OpcodeText, OpcodeBinary, OpcodePing, OpcodePong} {
			di := base_di()

			if err := sendFrame(&Frame{Opcode: opcode, FIN: true}, di); err != nil {
				t.Fatalf("opcode %#x: sendFrame: %v", opcode, err)
			}
			if di.conn.closeSent {
				t.Errorf("opcode %#x set closeSent", opcode)
			}
		}
	})

	t.Run("stops at a write error", func(t *testing.T) {
		wantErr := errors.New("refused")
		di := base_di()
		di.bufferedWriteFrame = func(*Frame) error { return wantErr }
		di.flushWriter = func() error {
			t.Error("flushed after a write error")
			return nil
		}

		if err := sendFrame(&Frame{Opcode: OpcodeClose, FIN: true}, di); !errors.Is(err, wantErr) {
			t.Errorf("sendFrame = %v, want %v", err, wantErr)
		}
		if di.conn.closeSent {
			t.Error("a close that was not written set closeSent")
		}
	})

	t.Run("a flush error leaves closeSent unset", func(t *testing.T) {
		wantErr := errors.New("socket gone")
		di := base_di()
		di.flushWriter = func() error { return wantErr }

		if err := sendFrame(&Frame{Opcode: OpcodeClose, FIN: true}, di); !errors.Is(err, wantErr) {
			t.Errorf("sendFrame = %v, want %v", err, wantErr)
		}
		if di.conn.closeSent {
			t.Error("a close that was not flushed set closeSent")
		}
	})
}

// The flush and the swap both happen under writeLocker: the unlock records
// whether the writer was swapped by then.
func TestRenewWriter(t *testing.T) {
	var steps []string
	var netConn *fakeConn
	var newWriter *bufio.Writer
	base_di := func() renewWriterDI {
		steps = nil
		netConn = newFakeConn(nil)
		netConn.onWrite = func() { steps = append(steps, "write") }
		newWriter = bufio.NewWriterSize(newFakeConn(nil), 100)
		conn, _ := NewConn(netConn, bufio.NewReader(netConn), bufio.NewWriter(netConn), false)
		conn.di.writeLocker = fakeFuncLocker{
			lock: func() { steps = append(steps, "write lock") },
			unlock: func() {
				steps = append(steps, fmt.Sprintf("write unlock swapped=%v", conn.writer == newWriter))
			},
		}
		conn.writer.WriteString("abc")
		return renewWriterDI{
			conn: conn,
			fitWriter: func(c net.Conn, w *bufio.Writer) (*bufio.Writer, error) {
				steps = append(steps, "fit")
				return w, nil
			},
		}
	}

	t.Run("flushes the old writer, then takes the fitted one", func(t *testing.T) {
		di := base_di()
		var gotConn net.Conn
		var gotWriter *bufio.Writer
		di.fitWriter = func(c net.Conn, w *bufio.Writer) (*bufio.Writer, error) {
			steps = append(steps, "fit")
			gotConn, gotWriter = c, w
			return w, nil
		}

		if err := renewWriter(newWriter, di); err != nil {
			t.Fatalf("renewWriter: %v", err)
		}
		if want := []string{"write lock", "write", "fit", "write unlock swapped=true"}; !slices.Equal(steps, want) {
			t.Errorf("steps %q, want %q", steps, want)
		}
		if string(netConn.written()) != "abc" {
			t.Errorf("wrote %q, want the buffered abc flushed", netConn.written())
		}
		if gotConn != net.Conn(netConn) || gotWriter != newWriter {
			t.Error("fitWriter did not get the Conn's net.Conn and the new writer")
		}
	})

	t.Run("a failed flush keeps the old writer", func(t *testing.T) {
		wantErr := errors.New("socket gone")
		di := base_di()
		netConn.writeErr = wantErr
		oldWriter := di.conn.writer

		if err := renewWriter(newWriter, di); !errors.Is(err, wantErr) {
			t.Errorf("renewWriter = %v, want %v", err, wantErr)
		}
		if want := []string{"write lock", "write", "write unlock swapped=false"}; !slices.Equal(steps, want) {
			t.Errorf("steps %q, want %q", steps, want)
		}
		if di.conn.writer != oldWriter {
			t.Error("the writer was replaced after a failed flush")
		}
	})

	t.Run("a failed fit keeps the old writer", func(t *testing.T) {
		wantErr := errors.New("small writer flush failed")
		di := base_di()
		di.fitWriter = func(c net.Conn, w *bufio.Writer) (*bufio.Writer, error) {
			steps = append(steps, "fit")
			return bufio.NewWriterSize(c, minWriterSize), wantErr
		}
		oldWriter := di.conn.writer

		if err := renewWriter(newWriter, di); !errors.Is(err, wantErr) {
			t.Errorf("renewWriter = %v, want %v", err, wantErr)
		}
		if want := []string{"write lock", "write", "fit", "write unlock swapped=false"}; !slices.Equal(steps, want) {
			t.Errorf("steps %q, want %q", steps, want)
		}
		if di.conn.writer != oldWriter {
			t.Error("the writer was replaced after a failed fit")
		}
	})
}

func TestFitWriter(t *testing.T) {
	t.Run("a writer of 14 bytes or more is kept, nothing flushed", func(t *testing.T) {
		for _, size := range []int{14, 4096} {
			netConn := newFakeConn(nil)
			w := bufio.NewWriterSize(netConn, size)
			w.WriteString("abc")

			got, err := fitWriter(netConn, w)
			if err != nil || got != w {
				t.Errorf("size %d: fitWriter = %p, %v, want w, nil", size, got, err)
			}
			if len(netConn.written()) != 0 {
				t.Errorf("size %d: wrote %q, want nothing flushed", size, netConn.written())
			}
		}
	})

	t.Run("a smaller writer is flushed and replaced by a 14 byte writer on c", func(t *testing.T) {
		oldConn := newFakeConn(nil)
		w := bufio.NewWriterSize(oldConn, 5)
		w.WriteString("abc")
		netConn := newFakeConn(nil)

		got, err := fitWriter(netConn, w)
		if err != nil {
			t.Fatalf("fitWriter: %v", err)
		}
		if string(oldConn.written()) != "abc" {
			t.Errorf("small writer wrote %q, want abc flushed", oldConn.written())
		}
		if got == w || got.Size() != minWriterSize {
			t.Errorf("got a writer of size %d, want a new one of %d", got.Size(), minWriterSize)
		}
		got.WriteString("x")
		got.Flush()
		if string(netConn.written()) != "x" {
			t.Errorf("new writer wrote %q to c, want x", netConn.written())
		}
	})

	t.Run("a failed flush is returned with the new writer", func(t *testing.T) {
		wantErr := errors.New("socket gone")
		oldConn := newFakeConn(nil)
		oldConn.writeErr = wantErr
		w := bufio.NewWriterSize(oldConn, 5)
		w.WriteString("abc")

		got, err := fitWriter(newFakeConn(nil), w)
		if !errors.Is(err, wantErr) {
			t.Errorf("fitWriter error = %v, want %v", err, wantErr)
		}
		if got == nil || got == w || got.Size() != minWriterSize {
			t.Error("a failed flush did not return a new 14 byte writer")
		}
	})
}
