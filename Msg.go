package wlgows

import (
	"crypto/rand"
	"net"
	"strings"
)

type Msg struct {
	Frames []*Frame
}

/*
Reference: https://studygolang.com/articles/12796
用傳統的string([]byte)方法可能會發生字串符段連接問題, 因為一個UTF-8中文是3bytes，如果剛好切一半在下一個frame就完了
而且很可能會有超大量字串，所以要用效能最好的strings.Builder底層自動分配資料至內部slice再組成string
*/
func (m *Msg) GetStr() string {
	var builder strings.Builder
	builder.Grow(m.payloadLength()) // one exact allocation instead of regrowth
	for _, f := range m.Frames {
		builder.Write(f.PayloadData)
	}
	return builder.String()
}

/*
GetBytes returns the payload of every frame joined into one slice.

Prefer it over []byte(GetStr()) whenever the caller wants bytes — the
conversion copies the assembled payload a second time, so the byte path costs
two full-size buffers where this costs one. A binary (opcode 2) message has no
reason to become a string at all.

The result is a fresh copy, so mutating it never reaches Frames. Callers
willing to trade that safety for zero allocations can read a single frame
message straight off m.Frames[0].PayloadData, but must then treat the slice as
owned by the Msg.
*/
func (m *Msg) GetBytes() []byte {
	result := make([]byte, 0, m.payloadLength())
	for _, f := range m.Frames {
		result = append(result, f.PayloadData...)
	}
	return result
}

// payloadLength is the summed size of every frame payload, so the assemblers
// above can allocate once at the exact size.
func (m *Msg) payloadLength() int {
	total := 0
	for _, f := range m.Frames {
		total += len(f.PayloadData)
	}
	return total
}

func GetMsgFromTCPConn(conn net.Conn) (Msg, error) {
	return getMsgFromTCPConn(conn, getMsgFromTCPConnDI{
		getFrameFromTCPConn: GetFrameFromTCPConn,
	})
}

type getMsgFromTCPConnDI struct {
	getFrameFromTCPConn func(conn net.Conn) (*Frame, error)
}

func getMsgFromTCPConn(conn net.Conn, di getMsgFromTCPConnDI) (Msg, error) {
	m := Msg{}
	for {
		f, err := di.getFrameFromTCPConn(conn)
		if err != nil {
			return m, err
		}
		m.Frames = append(m.Frames, f)
		if f.FIN {
			break
		}
	}
	return m, nil
}

func NewMsg(data []byte, opcode uint8, need_mask bool) (*Msg, error) {
	return newMsg(data, opcode, need_mask, newMsgDI{
		generateMaskingKey: GenerateMaskingKey,
	})
}

type newMsgDI struct {
	generateMaskingKey func() ([]byte, error)
}

func newMsg(data []byte, opcode uint8, need_mask bool, di newMsgDI) (*Msg, error) {
	m := new(Msg)
	dataLength := uint64(len(data))
	for dataLength > uint64(0) {
		f := new(Frame)
		f.Opcode = opcode
		if need_mask {
			f.Mask = true
			var err error
			f.MaskingKey, err = di.generateMaskingKey()
			if err != nil {
				return m, err
			}
		}

		if dataLength > uint64(18446744073709551612) {
			f.FIN = false
			f.PayloadLength = 127
			f.ExtendedPayloadLength = uint64(18446744073709551612)
			dataLength -= uint64(18446744073709551612)

			f.PayloadData = data[:9223372036854775806]
			data = data[9223372036854775806:]
			f.PayloadData = append(f.PayloadData, data[:9223372036854775806]...)
			data = data[9223372036854775806:]
		} else {
			f.FIN = true
			f.PayloadData = data
			if dataLength <= uint64(125) {
				f.PayloadLength = uint8(dataLength)
			} else if dataLength <= uint64(65535) {
				f.PayloadLength = uint8(126)
				f.ExtendedPayloadLength = dataLength
			} else {
				f.PayloadLength = uint8(127)
				f.ExtendedPayloadLength = dataLength
			}
			dataLength = 0
		}
		m.Frames = append(m.Frames, f)
	}
	return m, nil
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
