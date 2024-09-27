package connection

import (
	"fmt"
	"net"

	"github.com/weilun-shrimp/wlgows/frame"
	"github.com/weilun-shrimp/wlgows/msg"
)

type Conn struct {
	TCP_connection net.Conn
	ClientHeader   map[string]string // hand shake header from the other site
	ServerHeader   map[string]string
}

func (c *Conn) Close() {
	c.TCP_connection.Close()
}

func (c *Conn) GetNextFrame() (*frame.Frame, error) {
	frame, err := frame.GetFrameFromTCPConn(c.TCP_connection)
	return frame, err
}

func (c *Conn) GetNextMsg() (msg.Msg, error) {
	msg, err := msg.GetMsgFromTCPConn(c.TCP_connection)
	if err != nil {
		fmt.Printf("%+v\n", "GetNextMsg Error")
	}
	return msg, err
}

func (c *Conn) SendUnMaskedTextMsg(text string) error {
	msg, err := msg.GenUnMaskedTextMsg([]byte(text))
	if err != nil {
		return err
	}
	for _, f := range msg.Frames {
		// net TCP conn 方法
		_, err := c.TCP_connection.Write(f.Seal())
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
