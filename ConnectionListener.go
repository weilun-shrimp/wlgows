package wlgows

import (
	"fmt"
	"time"
)

// NewStandardListener's limits. Raise them together: the count is derived from
// the size, so a message may arrive in fragments of 256 bytes or more, and the
// frame limit matches it, so one frame may carry the whole message.
const (
	// StandardMaxDataFramesSize is 4 KB per message, headers included.
	StandardMaxDataFramesSize = 4 << 10
	// StandardMaxFrameSize is 4 KB per frame, header included.
	StandardMaxFrameSize = 4 << 10
	// StandardMaxDataFrameCount is 16 frames per message.
	StandardMaxDataFrameCount = StandardMaxDataFramesSize / 256
	// StandardFrameReadTimeout counts the wait for a frame to start too, so a
	// peer silent this long is cut off. Ping at no more than half of it, and the
	// pongs keep a healthy peer alive.
	StandardFrameReadTimeout = 60 * time.Second
)

/*
NewStandardListener builds a Listener with every obligation RFC 6455 puts on a
receiver already answered — ping (5.5.2), pong (5.5.3), close (5.5.1) and
reserved opcodes (5.2) — PeerIsClient settled from which side this Conn is, and
the limits at
StandardMaxDataFramesSize, StandardMaxFrameSize,
StandardMaxDataFrameCount and StandardFrameReadTimeout.

The timeout cuts off an idle peer, so run StartPingLoop at 30 seconds or less.

Left to you: the message hooks and closing the connection. Take
what is here and add to it, since SetConfig replaces all of it at once:

	listener := conn.NewStandardListener()
	config := listener.GetConfig()
	config.MaxDataFramesSize = 10 << 20
	config.MaxFrameSize = 10 << 20
	config.MaxDataFrameCount = 10 << 20 / 256
	config.Text = func(frames wlgows.DataFrames) { ... }
	listener.SetConfig(config)
	err := listener.Listen()

Every hook it sets can be replaced, so this is a starting point, not a fixture.
*/
func (c *Conn) NewStandardListener() *Listener {
	l := NewListener(c)

	l.SetConfig(ListenerConfig{
		PeerIsClient:      c.peerIsClient,
		MaxDataFramesSize: StandardMaxDataFramesSize,
		MaxFrameSize:      StandardMaxFrameSize,
		MaxDataFrameCount: StandardMaxDataFrameCount,
		FrameReadTimeout:  StandardFrameReadTimeout,

		Ping:    c.RFC6455PingHook,
		Pong:    c.RFC6455PongHook,
		Close:   c.RFC6455CloseHook(l),
		Unknown: c.RFC6455UnknownHook(l),
	})
	return l
}

// RFC6455PingHook answers a ping with a pong carrying the same payload, which
// is what RFC 6455 5.5.2 and 5.5.3 ask of a receiver.
//
// The error is dropped: a pong that cannot be written means the connection is
// already gone, and the read side will say so.
func (c *Conn) RFC6455PingHook(ping_frame *Frame) {
	c.SendPong(ping_frame.PayloadData)
}

// RFC6455PongHook does nothing, because RFC 6455 5.5.3 expects no answer to a
// pong. A nil hook drops the frame just the same — this only puts the rule
// somewhere it can be read.
func (c *Conn) RFC6455PongHook(pong_frame *Frame) {}

/*
RFC6455CloseHook answers a close frame and stops l.

RFC 6455 5.5.1: an endpoint that receives a close and has not sent one MUST
answer with a close, and typically echoes the status code. ErrCloseAlreadySent
decides whether to answer — if this side opened the handshake, the frame in
hand is the peer's answer and answering it again would put a second close on
the wire.

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
RFC6455UnknownHook answers an opcode RFC 6455 5.2 reserves with the close
StandardClosePayloadFor gives ErrReservedOpcode (1002), then stops l with that
error, since 5.2 fails the connection.

A reserved opcode is undefined, so there is nothing to do with the frame and no
way to know what follows it. 7.4.1 calls that a protocol error. The close is
skipped if one has already gone out, for the reason 5.5.1 gives in
RFC6455CloseHook.
*/
func (c *Conn) RFC6455UnknownHook(l *Listener) func(unknown_frame *Frame) {
	return func(unknown_frame *Frame) {
		err := fmt.Errorf("opcode %#x: %w", unknown_frame.Opcode, ErrReservedOpcode)
		c.SendClose(StandardClosePayloadFor(err))
		l.PauseListen(err)
	}
}
