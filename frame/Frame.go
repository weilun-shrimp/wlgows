package frame

import (
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
		boolToInt(f.FIN)<<7 + boolToInt(f.RSV1)<<6 + boolToInt(f.RSV2)<<5 + boolToInt(f.RSV3)<<4 + f.Opcode&15,
		boolToInt(f.Mask)<<7 + f.PayloadLength,
	}

	var ExtendedPayloadByte []byte
	if f.PayloadLength == 126 {
		ExtendedPayloadByte = make([]byte, 2)
		binary.BigEndian.PutUint16(ExtendedPayloadByte, uint16(f.ExtendedPayloadLength))
	} else if f.PayloadLength == 127 {
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
