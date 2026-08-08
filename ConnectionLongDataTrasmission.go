package wlgows

/*
SendText and SendBinary for a message too large to hold in memory. Pass it in
chunks and it goes out as one message, fragment by fragment.

	if err := conn.StartLongDataTransmission(wlgows.OpcodeText); err != nil {
		return err
	}
	defer conn.EndLongDataTransmission()

	for chunk := range chunks {
		if err := conn.TransmitData(chunk); err != nil {
			return err
		}
	}

RFC 6455 5.4 is kept for you: the opcode goes on the first frame and
OpcodeContinuation on every frame after, FIN lands on the last one, and no other
message interleaves. A chunk is one frame, so pick a size — 32 KB is a fair
default — rather than passing a byte at a time.

Two rules stay yours. A text transmission must be valid UTF-8, which is not
checked here as it is in SendText, and a peer may refuse a message larger than
its own limit.

Control frames from other goroutines still get through while this runs, which
5.4 permits and 5.5.2 wants. Call all three from one goroutine; ordering between
whole messages is yours.
*/

/*
StartLongDataTransmission opens a message and claims the connection for it,
blocking until other data frame senders are done.

opcode must be OpcodeText or OpcodeBinary. On an error nothing was locked, so
defer EndLongDataTransmission only after this returns nil.
*/
func (c *Conn) StartLongDataTransmission(opcode uint8) error {
	if opcode == OpcodeContinuation {
		return ErrContinuationFrameWithoutMsg
	}
	if !IsDataOpcode(opcode) {
		return ErrNotDataFrameOpcode
	}

	c.di.dataFramesWriteLocker.Lock()
	c.currentTransmitDataMsgOpcode = opcode
	return nil
}

/*
TransmitData adds one fragment. Empty data is dropped — a zero length fragment
adds nothing to the message 5.4 defines as the concatenation of its fragments.

What reaches the socket is the fragment before this one, held back so End has a
frame to set FIN on. An error here means an earlier fragment failed to send.
*/
func (c *Conn) TransmitData(data []byte) error {
	if c.currentTransmitDataMsgOpcode == 0 {
		return ErrLongDataTransmissionNotStarted
	}
	if len(data) == 0 {
		return nil // Useless action.
	}

	// 5.4: the message's own opcode opens it, every frame after continues it.
	opcode := c.currentTransmitDataMsgOpcode
	if c.currentTransmitDataFrame != nil {
		opcode = OpcodeContinuation
	}

	f, err := c.di.newDataFrame(NewFrameConfig{
		Opcode: opcode, Mask: c.maskSendFrame, PayloadData: data, FIN: false,
	})
	if err != nil {
		return err
	}

	// Flush the one held back, now that it is known not to be the last.
	if c.currentTransmitDataFrame != nil {
		if err := c.SendFrame(c.currentTransmitDataFrame); err != nil {
			return err
		}
	}

	c.currentTransmitDataFrame = f
	return nil
}

/*
EndLongDataTransmission sends the held back fragment with FIN set, which ends
the message (5.4), and releases the connection.

The lock is released even when the write fails, so a deferred call always frees
it.
*/
func (c *Conn) EndLongDataTransmission() error {
	if c.currentTransmitDataMsgOpcode == 0 {
		return ErrLongDataTransmissionNotStarted
	}

	defer func() {
		c.currentTransmitDataFrame = nil
		c.currentTransmitDataMsgOpcode = 0
		c.di.dataFramesWriteLocker.Unlock()
	}()

	if c.currentTransmitDataFrame == nil {
		return nil // Nothing was sent, so no message is open on the wire.
	}

	c.currentTransmitDataFrame.FIN = true
	return c.SendFrame(c.currentTransmitDataFrame)
}
