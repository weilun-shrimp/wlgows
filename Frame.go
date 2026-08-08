package wlgows

import (
	"crypto/rand"
	"encoding/binary"
)

type Frame struct {
	FIN                   bool
	RSV1                  bool
	RSV2                  bool
	RSV3                  bool
	Opcode                byte // 7 bit, 1 => text, 2 => byte, 8 => close, 9 => ping, A(10) => pong
	Mask                  bool
	PayloadLength         byte
	ExtendedPayloadLength uint64
	MaskingKey            []byte // mask == 1
	PayloadData           []byte // always unmasked
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

/*
* make self to []byte data
 */
func (f *Frame) Seal() []byte {
	result := []byte{
		boolToInt(f.FIN)<<7 +
			boolToInt(f.RSV1)<<6 +
			boolToInt(f.RSV2)<<5 +
			boolToInt(f.RSV3)<<4 +
			f.Opcode&15,
		boolToInt(f.Mask)<<7 +
			f.PayloadLength,
	}

	var ExtendedPayloadByte []byte
	switch f.PayloadLength {
	case 126:
		ExtendedPayloadByte = make([]byte, 2)
		binary.BigEndian.PutUint16(ExtendedPayloadByte, uint16(f.ExtendedPayloadLength))
	case 127:
		ExtendedPayloadByte = make([]byte, 8)
		binary.BigEndian.PutUint64(ExtendedPayloadByte, f.ExtendedPayloadLength)
	}
	result = append(result, ExtendedPayloadByte...)

	if f.Mask { // need to mask payload
		result = append(result, f.MaskingKey...)
		for i := uint64(0); i < f.GetMaxPayloadLength(); i++ {
			result = append(result, f.PayloadData[i]^f.MaskingKey[i%4])
		}
	} else {
		result = append(result, f.PayloadData...)
	}

	return result
}

/*
NewFrameConfig is everything NewFrame needs. A struct rather than positional
arguments so the two booleans cannot be swapped at a call site.

FIN marks the last frame of a message. Its zero value is false, so a caller who
omits it gets a frame the peer will wait for a continuation of — set it on every
single frame message.
*/
type NewFrameConfig struct {
	// Data is the payload, carried whole. Empty is legal: a zero length frame
	// is how an empty text message or a bare close goes out.
	PayloadData []byte
	// Opcode is 1 text, 2 binary, 8 close, 9 ping, 0xA pong, 0 continuation.
	Opcode uint8
	// Mask must be true on a frame a client sends and may be false on one a
	// server sends (RFC 6455 5.1).
	Mask bool
	// FIN marks this as the final frame of its message.
	FIN bool
}

/*
NewFrame builds one frame, encoding the payload length the way RFC 6455 5.2
requires: inline up to 125 bytes, a 16 bit extended length up to 65535, a 64 bit
one above that.

A single frame message sets FIN:

	f, _ := wlgows.NewFrame(wlgows.NewFrameConfig{
		Data: []byte("hello"), Opcode: 1, Mask: true, FIN: true,
	})

Fragmenting means leaving FIN off every frame but the last, and giving the
continuation frames opcode 0:

	head, _ := wlgows.NewFrame(wlgows.NewFrameConfig{Data: a, Opcode: 1, Mask: true})
	tail, _ := wlgows.NewFrame(wlgows.NewFrameConfig{Data: b, Opcode: 0, Mask: true, FIN: true})

Masking is applied by Seal, not here — MaskingKey is generated and stored while
PayloadData stays readable.
*/
func NewFrame(config NewFrameConfig) (*Frame, error) {
	return newFrame(config, newFrameDI{
		generateMaskingKey: GenerateMaskingKey,
	})
}

type newFrameDI struct {
	generateMaskingKey func() ([]byte, error)
}

func newFrame(config NewFrameConfig, di newFrameDI) (*Frame, error) {
	f := &Frame{FIN: config.FIN, Opcode: config.Opcode, PayloadData: config.PayloadData}
	if config.Mask {
		f.Mask = true
		key, err := di.generateMaskingKey()
		if err != nil {
			return f, err
		}
		f.MaskingKey = key
	}

	dataLength := uint64(len(config.PayloadData))
	switch {
	case dataLength <= uint64(125):
		f.PayloadLength = uint8(dataLength)
	case dataLength <= uint64(65535):
		f.PayloadLength = uint8(126)
		f.ExtendedPayloadLength = dataLength
	default:
		f.PayloadLength = uint8(127)
		f.ExtendedPayloadLength = dataLength
	}
	return f, nil
}

// 生成WebSocket的掩码密钥
func GenerateMaskingKey() ([]byte, error) {
	return generateMaskingKey(generateMaskingKeyDI{
		randRead: rand.Read,
	})
}

type generateMaskingKeyDI struct {
	randRead func(b []byte) (n int, err error)
}

func generateMaskingKey(di generateMaskingKeyDI) ([]byte, error) {
	key := make([]byte, 4) // WebSocket规范要求4个字节的掩码密钥
	_, err := di.randRead(key)
	if err != nil {
		return nil, err
	}
	return key, nil
}
