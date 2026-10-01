package wlgows

import "unicode/utf8"

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

It runs a long data transmission, the last chunk going to End, so locking and
ErrCloseAlreadySent work as they do there. The write buffer grows to one frame.

A chunk that fails leaves the message unterminated, as 5.4 has no way to end it
early: close the connection.
*/
func (c *Conn) SendData(opcode uint8, payload []byte, chunkSize int) error {
	return sendData(opcode, payload, chunkSize, sendDataDI{
		startLongDataTransmission:   c.StartLongDataTransmission,
		transmitData:                c.TransmitData,
		endLongDataTransmission:     c.EndLongDataTransmission,
		releaseLongDataTransmission: c.ReleaseLongDataTransmission,
	})
}

type sendDataDI struct {
	startLongDataTransmission   func(opcode uint8) error
	transmitData                func(data []byte) error
	endLongDataTransmission     func(data []byte) error
	releaseLongDataTransmission func()
}

func sendData(opcode uint8, payload []byte, chunkSize int, di sendDataDI) error {
	if err := di.startLongDataTransmission(opcode); err != nil {
		return err // Nothing was locked.
	}
	defer di.releaseLongDataTransmission()

	if chunkSize > 0 {
		for len(payload) > chunkSize {
			if err := di.transmitData(payload[:chunkSize]); err != nil {
				return err // No FIN: the message is missing chunks.
			}
			payload = payload[chunkSize:]
		}
	}
	return di.endLongDataTransmission(payload) // The last chunk, or the whole message.
}
