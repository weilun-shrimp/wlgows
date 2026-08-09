package wlgows

/*
NewStandardListener builds a Listener with every obligation RFC 6455 puts on a
receiver already answered — ping (5.5.2), pong (5.5.3), close (5.5.1) and
reserved opcodes (5.2) — and PeerIsClient settled from which side this Conn is.

Left to you: the message hooks, the limits, and closing the connection. Take
what is here and add to it, since SetConfig replaces all of it at once:

	listener := conn.NewStandardListener()
	config := listener.GetConfig()
	config.MaxMsgPayloadByteLen = 10 << 20
	config.Text = func(frames wlgows.Frames) { ... }
	listener.SetConfig(config)
	err := listener.Listen()

Every hook it sets can be replaced, so this is a starting point, not a fixture.
*/
func (c *Conn) NewStandardListener() *Listener {
	l := NewListener(c)

	l.SetConfig(ListenerConfig{
		PeerIsClient: !c.maskSendFrame, // 5.1: the peer is the side this one is not

		Ping:    c.RFC6455PingHook,
		Pong:    c.RFC6455PongHook,
		Close:   c.RFC6455CloseHook(l),
		Unknown: c.RFC6455UnknownHook(l),
	})
	return l
}

// RFC6455PingHook answers a ping with a pong carrying the same payload, which
// is what RFC 6455 5.5.2 asks of a receiver.
//
// The error is dropped: a pong that cannot be written means the connection is
// already gone, and the read side will say so.
func (c *Conn) RFC6455PingHook(ping_frame *Frame) {
	c.SendPong(ping_frame.PayloadData)
}

// RFC6455PongHook does nothing, because RFC 6455 5.5.3 says a pong MUST NOT be
// answered. A nil hook drops the frame just the same — this only puts the rule
// somewhere it can be read.
func (c *Conn) RFC6455PongHook(pong_frame *Frame) {}

/*
RFC6455CloseHook answers a close frame and stops l.

RFC 6455 5.5.1: an endpoint that receives a close and has not sent one MUST
answer with a close, SHOULD echo the status code, and MUST NOT process anything
further. CloseSent is what decides the first — if this side opened the
handshake, the frame in hand is the peer's answer and answering it again would
put a second close on the wire.

The payload is echoed exactly as it arrived, which is always legal: the Listener
validates it before any hook runs, so 7.4.1's local-only codes and a malformed
body never reach here. A close with no body is answered with no body.

It does not close the connection. 7.1.1 asks the two sides for different things —
a server closes the TCP connection at once, a client waits for the server to —
so that is yours, after Listen returns.
*/
func (c *Conn) RFC6455CloseHook(l *Listener) func(close_frame *Frame) {
	return func(close_frame *Frame) {
		payload, _ := close_frame.GetClosePayload()
		c.SendClose(payload) // ErrCloseAlreadySent if this side opened the handshake
		l.PauseListen(nil)   // the peer said why in its close payload
	}
}

/*
RFC6455UnknownHook answers an opcode RFC 6455 5.2 reserves with close 1002, then
stops l.

A reserved opcode is undefined, so there is nothing to do with the frame and no
way to know what follows it. 7.4.1 calls that a protocol error. The close is
skipped if one has already gone out, for the reason 5.5.1 gives in
RFC6455CloseHook.
*/
func (c *Conn) RFC6455UnknownHook(l *Listener) func(unknown_frame *Frame) {
	return func(unknown_frame *Frame) {
		c.SendClose(&ClosePayload{StatusCode: CloseProtocolError})
		l.PauseListen(nil)
	}
}
