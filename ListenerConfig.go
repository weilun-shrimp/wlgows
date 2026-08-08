package wlgows

import (
	"sync"
	"time"
)

/*
ListenerConfig is everything a Listener reads from. Hand it over with
SetConfig, which refuses while a loop is running — pause, set, start again.
*/
type ListenerConfig struct {
	// Where the frames come from.
	Conn interface {
		GetNextFrame(maxByteLength uint64) (*Frame, error)

		// SetReadDeadline sets the deadline for future Read calls and any
		// currently-blocked Read call. A zero value for t means Read will not time
		// out. Same as net.Conn.SetReadDeadline, and only called when
		// Listener.FrameReadTimeout is set.
		SetReadDeadline(t time.Time) error
	}

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
	Ping    func(ping_frame *Frame)    // RFC 6455 5.5.2: MUST answer with a pong echoing the payload.
	Pong    func(pong_frame *Frame)    // RFC 6455 5.5.3: MUST NOT answer.
	Close   func(close_frame *Frame)   // RFC 6455 5.5.1: MUST answer with a close, then close. GetClosePayload decodes it.
	Text    func(text_frames Frames)   // A whole text message, already checked as valid UTF-8 (5.6, 8.1).
	Binary  func(binary_frames Frames) // A whole binary message. Arbitrary bytes, no encoding rules.
	Unknown func(unknown_frame *Frame) // An opcode RFC 6455 5.2 reserves. A protocol error: answer 1002 and close.
}

// validate refuses a configuration no read loop could run on. SetConfig calls it
// on the way in; Listen calls it again, where a failure means SetConfig was
// never called at all.
func (config ListenerConfig) validate() error {
	if config.Conn == nil {
		return ErrListenerConnIsNil
	}
	return nil
}

/*
SetConfig replaces the configuration. It refuses a nil Conn, and refuses
entirely while a loop is running.

That refusal is what lets Listen read the configuration straight from the
Listener: nothing can write it between two frames, so there is no race to guard
against and no copy to take. Pause, set, start again.
*/
func (l *Listener) SetConfig(config ListenerConfig) error {
	return l.setConfig(config, setConfigDI{locker: &l.mu})
}

// setConfigDI is what setting reaches outside itself. Taking the lock and
// releasing it leaves no trace of its own, so a counting Locker is how a test
// sees it happened, and sees a refused config never took it at all.
type setConfigDI struct {
	locker sync.Locker
}

func (l *Listener) setConfig(config ListenerConfig, di setConfigDI) error {
	// Before the lock: a config that could never run has no business making a
	// running loop wait.
	if err := config.validate(); err != nil {
		return err
	}

	di.locker.Lock()
	defer di.locker.Unlock()

	if l.pauseChan != nil {
		return ErrListenerIsListening
	}
	l.config = config
	return nil
}
