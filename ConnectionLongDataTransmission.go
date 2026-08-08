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
OpcodeContinuation on every frame after, and no other message interleaves. A
chunk is one frame, so pick a size — 32 KB is a fair default — rather than
passing a byte at a time.

End terminates the message with an empty continuation frame carrying FIN. 5.4
puts FIN on the last frame and says nothing about its length, so an empty one
costs six bytes and lets every chunk go out the moment you pass it. Nothing is
held between calls, so the buffer you read into is yours to reuse.

Two rules stay yours. A text transmission must be valid UTF-8, which is not
checked here as it is in SendText, and a peer may refuse a message larger than
its own limit.

Control frames from other goroutines still get through while this runs, which
5.4 permits and 5.5.2 wants. Call all three from one goroutine; ordering between
whole messages is yours.

A close ends the transmission wherever it lands. 5.5.1 allows no data frame after
one, and a stream is the case where that matters — minutes of chunks against a
peer that stopped reading them — so all three refuse with ErrCloseAlreadySent
once SendClose has gone out: Start opens nothing, Transmit sends nothing, and End
skips the terminating frame while still releasing the connection. A message cut
off that way is left unterminated on the wire, which is what the close already
told the peer.
*/

/*
StartLongDataTransmission opens a message and claims the connection for it,
blocking until other data frame senders are done.

opcode must be OpcodeText or OpcodeBinary. Once a close has gone out there is no
message left to open, so this is refused with ErrCloseAlreadySent (5.5.1).

On an error nothing was locked, so defer EndLongDataTransmission only after this
returns nil.
*/
func (c *Conn) StartLongDataTransmission(opcode uint8) error {
	if opcode == OpcodeContinuation {
		return ErrContinuationFrameWithoutMsg
	}
	if !IsDataOpcode(opcode) {
		return ErrNotDataFrameOpcode
	}
	// Read under the lock that sets it, then released — not deferred. The next
	// line waits for dataFramesWriteLocker, which another transmission can hold
	// for minutes, and that transmission's TransmitData wants writeLocker for
	// every fragment: holding it here would have the two wait on each other.
	c.di.writeLocker.Lock()
	closeSent := c.closeSent
	c.di.writeLocker.Unlock()

	if closeSent {
		return ErrCloseAlreadySent
	}

	c.di.dataFramesWriteLocker.Lock()
	c.currentTransmitDataMsgOpcode = opcode
	return nil
}

/*
TransmitData adds one fragment, sealed and written before it returns. Empty data
is dropped — a zero length fragment adds nothing to the message 5.4 defines as
the concatenation of its fragments.

data is not retained, so the buffer you read into can be reused straight away.
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
	if c.currentTransmitDataMsgOpened {
		opcode = OpcodeContinuation
	}

	f, err := c.di.newDataFrame(NewFrameConfig{
		Opcode: opcode, Mask: c.maskSendFrame, PayloadData: data, FIN: false,
	})
	if err != nil {
		return err
	}

	c.di.writeLocker.Lock()
	defer c.di.writeLocker.Unlock()

	// Checked per fragment, not once at Start: a close arriving mid stream is
	// the ordinary case, and every chunk after it is a frame 5.5.1 forbids.
	if c.closeSent {
		return ErrCloseAlreadySent
	}
	if err := c.sendFrame(f); err != nil {
		return err
	}

	c.currentTransmitDataMsgOpened = true
	return nil
}

/*
EndLongDataTransmission terminates the message with an empty continuation frame
carrying FIN, which is what tells the peer it is complete (5.4), and releases the
connection.

A transmission that never sent a fragment opened no message on the wire, so
nothing is terminated and only the lock is released. So does a close having gone
out, which leaves the message unterminated and returns ErrCloseAlreadySent.

The lock is released whatever happens, so a deferred call always frees it.
*/
func (c *Conn) EndLongDataTransmission() error {
	if c.currentTransmitDataMsgOpcode == 0 {
		return ErrLongDataTransmissionNotStarted
	}

	defer func() {
		c.currentTransmitDataMsgOpcode = 0
		c.currentTransmitDataMsgOpened = false
		c.di.dataFramesWriteLocker.Unlock()
	}()

	if !c.currentTransmitDataMsgOpened {
		return nil // Nothing was sent, so no message is open on the wire.
	}

	f, err := c.di.newDataFrame(NewFrameConfig{
		Opcode: OpcodeContinuation, Mask: c.maskSendFrame, FIN: true,
	})
	if err != nil {
		return err
	}

	c.di.writeLocker.Lock()
	defer c.di.writeLocker.Unlock()

	// The FIN frame is a data frame too, so a close leaves the message
	// unterminated. The deferred release above runs either way.
	if c.closeSent {
		return ErrCloseAlreadySent
	}
	return c.sendFrame(f)
}
