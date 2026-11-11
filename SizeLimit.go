package wlgows

import "io"

/*
NewSizeLimitReader returns a reader of reader's bytes, up to max_size. When
reader has more, Read returns ErrSizeLimitExceeded, and keeps returning it.
The byte past max_size is read to know this, and never handed out. Answer with
close code 1009 (RFC 6455 7.4.1), as StandardClosePayloadFor gives.

Read until io.EOF: only then is the message within max_size. It has no Close:
close reader yourself, even after ErrSizeLimitExceeded.

max_size must not be negative.
*/
func NewSizeLimitReader(reader io.Reader, max_size int64) io.Reader {
	return &sizeLimitReader{reader: reader, remainingSize: max_size}
}

type sizeLimitReader struct {
	reader io.Reader
	// remainingSize is how many bytes may still be read.
	remainingSize int64
	// exceeded is whether reader had more than max_size. Once true, Read
	// returns ErrSizeLimitExceeded.
	exceeded bool
}

// Read reads into p, at most remainingSize bytes. Once none remain, it reads
// one byte more: a byte means reader has more than max_size.
func (r *sizeLimitReader) Read(p []byte) (int, error) {
	if r.exceeded {
		return 0, ErrSizeLimitExceeded
	}
	if len(p) == 0 {
		return 0, nil
	}

	if r.remainingSize == 0 {
		var next_byte [1]byte
		n, err := r.reader.Read(next_byte[:])
		if n > 0 {
			r.exceeded = true
			return 0, ErrSizeLimitExceeded
		}
		return 0, err
	}

	if int64(len(p)) > r.remainingSize {
		p = p[:r.remainingSize]
	}
	n, err := r.reader.Read(p)
	r.remainingSize -= int64(n)
	return n, err
}

/*
ReadMessageInto reads reader into buf, and returns how many bytes it read. It
returns nil only when reader reached io.EOF and the whole message fits in buf.

When reader has more than len(buf), it returns ErrSizeLimitExceeded: answer
with close code 1009 (RFC 6455 7.4.1), as StandardClosePayloadFor gives. To
find this, it reads one byte past len(buf). That byte is never written to buf.
Any other error is the one reader returned.

On an error, do not use buf[:n]: it may be cut short, or it failed a check.
*/
func ReadMessageInto(reader io.Reader, buf []byte) (int, error) {
	n := 0
	for n < len(buf) {
		read_size, err := reader.Read(buf[n:])
		n += read_size
		if err == io.EOF {
			return n, nil
		}
		if err != nil {
			return n, err
		}
	}

	// buf is full: read one byte more to know whether the message ends here.
	var next_byte [1]byte
	for {
		read_size, err := reader.Read(next_byte[:])
		if read_size > 0 {
			return n, ErrSizeLimitExceeded
		}
		if err == io.EOF {
			return n, nil
		}
		if err != nil {
			return n, err
		}
	}
}
