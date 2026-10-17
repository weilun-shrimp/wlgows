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
func (c *Conn) SendText(text []byte, config SendDataConfig) error {
	return sendText(text, config, sendTextDI{
		sendData: c.SendData,
	})
}

type sendTextDI struct {
	sendData func(opcode uint8, payload []byte, config SendDataConfig) error
}

func sendText(text []byte, config SendDataConfig, di sendTextDI) error {
	if !utf8.Valid(text) {
		return ErrInvalidUTF8
	}
	return di.sendData(OpcodeText, text, config)
}

/*
SendBinary sends data as one binary message, through SendData. Nothing is
checked: 5.6 gives binary no encoding.
*/
func (c *Conn) SendBinary(data []byte, config SendDataConfig) error {
	return sendBinary(data, config, sendBinaryDI{
		sendData: c.SendData,
	})
}

type sendBinaryDI struct {
	sendData func(opcode uint8, payload []byte, config SendDataConfig) error
}

func sendBinary(data []byte, config SendDataConfig, di sendBinaryDI) error {
	return di.sendData(OpcodeBinary, data, config)
}

// SendDataConfig is how SendData, SendText and SendBinary send one message.
type SendDataConfig struct {
	// ChunkSize is the payload of each frame, header not counted. 0 or less
	// sends one frame. A deflated message is cut after deflating.
	ChunkSize int

	// DeflateIfAgreed deflates the message when permessage-deflate was
	// agreed in the handshake. Without the agreement it is sent as is
	// (RFC 7692 6: the sender chooses for each message).
	DeflateIfAgreed bool

	// DeflateLevel is the compress/flate level, from flate.HuffmanOnly (-2)
	// to flate.BestCompression (9), used as it is: 0 is flate.NoCompression.
	// It may change from one message to the next. Another value is refused,
	// even when nothing would be deflated.
	DeflateLevel int
}

/*
SendData sends payload as one message of opcode, OpcodeText or OpcodeBinary.
With OpcodeText, payload must be valid UTF-8 (5.6); unlike SendText, nothing is
checked.

config.ChunkSize of 0 or less sends one frame. A positive one is the payload of
each frame, header not counted, with FIN on the last (5.4). An empty payload is
one empty frame, or one frame of the byte 00 when deflated (RFC 7692 7.2.3.6).

With config.DeflateIfAgreed and permessage-deflate agreed, the whole payload is
deflated first, and RSV1 is set on the first frame (RFC 7692 6).

A DeflateLevel compress/flate has no level for is refused with
ErrDeflateLevelInvalid before anything is sent, even when nothing would be
deflated.

dataFramesWriteLocker is held throughout, so no other message interleaves.
writeLocker is held for each frame appended to the write buffer and for the
final flush, so a control frame sent in between still gets out (5.4) — and its
send flushes the frames buffered so far ahead of it, which 5.4 allows. DataFrames go
out as the buffer fills, so small frames share a Write. Once a close has gone
out, the rest is refused with ErrCloseAlreadySent.

A failure leaves the message unterminated, as 5.4 has no way to end it early:
close the connection.
*/
func (c *Conn) SendData(opcode uint8, payload []byte, config SendDataConfig) error {
	return sendData(opcode, payload, config, sendDataDI{
		dataFramesWriteLocker: c.di.dataFramesWriteLocker,
		writeLocker:           c.di.writeLocker,
		newDataFrame:          c.di.newDataFrame,
		bufferedWriteFrame:    c.bufferedWriteFrame,
		// Looked up at flush time, not bound now: RenewWriter may replace
		// c.writer between frames.
		flushWriter:         func() error { return c.writer.Flush() },
		peerIsClient:        c.peerIsClient,
		deflateAgreedConfig: c.permessageDeflateAgreedConfig,
		contextDeflater:     c.contextDeflater,
		noContextDeflater:   c.noContextDeflater,
	})
}

type sendDataDeflater interface {
	deflate(data []byte, level int) ([]byte, error)
}

type sendDataDI struct {
	dataFramesWriteLocker sync.Locker
	writeLocker           sync.Locker
	newDataFrame          func(config NewFrameConfig) (*Frame, error)
	bufferedWriteFrame    func(f *Frame) error
	flushWriter           func() error

	peerIsClient        bool
	deflateAgreedConfig *PermessageDeflateConfig
	contextDeflater     sendDataDeflater
	noContextDeflater   sendDataDeflater
}

func sendData(opcode uint8, payload []byte, config SendDataConfig, di sendDataDI) error {
	if opcode == OpcodeContinuation {
		return ErrContinuationFrameWithoutMsg
	}
	if err := validateDeflateLevel(config.DeflateLevel); err != nil {
		return err
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

	if config.DeflateIfAgreed && di.deflateAgreedConfig != nil {
		// RFC 7692 7.1.1: server_no_context_takeover binds a server,
		// client_no_context_takeover a client.
		no_context := di.deflateAgreedConfig.ClientNoContextTakeover
		if di.peerIsClient {
			no_context = di.deflateAgreedConfig.ServerNoContextTakeover
		}
		var deflater sendDataDeflater = di.contextDeflater
		if no_context {
			deflater = di.noContextDeflater
		}

		deflated, err := deflater.deflate(payload, config.DeflateLevel)
		if err != nil {
			return err
		}
		// payload is that result from here on. Its memory stays in the
		// deflater's result buffer, see ReleaseDeflaterResultBuffer.
		// RFC 7692 7.2.1: the trailing 00 00 ff ff of the flush is not sent.
		payload = deflated[:len(deflated)-4]
		f.RSV1 = true // RFC 7692 6: the first frame marks the message compressed
	}

	// Buffer every frame, then flush what is left.
	// 5.4: only the first carries the opcode. RFC 7692 6: and RSV1.
	for fin := false; !fin; f.Opcode, f.RSV1 = OpcodeContinuation, false {
		chunk := payload
		fin = config.ChunkSize <= 0 || len(payload) <= config.ChunkSize
		if !fin {
			chunk, payload = payload[:config.ChunkSize], payload[config.ChunkSize:]
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
