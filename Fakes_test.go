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
	onWrite  func() // called at the top of Write, before writeErr is checked
}

func newFakeConn(readData []byte) *fakeConn {
	return &fakeConn{
		readBuf:  bytes.NewBuffer(readData),
		writeBuf: &bytes.Buffer{},
	}
}

func (conn *fakeConn) Read(data []byte) (int, error) {
	if conn.readErr != nil {
		return 0, conn.readErr
	}
	if conn.readBuf.Len() == 0 {
		return 0, io.EOF
	}
	return conn.readBuf.Read(data)
}

func (conn *fakeConn) Write(data []byte) (int, error) {
	if conn.onWrite != nil {
		conn.onWrite()
	}
	if conn.writeErr != nil {
		return 0, conn.writeErr
	}
	return conn.writeBuf.Write(data)
}

func (conn *fakeConn) Close() error {
	conn.closed = true
	return conn.closeErr
}

func (conn *fakeConn) written() []byte { return conn.writeBuf.Bytes() }

type fakeAddr struct{}

func (fakeAddr) Network() string { return "fake" }
func (fakeAddr) String() string  { return "fake://addr" }

func (conn *fakeConn) LocalAddr() net.Addr                { return fakeAddr{} }
func (conn *fakeConn) RemoteAddr() net.Addr               { return fakeAddr{} }
func (conn *fakeConn) SetDeadline(t time.Time) error      { return nil }
func (conn *fakeConn) SetReadDeadline(t time.Time) error  { return nil }
func (conn *fakeConn) SetWriteDeadline(t time.Time) error { return nil }

/*
fakeLocker counts Lock/Unlock so tests can assert a section was guarded, and how
widely. Substituted for the *sync.Mutex the constructor puts in di.

It also tracks whether the lock is currently held, so misuse counts the two
orderings a real sync.Mutex would reject: unlocking one that is not held, and
locking one that already is. Counting alone cannot catch those — an Unlock/Lock
pair still totals 1 and 1.
*/
type fakeLocker struct {
	locks   int
	unlocks int
	held    bool
	misuse  int
}

func (locker *fakeLocker) Lock() {
	if locker.held {
		locker.misuse++
	}
	locker.held = true
	locker.locks++
}

func (locker *fakeLocker) Unlock() {
	if !locker.held {
		locker.misuse++
	}
	locker.held = false
	locker.unlocks++
}

// ok reports the lock being taken exactly want times, each properly paired and
// released by the time the call returned.
func (locker *fakeLocker) ok(want int) bool {
	return locker.locks == want && locker.unlocks == want && locker.misuse == 0 && !locker.held
}

// fixedRandRead builds a randRead replacement that fills the buffer with a
// repeating pattern, making every key/mask deterministic.
func fixedRandRead(pattern ...byte) func(data []byte) (int, error) {
	return func(data []byte) (int, error) {
		for i := range data {
			data[i] = pattern[i%len(pattern)]
		}
		return len(data), nil
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
