package wlgows

import "time"

/*
ListenerConfig is everything a Listener reads from, and it is never handed over
whole: each item has its own setter on the Listener, below. Every one of them
has a working zero value, so a Listener is usable before any is called — one
that reads conformingly and answers nothing.
*/
type ListenerConfig struct {

	// Bounds one frame, armed before each read. A frame's header, extended
	// length, masking key and payload are several reads and all of them must
	// land inside it, so this is a per frame budget, not a per read one.
	// 0 means no timeout.
	//
	// A timeout leaves the stream desynced — a half read frame cannot be
	// resumed — so treat it as fatal and close. For liveness prefer ping and
	// pong, which do not touch the transport.
	FrameReadTimeout time.Duration

	// A payload budget for one data message, spent down frame by frame and
	// checked at each header before anything is allocated. One number stops
	// both a single frame claiming 10 GB and fragments that never set FIN.
	//
	// It bounds control frames too, since the opcode is unknown until the frame
	// has been read: a ping can never cost more than this. RFC 6455 5.5 caps
	// them at 125 anyway, and that allowance is always granted, so a message may
	// overshoot its budget by up to 125 bytes before being refused.
	//
	// 0 means no limit, which lets a peer claim any size it likes. That is the
	// caller's decision to make.
	//
	// Exceeding it leaves the stream desynced — answer 1009 and close.
	MaxMsgPayloadByteLen uint64

	// How many frames one message may be assembled from. 0 means no limit.
	//
	// Data frames only. A control frame goes straight to its hook and never
	// joins the message, so a ping arriving between two fragments does not
	// spend from this — unlike MaxMsgPayloadByteLen, which caps every frame at
	// the header whatever its opcode.
	//
	// MaxMsgPayloadByteLen bounds the bytes a message carries; this bounds the
	// frames it arrives in. The two are not the same: an empty continuation
	// frame is dropped rather than kept, so it never spends the byte budget,
	// and a peer can send them forever to hold a message open that never
	// completes. This is what ends that.
	//
	// Choosing a value. You do not control how the peer fragments, so start
	// from the smallest fragment you are willing to accept:
	//
	//	MaxMsgFrameCount = MaxMsgPayloadByteLen / smallest fragment expected
	//
	// A 10 MB budget arriving in 4 KB fragments is 2560 frames, so 4000 leaves
	// room. Senders commonly fragment at their write buffer size, a few KB, and
	// many do not fragment at all.
	//
	// Err high. Too high only weakens a bound on memory you already capped with
	// MaxMsgPayloadByteLen; too low refuses messages a conforming peer was
	// entitled to send, and you will not see why.
	//
	// Exceeding it leaves the stream desynced — answer 1009 and close.
	MaxMsgFrameCount uint64

	// Which side the peer is on, which is what decides masking: RFC 6455 5.1
	// requires a client to mask every frame it sends and a server to mask none.
	// True on a Listener reading from a client, false reading from a server.
	//
	// There is no unchecked setting, because the RFC leaves no conforming state
	// where masking does not matter. Getting it backwards fails every frame at
	// once with ErrFrameNotMasked or ErrFrameMasked, which is a loud mistake
	// rather than a silent one — both are yours to answer with close code 1002.
	PeerIsClient bool

	// Opcode hooks, called one at a time from the read loop. A hook blocks it
	// for as long as it runs — no frames are read meanwhile, including pings
	// waiting to be answered — so a slow one should hand off to a goroutine or
	// a queue of its own. Calling them concurrently here would throw away the
	// ordering TCP and RFC 6455 5.4 hand us for free.
	//
	// A nil hook drops those frames.
	Ping  func(ping_frame *Frame)  // RFC 6455 5.5.2: MUST answer with a pong echoing the payload.
	Pong  func(pong_frame *Frame)  // RFC 6455 5.5.3: MUST NOT answer.
	Close func(close_frame *Frame) // RFC 6455 5.5.1: MUST answer with a close, then close. GetClosePayload decodes it.

	// Data takes each data frame instead of the message they assemble into. Set
	// it and Text and Binary never run.
	//
	// It is the sharp edge of this config. Take it only when you want the frames
	// themselves — a transmission too large to hold, a stream you forward on —
	// and only knowing exactly what comes with them, which is the rest of this.
	// A message that fits in memory belongs to Text or Binary, which discharge
	// all of it for you.
	//
	// Nothing is retained, so FIN is yours to watch for, and 5.6 UTF-8 validity
	// is not checked — a frame may end mid rune, so only the joined bytes can be
	// judged. That check is yours, and so is answering 1007.
	// GetCurrentMsgOpcode says whether the message is text and needs one.
	//
	// The budgets, 5.4 and the empty continuation drop are unchanged: these are
	// the frames the message is made of. Set it between messages, not during
	// one — frames already assembled are dropped when that message ends.
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
