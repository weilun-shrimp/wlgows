package wlgows

import (
	"bufio"
	"encoding/binary"
	"fmt"
	"io"
)

/*
GetFrameFromReader reads one frame, refusing any whose header declares a
payload larger than maxByteLength. Pass 0 for no limit.

The check happens after the header is parsed and before the payload is
allocated, which is the only place it helps: a peer can claim a 10 GB payload in
a 10 byte header, and without the guard that claim becomes a 10 GB make() before
a single payload byte has arrived.

Exceeding the limit returns an error wrapping ErrFrameByteLengthExceeded and
leaves the payload unread, so the stream is desynced and the connection cannot
be reused — send RFC 6455 close code 1009 and close it.
*/
func GetFrameFromReader(r *bufio.Reader, maxByteLength uint64) (*Frame, error) {
	return getFrameFromReader(r, maxByteLength, getFrameFromReaderDI{
		ioReadFull:  io.ReadFull,
		maskPayload: MaskPayload,
	})
}

type getFrameFromReaderBufioReader interface {
	io.Reader
	Peek(n int) ([]byte, error)
	Discard(n int) (discarded int, err error)
}

type getFrameFromReaderDI struct {
	ioReadFull  func(r io.Reader, buf []byte) (n int, err error)
	maskPayload func(payload []byte, maskingKey [4]byte)
}

func getFrameFromReader(r getFrameFromReaderBufioReader, maxByteLength uint64, di getFrameFromReaderDI) (*Frame, error) {
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
		case 127:
			f.ExtendedPayloadLength = binary.BigEndian.Uint64(header[2:])
			// RFC 6455 5.2 says the 64 bit length's most significant bit must be
			// 0. A peer that sets it breaks the protocol, and such a length is so
			// large that the make below would panic on it.
			if f.ExtendedPayloadLength>>63 == 1 {
				return f, fmt.Errorf("64 bit payload length %d has its most significant bit set: %w",
					f.ExtendedPayloadLength, ErrPayloadLengthMSBSet)
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

	// Guard before the allocation, never after: the make below is sized by the
	// header's claim, so by the time it returns the damage is done.
	if maxByteLength > 0 && f.GetMaxPayloadLength() > maxByteLength {
		return f, fmt.Errorf(
			"frame header declares %d bytes against a %d byte max, payload left unread: %w",
			f.GetMaxPayloadLength(), maxByteLength, ErrFrameByteLengthExceeded,
		)
	}

	f.PayloadData = make([]byte, f.GetMaxPayloadLength())
	if _, err = di.ioReadFull(r, f.PayloadData); err != nil {
		return f, frameReadErr(headerSize, err)
	}

	if f.Mask {
		di.maskPayload(f.PayloadData, f.MaskingKey) // do unmask by mask again.
	}

	return f, nil
}

// frameReadErr returns io.EOF only when the stream ended before the frame's
// first byte. Ending later is io.ErrUnexpectedEOF: the frame was cut short.
func frameReadErr(frameBytesRead int, err error) error {
	if err == io.EOF && frameBytesRead > 0 {
		return io.ErrUnexpectedEOF
	}
	return err
}
