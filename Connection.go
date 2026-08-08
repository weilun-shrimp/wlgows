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
	getFrameFromTCPConn func(conn net.Conn, maxByteLength uint64) (*Frame, error)
	newControlFrame     func(config NewControlFrameConfig) (*Frame, error)
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
			newControlFrame:     NewControlFrame,
			writeLocker:         &sync.Mutex{},
			readLocker:          &sync.Mutex{},
		},
	}
}

/*
GetNextFrame reads one frame, refusing any whose header declares a payload
larger than maxByteLength. Pass 0 for no limit.

One frame is the whole unit this returns: a message split across frames is
assembled by the caller, appending into a Frames until a frame with FIN set
arrives. That is what lets a caller stream a huge message somewhere else instead
of holding it:

	var frames wlgows.Frames
	for {
		f, err := conn.GetNextFrame(10 << 20)
		if err != nil {
			return err
		}
		if !IsDataOpcode(f.Opcode) { // control or reserved, never message payload
			return nil
		}
		frames = append(frames, f)
		if f.FIN {
			break
		}
	}

Exceeding maxByteLength leaves the payload unread and the stream desynced, so
the connection cannot be reused — see GetFrameFromTCPConn.
*/
func (c *Conn) GetNextFrame(maxByteLength uint64) (*Frame, error) {
	c.di.readLocker.Lock()
	defer c.di.readLocker.Unlock()
	f, err := c.di.getFrameFromTCPConn(c.Conn, maxByteLength)
	return f, err
}

// writeFrame seals a frame and puts it on the socket, holding writeLocker for
// the whole write so frames from concurrent senders cannot interleave.
func (c *Conn) writeFrame(f *Frame) error {
	c.di.writeLocker.Lock()
	defer c.di.writeLocker.Unlock()
	_, err := c.Conn.Write(f.Seal())
	return err
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
