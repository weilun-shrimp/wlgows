package wlgows

import (
	"net"
	"net/http"
)

type Conn struct {
	net.Conn
	ClientRequest  *http.Request
	ServerResponse *http.Response
	di             connDI
}

type connDI struct {
	getFrameFromTCPConn func(conn net.Conn) (*Frame, error)
	getMsgFromTCPConn   func(conn net.Conn) (Msg, error)
}

func NewConn(c net.Conn, req *http.Request, res *http.Response) *Conn {
	return &Conn{
		Conn:           c,
		ClientRequest:  req,
		ServerResponse: res,
		di: connDI{
			getFrameFromTCPConn: GetFrameFromTCPConn,
			getMsgFromTCPConn:   GetMsgFromTCPConn,
		},
	}
}

func (c *Conn) GetNextFrame() (*Frame, error) {
	f, err := c.di.getFrameFromTCPConn(c.Conn)
	return f, err
}

func (c *Conn) GetNextMsg() (Msg, error) {
	m, err := c.di.getMsgFromTCPConn(c.Conn)
	return m, err
}

func (c *Conn) SendMsg(m *Msg) error {
	for _, f := range m.Frames {
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
