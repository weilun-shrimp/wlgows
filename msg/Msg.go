package msg

import (
	"crypto/rand"
	"net"
	"strings"

	"github.com/weilun-shrimp/wlgows/frame"
)

type Msg struct {
	Frames []*frame.Frame
}

/*
Reference: https://studygolang.com/articles/12796
用傳統的string([]byte)方法可能會發生字串符段連接問題, 因為一個UTF-8中文是3bytes，如果剛好切一半在下一個frame就完了
而且很可能會有超大量字串，所以要用效能最好的strings.Builder底層自動分配資料至內部slice再組成string
*/
func (msg *Msg) GetStr() string {
	var builder strings.Builder
	for _, f := range msg.Frames {
		builder.Write(f.PayloadData)
	}
	return builder.String()
}

func GetMsgFromTCPConn(conn net.Conn) (Msg, error) {
	msg := Msg{}
	for {
		f, err := frame.GetFrameFromTCPConn(conn)
		if err != nil {
			return msg, err
		}
		msg.Frames = append(msg.Frames, f)
		if f.FIN {
			break
		}
	}
	return msg, nil
}

func NewMsg(data []byte, opcode uint8, need_mask bool) (*Msg, error) {
	msg := new(Msg)
	dataLength := uint64(len(data))
	for dataLength > uint64(0) {
		f := new(frame.Frame)
		f.Opcode = opcode
		if need_mask {
			f.Mask = true
			var err error
			f.MaskingKey, err = generateMaskingKey()
			if err != nil {
				return msg, err
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
		msg.Frames = append(msg.Frames, f)
	}
	return msg, nil
}

// 生成WebSocket的掩码密钥
func generateMaskingKey() ([]byte, error) {
	key := make([]byte, 4) // WebSocket规范要求4个字节的掩码密钥
	_, err := rand.Read(key)
	if err != nil {
		return nil, err
	}
	return key, nil
}
