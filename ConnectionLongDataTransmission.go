package wlgows

/*
SendText and SendBinary for a message too large to hold in memory. Pass it in
chunks and it goes out as one message, fragment by fragment.

	if err := conn.StartLongDataTransmission(wlgows.OpcodeText, wlgows.StartLongDataTransmissionConfig{}); err != nil {
		return err
	}
	defer conn.ReleaseLongDataTransmission()

	for chunk := range chunks {
		if err := conn.TransmitData(chunk, wlgows.TransmitDataConfig{}); err != nil {
			return err // no FIN: the message is left unterminated
		}
	}
	return conn.EndLongDataTransmission(nil, wlgows.TransmitDataConfig{})

RFC 6455 5.4 is kept for you: the opcode goes on the first frame and
OpcodeContinuation on every frame after, and no other message interleaves. A
chunk is one frame, so pick a size — 4 KB is a fair default — rather than
passing a byte at a time.

Start and Release pair like Lock and Unlock: always defer Release. End only
sends the last frame, carrying FIN: the final chunk if you pass it, or an empty
continuation frame for nil (deflated, the single byte 00). Returning before End leaves the message
unterminated, so close the connection; calling End after a failure instead
delivers what was sent as a complete message. Nothing is held between calls, so
the buffer you read into is yours to reuse. For a message already in memory, use
SendData.

Two rules stay yours. A text transmission must be valid UTF-8, which is not
checked here as it is in SendText, and a peer may refuse a message larger than
its own limit.

With StartLongDataTransmissionConfig.DeflateIfAgreed and permessage-deflate
agreed, the whole message is deflated as one stream, chunk by chunk, and RSV1
is set on its first frame (RFC 7692 6). Each chunk is sync flushed, so it is
sent whole before TransmitData returns. A failure leaves the stream broken
too: End cannot save it, so close the connection.

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

// StartLongDataTransmissionConfig is how a long data transmission is sent.
type StartLongDataTransmissionConfig struct {
	// DeflateIfAgreed deflates the message when permessage-deflate was agreed
	// (RFC 7692); when not, it is sent as is. The level is per chunk, see
	// TransmitDataConfig.
	DeflateIfAgreed bool
}

/*
StartLongDataTransmission opens a message and claims the connection for it,
blocking until other data frame senders are done. config decides whether the
message is deflated, see StartLongDataTransmissionConfig.

opcode must be OpcodeText or OpcodeBinary. Once a close has gone out there is no
message left to open, so this is refused with ErrCloseAlreadySent (5.5.1).

On an error nothing was locked, so defer ReleaseLongDataTransmission only after
this returns nil.
*/
func (c *Conn) StartLongDataTransmission(opcode uint8, config StartLongDataTransmissionConfig) error {
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
	c.currentTransmitDataMsgDeflating = config.DeflateIfAgreed && c.permessageDeflateAgreedConfig != nil
	return nil
}

// TransmitDataConfig is how one chunk of a long data transmission is sent.
type TransmitDataConfig struct {
	// DeflateLevel is the compress/flate level the chunk is deflated at, when
	// the message is deflated. 0 is flate.NoCompression. It may change from
	// chunk to chunk. A level compress/flate has not is refused with
	// ErrDeflateLevelInvalid, even when nothing would be deflated.
	DeflateLevel int
}

type transmitDataDI struct {
	conn      *Conn
	sendFrame func(f *Frame) error
}

/*
TransmitData adds one fragment, sealed and written before it returns. Empty data
is sent too, as a zero length fragment, or as the flush's bytes when deflated.

When the message is deflated, the fragment is data deflated at
config.DeflateLevel and sync flushed, so it keeps its trailing 00 00 ff ff
(RFC 7692 7.2.1): only End strips it.

data is not retained, so the buffer you read into can be reused straight away.
Not after End: the message is over.
*/
func (c *Conn) TransmitData(data []byte, config TransmitDataConfig) error {
	return transmitData(data, config, transmitDataDI{
		conn:      c,
		sendFrame: c.sendFrame,
	})
}

func transmitData(data []byte, config TransmitDataConfig, di transmitDataDI) error {
	if di.conn.currentTransmitDataMsgOpcode == 0 {
		return ErrLongDataTransmissionNotStarted
	}
	if err := validateDeflateLevel(config.DeflateLevel); err != nil {
		return err
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

	if di.conn.currentTransmitDataMsgDeflating {
		// The message is one stream, so its chunks may refer to each other:
		// always contextDeflater, see endLongDataTransmission. Deflated before
		// the write lock, so control frames are not held up by it.
		deflated, err := di.conn.contextDeflater.deflate(data, config.DeflateLevel)
		if err != nil {
			return err
		}
		f.PayloadData = deflated
		f.RSV1 = !di.conn.currentTransmitDataMsgOpened // RFC 7692 6: the first frame only
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

When the message is deflated, data is deflated at config.DeflateLevel and the
trailing 00 00 ff ff is not sent (RFC 7692 7.2.1).

Call it once. The message is over after it, even when it fails, so only
Release is left. After a close it sends nothing and returns ErrCloseAlreadySent.
*/
func (c *Conn) EndLongDataTransmission(data []byte, config TransmitDataConfig) error {
	return endLongDataTransmission(data, config, endLongDataTransmissionDI{
		conn:      c,
		sendFrame: c.sendFrame,
	})
}

type endLongDataTransmissionDI struct {
	conn      *Conn
	sendFrame func(f *Frame) error
}

func endLongDataTransmission(data []byte, config TransmitDataConfig, di endLongDataTransmissionDI) error {
	if di.conn.currentTransmitDataMsgOpcode == 0 {
		return ErrLongDataTransmissionNotStarted
	}
	if err := validateDeflateLevel(config.DeflateLevel); err != nil {
		return err
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

	if di.conn.currentTransmitDataMsgDeflating {
		deflated, err := di.conn.contextDeflater.deflate(data, config.DeflateLevel)
		if err != nil {
			return err
		}
		// RFC 7692 7.2.1: the trailing 00 00 ff ff of the flush is not sent.
		f.PayloadData = deflated[:len(deflated)-4]
		f.RSV1 = !di.conn.currentTransmitDataMsgOpened // RFC 7692 6: the first frame only

		// RFC 7692 7.1.1: server_no_context_takeover binds a server,
		// client_no_context_takeover a client. A side bound by it starts the
		// next message from an empty window. dropContext leaves the result
		// alone, so it can go before the send.
		no_context := di.conn.permessageDeflateAgreedConfig.ClientNoContextTakeover
		if di.conn.peerIsClient {
			no_context = di.conn.permessageDeflateAgreedConfig.ServerNoContextTakeover
		}
		if no_context {
			di.conn.contextDeflater.dropContext()
		}
		// The stream is whole: Release has nothing to drop.
		di.conn.currentTransmitDataMsgDeflating = false
	}

	di.conn.di.writeLocker.Lock()
	defer di.conn.di.writeLocker.Unlock()

	return di.sendFrame(f)
}

/*
ReleaseLongDataTransmission ends the transmission and frees the connection,
sending nothing. Defer it once, right after Start returns nil — never without a
Start, which would unlock what was never locked.

Before End, the message is left unterminated: close the connection. A deflated
one also leaves its chunks in the context, which the peer may not have, so the
context is dropped.
*/
func (c *Conn) ReleaseLongDataTransmission() {
	if c.currentTransmitDataMsgDeflating {
		c.contextDeflater.dropContext()
	}
	c.currentTransmitDataMsgOpcode = 0
	c.currentTransmitDataMsgOpened = false
	c.currentTransmitDataMsgDeflating = false
	c.di.dataFramesWriteLocker.Unlock()
}
