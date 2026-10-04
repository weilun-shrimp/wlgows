package wlgows

import (
	"crypto/rand"
	"encoding/binary"
	"slices"
)

type Frame struct {
	FIN                   bool
	RSV1                  bool
	RSV2                  bool
	RSV3                  bool
	Opcode                byte    // 4 bit: 0 continuation, 1 text, 2 binary, 8 close, 9 ping, A(10) pong
	Mask                  bool    // as read; a send sets it from the Conn's side
	PayloadLength         byte    // as read; a send rewrites it from len(PayloadData)
	ExtendedPayloadLength uint64  // as read; a send rewrites it from len(PayloadData)
	MaskingKey            [4]byte // as read; a client's send draws a fresh one
	PayloadData           []byte  // always unmasked: a read unmasks it, a send masks a copy
}

func (f *Frame) GetMaxPayloadLength() uint64 {
	if f.PayloadLength == 127 || f.PayloadLength == 126 {
		return f.ExtendedPayloadLength
	} else {
		return uint64(f.PayloadLength)
	}
}

func boolToInt(data bool) uint8 {
	if data {
		return 1
	}
	return 0
}

// getHeaderSize is the wire size of the header: 2 bytes, plus the extended
// length (2 or 8) and the masking key (4) when present.
func (f *Frame) getHeaderSize() int {
	size := 2
	switch f.PayloadLength {
	case 126:
		size += 2
	case 127:
		size += 8
	}
	if f.Mask {
		size += 4
	}
	return size
}

// appendSealedHeader appends the wire header to buffer and returns it; keep the
// result, it may be a new array. nil is fine.
func (f *Frame) appendSealedHeader(buffer []byte) []byte {
	buffer = slices.Grow(buffer, f.getHeaderSize())

	buffer = append(buffer,
		// Byte 0: FIN, RSV1-3, then the 4 bit opcode.
		boolToInt(f.FIN)<<7|
			boolToInt(f.RSV1)<<6|
			boolToInt(f.RSV2)<<5|
			boolToInt(f.RSV3)<<4|
			f.Opcode&15,
		// Byte 1: MASK, then the 7 bit payload length.
		boolToInt(f.Mask)<<7|
			f.PayloadLength,
	)
	switch f.PayloadLength {
	case 126:
		buffer = binary.BigEndian.AppendUint16(buffer, uint16(f.ExtendedPayloadLength))
	case 127:
		buffer = binary.BigEndian.AppendUint64(buffer, f.ExtendedPayloadLength)
	}
	if f.Mask {
		buffer = append(buffer, f.MaskingKey[:]...)
	}
	return buffer
}

// MaskPayload XORs payload in place with maskingKey, starting on maskingKey[0].
// Masking is its own inverse: the same call unmasks.
func MaskPayload(payload []byte, maskingKey [4]byte) {
	// 8 bytes at a time with the key repeated twice, then the tail byte by
	// byte. Every step is a multiple of 4, so the tail starts on key[0].
	key := uint64(binary.LittleEndian.Uint32(maskingKey[:]))
	key |= key << 32
	for len(payload) >= 8 {
		binary.LittleEndian.PutUint64(payload, binary.LittleEndian.Uint64(payload)^key)
		payload = payload[8:]
	}
	for i := range payload {
		payload[i] ^= maskingKey[i&3]
	}
}

/*
NewFrameConfig is everything NewFrame needs. A struct rather than positional
arguments so the two booleans cannot be swapped at a call site.

FIN marks the last frame of a message. Its zero value is false, so a caller who
omits it gets a frame the peer will wait for a continuation of — set it on every
single frame message.
*/
type NewFrameConfig struct {
	// PayloadData is the payload, carried whole. Empty is legal: a zero length frame
	// is how an empty text message or a bare close goes out.
	PayloadData []byte
	// Opcode is 1 text, 2 binary, 8 close, 9 ping, 0xA pong, 0 continuation.
	Opcode uint8
	// FIN marks this as the final frame of its message.
	FIN bool
}

/*
NewFrame builds one frame.

A single frame message sets FIN:

	f := wlgows.NewFrame(wlgows.NewFrameConfig{
		PayloadData: []byte("hello"), Opcode: 1, FIN: true,
	})

Fragmenting means leaving FIN off every frame but the last, and giving the
continuation frames opcode 0:

	head := wlgows.NewFrame(wlgows.NewFrameConfig{PayloadData: a, Opcode: 1})
	tail := wlgows.NewFrame(wlgows.NewFrameConfig{PayloadData: b, Opcode: 0, FIN: true})

Masking and the length fields are left unset: the Conn settles both when it
sends — see SendFrame.
*/
func NewFrame(config NewFrameConfig) *Frame {
	return &Frame{FIN: config.FIN, Opcode: config.Opcode, PayloadData: config.PayloadData}
}

// fillPayloadLength encodes len(PayloadData) the way RFC 6455 5.2 requires:
// inline up to 125 bytes, a 16 bit extended length up to 65535, a 64 bit one
// above that.
func (f *Frame) fillPayloadLength() {
	dataLength := uint64(len(f.PayloadData))
	switch {
	case dataLength <= 125:
		f.PayloadLength = uint8(dataLength)
		f.ExtendedPayloadLength = 0
	case dataLength <= 65535:
		f.PayloadLength = 126
		f.ExtendedPayloadLength = dataLength
	default:
		f.PayloadLength = 127
		f.ExtendedPayloadLength = dataLength
	}
}

// FillMaskingKey fills key with 4 random bytes; on error its contents are unspecified.
func FillMaskingKey(key *[4]byte) error {
	return fillMaskingKey(key, fillMaskingKeyDI{
		randRead: rand.Read,
	})
}

type fillMaskingKeyDI struct {
	randRead func(b []byte) (n int, err error)
}

// fillMaskingKey overwrites key; on error its contents are unspecified.
func fillMaskingKey(key *[4]byte, di fillMaskingKeyDI) error {
	_, err := di.randRead(key[:]) // WebSocket规范要求4个字节的掩码密钥
	return err
}
