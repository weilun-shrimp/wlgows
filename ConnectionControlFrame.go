package wlgows

/*
SendClose sends a close frame (OpcodeClose). A nil payload sends one with no
body at all, which RFC 6455 7.1.5 permits and a peer reports locally as close
code 1005.

Only the first call goes out. 5.5.1 gives the handshake one close each way, so a
second returns ErrCloseAlreadySent having written nothing. The claim is taken
under the same hold of writeLocker as the send, so two goroutines racing to
answer one close cannot both put a frame on the wire.

A payload that cannot be built leaves the claim unspent, so a bad reason can be
corrected and sent. A write that fails does spend it: the connection is already
gone, and a second close will not reach a peer the first could not.

Sending close does not close the socket. The RFC handshake is to send this, wait
for the peer's close frame, then Close.
*/
func (c *Conn) SendClose(payload *ClosePayload) error {
	c.di.writeLocker.Lock()
	defer c.di.writeLocker.Unlock()

	if c.closeSent {
		return ErrCloseAlreadySent
	}
	f, err := c.di.newControlFrame(NewControlFrameConfig{
		Opcode: OpcodeClose, Mask: c.maskSendFrame, PayloadData: payload.Bytes(),
	})
	if err != nil {
		return err
	}
	c.closeSent = true
	return c.sendFrame(f)
}

/*
SendPing sends a ping frame (OpcodePing). payloadData may be nil, and whatever is
sent must come back verbatim in the peer's pong (RFC 6455 5.5.2).
*/
func (c *Conn) SendPing(payloadData []byte) error {
	f, err := c.di.newControlFrame(NewControlFrameConfig{
		Opcode: OpcodePing, Mask: c.maskSendFrame, PayloadData: payloadData,
	})
	if err != nil {
		return err
	}
	return c.SendFrame(f)
}

/*
SendPong sends a pong frame (OpcodePong).

Answering a ping means echoing that ping's payload back unchanged:

	if frame.Opcode == OpcodePing {
		conn.SendPong(frame.PayloadData)
	}

An unsolicited pong is legal too and needs no payload — RFC 6455 5.5.3 treats it
as a one way heartbeat that must not be answered.
*/
func (c *Conn) SendPong(payloadData []byte) error {
	f, err := c.di.newControlFrame(NewControlFrameConfig{
		Opcode: OpcodePong, Mask: c.maskSendFrame, PayloadData: payloadData,
	})
	if err != nil {
		return err
	}
	return c.SendFrame(f)
}
