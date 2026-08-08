package wlgows

import (
	"encoding/binary"
	"fmt"
)

// ControlFramePayloadMaxByteLength is the cap RFC 6455 5.5 puts on a control
// frame payload, so the length always fits the 7 bit inline field.
const ControlFramePayloadMaxByteLength = 125

// NewControlFrameConfig describes the control frame to build.
type NewControlFrameConfig struct {
	// Opcode must be a control opcode — OpcodeClose, OpcodePing, OpcodePong, or
	// one the RFC reserves. A data opcode is refused.
	Opcode byte
	// Mask must be true on a frame a client sends and may be false on one a
	// server sends (RFC 6455 5.1).
	Mask bool
	// PayloadData is at most ControlFramePayloadMaxByteLength bytes. May be nil.
	PayloadData []byte
}

/*
NewControlFrame builds one control frame, refusing anything the RFC forbids
before the frame exists.

Two rules, neither recoverable once bytes are on the wire:

  - the opcode must be a control opcode, or the frame would be built with FIN
    forced on and a 125 byte cap applied, which is wrong for a data frame;
  - the payload must fit in 125 bytes, or NewFrame would silently encode a 16
    bit extended length, which a peer reads as a protocol error.

Control frames are never fragmented (RFC 6455 5.5), so FIN is always set and is
not the caller's to choose.
*/
func NewControlFrame(config NewControlFrameConfig) (*Frame, error) {
	if !IsControlOpcode(config.Opcode) {
		return nil, fmt.Errorf("opcode %#x: %w", config.Opcode, ErrNotControlFrameOpcode)
	}
	if len(config.PayloadData) > ControlFramePayloadMaxByteLength {
		return nil, fmt.Errorf("control frame payload is %d bytes: %w",
			len(config.PayloadData), ErrControlFramePayloadTooLong)
	}

	return NewFrame(NewFrameConfig{
		Data:   config.PayloadData,
		Opcode: config.Opcode,
		Mask:   config.Mask,
		FIN:    true,
	})
}

// Close status codes from RFC 6455 7.4.1, for SendClose.
const (
	CloseNormalClosure           uint16 = 1000
	CloseGoingAway               uint16 = 1001
	CloseProtocolError           uint16 = 1002
	CloseUnsupportedData         uint16 = 1003
	CloseInvalidFramePayloadData uint16 = 1007
	ClosePolicyViolation         uint16 = 1008
	CloseMessageTooBig           uint16 = 1009
	CloseMandatoryExtension      uint16 = 1010
	CloseInternalServerErr       uint16 = 1011
)

/*
ClosePayload is the body of a close frame, which RFC 6455 5.5.1 makes
optional in two steps: the body itself may be absent, and within a body the
reason may be absent. A reason cannot exist without a status code, since it is
defined as whatever follows the two code bytes.

A pointer expresses exactly those three shapes and no illegal fourth:

	nil                                           // no body
	&ClosePayload{StatusCode: c}                  // status code only
	&ClosePayload{StatusCode: c, Reason: "bye"}   // status code and reason

StatusCode is one of the Close* constants. Reason is capped at 123 bytes, since
the status code spends 2 of the 125 a control frame allows — and it is bytes,
not characters, so a CJK reason costs 3 each.
*/
type ClosePayload struct {
	StatusCode uint16
	Reason     string
}

/*
Bytes encodes the payload: the 2 byte big endian status code followed by the
reason. A nil receiver encodes no body at all, so the three legal shapes come out
of one call with no branching at the call site.
*/
func (c *ClosePayload) Bytes() []byte {
	if c == nil {
		return nil
	}
	payloadData := make([]byte, 2, 2+len(c.Reason))
	binary.BigEndian.PutUint16(payloadData, c.StatusCode)
	return append(payloadData, c.Reason...)
}

/*
GetClosePayload decodes a close frame's payload back into a ClosePayload.

The three shapes RFC 6455 5.5.1 allows map onto the two returns:

	no body       -> nil, nil          the peer reported no status; treat as 1005
	status code   -> &ClosePayload{StatusCode: c}, nil
	code + reason -> &ClosePayload{StatusCode: c, Reason: r}, nil

A one byte payload is the fourth shape the RFC forbids and returns
ErrClosePayloadTooShort. That check is what keeps this from panicking on the
slice, so it protects the process before it protects the spec.

Reason is returned as sent. RFC 6455 5.5.1 requires it to be valid UTF-8 and a
peer sending otherwise should be failed with close code 1007, but that is not
checked here — use utf8.ValidString if it matters to you.
*/
func (f *Frame) GetClosePayload() (*ClosePayload, error) {
	if f.Opcode != OpcodeClose {
		return nil, fmt.Errorf("opcode %#x: %w", f.Opcode, ErrNotCloseFrameOpcode)
	}
	switch len(f.PayloadData) {
	case 0:
		return nil, nil
	case 1:
		return nil, ErrClosePayloadTooShort
	}
	return &ClosePayload{
		StatusCode: binary.BigEndian.Uint16(f.PayloadData[:2]),
		Reason:     string(f.PayloadData[2:]),
	}, nil
}
