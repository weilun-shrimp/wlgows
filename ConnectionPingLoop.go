package wlgows

import "time"

/*
StartPingLoop sends a ping every interval, starting with one straight away, and
blocks until the loop ends. Run it in a goroutine of your own if you want it
beside something else — which is the usual shape, since the reading is a Listen
somewhere.

payload is called for each ping. RFC 6455 5.5.2 has the peer echo it back
verbatim, so varying it — a counter, a timestamp — is what lets a Pong hook tell
which ping came back. nil sends an empty ping, which is the ordinary heartbeat.
5.5 caps a control payload at 125 bytes.

It ends itself, and there is nothing to hold. Two things stop it.

A ping that cannot be sent — a dead socket, or a payload the frame rules refuse.
Nothing retries, so a failed ping is the last one.

A close already sent from this side, whoever started the handshake: a Close hook
answering the peer sets that as surely as closing first does. This one is a
choice rather than a rule. 5.5.1 forbids data frames after a close and says
nothing about control frames, and 5.5.2 permits a ping until both sides have
closed — so pinging on here would be legal, and pointless. A heartbeat exists to
learn whether the peer is still there, which is no longer a question worth
asking about a connection being shut down.

	go conn.StartPingLoop(30*time.Second, nil)

Silence is not its business. A peer that reads pings and never answers looks
alive to every write here, so detecting one is a deadline you keep: stamp a time
in your Pong hook and compare it against your own.
*/
func (c *Conn) StartPingLoop(interval time.Duration, payload func() []byte) {
	c.di.loop(func(stop_signal chan<- struct{}) {
		// Once a close is on the wire there is nothing left to keep alive —
		// see the choice above. Read under the lock that sets it.
		c.di.writeLocker.Lock()
		closeSent := c.closeSent
		c.di.writeLocker.Unlock()

		if closeSent {
			stop_signal <- struct{}{}
			return
		}

		var payloadData []byte
		if payload != nil {
			payloadData = payload()
		}
		if err := c.SendPing(payloadData); err != nil {
			stop_signal <- struct{}{}
		}
	}, interval)
}
