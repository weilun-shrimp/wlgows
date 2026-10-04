package wlgows

import (
	"bufio"
	"fmt"
	"net"
)

// bufferedWriteFrame validates f, prepares it for sending, then writes its
// header and payload through writer. What fits waits there for writer.Flush;
// the rest may go out before this returns. f.PayloadData is only read.
func (c *Conn) bufferedWriteFrame(f *Frame) error {
	return bufferedWriteFrame(f, bufferedWriteFrameDI{
		closeSent:         c.closeSent,
		maskSendFrame:     c.maskSendFrame,
		writer:            c.writer,
		validateSendFrame: validateSendFrame,
		prepareSendFrame:  prepareSendFrame,
		// Passed as a value, not closed over, so building this allocates nothing.
		prepareSendFrameDI: prepareSendFrameDI{fillMaskingKey: c.di.fillMaskingKey},
		writeFrameHeader:   writeFrameHeader,
		writeFramePayload:  writeFramePayload,
	})
}

type bufferedWriteFrameDI struct {
	closeSent          bool
	maskSendFrame      bool
	writer             *bufio.Writer
	validateSendFrame  func(f *Frame, closeSent bool) error
	prepareSendFrame   func(f *Frame, maskSendFrame bool, di prepareSendFrameDI) error
	prepareSendFrameDI prepareSendFrameDI
	writeFrameHeader   func(f *Frame, w *bufio.Writer) error
	writeFramePayload  func(f *Frame, w *bufio.Writer) error
}

func bufferedWriteFrame(f *Frame, di bufferedWriteFrameDI) error {
	if err := di.validateSendFrame(f, di.closeSent); err != nil {
		return err
	}
	if err := di.prepareSendFrame(f, di.maskSendFrame, di.prepareSendFrameDI); err != nil {
		return err
	}
	if err := di.writeFrameHeader(f, di.writer); err != nil {
		return err
	}
	return di.writeFramePayload(f, di.writer)
}

// validateSendFrame refuses a frame that must not be sent: a data frame or a
// second close after this side's close (5.5.1), or a control frame payload
// over 125 bytes (5.5).
func validateSendFrame(f *Frame, closeSent bool) error {
	if closeSent && (IsDataOpcode(f.Opcode) || f.Opcode == OpcodeClose) {
		return ErrCloseAlreadySent
	}
	if IsControlOpcode(f.Opcode) && len(f.PayloadData) > ControlFramePayloadMaxByteLength {
		return fmt.Errorf("control frame payload is %d bytes: %w",
			len(f.PayloadData), ErrControlFramePayloadTooLong)
	}
	return nil
}

type prepareSendFrameDI struct {
	fillMaskingKey func(key *[4]byte) error
}

// prepareSendFrame sets what the header carries. A client gets Mask and a
// fresh key for every frame (5.3), a server neither. The length fields follow
// len(PayloadData), and a control frame gets FIN (5.5).
func prepareSendFrame(f *Frame, maskSendFrame bool, di prepareSendFrameDI) error {
	if maskSendFrame {
		if err := di.fillMaskingKey(&f.MaskingKey); err != nil {
			return err
		}
	} else {
		f.MaskingKey = [4]byte{}
	}
	f.Mask = maskSendFrame
	f.fillPayloadLength()
	if IsControlOpcode(f.Opcode) {
		f.FIN = true
	}
	return nil
}

// writeFrameHeader seals f's header straight into w's free space, flushing
// first when it does not fit, so it never allocates.
func writeFrameHeader(f *Frame, w *bufio.Writer) error {
	if w.Available() < f.getHeaderSize() {
		if err := w.Flush(); err != nil {
			return err
		}
	}
	_, err := w.Write(f.appendSealedHeader(w.AvailableBuffer()))
	return err
}

// writeFramePayload writes f's payload through w. Unmasked, it goes as it is,
// and bufio sends a large one straight from PayloadData. Masked, each piece is
// copied into w's free space and masked there, so PayloadData is never written
// to.
func writeFramePayload(f *Frame, w *bufio.Writer) error {
	if !f.Mask {
		_, err := w.Write(f.PayloadData)
		return err
	}

	payload := f.PayloadData
	for len(payload) > 0 {
		n := min(w.Available(), len(payload))
		if n < len(payload) {
			n -= n % 4 // whole keys only, so the next piece starts on key[0]
		}
		if n == 0 {
			if err := w.Flush(); err != nil {
				return err
			}
			continue
		}
		piece := append(w.AvailableBuffer(), payload[:n]...)
		MaskPayload(piece, f.MaskingKey)
		if _, err := w.Write(piece); err != nil {
			return err
		}
		payload = payload[n:]
	}
	return nil
}

/*
SendFrame is low level: it writes the frame you built, flushed before it
returns. Prefer SendText, SendClose, SendPing or SendPong.

Yours to keep: a fragmented message's sequence (5.4) — first frame carries the
opcode, continuations 0, only the last sets FIN, nothing in between; serialise
concurrent senders yourself. The opcode and RSV bits are sent as they are.

Settled in place, so your frame is modified: Mask and a fresh MaskingKey per
send from the Conn's side (5.1, 5.3), the length fields from len(PayloadData),
and FIN on a close, ping or pong (5.5). PayloadData is never modified.

Refused with nothing written: a control payload over 125 bytes
(ErrControlFramePayloadTooLong), and a data frame or second close after this
side's close (ErrCloseAlreadySent).
*/
func (c *Conn) SendFrame(f *Frame) error {
	c.di.writeLocker.Lock()
	defer c.di.writeLocker.Unlock()

	return c.sendFrame(f)
}

// sendFrame writes f and flushes it. Call it under writeLocker.
func (c *Conn) sendFrame(f *Frame) error {
	return sendFrame(f, sendFrameDI{
		conn:               c,
		bufferedWriteFrame: c.bufferedWriteFrame,
		flushWriter:        c.writer.Flush,
	})
}

type sendFrameDI struct {
	conn               *Conn
	bufferedWriteFrame func(f *Frame) error
	flushWriter        func() error
}

func sendFrame(f *Frame, di sendFrameDI) error {
	if err := di.bufferedWriteFrame(f); err != nil {
		return err
	}

	if err := di.flushWriter(); err != nil {
		return err
	}

	// A close counts only once it is on the wire.
	if f.Opcode == OpcodeClose {
		di.conn.closeSent = true
	}
	return nil
}

/*
RenewWriter flushes the current writer, then sends every frame after it through
w, and the old one is released to the GC. w must write to the Conn, and is
taken as NewConn takes it: one smaller than 14 bytes is flushed and replaced.

The writer's size is the trade: frames that fit go out together in one Write. A
larger payload is written straight from PayloadData on a server, and a writer's
worth at a time on a client, which masks it in the buffer.

It holds writeLocker, so it waits for a frame being written to finish. A failed
flush leaves the old writer in place.
*/
func (c *Conn) RenewWriter(w *bufio.Writer) error {
	return renewWriter(w, renewWriterDI{
		conn:      c,
		fitWriter: fitWriter,
	})
}

type renewWriterDI struct {
	conn      *Conn
	fitWriter func(c net.Conn, w *bufio.Writer) (*bufio.Writer, error)
}

func renewWriter(w *bufio.Writer, di renewWriterDI) error {
	di.conn.di.writeLocker.Lock()
	defer di.conn.di.writeLocker.Unlock()

	if err := di.conn.writer.Flush(); err != nil {
		return err
	}
	w, err := di.fitWriter(di.conn.Conn, w)
	if err != nil {
		return err
	}
	di.conn.writer = w
	return nil
}

// minWriterSize is the longest header (2 + 8 extended length + 4 masking key):
// after a flush, any header fits in the writer's buffer, and so does at least
// one whole masking key.
const minWriterSize = 14

// fitWriter returns w when it holds at least minWriterSize bytes. Otherwise it
// flushes w and returns a new minWriterSize writer on c, with w's flush error.
func fitWriter(c net.Conn, w *bufio.Writer) (*bufio.Writer, error) {
	if w.Size() >= minWriterSize {
		return w, nil
	}
	return bufio.NewWriterSize(c, minWriterSize), w.Flush()
}
