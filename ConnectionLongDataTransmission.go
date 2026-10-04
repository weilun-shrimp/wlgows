package wlgows

/*
SendText and SendBinary for a message too large to hold in memory. Pass it in
chunks and it goes out as one message, fragment by fragment.

	if err := conn.StartLongDataTransmission(wlgows.OpcodeText); err != nil {
		return err
	}
	defer conn.ReleaseLongDataTransmission()

	for chunk := range chunks {
		if err := conn.TransmitData(chunk); err != nil {
			return err // no FIN: the message is left unterminated
		}
	}
	return conn.EndLongDataTransmission(nil)

RFC 6455 5.4 is kept for you: the opcode goes on the first frame and
OpcodeContinuation on every frame after, and no other message interleaves. A
chunk is one frame, so pick a size — 4 KB is a fair default — rather than
passing a byte at a time.

Start and Release pair like Lock and Unlock: always defer Release. End only
sends the last frame, carrying FIN: the final chunk if you pass it, or an empty
continuation frame for nil. Returning before End leaves the message
unterminated, so close the connection; calling End after a failure instead
delivers what was sent as a complete message. Nothing is held between calls, so
the buffer you read into is yours to reuse. For a message already in memory, use
SendData.

Two rules stay yours. A text transmission must be valid UTF-8, which is not
checked here as it is in SendText, and a peer may refuse a message larger than
its own limit.

Control frames from other goroutines still get through while this runs, which
5.4 permits and 5.5.2 wants. Call all four from one goroutine; ordering between
whole messages is yours.

A close ends the transmission wherever it lands. 5.5.1 allows no data frame after
one, and a stream is the case where that matters — minutes of chunks against a
peer that stopped reading them — so all three refuse with ErrCloseAlreadySent
once a close has gone out: Start opens nothing, Transmit sends nothing, and End
skips the terminating frame. A message cut off that way is left unterminated on
the wire, which is what the close already told the peer.
*/

/*
StartLongDataTransmission opens a message and claims the connection for it,
blocking until other data frame senders are done.

opcode must be OpcodeText or OpcodeBinary. Once a close has gone out there is no
message left to open, so this is refused with ErrCloseAlreadySent (5.5.1).

On an error nothing was locked, so defer ReleaseLongDataTransmission only after
this returns nil.
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

type transmitDataDI struct {
	conn      *Conn
	sendFrame func(f *Frame) error
}

/*
TransmitData adds one fragment, sealed and written before it returns. Empty data
is dropped — a zero length fragment adds nothing to the message 5.4 defines as
the concatenation of its fragments.

data is not retained, so the buffer you read into can be reused straight away.
Not after End: the message is over.
*/
func (c *Conn) TransmitData(data []byte) error {
	return transmitData(data, transmitDataDI{
		conn:      c,
		sendFrame: c.sendFrame,
	})
}

func transmitData(data []byte, di transmitDataDI) error {
	if di.conn.currentTransmitDataMsgOpcode == 0 {
		return ErrLongDataTransmissionNotStarted
	}
	if len(data) == 0 {
		return nil // Useless action.
	}

	// 5.4: the message's own opcode opens it, every frame after continues it.
	opcode := di.conn.currentTransmitDataMsgOpcode
	if di.conn.currentTransmitDataMsgOpened {
		opcode = OpcodeContinuation
	}

	f, err := di.conn.di.newDataFrame(NewFrameConfig{
		Opcode: opcode, PayloadData: data, FIN: false,
	})
	if err != nil {
		return err
	}

	di.conn.di.writeLocker.Lock()
	defer di.conn.di.writeLocker.Unlock()

	if err := di.sendFrame(f); err != nil {
		return err
	}

	di.conn.currentTransmitDataMsgOpened = true
	return nil
}

/*
EndLongDataTransmission sends data as the last frame, carrying FIN (5.4). It
does not release the connection; Release does.

With nothing transmitted before it, data is the whole message, which may be
empty. data is not retained.

Call it once. The message is over after it, even when it fails, so only
Release is left. After a close it sends nothing and returns ErrCloseAlreadySent.
*/
func (c *Conn) EndLongDataTransmission(data []byte) error {
	return endLongDataTransmission(data, endLongDataTransmissionDI{
		conn:      c,
		sendFrame: c.sendFrame,
	})
}

type endLongDataTransmissionDI struct {
	conn      *Conn
	sendFrame func(f *Frame) error
}

func endLongDataTransmission(data []byte, di endLongDataTransmissionDI) error {
	if di.conn.currentTransmitDataMsgOpcode == 0 {
		return ErrLongDataTransmissionNotStarted
	}

	// Nothing sent yet: this frame is the whole message, with its own opcode.
	opcode := di.conn.currentTransmitDataMsgOpcode
	if di.conn.currentTransmitDataMsgOpened {
		opcode = OpcodeContinuation
	}

	f, err := di.conn.di.newDataFrame(NewFrameConfig{
		Opcode: opcode, PayloadData: data, FIN: true,
	})
	if err != nil {
		return err
	}

	di.conn.di.writeLocker.Lock()
	defer di.conn.di.writeLocker.Unlock()

	return di.sendFrame(f)
}

/*
ReleaseLongDataTransmission ends the transmission and frees the connection,
sending nothing. Defer it once, right after Start returns nil — never without a
Start, which would unlock what was never locked.

Before End, the message is left unterminated: close the connection.
*/
func (c *Conn) ReleaseLongDataTransmission() {
	c.currentTransmitDataMsgOpcode = 0
	c.currentTransmitDataMsgOpened = false
	c.di.dataFramesWriteLocker.Unlock()
}
