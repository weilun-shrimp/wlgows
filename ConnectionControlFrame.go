package wlgows

/*
SendClose sends a close frame (OpcodeClose). A nil config sends one with no
payload at all, which RFC 6455 7.1.5 permits and a peer reports locally as close
code 1005.

Sending close does not close the socket. The RFC handshake is to send this, wait
for the peer's close frame, then Close.
*/
func (c *Conn) SendClose(mask bool, config *ClosePayload) error {
	f, err := c.di.newControlFrame(NewControlFrameConfig{
		Opcode: OpcodeClose, Mask: mask, PayloadData: config.Bytes(),
	})
	if err != nil {
		return err
	}
	return c.writeFrame(f)
}

/*
SendPing sends a ping frame (OpcodePing). payloadData may be nil, and whatever is
sent must come back verbatim in the peer's pong (RFC 6455 5.5.2).
*/
func (c *Conn) SendPing(mask bool, payloadData []byte) error {
	f, err := c.di.newControlFrame(NewControlFrameConfig{
		Opcode: OpcodePing, Mask: mask, PayloadData: payloadData,
	})
	if err != nil {
		return err
	}
	return c.writeFrame(f)
}

/*
SendPong sends a pong frame (OpcodePong).

Answering a ping means echoing that ping's payload back unchanged:

	if frame.Opcode == OpcodePing {
		conn.SendPong(true, frame.PayloadData)
	}

An unsolicited pong is legal too and needs no payload — RFC 6455 5.5.3 treats it
as a one way heartbeat that must not be answered.
*/
func (c *Conn) SendPong(mask bool, payloadData []byte) error {
	f, err := c.di.newControlFrame(NewControlFrameConfig{
		Opcode: OpcodePong, Mask: mask, PayloadData: payloadData,
	})
	if err != nil {
		return err
	}
	return c.writeFrame(f)
}
