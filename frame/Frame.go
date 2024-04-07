package frame

import (
	"encoding/binary"
	"fmt"
	"io"
	"net"
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

func readTCPConn(conn net.Conn, maxLen uint64) ([]byte, error) {
	/*
		Reference: https://zhuanlan.zhihu.com/p/455921908
		用net包裡原生的conn.Read方法會造成io不同步問題，會發生確實拿到如此多的資料但是因為io阻塞後面read會錯亂
		所以要用最高效能的io.ReadFull方法，不只比較較簡短，速度更快
	*/
	// origin net.TCPConn.Read
	// var standardLength uint64 = 0
	// bytes := make([]byte, maxLen)
	// for {
	// 	n, err := conn.Read(bytes)
	// 	if err != nil {
	// 		return bytes, err
	// 	}
	// 	standardLength += uint64(n)
	// 	if standardLength >= maxLen {
	// 		break
	// 	}
	// }
	// return bytes, nil

	// io.ReadFull
	bytes := make([]byte, maxLen)
	_, err := io.ReadFull(conn, bytes)
	// fmt.Printf("%+v\n", "readTCPConn n: "+strconv.FormatInt(int64(n), 10))
	if err != nil {
		return bytes, err
	}
	return bytes, nil
}

func GetFrameFromTCPConn(conn net.Conn) (*Frame, error) {
	frame := new(Frame)
	firstSec, err := readTCPConn(conn, 2)
	if err != nil {
		fmt.Printf("%+v\n", "Error GetFrameFromTCPConn first")
		return frame, err
	}
	frame.FIN = firstSec[0]>>7 == 1
	frame.RSV1 = firstSec[0]>>6&1 == 1
	frame.RSV2 = firstSec[0]>>5&1 == 1
	frame.RSV3 = firstSec[0]>>4&1 == 1
	frame.Opcode = firstSec[0] & 15
	// fmt.Printf("FIN:%v \n", strconv.FormatBool(frame.FIN))

	frame.PayloadLength = uint8(firstSec[1] & 0x7F)
	frame.Mask = firstSec[1]>>7 == 1

	if frame.PayloadLength == 127 {
		extendedPayloadLength, err := readTCPConn(conn, 8)
		if err != nil {
			fmt.Printf("%+v\n", "Error GetFrameFromTCPConn PayloadLength 127")
			return frame, err
		}
		frame.ExtendedPayloadLength = uint64(binary.BigEndian.Uint64(extendedPayloadLength))
	} else if frame.PayloadLength == 126 {
		extendedPayloadLength, err := readTCPConn(conn, 2)
		if err != nil {
			fmt.Printf("%+v\n", "Error GetFrameFromTCPConn PayloadLength 126")
			return frame, err
		}
		frame.ExtendedPayloadLength = uint64(binary.BigEndian.Uint16(extendedPayloadLength))
	}

	if frame.Mask == true {
		frame.MaskingKey, err = readTCPConn(conn, 4)
		if err != nil {
			fmt.Printf("%+v\n", "Error GetFrameFromTCPConn maskingKeyByte")
			return frame, err
		}
	}

	frame.PayloadData, err = readTCPConn(conn, frame.GetMaxPayloadLength())
	if err != nil {
		fmt.Printf("%+v\n", "Error GetFrameFromTCPConn payloadByte")
		return frame, err
	}

	for i := uint64(0); i < frame.GetMaxPayloadLength(); i++ {
		if frame.Mask == true {
			frame.PayloadData[i] = frame.PayloadData[i] ^ frame.MaskingKey[i%4]
		}
	}

	return frame, nil
}
