package connection

import (
	"net"
	"net/http"

	"github.com/weilun-shrimp/wlgows/frame"
	"github.com/weilun-shrimp/wlgows/msg"
)

type Conn struct {
	net.Conn
	ClientRequest  *http.Request
	ServerResponse *http.Response
}

func (c *Conn) GetNextFrame() (*frame.Frame, error) {
	frame, err := frame.GetFrameFromTCPConn(c.Conn)
	return frame, err
}

func (c *Conn) GetNextMsg() (msg.Msg, error) {
	msg, err := msg.GetMsgFromTCPConn(c.Conn)
	return msg, err
}

func (c *Conn) SendMsg(msg *msg.Msg) error {
	for _, f := range msg.Frames {
		// net TCP conn 方法
		_, err := c.Conn.Write(f.Seal())
		if err != nil {
			// fmt.Println("Error writing:", err.Error())
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

func (c *Conn) Close() error {
	if err := c.Conn.Close(); err != nil {
		return err
	}
	if c.ClientRequest != nil {
		c.ClientRequest.Close = true
	}
	if c.ServerResponse != nil {
		c.ServerResponse.Close = true
	}
	return nil
}
