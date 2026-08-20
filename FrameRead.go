package wlgows

import (
	"encoding/binary"
	"fmt"
	"io"
)

func ReadFromReader(r io.Reader, maxLen uint64) ([]byte, error) {
	return readFromReader(r, maxLen, readFromReaderDI{
		ioReadFull: io.ReadFull,
	})
}

type readFromReaderDI struct {
	ioReadFull func(r io.Reader, buf []byte) (n int, err error)
}

func readFromReader(r io.Reader, maxLen uint64, di readFromReaderDI) ([]byte, error) {
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
	_, err := di.ioReadFull(r, bytes)
	// fmt.Printf("%+v\n", "readFromReader n: "+strconv.FormatInt(int64(n), 10))
	if err != nil {
		return bytes, err
	}
	return bytes, nil
}

/*
GetFrameFromReader reads one frame, refusing any whose header declares a
payload larger than maxByteLength. Pass 0 for no limit.

The check happens after the header is parsed and before the payload is
allocated, which is the only place it helps: a peer can claim a 10 GB payload in
a 10 byte header, and without the guard that claim becomes a 10 GB make() before
a single payload byte has arrived.

Exceeding the limit returns an error wrapping ErrFrameByteLengthExceeded and
leaves the payload unread, so the stream is desynced and the connection cannot
be reused — send RFC 6455 close code 1009 and close it.
*/
func GetFrameFromReader(r io.Reader, maxByteLength uint64) (*Frame, error) {
	return getFrameFromReader(r, maxByteLength, getFrameFromReaderDI{
		readFromReader: ReadFromReader,
	})
}

type getFrameFromReaderDI struct {
	readFromReader func(r io.Reader, maxLen uint64) ([]byte, error)
}

func getFrameFromReader(r io.Reader, maxByteLength uint64, di getFrameFromReaderDI) (*Frame, error) {
	f := new(Frame)
	firstSec, err := di.readFromReader(r, 2)
	if err != nil {
		// fmt.Printf("%+v\n", "Error GetFrameFromReader first")
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
		extendedPayloadLength, err := di.readFromReader(r, 2)
		if err != nil {
			// fmt.Printf("%+v\n", "Error GetFrameFromReader PayloadLength 126")
			return f, err
		}
		f.ExtendedPayloadLength = uint64(binary.BigEndian.Uint16(extendedPayloadLength))
	case 127:
		extendedPayloadLength, err := di.readFromReader(r, 8)
		if err != nil {
			// fmt.Printf("%+v\n", "Error GetFrameFromReader PayloadLength 127")
			return f, err
		}
		f.ExtendedPayloadLength = uint64(binary.BigEndian.Uint64(extendedPayloadLength))

	}

	if f.Mask {
		f.MaskingKey, err = di.readFromReader(r, 4)
		if err != nil {
			// fmt.Printf("%+v\n", "Error GetFrameFromReader maskingKeyByte")
			return f, err
		}
	}

	// Guard before the allocation, never after: readFromReader's first act is
	// make([]byte, maxLen), so by the time it returns the damage is done.
	if maxByteLength > 0 && f.GetMaxPayloadLength() > maxByteLength {
		return f, fmt.Errorf(
			"frame header declares %d bytes against a %d byte max, payload left unread: %w",
			f.GetMaxPayloadLength(), maxByteLength, ErrFrameByteLengthExceeded,
		)
	}

	f.PayloadData, err = di.readFromReader(r, f.GetMaxPayloadLength())
	if err != nil {
		// fmt.Printf("%+v\n", "Error GetFrameFromReader payloadByte")
		return f, err
	}

	if f.Mask { // need to unmask payload
		for i := uint64(0); i < f.GetMaxPayloadLength(); i++ {
			f.PayloadData[i] = f.PayloadData[i] ^ f.MaskingKey[i%4]
		}
	}

	return f, nil
}
