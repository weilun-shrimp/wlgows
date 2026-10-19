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

	// Frame writes go through this, so small frames share a Write. It writes to
	// the embedded net.Conn, and is at least minWriterSize. Guarded by
	// writeLocker. Replaced only by RenewWriter.
	writer *bufio.Writer

	// Whether the peer is a client, settled once at construction. RFC 6455 5.1
	// leaves no choice — a client masks every frame it sends, a server masks
	// none, and a peer must fail the connection on the wrong one — so the frames
	// this Conn builds are masked when this is false.
	peerIsClient bool

	// The opcode of the message being sent by a long data transmission. Zero
	// means no message is open: a message never starts with
	// OpcodeContinuation (0).
	currentTransmitDataMsgOpcode uint8

	// Whether the message's first frame has been sent. 5.4: only the first
	// frame carries the message's opcode; every frame after it is
	// OpcodeContinuation.
	currentTransmitDataMsgOpened bool

	// Whether the message is deflated and its stream is not yet ended: set by
	// Start, cleared once End has deflated the last piece. Release drops the
	// context while it is set.
	currentTransmitDataMsgDeflating bool

	// Whether a close has been written. The closing handshake is one close each
	// way (RFC 6455 1.4, 7.1.5), so this is what says the answer is already
	// spent.
	//
	// 5.5.1 allows no data frame after a close, so after one every data frame
	// and every second close is refused (bufferedWriteFrame); a ping or pong
	// still goes out, as 5.5.2 wants a ping answered until the peer's close
	// arrives. It is checked under writeLocker, the lock it is set under, so a
	// check and the write it guards cannot cross. It is set by any close that
	// is written, SendFrame's included, and only once the write succeeds.
	closeSent bool

	// The permessage-deflate agreement from the handshake. nil means none was
	// agreed, so nothing is compressed (RFC 7692 5).
	permessageDeflateAgreedConfig *PermessageDeflateConfig
	// The deflaters for data messages sent deflated. A message without context
	// takeover goes through noContextDeflater, any other through
	// contextDeflater (RFC 7692 7.1.1). Each guards itself; deflate runs under
	// dataFramesWriteLocker.
	contextDeflater   connContextDeflater
	noContextDeflater connNoContextDeflater
	// The inflaters for data messages received deflated. A whole message from
	// a peer without context takeover goes through noContextInflater; one
	// from any other peer, and every message inflated frame by frame, through
	// contextInflater (RFC 7692 7.1.1). Each guards itself.
	contextInflater   connContextInflater
	noContextInflater connNoContextInflater

	di connDI
}

type connDI struct {
	getFrameHeader        func(r *bufio.Reader) (*Frame, error)
	fillFramePayload      func(f *Frame, r *bufio.Reader) error
	newControlFrame       func(config NewControlFrameConfig) (*Frame, error)
	newDataFrame          func(config NewFrameConfig) (*Frame, error)
	loop                  func(trigger func(stop_signal chan<- struct{}), interval time.Duration)
	writeLocker           sync.Locker // Guards writer and closeSent; held per frame.
	readLocker            sync.Locker // Guards reading one frame, header and payload; see LockRead.
	dataFramesWriteLocker sync.Locker // Protect writing data frames to TCP conn.
}

// NewConnConfig is what NewConn needs to know about the handshake it follows.
type NewConnConfig struct {
	// PeerIsClient is true when the peer is a client: false from a client,
	// true from a server. It decides masking (RFC 6455 5.1).
	PeerIsClient bool

	// PermessageDeflate is the agreed config the handshake returned. nil means
	// permessage-deflate was not agreed.
	PermessageDeflate *PermessageDeflateConfig
}

/*
NewConn wraps an already handshaken connection.

r must be the same *bufio.Reader the handshake was read through, so any bytes
it buffered past the header block are not stranded — see Conn.reader.

w is the writer every frame goes through. It must write to c. A hijacked
connection's bufRW.Writer works, and so does bufio.NewWriterSize(c, size). One
smaller than 14 bytes, the longest header, is flushed and replaced by a new
14 byte writer on c. If that flush fails, no Conn is returned.

config follows from the handshake, see NewConnConfig. ClientHandShake and
ServerHandShake fill it in, so it is only yours to answer when you build a Conn
directly.
*/
func NewConn(c net.Conn, r *bufio.Reader, w *bufio.Writer, config NewConnConfig) (*Conn, error) {
	w, err := fitWriter(c, w)
	if err != nil {
		return nil, err
	}
	return &Conn{
		Conn:                          c,
		reader:                        r,
		writer:                        w,
		peerIsClient:                  config.PeerIsClient,
		permessageDeflateAgreedConfig: config.PermessageDeflate,
		contextDeflater:               &contextDeflater{locker: &sync.Mutex{}},
		noContextDeflater:             &noContextDeflater{locker: &sync.Mutex{}},
		contextInflater:               &contextInflater{framesLocker: &sync.Mutex{}, putPullLocker: &sync.Mutex{}},
		noContextInflater:             &noContextInflater{},

		di: connDI{
			getFrameHeader:        GetFrameHeaderFromReader,
			fillFramePayload:      (*Frame).FillFramePayloadFromReader,
			newControlFrame:       NewControlFrame,
			newDataFrame:          NewDataFrame,
			loop:                  Loop,
			writeLocker:           &sync.Mutex{},
			readLocker:            &sync.Mutex{},
			dataFramesWriteLocker: &sync.Mutex{},
		},
	}, nil
}

/*
LockRead takes the read lock for one frame. Hold it across GetNextFrameHeader
and ReadFramePayload, so no other reader takes bytes between the two steps,
then release it with UnlockRead:

	conn.LockRead()
	frame, err := conn.GetNextFrameHeader()
	if err == nil {
		err = conn.ReadFramePayload(frame) // judge frame's size first
	}
	conn.UnlockRead()

A Listener takes it the same way, once per frame.
*/
func (c *Conn) LockRead() {
	c.di.readLocker.Lock()
}

// UnlockRead releases the read lock LockRead took.
func (c *Conn) UnlockRead() {
	c.di.readLocker.Unlock()
}

/*
GetNextFrameHeader reads the next frame's header and returns the frame with
every header field set and no payload, so it can be judged before its payload is
allocated. Call it with the read lock held (LockRead), and ReadFramePayload on
the frame next, before releasing it.

A control frame over 125 bytes (RFC 6455 5.5) is refused here, with the stream
desynced, so the connection cannot be read again — see GetFrameHeaderFromReader.
*/
func (c *Conn) GetNextFrameHeader() (*Frame, error) {
	return c.di.getFrameHeader(c.reader)
}

// ReadFramePayload reads the payload of f, whose header GetNextFrameHeader just
// returned, and unmasks it. It allocates the length the header declares, so
// judge that length first. Call it with the read lock still held.
func (c *Conn) ReadFramePayload(f *Frame) error {
	return c.di.fillFramePayload(f, c.reader)
}

// GetPermessageDeflateAgreedConfig is the permessage-deflate config the
// handshake agreed, nil if none was. It is a copy, so changing it changes
// nothing here. Pass it to NewListenerConfig.
func (c *Conn) GetPermessageDeflateAgreedConfig() *PermessageDeflateConfig {
	if c.permessageDeflateAgreedConfig == nil {
		return nil
	}
	agreed := *c.permessageDeflateAgreedConfig
	return &agreed
}
