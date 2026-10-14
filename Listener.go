package wlgows

import (
	"sync"
	"time"
)

/*
Listener reads frames from a connection and hands each one to the hook
configured for it.

It knows the protocol's shape, not its policy. A ping arrives and your Ping hook
runs; sending the pong is yours. Every obligation RFC 6455 puts on a receiver
lands on a hook, never here:

	ping    5.5.2, 5.5.3    MUST answer with a pong echoing the payload
	pong    5.5.3           no answer expected
	close   5.5.1           MUST answer with a close unless one was sent, then close
	unknown 5.2             MUST fail the connection: answer 1002, then close

A frame that breaks a rule ends Listen with an error. A reserved opcode goes to
the Unknown hook instead, and a nil one drops it like any other.

It never closes the connection either — not on a close frame, a read error, or a
protocol error. It reports and returns. That is yours because RFC 6455 7.1.1 asks
the two sides for different things and a Listener does not know which it is on.
*/
type Listener struct {
	// Where the frames come from. Fixed by NewListener and never written again,
	// so the loop reads it without a lock — unlike config, which SetConfig can
	// replace at any time, and which is read through GetConfig for that reason.
	conn ListenerConn

	// Written one item at a time by the setters, which a hook may call from
	// inside the read loop. Nothing reads it directly: GetConfig is the only way
	// in, so there is one place taking configLocker rather than one per reader
	// to forget. It hands back a copy, which is also what keeps the lock from
	// being held while a hook runs — that would deadlock the setter it calls.
	config       ListenerConfig
	configLocker sync.Locker

	// Assembly state for the message in flight, kept on the struct rather than
	// in Listen so a pause and restart does not throw away frames already
	// received. A continuation arriving after a restart still finds its message
	// open.
	//
	// Never nil once Listen has started, so nothing has to allocate it on the
	// way in. A Data hook leaves it empty — that hook takes each frame, so
	// nothing is ever appended.
	currentDataFrames DataFrames
	// Counted as frames arrive rather than read back off currentDataFrames, so
	// the budgets are two running totals kept the same way and neither depends
	// on what the slice happens to hold. It is also what says a message is open
	// at all, which the frames cannot answer with a Data hook set.
	currentDataFrameCount uint64
	currentDataFramesSize uint64
	// The message's type, from its first frame — 5.4 makes it the type of every
	// fragment after it. See GetCurrentMsgOpcode.
	currentDataFrameOpcode byte

	// Guards pauseChan, so Listen and PauseListen can be called from different
	// goroutines without two loops starting on one connection or a closed
	// channel being closed twice. The configuration has its own lock: it is
	// replaceable while a loop runs, and a claim is not.
	//
	// An interface rather than a sync.Mutex so a test can substitute one and see
	// the ordering. That is also why NewListener exists: a nil Locker panics on
	// the first Lock, so the zero Listener is not usable.
	listenLocker sync.Locker
	// Buffered by one and carrying whatever PauseListen was given, so the run
	// returns the pauser's error rather than only the fact of a pause. Cleared
	// on release, which is what says nothing is running.
	pauseChan chan error
}

// ListenerConn is what a Listener reads from: an already handshaken *Conn
// (from DialContext, Server.Accept or an http.Hijacker), or anything else that can
// hand over the next frame — a wrapper of your own, or a scripted one in a
// test.
type ListenerConn interface {
	// GetNextFrameHeader and ReadFramePayload read one frame in two steps, so
	// the Listener can refuse it by its header before the payload is
	// allocated. LockRead and UnlockRead are held around both. See
	// Conn.LockRead.
	LockRead()
	UnlockRead()
	GetNextFrameHeader() (*Frame, error)
	ReadFramePayload(f *Frame) error

	// SetReadDeadline sets the deadline for future Read calls and any
	// currently-blocked Read call. A zero value for t means Read will not time
	// out. Same as net.Conn.SetReadDeadline, and only called when
	// Listener.FrameReadTimeout is set.
	SetReadDeadline(t time.Time) error
}

/*
NewListener builds a Listener reading from conn.

The zero value is not usable — the lockers would be nil and panic on the first
Lock — so this is the only way to make one. The connection is settled here for
good; the configuration is not, and SetConfig may replace it whenever, including
while a loop runs.

	listener := wlgows.NewListener(conn)
	listener.SetConfig(config)
	err := listener.Listen()

A nil conn is not refused here, since there is nothing to return it in. Listen
answers ErrListenerConnIsNil instead, before it reads anything.
*/
func NewListener(conn ListenerConn) *Listener {
	return &Listener{
		conn: conn,

		configLocker: &sync.Mutex{},

		listenLocker: &sync.Mutex{},
	}
}

/*
GetCurrentMsgOpcode is the type of the message in flight: OpcodeText or
OpcodeBinary. OpcodeContinuation, which is 0, means none is open.

It is for the Data hook, where a continuation frame does not carry the type and
only text owes RFC 6455 5.6 a UTF-8 check. Call it from inside the hook: the
read loop is parked there, so the answer is the type of the frame you were
handed, FIN frame included.
*/
func (l *Listener) GetCurrentMsgOpcode() byte {
	return l.currentDataFrameOpcode
}

/*
validateFrameHeader refuses a frame by its header, before its payload is
allocated: the reserved bits, the masking direction, the rules every control
frame shares, the fragment order and the limits. What a particular opcode
requires of its own payload is routeFrame's question, since that is where each
one is handled.

f has every header field set and no payload yet. The rules are MUSTs in RFC
6455, answered with close code 1002; the limits are this Listener's, answered
with 1009.
*/
func (l *Listener) validateFrameHeader(f *Frame) error {
	config := l.GetConfig()
	peerIsClient := config.PeerIsClient

	// 5.2: the reserved bits belong to negotiated extensions, and none is.
	if f.RSV1 || f.RSV2 || f.RSV3 {
		return ErrReservedBitsSet
	}

	// 5.1: a client masks every frame, a server masks none.
	if f.Mask != peerIsClient {
		if peerIsClient {
			return ErrFrameNotMasked
		}
		return ErrFrameMasked
	}

	if IsControlOpcode(f.Opcode) {
		// 5.5: control frames are never fragmented and never exceed 125 bytes.
		if !f.FIN {
			return ErrControlFrameFragmented
		}
		if f.GetDeclaredPayloadSize() > ControlFramePayloadMaxByteLength {
			return ErrControlFramePayloadTooLong
		}
		return nil
	}

	// 10.4: frameSize counts the header too, so an empty frame still spends
	// some.
	frameSize := uint64(f.getHeaderSize()) + f.GetDeclaredPayloadSize()

	// 5.2: a reserved opcode goes to the Unknown hook, which answers it. Only
	// MaxFrameSize applies, since it never joins a message.
	if !IsDataOpcode(f.Opcode) {
		if config.MaxFrameSize > 0 && frameSize > config.MaxFrameSize {
			return ErrFrameByteLengthExceeded
		}
		return nil
	}

	// 5.4: a data opcode opens a message and every frame after it carries
	// opcode 0, so the frame's type has to agree with whether one is open.
	// Neither way of breaking that leaves anything sensible to assemble.
	//
	// The count, not the frames: a Data hook retains none, so an open message
	// leaves currentDataFrames empty.
	if l.currentDataFrameCount > 0 {
		if f.Opcode != OpcodeContinuation {
			return ErrDataFrameDuringMsg
		}
	} else if f.Opcode == OpcodeContinuation {
		return ErrContinuationFrameWithoutMsg
	}

	// 10.4: the limits, judged from the header so a frame over one is refused
	// before its payload is allocated. A message over a limit can never
	// complete, so its frames are dropped.
	if config.MaxFrameSize > 0 && frameSize > config.MaxFrameSize {
		l.resetCurrentDataFrames()
		return ErrFrameByteLengthExceeded
	}
	// Guarded, not just subtracted. Pausing mid message and lowering
	// MaxDataFramesSize before starting again leaves currentDataFramesSize above
	// the new budget, and an unguarded uint64 subtraction would wrap there.
	if config.MaxDataFramesSize > 0 &&
		(l.currentDataFramesSize > config.MaxDataFramesSize ||
			frameSize > config.MaxDataFramesSize-l.currentDataFramesSize) {
		l.resetCurrentDataFrames()
		return ErrFrameByteLengthExceeded
	}
	// Counted with this frame, so a message of exactly MaxDataFrameCount frames
	// is allowed and the next one is not.
	if config.MaxDataFrameCount > 0 && l.currentDataFrameCount >= config.MaxDataFrameCount {
		l.resetCurrentDataFrames()
		return ErrDataFrameCountExceeded
	}
	return nil
}
