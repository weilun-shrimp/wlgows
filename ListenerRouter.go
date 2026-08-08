package wlgows

import (
	"encoding/binary"
	"unicode/utf8"
)

/*
routeFrame hands one frame to the hook it belongs to.

validateFrame has already agreed the frame is legal, so the only rule left is
the message budget, which cannot be judged until the payload is in hand.

Control frames go straight out and never join the message being assembled — RFC
6455 5.4 lets one arrive between two continuation frames, so touching the
assembly state here would corrupt it. Data frames accumulate until FIN, then the
whole message goes to the hook named by the opcode of its first frame, which 5.4
makes the type of every fragment in it.
*/
func (l *Listener) routeFrame(f *Frame) error {
	switch f.Opcode {
	case OpcodePing:
		if l.config.Ping != nil {
			l.config.Ping(f)
		}
		return nil
	case OpcodePong:
		if l.config.Pong != nil {
			l.config.Pong(f)
		}
		return nil
	case OpcodeClose:
		if err := validateClosePayload(f.PayloadData); err != nil {
			return err
		}
		if l.config.Close != nil {
			l.config.Close(f)
		}
		return nil
	}
	if !IsDataOpcode(f.Opcode) { // reserved by 5.2, meaning undefined
		if l.config.Unknown != nil {
			l.config.Unknown(f)
		}
		return nil
	}

	/*
		An empty continuation adds nothing to the message RFC 6455 5.4 defines
		as the concatenation of its fragments, so dropping it leaves the
		assembled bytes identical.

		Dropping it is also what bounds memory. MaxMsgPayloadByteLen counts
		payload bytes, so empty frames never touch it and a peer could grow
		currentDataFrames without limit. With them gone every retained frame
		carries at least one byte, and the frame count cannot outrun the budget.

		A frame with FIN ends the message whether or not it carries payload, and
		the first frame of a message names its type, so neither is dropped.
	*/
	if f.Opcode == OpcodeContinuation && !f.FIN && len(f.PayloadData) == 0 {
		return nil
	}

	// The read limit granted the larger of the control allowance and what the
	// message had left, so a data frame can still arrive over budget.
	l.currentDataAccLength += uint64(len(f.PayloadData))
	if l.config.MaxMsgPayloadByteLen > 0 && l.currentDataAccLength > l.config.MaxMsgPayloadByteLen {
		l.resetCurrentDataFrames() // it can never complete now
		return ErrFrameByteLengthExceeded
	}

	// Checked before the append, so a message of exactly MaxMsgFrameCount
	// frames is allowed and the next one is not.
	if l.config.MaxMsgFrameCount > 0 &&
		uint64(len(l.currentDataFrames)) >= l.config.MaxMsgFrameCount {
		l.resetCurrentDataFrames() // it can never complete now
		return ErrMsgFrameCountExceeded
	}

	l.currentDataFrames = append(l.currentDataFrames, f)
	if !f.FIN {
		return nil
	}

	frames := l.currentDataFrames
	l.resetCurrentDataFrames()
	// Only text or binary can be first: validateFrame refuses a continuation
	// with no message open, and a reserved opcode never got this far.
	switch frames[0].Opcode {
	case OpcodeText:
		if !validTextUTF8(frames) {
			return ErrInvalidUTF8
		}
		if l.config.Text != nil {
			l.config.Text(frames)
		}
	case OpcodeBinary:
		if l.config.Binary != nil {
			l.config.Binary(frames)
		}
	}
	return nil
}

// resetCurrentDataFrames reopens the assembly state for the next message.
//
// A fresh slice, never currentDataFrames[:0]: the frames just handed to a hook
// share that backing array, and reusing it would let the next message overwrite
// what the hook is still holding.
func (l *Listener) resetCurrentDataFrames() {
	l.currentDataFrames = Frames{}
	l.currentDataAccLength = 0
}

/*
validTextUTF8 reports whether a text message is valid UTF-8.

RFC 6455 5.6 puts the rule on the whole message, not on each frame: a frame may
end halfway through a rune, so the bytes have to be judged joined. A single
frame message is judged in place, since joining one frame would copy the whole
payload for nothing.
*/
func validTextUTF8(frames Frames) bool {
	if len(frames) == 1 { // Avoid an allocation
		return utf8.Valid(frames[0].PayloadData)
	}
	return utf8.Valid(frames.Bytes())
}

/*
validateClosePayload checks the shape RFC 6455 5.5.1 gives a close body: absent,
or a 2 byte status code optionally followed by a UTF-8 reason.

One byte is half a status code and decodes to nothing. The reason is checked
from the third byte on, because the code in front of it is a 2 byte integer and
not text — checking the whole payload would refuse every close frame carrying a
reason.
*/
func validateClosePayload(payload []byte) error {
	if len(payload) == 1 {
		return ErrClosePayloadTooShort
	}
	if len(payload) >= 2 && !validCloseStatusCode(binary.BigEndian.Uint16(payload[:2])) {
		return ErrInvalidCloseStatusCode
	}
	if len(payload) > 2 && !utf8.Valid(payload[2:]) {
		return ErrInvalidUTF8
	}
	return nil
}

/*
validCloseStatusCode reports whether a status code may appear on the wire.

RFC 6455 7.4.2 splits the space: 0 to 999 are unused, 1000 to 2999 belong to the
protocol and only the registered ones are defined, 3000 to 3999 are registered
by libraries and frameworks, and 4000 to 4999 are private.

Three of the registered codes are local only and 7.4.1 forbids sending them —
1005 no status received, 1006 abnormal closure, 1015 TLS handshake failure. 1004
is reserved with no meaning. 1012 to 1014 are absent from RFC 6455 but were
registered with IANA afterwards through the process 7.4.2 describes, so they are
allowed.
*/
func validCloseStatusCode(statusCode uint16) bool {
	switch {
	case statusCode >= 3000 && statusCode <= 4999:
		return true
	case statusCode >= 1000 && statusCode <= 1015:
		return statusCode != 1004 &&
			statusCode != 1005 &&
			statusCode != 1006 &&
			statusCode != 1015
	default:
		return false
	}
}
