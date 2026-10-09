package wlgows

import (
	"bufio"
	"net"
	"sync"
	"time"
)

type Conn struct {
	net.Conn

	// Frame reads go through this, never through the embedded net.Conn
	// directly. It must be the same *bufio.Reader used to read the handshake
	// off this connection — http.ReadRequest/http.ReadResponse can buffer
	// bytes past the header block (the start of the first frame), and a
	// fresh reader here would strand them. See HandShakeClient.go's
	// ClientHandShake doc comment.
	reader *bufio.Reader

	// Whether the frames this Conn builds are masked. RFC 6455 5.1 leaves no
	// choice — a client masks every frame it sends, a server masks none, and a
	// peer must fail the connection on the wrong one — so it is settled once at
	// construction rather than at each send. ClientHandShake passes true,
	// ServerHandShake false.
	maskSendFrame bool

	// The opcode of the message being sent by a long data transmission. Zero
	// means no message is open: a message never starts with
	// OpcodeContinuation (0).
	currentTransmitDataMsgOpcode uint8

	// Whether the message's first frame has been sent. 5.4: only the first
	// frame carries the message's opcode; every frame after it is
	// OpcodeContinuation.
	currentTransmitDataMsgOpened bool

	// Whether a close has been written. RFC 6455 5.5.1 puts the closing
	// handshake at one close each way, so this is what says the answer is
	// already spent.
	//
	// 5.5.1 allows no data frame after a close, so after one every data frame
	// and every second close is refused (bufferedWriteFrame); a ping or pong
	// still goes out, as 5.5.2 wants a ping answered until the peer's close
	// arrives. It is checked under writeLocker, the lock it is set under, so a
	// check and the write it guards cannot cross. It is set by any close that
	// is written, SendFrame's included, and only once the write succeeds.
	closeSent bool

	// Frame writes go through this, so small frames share a Write. It writes to
	// the embedded net.Conn, and is at least minWriterSize. Guarded by
	// writeLocker. Replaced only by RenewWriter.
	writer *bufio.Writer

	di connDI
}

type connDI struct {
	getFrameFromReader    func(r *bufio.Reader, maxByteLength uint64) (*Frame, error)
	newControlFrame       func(config NewControlFrameConfig) (*Frame, error)
	newDataFrame          func(config NewFrameConfig) (*Frame, error)
	fillMaskingKey        func(key *[4]byte) error
	loop                  func(trigger func(stop_signal chan<- struct{}), interval time.Duration)
	writeLocker           sync.Locker // Guards writer and closeSent; held per frame.
	readLocker            sync.Locker // Protect reading one frame to TCP conn.
	dataFramesWriteLocker sync.Locker // Protect writing data frames to TCP conn.
}

/*
NewConn wraps an already handshaken connection.

r must be the same *bufio.Reader the handshake was read through, so any bytes
it buffered past the header block are not stranded — see Conn.reader.

w is the writer every frame goes through. It must write to c. A hijacked
connection's bufRW.Writer works, and so does bufio.NewWriterSize(c, size). One
smaller than 14 bytes, the longest header, is flushed and replaced by a new
14 byte writer on c. If that flush fails, no Conn is returned.

maskSendFrame is RFC 6455 5.1 and follows from which side this is: pass true
from a client, false from a server. ClientHandShake and ServerHandShake fill it
in, so it is only yours to answer when you build a Conn directly.
*/
func NewConn(c net.Conn, r *bufio.Reader, w *bufio.Writer, maskSendFrame bool) (*Conn, error) {
	w, err := fitWriter(c, w)
	if err != nil {
		return nil, err
	}
	return &Conn{
		Conn:          c,
		reader:        r,
		maskSendFrame: maskSendFrame,
		writer:        w,
		di: connDI{
			getFrameFromReader:    GetFrameFromReader,
			newControlFrame:       NewControlFrame,
			newDataFrame:          NewDataFrame,
			fillMaskingKey:        FillMaskingKey,
			loop:                  Loop,
			writeLocker:           &sync.Mutex{},
			readLocker:            &sync.Mutex{},
			dataFramesWriteLocker: &sync.Mutex{},
		},
	}, nil
}

/*
GetNextFrame reads one frame, refusing any whose header declares a payload
larger than maxByteLength. Pass 0 for no limit.

One frame is the whole unit this returns: a message split across frames is
assembled by the caller, appending into a DataFrames until a frame with FIN set
arrives. That is what lets a caller stream a huge message somewhere else instead
of holding it:

	var frames wlgows.DataFrames
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
the connection cannot be reused — see GetFrameFromReader.
*/
func (c *Conn) GetNextFrame(maxByteLength uint64) (*Frame, error) {
	c.di.readLocker.Lock()
	defer c.di.readLocker.Unlock()
	f, err := c.di.getFrameFromReader(c.reader, maxByteLength)
	return f, err
}
