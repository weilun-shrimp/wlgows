package wlgows

import "sync"

/*
Listener reads frames from a connection and hands each one to the hook
configured for it.

It knows the protocol's shape, not its policy. A ping arrives and your Ping hook
runs; sending the pong is yours. Every obligation RFC 6455 puts on a receiver
lands on a hook, never here:

	ping    5.5.2    MUST answer with a pong echoing the payload
	pong    5.5.3    MUST NOT answer
	close   5.5.1    MUST answer with a close, then close the connection

So a Listener with no hooks reads conformingly and answers nothing.

It never closes the connection either — not on a close frame, a read error, or a
protocol error. It reports and returns. That is yours because RFC 6455 7.1.1 asks
the two sides for different things and a Listener does not know which it is on.
*/
type Listener struct {
	// Set through SetConfig, which refuses while a loop is running — so the
	// loop can read this without a lock, and without a frozen copy to read
	// instead.
	config ListenerConfig

	// Assembly state for the message in flight, kept on the struct rather than
	// in Listen so a pause and restart does not throw away frames already
	// received. A continuation arriving after a restart still finds its message
	// open.
	//
	// Never nil once Listen has started: empty means no message is open, and
	// the frames of one being assembled otherwise. So the state is always ready
	// for the next frame and nothing has to allocate it on the way in.
	currentDataFrames    Frames
	currentDataAccLength uint64

	mu        sync.Mutex
	pauseChan chan struct{}
}

/*
nextFrameByteLimit is what the next frame is allowed to declare.

The opcode is not known until the frame has been read, so one number has to
serve both classes. Data frames get whatever is left of MaxMsgPayloadByteLen;
control frames get their RFC 6455 5.5 allowance of 125, which a ping arriving
late in a large message still needs. The larger of the two is passed, and the
class that overshoots is caught once the opcode is known.

0 means no limit, which is the caller's decision to make.
*/
func (l *Listener) nextFrameByteLimit() uint64 {
	if l.config.MaxMsgPayloadByteLen == 0 {
		return 0
	}
	// Guarded, not just subtracted. Pausing mid message and lowering
	// MaxMsgPayloadByteLen before starting again leaves currentDataAccLength above
	// the new budget with nothing having gone wrong, and an unguarded uint64
	// subtraction would wrap to ~1.8e19 there — handing back a limit larger
	// than the one just tightened.
	if l.currentDataAccLength > l.config.MaxMsgPayloadByteLen {
		return ControlFramePayloadMaxByteLength
	}
	return max(
		l.config.MaxMsgPayloadByteLen-l.currentDataAccLength, // Remain
		ControlFramePayloadMaxByteLength,
	)
}

/*
validateFrame refuses a frame that is malformed whatever its opcode: the
reserved bits, the masking direction, and the rules every control frame shares.
What a particular opcode requires of its own payload is routeFrame's question,
since that is where each one is handled.

Every rule here is a MUST in RFC 6455, and breaking any of them is a protocol
error the caller should answer with close code 1002.
*/
func (l *Listener) validateFrame(f *Frame) error {
	// 5.2: the reserved bits belong to negotiated extensions, and none is.
	if f.RSV1 || f.RSV2 || f.RSV3 {
		return ErrReservedBitsSet
	}

	// 5.1: a client masks every frame, a server masks none.
	if f.Mask != l.config.PeerIsClient {
		if l.config.PeerIsClient {
			return ErrFrameNotMasked
		}
		return ErrFrameMasked
	}

	if IsControlOpcode(f.Opcode) {
		// 5.5: control frames are never fragmented and never exceed 125 bytes.
		// The read limit could not enforce the second one, since it had to grant
		// the larger of the control allowance and what the message had left.
		if !f.FIN {
			return ErrControlFrameFragmented
		}
		if len(f.PayloadData) > ControlFramePayloadMaxByteLength {
			return ErrControlFramePayloadTooLong
		}
		return nil
	}

	// A reserved opcode carries no rule to check against — it goes to Unknown
	// untouched, and closing is the caller's answer.
	if !IsDataOpcode(f.Opcode) {
		return nil
	}

	// 5.4: a data opcode opens a message and every frame after it carries
	// opcode 0, so the frame's type has to agree with whether one is open.
	// Neither way of breaking that leaves anything sensible to assemble.
	if len(l.currentDataFrames) > 0 {
		if f.Opcode != OpcodeContinuation {
			return ErrDataFrameDuringMsg
		}
		return nil
	}
	if f.Opcode == OpcodeContinuation {
		return ErrContinuationFrameWithoutMsg
	}
	return nil
}
