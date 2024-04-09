package frame

import (
	"encoding/binary"
)

type Frame struct {
	FIN                   bool
	RSV1                  bool
	RSV2                  bool
	RSV3                  bool
	Opcode                byte // 7 bit
	Mask                  bool
	PayloadLength         byte
	ExtendedPayloadLength uint64
	MaskingKey            []byte // mask == 1
	PayloadData           []byte
}

func (this *Frame) GetMaxPayloadLength() uint64 {
	if this.PayloadLength == 127 || this.PayloadLength == 126 {
		return this.ExtendedPayloadLength
	} else {
		return uint64(this.PayloadLength)
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
func (this *Frame) Seal() []byte {
	result := []byte{
		boolToInt(this.FIN)<<7 + boolToInt(this.RSV1)<<6 + boolToInt(this.RSV2)<<5 + boolToInt(this.RSV3)<<4 + this.Opcode&15,
		boolToInt(this.Mask)<<7 + this.PayloadLength,
	}

	var ExtendedPayloadByte []byte
	if this.PayloadLength == 126 {
		ExtendedPayloadByte = make([]byte, 2)
		binary.BigEndian.PutUint16(ExtendedPayloadByte, uint16(this.ExtendedPayloadLength))
	} else if this.PayloadLength == 127 {
		ExtendedPayloadByte = make([]byte, 8)
		binary.BigEndian.PutUint64(ExtendedPayloadByte, this.ExtendedPayloadLength)
	}
	result = append(result, ExtendedPayloadByte...)

	if this.Mask {
		result = append(result, this.MaskingKey...)
		for i := uint64(0); i < this.GetMaxPayloadLength(); i++ {
			result = append(result, this.PayloadData[i]^this.MaskingKey[i%4])
		}
	} else {
		result = append(result, this.PayloadData...)
	}

	return result
}
