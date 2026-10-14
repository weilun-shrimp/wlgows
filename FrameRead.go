package wlgows

import (
	"bufio"
	"encoding/binary"
	"fmt"
	"io"
)

/*
GetFrameHeaderFromReader reads one frame's header and returns the frame with
every header field set and no payload. Nothing is allocated for the payload, so
the frame can be judged by its header first. Read the payload with
Frame.FillFramePayloadFromReader next, before anything else reads from r. Reading a
frame always takes these two steps.

Judge the declared size before reading the payload: a peer can claim a 10 GB
payload in a 10 byte header, and the payload step allocates what the header
claims. A frame refused here, or by you, leaves its payload unread, so the
stream is desynced and the connection cannot be read again.

A control frame over 125 bytes (RFC 6455 5.5) is refused here with
ErrControlFramePayloadTooLong: send close code 1002.
*/
func GetFrameHeaderFromReader(r *bufio.Reader) (*Frame, error) {
	return getFrameHeaderFromReader(r)
}

type getFrameHeaderFromReaderBufioReader interface {
	Peek(n int) ([]byte, error)
	Discard(n int) (discarded int, err error)
}

func getFrameHeaderFromReader(r getFrameHeaderFromReaderBufioReader) (*Frame, error) {
	f := new(Frame)
	// The header is parsed in place from the reader's buffer: Peek copies
	// nothing, so no header byte is allocated. The longest header is 14 bytes,
	// under bufio's 16 byte minimum buffer, so Peek never hits ErrBufferFull.
	header, err := r.Peek(2)
	if err != nil {
		return f, frameReadErr(len(header), err)
	}
	f.FIN = header[0]>>7 == 1
	f.RSV1 = header[0]>>6&1 == 1
	f.RSV2 = header[0]>>5&1 == 1
	f.RSV3 = header[0]>>4&1 == 1
	f.Opcode = header[0] & 15

	f.PayloadLength = uint8(header[1] & 0x7F)
	f.Mask = header[1]>>7 == 1

	headerSize := f.getHeaderSize()
	if headerSize > 2 {
		header, err = r.Peek(headerSize)
		if err != nil {
			return f, frameReadErr(len(header), err)
		}
		switch f.PayloadLength {
		case 126:
			f.ExtendedPayloadLength = uint64(binary.BigEndian.Uint16(header[2:]))
			if f.ExtendedPayloadLength < 126 {
				return f, fmt.Errorf("16 bit payload length %d fits in 7 bits: %w",
					f.ExtendedPayloadLength, ErrPayloadLengthNotMinimal)
			}
		case 127:
			f.ExtendedPayloadLength = binary.BigEndian.Uint64(header[2:])
			// RFC 6455 5.2 says the 64 bit length's most significant bit must be
			// 0. A peer that sets it breaks the protocol, and such a length is so
			// large that allocating the payload would panic on it.
			if f.ExtendedPayloadLength>>63 == 1 {
				return f, fmt.Errorf("64 bit payload length %d has its most significant bit set: %w",
					f.ExtendedPayloadLength, ErrPayloadLengthMSBSet)
			}
			if f.ExtendedPayloadLength < 65536 {
				return f, fmt.Errorf("64 bit payload length %d fits in 16 bits: %w",
					f.ExtendedPayloadLength, ErrPayloadLengthNotMinimal)
			}
		}
		if f.Mask {
			f.MaskingKey = [4]byte(header[headerSize-4:]) // always the last 4 bytes
		}
	}
	// Only now, with every header field copied out: header points into the
	// reader's buffer and is invalid once the reader moves.
	if _, err = r.Discard(headerSize); err != nil {
		return f, err
	}

	// RFC 6455 5.5 caps a control frame at 125 bytes. Refusing a larger one
	// here, whatever the caller checks next, keeps a ping from claiming a size
	// that crashes the payload allocation.
	if IsControlOpcode(f.Opcode) && f.GetDeclaredPayloadSize() > ControlFramePayloadMaxByteLength {
		return f, fmt.Errorf(
			"control frame header declares %d bytes, payload left unread: %w",
			f.GetDeclaredPayloadSize(), ErrControlFramePayloadTooLong,
		)
	}

	return f, nil
}

/*
FillFramePayloadFromReader reads the payload of f, whose header
GetFrameHeaderFromReader just read from r, into f.PayloadData and unmasks it.
It allocates the length the header declares, so judge that length first.
*/
func (f *Frame) FillFramePayloadFromReader(r *bufio.Reader) error {
	return f.fillFramePayloadFromReader(r, fillFramePayloadFromReaderDI{
		ioReadFull:  io.ReadFull,
		maskPayload: MaskPayload,
	})
}

type fillFramePayloadFromReaderDI struct {
	ioReadFull  func(r io.Reader, buf []byte) (n int, err error)
	maskPayload func(payload []byte, maskingKey [4]byte)
}

func (f *Frame) fillFramePayloadFromReader(r io.Reader, di fillFramePayloadFromReaderDI) error {
	f.PayloadData = make([]byte, f.GetDeclaredPayloadSize())
	if _, err := di.ioReadFull(r, f.PayloadData); err != nil {
		return frameReadErr(f.getHeaderSize(), err)
	}

	if f.Mask {
		di.maskPayload(f.PayloadData, f.MaskingKey) // do unmask by mask again.
	}

	return nil
}

// frameReadErr returns io.EOF only when the stream ended before the frame's
// first byte. Ending later is io.ErrUnexpectedEOF: the frame was cut short.
func frameReadErr(frameBytesRead int, err error) error {
	if err == io.EOF && frameBytesRead > 0 {
		return io.ErrUnexpectedEOF
	}
	return err
}
