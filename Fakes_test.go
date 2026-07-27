package wlgows

import (
	"bytes"
	"io"
	"net"
	"time"
)

/*
Shared test doubles.

fakeConn is an in-memory net.Conn: reads drain readBuf, writes accumulate in
writeBuf. It lets every Conn/ClientConn/ServerConn path run without a socket.
*/
type fakeConn struct {
	readBuf  *bytes.Buffer
	writeBuf *bytes.Buffer
	readErr  error
	writeErr error
	closeErr error
	closed   bool
}

func newFakeConn(readData []byte) *fakeConn {
	return &fakeConn{
		readBuf:  bytes.NewBuffer(readData),
		writeBuf: &bytes.Buffer{},
	}
}

func (c *fakeConn) Read(b []byte) (int, error) {
	if c.readErr != nil {
		return 0, c.readErr
	}
	if c.readBuf.Len() == 0 {
		return 0, io.EOF
	}
	return c.readBuf.Read(b)
}

func (c *fakeConn) Write(b []byte) (int, error) {
	if c.writeErr != nil {
		return 0, c.writeErr
	}
	return c.writeBuf.Write(b)
}

func (c *fakeConn) Close() error {
	c.closed = true
	return c.closeErr
}

func (c *fakeConn) written() []byte { return c.writeBuf.Bytes() }

type fakeAddr struct{}

func (fakeAddr) Network() string { return "fake" }
func (fakeAddr) String() string  { return "fake://addr" }

func (c *fakeConn) LocalAddr() net.Addr                { return fakeAddr{} }
func (c *fakeConn) RemoteAddr() net.Addr               { return fakeAddr{} }
func (c *fakeConn) SetDeadline(t time.Time) error      { return nil }
func (c *fakeConn) SetReadDeadline(t time.Time) error  { return nil }
func (c *fakeConn) SetWriteDeadline(t time.Time) error { return nil }

// fixedRandRead builds a randRead replacement that fills the buffer with a
// repeating pattern, making every key/mask deterministic.
func fixedRandRead(pattern ...byte) func(b []byte) (int, error) {
	return func(b []byte) (int, error) {
		for i := range b {
			b[i] = pattern[i%len(pattern)]
		}
		return len(b), nil
	}
}

// scriptedReadTCPConn returns each chunk in order, then io.EOF.
func scriptedReadTCPConn(chunks ...[]byte) func(net.Conn, uint64) ([]byte, error) {
	i := 0
	return func(_ net.Conn, _ uint64) ([]byte, error) {
		if i >= len(chunks) {
			return nil, io.EOF
		}
		c := chunks[i]
		i++
		return c, nil
	}
}
