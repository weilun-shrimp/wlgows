package wlgows

import (
	"net"
	"net/http"
	"sync"
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
	writeLocker         sync.Locker
	readLocker          sync.Locker
}

func NewConn(c net.Conn, req *http.Request, res *http.Response) *Conn {
	return &Conn{
		Conn:           c,
		ClientRequest:  req,
		ServerResponse: res,
		di: connDI{
			getFrameFromTCPConn: GetFrameFromTCPConn,
			getMsgFromTCPConn:   GetMsgFromTCPConn,
			writeLocker:         &sync.Mutex{},
			readLocker:          &sync.Mutex{},
		},
	}
}

func (c *Conn) GetNextFrame() (*Frame, error) {
	c.di.readLocker.Lock()
	defer c.di.readLocker.Unlock()
	f, err := c.di.getFrameFromTCPConn(c.Conn)
	return f, err
}

func (c *Conn) GetNextMsg() (Msg, error) {
	c.di.readLocker.Lock()
	defer c.di.readLocker.Unlock()
	m, err := c.di.getMsgFromTCPConn(c.Conn)
	return m, err
}

func (c *Conn) SendMsg(m *Msg) error {
	c.di.writeLocker.Lock()
	defer c.di.writeLocker.Unlock()
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
