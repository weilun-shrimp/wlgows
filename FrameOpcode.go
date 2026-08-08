package wlgows

/*
Opcodes from RFC 6455 5.2. The field is 4 bits, so 0x0 to 0xF is the whole
space: 0x0 to 0x2 name a data frame, 0x8 to 0xA a control frame, and everything
else — 0x3 to 0x7 and 0xB to 0xF — the RFC reserves without defining.
*/
const (
	OpcodeContinuation byte = 0x0
	OpcodeText         byte = 0x1
	OpcodeBinary       byte = 0x2
	OpcodeClose        byte = 0x8
	OpcodePing         byte = 0x9
	OpcodePong         byte = 0xA
)

/*
IsControlOpcode reports whether opcode is one of the three control frames the
RFC defines: close, ping or pong.

A reserved opcode answers false here and false to IsDataOpcode, because there is
nothing useful to say about a frame whose meaning is undefined — no rule can be
applied to it and no payload interpreted. Frame.Opcode is a byte, so a value
above 0xF cannot be a real opcode at all and answers false to both as well.
*/
func IsControlOpcode(opcode byte) bool {
	return opcode == OpcodeClose || opcode == OpcodePing || opcode == OpcodePong
}

// IsDataOpcode reports whether opcode carries message payload: a continuation,
// text or binary frame. Reserved and out of range values answer false, the same
// as they do to IsControlOpcode.
func IsDataOpcode(opcode byte) bool {
	return opcode == OpcodeContinuation || opcode == OpcodeText || opcode == OpcodeBinary
}
