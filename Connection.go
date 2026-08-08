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

	// Whether the frames this Conn builds are masked. RFC 6455 5.1 leaves no
	// choice — a client masks every frame it sends, a server masks none, and a
	// peer must fail the connection on the wrong one — so it is settled once at
	// construction rather than at each send. NewClientConn passes true,
	// NewServerConn false.
	//
	// Today only SendClose, SendPing and SendPong reach it, since they are the
	// only things here that build a frame. The rule is not about control
	// frames: it covers every frame this Conn ever builds.
	maskSendFrame bool

	// The opcode the message opened with, and the record that one is open —
	// 5.4 never lets a message start with OpcodeContinuation, so zero means no
	// transmission.
	currentTransmitDataMsgOpcode uint8

	// Whether the opening frame has gone out, which decides two things: the
	// next frame carries OpcodeContinuation rather than the opcode again, and
	// End has a message to terminate. Ending one that never opened would send
	// a continuation the peer has nothing to continue.
	currentTransmitDataMsgOpened bool

	// Whether SendClose has been called and taken. RFC 6455 5.5.1 puts the
	// closing handshake at one close each way, so this is what says the answer
	// is already spent.
	//
	// SendClose owns it, reading and writing it under writeLocker so the check
	// and the send are one step. A close built by hand and pushed through
	// SendFrame does not count — that path checks nothing by design.
	closeSent bool

	di connDI
}

type connDI struct {
	getFrameFromTCPConn   func(conn net.Conn, maxByteLength uint64) (*Frame, error)
	newControlFrame       func(config NewControlFrameConfig) (*Frame, error)
	newDataFrame          func(config NewFrameConfig) (*Frame, error)
	generateMaskingKey    func() ([]byte, error)
	writeLocker           sync.Locker // Protect writing one frame to TCP conn.
	readLocker            sync.Locker // Protect reading one frame to TCP conn.
	dataFramesWriteLocker sync.Locker // Protect writing data frames to TCP conn.
}

/*
NewConn wraps an already handshaken connection.

maskSendFrame is RFC 6455 5.1 and follows from which side this is: pass true
from a client, false from a server. NewClientConn and NewServerConn fill it in,
so it is only yours to answer when you build a Conn directly.
*/
func NewConn(c net.Conn, req *http.Request, res *http.Response, maskSendFrame bool) *Conn {
	return &Conn{
		Conn:           c,
		ClientRequest:  req,
		ServerResponse: res,
		maskSendFrame:  maskSendFrame,
		di: connDI{
			getFrameFromTCPConn:   GetFrameFromTCPConn,
			newControlFrame:       NewControlFrame,
			newDataFrame:          NewDataFrame,
			generateMaskingKey:    GenerateMaskingKey,
			writeLocker:           &sync.Mutex{},
			readLocker:            &sync.Mutex{},
			dataFramesWriteLocker: &sync.Mutex{},
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

/*
SendFrame is low level. Do not use it unless you know exactly what you are
putting on the wire — it writes the frame you built and checks almost nothing,
so every rule below is yours to keep. Reach for SendText, SendClose, SendPing or
SendPong first; they build a conforming frame for you.

It seals one frame and puts it on the socket, holding writeLocker for the whole
write so frames from concurrent senders cannot interleave.

What it does not do:

  - It sends ONE frame. RFC 6455 5.4 forbids a second message interleaving with
    a fragmented one, and the lock is released between calls — so two goroutines
    each sending a fragmented message will shuffle their frames together and
    corrupt both. Serialise them with a lock of your own.

  - It does not check the opcode, FIN, or the fragmentation sequence. 5.4 wants
    the first frame to carry the opcode, every continuation to carry 0, and only
    the last to set FIN. All yours.

  - It does not apply the control frame rules. A control frame must not be
    fragmented and must stay within 125 bytes (5.5); NewControlFrame enforces
    that, NewFrame does not, and this will send whatever you built.

  - It does not check the reserved bits. RSV1 belongs to a negotiated extension,
    so refusing it would make one impossible.

The one thing it does settle is masking. The frame is brought into line with
maskSendFrame in place, because 5.1 gives each side exactly one answer and the
peer fails the connection on the wrong one — so your frame is modified. Mask and
MaskingKey always move together, since Seal indexes the key for every payload
byte and panics on a frame claiming to be masked without one. PayloadData stays
plaintext throughout, whether the frame came from NewFrame or off the wire, so
unmasking is just dropping the key.

Seal builds a complete second copy of the payload, so a large message costs
twice its size while this runs.
*/
func (c *Conn) SendFrame(f *Frame) error {
	c.di.writeLocker.Lock()
	defer c.di.writeLocker.Unlock()

	return c.sendFrame(f)
}

func (c *Conn) sendFrame(f *Frame) error {
	switch {
	case c.maskSendFrame && !f.Mask:
		key, err := c.di.generateMaskingKey()
		if err != nil {
			return err
		}
		f.Mask = true
		f.MaskingKey = key

	case !c.maskSendFrame && f.Mask:
		f.Mask = false
		f.MaskingKey = nil
	}

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
