package wlgows

import (
	"sync"
	"unicode/utf8"
)

/*
SendText sends text as one text message, through SendData.

text must be valid UTF-8 (5.6) or it is refused with ErrInvalidUTF8 before
anything is sent; a peer would fail the connection over it (8.1). It is checked
on the whole message, so a chunk boundary inside a rune is fine.
*/
func (c *Conn) SendText(text []byte, chunkSize int) error {
	return sendText(text, chunkSize, sendTextDI{
		sendData: c.SendData,
	})
}

type sendTextDI struct {
	sendData func(opcode uint8, payload []byte, chunkSize int) error
}

func sendText(text []byte, chunkSize int, di sendTextDI) error {
	if !utf8.Valid(text) {
		return ErrInvalidUTF8
	}
	return di.sendData(OpcodeText, text, chunkSize)
}

/*
SendBinary sends data as one binary message, through SendData. Nothing is
checked: 5.6 gives binary no encoding.
*/
func (c *Conn) SendBinary(data []byte, chunkSize int) error {
	return sendBinary(data, chunkSize, sendBinaryDI{
		sendData: c.SendData,
	})
}

type sendBinaryDI struct {
	sendData func(opcode uint8, payload []byte, chunkSize int) error
}

func sendBinary(data []byte, chunkSize int, di sendBinaryDI) error {
	return di.sendData(OpcodeBinary, data, chunkSize)
}

/*
SendData sends payload as one message of opcode, OpcodeText or OpcodeBinary.

chunkSize of 0 or less sends one frame. A positive chunkSize is the payload of
each frame, header not counted, with FIN on the last (5.4). An empty payload is
one empty frame.

dataFramesWriteLocker is held throughout, so no other message interleaves.
writeLocker is held for each frame appended to the write buffer and for the
final flush, so a control frame sent in between still gets out (5.5.2) — and its
send flushes the frames buffered so far ahead of it, which 5.4 allows. Frames go
out as the buffer fills, so small frames share a Write. Once a close has gone
out, the rest is refused with ErrCloseAlreadySent.

A failure leaves the message unterminated, as 5.4 has no way to end it early:
close the connection.
*/
func (c *Conn) SendData(opcode uint8, payload []byte, chunkSize int) error {
	return sendData(opcode, payload, chunkSize, sendDataDI{
		dataFramesWriteLocker: c.di.dataFramesWriteLocker,
		writeLocker:           c.di.writeLocker,
		newDataFrame:          c.di.newDataFrame,
		bufferedWriteFrame:    c.bufferedWriteFrame,
		// Looked up at flush time, not bound now: RenewWriter may replace
		// c.writer between frames.
		flushWriter: func() error { return c.writer.Flush() },
	})
}

type sendDataDI struct {
	dataFramesWriteLocker sync.Locker
	writeLocker           sync.Locker
	newDataFrame          func(config NewFrameConfig) (*Frame, error)
	bufferedWriteFrame    func(f *Frame) error
	flushWriter           func() error
}

func sendData(opcode uint8, payload []byte, chunkSize int, di sendDataDI) error {
	if opcode == OpcodeContinuation {
		return ErrContinuationFrameWithoutMsg
	}
	// dataFramesWriteLocker first, as a long data transmission takes them, so
	// this cannot land between its fragments (5.4).
	di.dataFramesWriteLocker.Lock()
	defer di.dataFramesWriteLocker.Unlock()

	// One frame for every chunk, built once: it also refuses a non data opcode.
	f, err := di.newDataFrame(NewFrameConfig{Opcode: opcode})
	if err != nil {
		return err
	}

	// Buffer every frame, then flush what is left.
	for fin := false; !fin; f.Opcode = OpcodeContinuation { // 5.4: only the first carries the opcode
		chunk := payload
		fin = chunkSize <= 0 || len(payload) <= chunkSize
		if !fin {
			chunk, payload = payload[:chunkSize], payload[chunkSize:]
		}
		f.FIN = fin
		f.PayloadData = chunk

		di.writeLocker.Lock()
		err = di.bufferedWriteFrame(f)
		di.writeLocker.Unlock()
		if err != nil {
			return err
		}
	}

	di.writeLocker.Lock()
	defer di.writeLocker.Unlock()
	return di.flushWriter()
}
