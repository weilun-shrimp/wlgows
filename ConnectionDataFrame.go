package wlgows

import "unicode/utf8"

/*
SendText sends text as one unfragmented text message (OpcodeText, FIN set).

text must be valid UTF-8 or it is refused with ErrInvalidUTF8, before the lock
and before anything reaches the socket. RFC 6455 5.6 defines a text payload as
UTF-8 and 8.1 has the peer fail the connection over it, so sending invalid bytes
buys a close 1007 from the far end and a dead connection you cannot diagnose
locally. There is no reason to want it either: arbitrary bytes are what binary
is for, and a conformance test that needs malformed text can build the frame and
use SendFrame.

dataFramesWriteLocker is held for the call, so a message from another goroutine
cannot interleave with this one (5.4). A control frame still can, which is what
5.4 permits and 5.5.2 needs.

Refused with ErrCloseAlreadySent once a close has gone out: 5.5.1 allows no data
frame after one, and nothing was written.
*/
func (c *Conn) SendText(text []byte) error {
	if !utf8.Valid(text) {
		return ErrInvalidUTF8
	}
	c.di.dataFramesWriteLocker.Lock()
	defer c.di.dataFramesWriteLocker.Unlock()

	f, err := c.di.newDataFrame(NewFrameConfig{
		Opcode: OpcodeText, Mask: c.maskSendFrame, PayloadData: text, FIN: true,
	})
	if err != nil {
		return err
	}

	c.di.writeLocker.Lock()
	defer c.di.writeLocker.Unlock()

	// 5.5.1: no data frame after a close has gone out. Checked under the lock
	// that sets it, the way SendClose does, so the two cannot cross.
	if c.closeSent {
		return ErrCloseAlreadySent
	}
	return c.sendFrame(f)
}

/*
SendBinary sends data as one unfragmented binary message (OpcodeBinary, FIN set).

Nothing about the payload is checked. RFC 6455 5.6 gives binary no encoding at
all — the bytes mean whatever the two ends agreed — which is the whole
difference from SendText, and why anything that is not text belongs here rather
than in a text frame the peer will reject.

dataFramesWriteLocker is held for the call, so a message from another goroutine
cannot interleave with this one (5.4). A control frame still can, which is what
5.4 permits and 5.5.2 needs.

Refused with ErrCloseAlreadySent once a close has gone out: 5.5.1 allows no data
frame after one, and nothing was written.
*/
func (c *Conn) SendBinary(data []byte) error {
	c.di.dataFramesWriteLocker.Lock()
	defer c.di.dataFramesWriteLocker.Unlock()

	f, err := c.di.newDataFrame(NewFrameConfig{
		Opcode: OpcodeBinary, Mask: c.maskSendFrame, PayloadData: data, FIN: true,
	})
	if err != nil {
		return err
	}

	c.di.writeLocker.Lock()
	defer c.di.writeLocker.Unlock()

	// 5.5.1: no data frame after a close has gone out. Checked under the lock
	// that sets it, the way SendClose does, so the two cannot cross.
	if c.closeSent {
		return ErrCloseAlreadySent
	}
	return c.sendFrame(f)
}
