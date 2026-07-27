package wlgows

import (
	"encoding/binary"
	"io"
	"net"
)

func ReadTCPConn(conn net.Conn, maxLen uint64) ([]byte, error) {
	return readTCPConn(conn, maxLen, readTCPConnDI{
		ioReadFull: io.ReadFull,
	})
}

type readTCPConnDI struct {
	ioReadFull func(r io.Reader, buf []byte) (n int, err error)
}

func readTCPConn(conn net.Conn, maxLen uint64, di readTCPConnDI) ([]byte, error) {
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
	_, err := di.ioReadFull(conn, bytes)
	// fmt.Printf("%+v\n", "readTCPConn n: "+strconv.FormatInt(int64(n), 10))
	if err != nil {
		return bytes, err
	}
	return bytes, nil
}

func GetFrameFromTCPConn(conn net.Conn) (*Frame, error) {
	return getFrameFromTCPConn(conn, getFrameFromTCPConnDI{
		readTCPConn: ReadTCPConn,
	})
}

type getFrameFromTCPConnDI struct {
	readTCPConn func(conn net.Conn, maxLen uint64) ([]byte, error)
}

func getFrameFromTCPConn(conn net.Conn, di getFrameFromTCPConnDI) (*Frame, error) {
	f := new(Frame)
	firstSec, err := di.readTCPConn(conn, 2)
	if err != nil {
		// fmt.Printf("%+v\n", "Error GetFrameFromTCPConn first")
		return f, err
	}
	f.FIN = firstSec[0]>>7 == 1
	f.RSV1 = firstSec[0]>>6&1 == 1
	f.RSV2 = firstSec[0]>>5&1 == 1
	f.RSV3 = firstSec[0]>>4&1 == 1
	f.Opcode = firstSec[0] & 15
	// fmt.Printf("FIN:%v \n", strconv.FormatBool(f.FIN))

	f.PayloadLength = uint8(firstSec[1] & 0x7F)
	f.Mask = firstSec[1]>>7 == 1

	switch f.PayloadLength {
	case 126:
		extendedPayloadLength, err := di.readTCPConn(conn, 2)
		if err != nil {
			// fmt.Printf("%+v\n", "Error GetFrameFromTCPConn PayloadLength 126")
			return f, err
		}
		f.ExtendedPayloadLength = uint64(binary.BigEndian.Uint16(extendedPayloadLength))
	case 127:
		extendedPayloadLength, err := di.readTCPConn(conn, 8)
		if err != nil {
			// fmt.Printf("%+v\n", "Error GetFrameFromTCPConn PayloadLength 127")
			return f, err
		}
		f.ExtendedPayloadLength = uint64(binary.BigEndian.Uint64(extendedPayloadLength))

	}

	if f.Mask {
		f.MaskingKey, err = di.readTCPConn(conn, 4)
		if err != nil {
			// fmt.Printf("%+v\n", "Error GetFrameFromTCPConn maskingKeyByte")
			return f, err
		}
	}

	f.PayloadData, err = di.readTCPConn(conn, f.GetMaxPayloadLength())
	if err != nil {
		// fmt.Printf("%+v\n", "Error GetFrameFromTCPConn payloadByte")
		return f, err
	}

	if f.Mask { // need to unmask payload
		for i := uint64(0); i < f.GetMaxPayloadLength(); i++ {
			f.PayloadData[i] = f.PayloadData[i] ^ f.MaskingKey[i%4]
		}
	}

	return f, nil
}
