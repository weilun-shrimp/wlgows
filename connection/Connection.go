package connection

import (
	"fmt"
	"net"
	"net/http"

	"github.com/weilun-shrimp/wlgows/frame"
	"github.com/weilun-shrimp/wlgows/msg"
)

type Conn struct {
	net.Conn
	ClientRequest *http.Request
}

func (c *Conn) GetNextFrame() (*frame.Frame, error) {
	frame, err := frame.GetFrameFromTCPConn(c.Conn)
	return frame, err
}

func (c *Conn) GetNextMsg() (msg.Msg, error) {
	msg, err := msg.GetMsgFromTCPConn(c.Conn)
	return msg, err
}

func (c *Conn) SendUnMaskedTextMsg(text string) error {
	msg, err := msg.GenUnMaskedTextMsg([]byte(text))
	if err != nil {
		return err
	}
	for _, f := range msg.Frames {
		// net TCP conn 方法
		_, err := c.Conn.Write(f.Seal())
		if err != nil {
			fmt.Println("Error writing:", err.Error())
			return err
		}

		// io 方法
		// _, err := io.WriteString(this.TCP_connection, string(f.Seal()))
		// if err != nil {
		// 	fmt.Println("Error writing:", err.Error())
		// 	return err
		// }
	}
	return nil
}
