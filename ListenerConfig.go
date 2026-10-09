package wlgows

import "time"

/*
ListenerConfig is everything a Listener reads from, handed over whole by
SetConfig. Every item has a working zero value, so a Listener is usable before
SetConfig is called — one that reads conformingly and answers nothing.
*/
type ListenerConfig struct {
	// FrameReadTimeout is the time limit for reading one whole frame. 0 means
	// no timeout. After a timeout the stream cannot be read again, so close the
	// connection. To check that the peer is alive, use ping and pong instead.
	FrameReadTimeout time.Duration

	// MaxDataFramesSize caps the bytes of one message's data frames,
	// headers included. It is checked at each frame header, before the payload
	// is allocated. 0 means no limit, which lets a peer claim a size that
	// crashes the process. When a message goes over it, answer 1009 and close.
	MaxDataFramesSize uint64

	// MaxDataFrameCount caps how many data frames one message may arrive in.
	// It bounds the memory of held frames, which tiny fragments make far larger
	// than the bytes they spend. 0 means no limit. When a message goes over it,
	// answer 1009 and close. LISTENER_README.md explains how to pick a value.
	MaxDataFrameCount uint64

	// PeerIsClient is true when the peer is a client. RFC 6455 5.1 says a
	// client masks every frame and a server masks none. If it is set wrong,
	// every frame fails with ErrFrameNotMasked or ErrFrameMasked.
	PeerIsClient bool

	// The hooks run one at a time in the read loop. A slow hook stops reading,
	// so hand slow work to a goroutine. A nil hook drops its frames.
	Ping  func(ping_frame *Frame)  // RFC 6455 5.5.2: MUST answer with a pong echoing the payload.
	Pong  func(pong_frame *Frame)  // RFC 6455 5.5.3: MUST NOT answer.
	Close func(close_frame *Frame) // RFC 6455 5.5.1: MUST answer with a close, then close. GetClosePayload decodes it.

	// Data gets each data frame as it arrives, for messages too large to hold.
	// When it is set, Text and Binary never run, and watching FIN and checking
	// UTF-8 are yours. Set it between messages, not during one.
	Data func(data_frame *Frame)

	Text    func(text_frames Frames)   // A whole text message, already checked as valid UTF-8 (5.6, 8.1).
	Binary  func(binary_frames Frames) // A whole binary message. Arbitrary bytes, no encoding rules.
	Unknown func(unknown_frame *Frame) // An opcode RFC 6455 5.2 reserves. A protocol error: answer 1002 and close.
}

/*
GetConfig is what the Listener is running on, as a copy taken under
configLocker. Safe from any goroutine, including from inside a hook.

A copy, so writing to it changes nothing — SetConfig is the only way in. It is
for reading back what was set, for changing one item without restating the rest,
and for a hook wanting the configuration it did not capture.

The hooks come back callable, and calling one is yours to serialise. The read
loop calls them one at a time, which is what keeps the ordering RFC 6455 5.4
gives you; calling one from here while a loop runs is a second caller that
ordering does not cover. A hook can also only be compared to nil, so this
answers whether one is set, never which one.

It stands on its own only because every item is a value or a func value. Keep it
that way: a slice or a map here would come back sharing its storage, and this
would hand out a race instead of a snapshot.
*/
func (l *Listener) GetConfig() ListenerConfig {
	l.configLocker.Lock()
	defer l.configLocker.Unlock()

	return l.config
}

/*
SetConfig replaces the whole configuration, under configLocker and by copy.

Callable whenever — before Listen, between two runs, or from a hook inside the
read loop. Nothing is refused: the lock makes the write safe against a running
loop, and the copy means the frame in flight finishes on the values it read, so
a change lands on the next frame rather than halfway through this one.

All of it, every time. What you do not set is set to its zero value, which is
why changing one thing is a GetConfig away:

	config := listener.GetConfig()
	config.Text = hook
	listener.SetConfig(config)

Nothing here is required. Every item has a working zero value, so a Listener
this was never called on still reads — though a zero PeerIsClient says the peer
is a server, and a server that never says otherwise refuses every frame a client
sends.
*/
func (l *Listener) SetConfig(config ListenerConfig) {
	l.configLocker.Lock()
	defer l.configLocker.Unlock()

	l.config = config
}
