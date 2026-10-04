package wlgows

/*
SendClose sends a close frame (OpcodeClose). A nil payload sends one with no
body at all, which RFC 6455 7.1.5 permits and a peer reports locally as close
code 1005.

Only one close goes out. 5.5.1 gives the handshake one close each way, so a
second returns ErrCloseAlreadySent having written nothing. The claim is taken
under the same hold of writeLocker as the send, so two goroutines racing to
answer one close cannot both put a frame on the wire.

Only a close that is written spends the claim. A payload that cannot be built,
or a write that fails, leaves it unspent, so the close can be sent again.

Sending close does not close the socket. The RFC handshake is to send this, wait
for the peer's close frame, then Close.
*/
func (c *Conn) SendClose(payload *ClosePayload) error {
	return sendClose(payload, sendCloseDI{
		newControlFrame: c.di.newControlFrame,
		SendFrame:       c.SendFrame,
	})
}

type sendCloseDI struct {
	newControlFrame func(config NewControlFrameConfig) (*Frame, error)
	SendFrame       func(f *Frame) error
}

func sendClose(payload *ClosePayload, di sendCloseDI) error {
	f, err := di.newControlFrame(NewControlFrameConfig{
		Opcode: OpcodeClose, PayloadData: payload.Bytes(),
	})
	if err != nil {
		return err
	}

	return di.SendFrame(f)
}

/*
SendPing sends a ping frame (OpcodePing). payloadData may be nil, and whatever is
sent must come back verbatim in the peer's pong (RFC 6455 5.5.2).

It still goes out after this side's close: 5.5.2 allows a ping until the
connection is closed.
*/
func (c *Conn) SendPing(payloadData []byte) error {
	return sendPing(payloadData, sendPingDI{
		newControlFrame: c.di.newControlFrame,
		SendFrame:       c.SendFrame,
	})
}

type sendPingDI struct {
	newControlFrame func(config NewControlFrameConfig) (*Frame, error)
	SendFrame       func(f *Frame) error
}

func sendPing(payloadData []byte, di sendPingDI) error {
	f, err := di.newControlFrame(NewControlFrameConfig{
		Opcode: OpcodePing, PayloadData: payloadData,
	})
	if err != nil {
		return err
	}

	return di.SendFrame(f)
}

/*
SendPong sends a pong frame (OpcodePong).

Answering a ping means echoing that ping's payload back unchanged:

	if frame.Opcode == OpcodePing {
		conn.SendPong(frame.PayloadData)
	}

An unsolicited pong is legal too and needs no payload — RFC 6455 5.5.3 treats it
as a one way heartbeat that must not be answered.

It still goes out after this side's close: 5.5.2 wants a ping answered until the
peer's close arrives.
*/
func (c *Conn) SendPong(payloadData []byte) error {
	return sendPong(payloadData, sendPongDI{
		newControlFrame: c.di.newControlFrame,
		SendFrame:       c.SendFrame,
	})
}

type sendPongDI struct {
	newControlFrame func(config NewControlFrameConfig) (*Frame, error)
	SendFrame       func(f *Frame) error
}

func sendPong(payloadData []byte, di sendPongDI) error {
	f, err := di.newControlFrame(NewControlFrameConfig{
		Opcode: OpcodePong, PayloadData: payloadData,
	})
	if err != nil {
		return err
	}

	return di.SendFrame(f)
}
