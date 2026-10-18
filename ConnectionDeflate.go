package wlgows

import (
	"bytes"
	"compress/flate"
	"fmt"
	"sync"
)

// deflateWindowSize is how far back a reference may reach, 32 KB. The
// handshakes never agree to send with less (library choice: compress/flate
// cannot), so this is always the sending window (RFC 7692 7.1.2).
const deflateWindowSize = 32 * 1024 // 32 KB

type connNoContextDeflater interface {
	deflate(data []byte, level int) ([]byte, error)
	releaseResultBuffer()
	resultBufferCap() int
}

type connContextDeflater interface {
	connNoContextDeflater
	dropContext()
}

/*
DeflaterResultBufferCap is the memory, in bytes, the deflaters' result buffers
hold. Each keeps the memory of the largest result it produced since it was last
released, for the rest of the connection; ReleaseDeflaterResultBuffer frees
them.

It waits only for a deflate in progress, not for the frames to be written.
*/
func (c *Conn) DeflaterResultBufferCap() int {
	return c.noContextDeflater.resultBufferCap() + c.contextDeflater.resultBufferCap()
}

/*
ReleaseDeflaterResultBuffer frees the deflaters' result buffers, see
DeflaterResultBufferCap. Call it after sending a large deflated message:
nothing frees them otherwise. The next deflated message grows a new one.

It waits for a message being sent, so do not call it between
StartLongDataTransmission and its end on the same goroutine.
*/
func (c *Conn) ReleaseDeflaterResultBuffer() {
	c.di.dataFramesWriteLocker.Lock()
	c.noContextDeflater.releaseResultBuffer()
	c.contextDeflater.releaseResultBuffer()
	c.di.dataFramesWriteLocker.Unlock()
}

/*
noContextFlateWriters pools the writers of every noContextDeflater, one pool per
level: flate.HuffmanOnly (-2) to flate.BestCompression (9), at index
level - flate.HuffmanOnly (library choice).

Without context takeover a writer is built with no dict, so after Reset it holds
nothing of the last message: any Conn can use any writer of the level. A writer
is about 1 MB, so sharing them means an idle Conn holds none, and many Conns
need only as many as send at once. A Conn of its own would gain nothing: it
deflates one message at a time.

A writer back in the pool still points at the last result buffer it wrote to,
until it is taken again or the GC empties the pool, so that buffer stays alive
until then. It never writes there: whoever takes it calls Reset with their own
buffer first.

contextDeflater cannot use it: its writer's window is the stream, which the
next message continues.
*/
var noContextFlateWriters [12]sync.Pool // levels -2 to 9

/*
noContextDeflater is a Conn's sending side of permessage-deflate when this side
agreed to no context takeover (RFC 7692 7.1.1): each deflate is one whole
message, from an empty window. NewConn sets locker; each deflate takes a
writer from noContextFlateWriters.
*/
type noContextDeflater struct {
	locker sync.Locker

	// Where the writer writes. It keeps the memory of the largest result
	// until releaseResultBuffer.
	result_buffer bytes.Buffer
}

/*
deflate deflates data as one whole message at level, then sync flushes, so the
result holds all of data and ends with 00 00 ff ff (RFC 7692 7.2.1). The result
is valid until the next deflate or releaseResultBuffer, so a caller holds it
alone until it is sent.

level must be a compress/flate level, see validateDeflateLevel.
*/
func (c *noContextDeflater) deflate(data []byte, level int) ([]byte, error) {
	if err := validateDeflateLevel(level); err != nil {
		return nil, err
	}

	c.locker.Lock()
	defer c.locker.Unlock()

	c.result_buffer.Reset()
	pool := &noContextFlateWriters[level-flate.HuffmanOnly]
	flate_writer, _ := pool.Get().(*flate.Writer)
	if flate_writer == nil {
		var err error
		if flate_writer, err = flate.NewWriter(&c.result_buffer, level); err != nil {
			return nil, err
		}
	} else {
		// Built without a dict, so Reset empties the window: nothing refers
		// back to an earlier message, which the peer has dropped. It also
		// points the writer at this buffer, not the last user's.
		flate_writer.Reset(&c.result_buffer)
	}
	if _, err := flate_writer.Write(data); err != nil {
		return nil, err
	}
	if err := flate_writer.Flush(); err != nil {
		return nil, err
	}
	// Flushed, so nothing of data is left in it for the next user.
	pool.Put(flate_writer)
	return c.result_buffer.Bytes(), nil
}

/*
releaseResultBuffer lets go of the result buffer. Call it only when the last
deflate result is no longer used: it is that buffer.
*/
func (c *noContextDeflater) releaseResultBuffer() {
	c.locker.Lock()
	defer c.locker.Unlock()
	c.result_buffer = bytes.Buffer{}
}

// resultBufferCap is the memory result_buffer holds, its Cap.
func (c *noContextDeflater) resultBufferCap() int {
	c.locker.Lock()
	defer c.locker.Unlock()
	return c.result_buffer.Cap()
}

/*
contextDeflater is a Conn's sending side of permessage-deflate. NewConn sets
locker; the flate.Writer is built on the first deflate, and again on each level
change (about 1 MB each).
*/
type contextDeflater struct {
	locker       sync.Locker
	flate_writer *flate.Writer
	flate_level  int // the level flate_writer was built with

	// Where flate_writer writes. It keeps the memory of the largest result
	// until releaseResultBuffer.
	result_buffer bytes.Buffer

	// At least the last 32 KB fed to flate_writer, at most twice that, see
	// appendWindow. compress/flate cannot change a writer's level, nor expose
	// its window, so a writer for a new level starts from the last 32 KB of
	// this instead, and references into earlier data stay valid. HuffmanOnly,
	// NoCompression and BestSpeed ignore it (compress/flate), so they start
	// from an empty window; they never refer to data from before they were
	// built, so the stream stays valid.
	history []byte
}

/*
deflate feeds data to the deflate stream at level, then sync flushes, so the
result holds all of data and ends with 00 00 ff ff (RFC 7692 7.2.1). The result
is valid until the next deflate or releaseResultBuffer, so a caller holds it
alone until it is sent.

level must be a compress/flate level, see validateDeflateLevel.
*/
func (c *contextDeflater) deflate(data []byte, level int) ([]byte, error) {
	if err := validateDeflateLevel(level); err != nil {
		return nil, err
	}

	c.locker.Lock()
	defer c.locker.Unlock()

	c.result_buffer.Reset()
	if level != c.flate_level || c.flate_writer == nil {
		// The old writer was flushed at the end of its last deflate, so it
		// holds nothing left to write.
		// NewWriterDict keeps a copy of all of dict, so pass only the window.
		dict := c.history[max(len(c.history)-deflateWindowSize, 0):]
		flate_writer, err := flate.NewWriterDict(&c.result_buffer, level, dict)
		if err != nil {
			return nil, err
		}
		c.flate_writer, c.flate_level = flate_writer, level
	}
	if _, err := c.flate_writer.Write(data); err != nil {
		return nil, err
	}
	if err := c.flate_writer.Flush(); err != nil {
		return nil, err
	}
	c.history = appendWindow(c.history, data)
	return c.result_buffer.Bytes(), nil
}

/*
dropContext starts the next message from an empty window, as a side that
agreed to no context takeover must (RFC 7692 7.1.1), and frees the writer and
history. It does not touch the last deflate result.
*/
func (c *contextDeflater) dropContext() {
	c.locker.Lock()
	defer c.locker.Unlock()
	c.history = nil
	c.flate_writer = nil
}

/*
releaseResultBuffer lets go of the result buffer. Call it only when the last
deflate result is no longer used: it is that buffer.
*/
func (c *contextDeflater) releaseResultBuffer() {
	c.locker.Lock()
	defer c.locker.Unlock()
	c.result_buffer = bytes.Buffer{} // flate_writer writes to &c.result_buffer, which stays the same
}

// resultBufferCap is the memory result_buffer holds, its Cap.
func (c *contextDeflater) resultBufferCap() int {
	c.locker.Lock()
	defer c.locker.Unlock()
	return c.result_buffer.Cap()
}

/*
appendWindow appends data to history and keeps at least the last
deflateWindowSize bytes. They slide to the front only when history would pass
twice that, so a small message costs its own length, not the window's.
*/
func appendWindow(history, data []byte) []byte {
	if len(data) >= deflateWindowSize {
		return append(history[:0], data[len(data)-deflateWindowSize:]...)
	}
	if len(history)+len(data) > 2*deflateWindowSize {
		keep := deflateWindowSize - len(data)
		history = append(history[:0], history[len(history)-keep:]...)
	}
	return append(history, data...)
}

// validateDeflateLevel refuses a DeflateLevel compress/flate has no level
// for, from flate.HuffmanOnly (-2) to flate.BestCompression (9), with
// ErrDeflateLevelInvalid.
func validateDeflateLevel(level int) error {
	if level < flate.HuffmanOnly || level > flate.BestCompression {
		return fmt.Errorf("DeflateLevel %d: %w", level, ErrDeflateLevelInvalid)
	}
	return nil
}
