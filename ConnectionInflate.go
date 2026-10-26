package wlgows

import (
	"compress/flate"
	"io"
	"sync"
)

/*
inflateTail goes after a message's payloads. 00 00 ff ff is what the sender
stripped from its last flush (RFC 7692 7.2.2). 01 00 00 ff ff is an empty
final block (library choice): without it compress/flate reads on past the
message and ends with io.ErrUnexpectedEOF instead of io.EOF.
*/
var inflateTail = []byte{0x00, 0x00, 0xff, 0xff, 0x01, 0x00, 0x00, 0xff, 0xff}

/*
inflaterPipeSource is what an inflater's flate reader reads a message from,
see pipeSource. put, close and drop are the inflater's side; Read and
ReadByte (flate.Reader) are compress/flate's.
*/
type inflaterPipeSource interface {
	flate.Reader
	put(data []byte) error
	close(err error)
	drop()
}

// connNoContextInflater inflates a whole message from an empty window.
type connNoContextInflater interface {
	inflateDataFrames(frames DataFrames, buf []byte) (int, error)
}

// connContextInflater inflates a message frame by frame, each from the
// messages before it.
type connContextInflater interface {
	connNoContextInflater

	startInflateFrames() int64
	putDeflatedFrame(token int64, f *Frame) error
	pullInflatedBytes(token int64, buf []byte) (int, error)
	endInflateFrames(token int64)
	dropContext(token int64)
}

/*
noContextFlateReaders pools the readers of every noContextInflater (library
choice). A pooled reader still points at the last message it read until
whoever takes it calls Reset with their own source.

contextInflater cannot use it: its reader starts each message from the last
32 KB of history.
*/
var noContextFlateReaders sync.Pool

// flateReaderPool is a pool of flate readers as its users see it;
// *sync.Pool is one.
type flateReaderPool interface {
	Get() any
	Put(x any)
}

/*
noContextInflater is a Conn's receiving side of permessage-deflate for a whole
message from a peer that agreed to no context takeover (RFC 7692 7.1.1): each
message inflates from an empty window. Each inflateDataFrames takes a reader
from noContextFlateReaders, so messages may inflate at once.
*/
type noContextInflater struct{}

/*
inflateDataFrames inflates frames, one whole message, into buf[:cap(buf)] and
returns how many bytes it wrote. cap(buf) is the size allowed: a message that
inflates past it returns ErrInflatedSizeExceeded. Any other error is
compress/flate's.
*/
func (c *noContextInflater) inflateDataFrames(frames DataFrames, buf []byte) (int, error) {
	return noContextInflateDataFrames(frames, buf, noContextInflateDataFramesDI{
		newPipeSource:         newPipeSource,
		getFlateReader:        getNoContextFlateReader,
		noContextFlateReaders: &noContextFlateReaders,
		readInflated:          readInflated,
	})
}

// noContextInflateDataFramesDI is what noContextInflateDataFrames calls.
type noContextInflateDataFramesDI struct {
	newPipeSource         func() inflaterPipeSource
	getFlateReader        func(source io.Reader) io.ReadCloser
	noContextFlateReaders flateReaderPool
	readInflated          func(inflate_read func(buf []byte) (int, error), buf []byte) (int, error)
}

// noContextInflateDataFrames is noContextInflater.inflateDataFrames.
func noContextInflateDataFrames(frames DataFrames, buf []byte, di noContextInflateDataFramesDI) (int, error) {
	source := di.newPipeSource()
	for _, f := range frames {
		if len(f.PayloadData) != 0 {
			source.put(f.PayloadData)
		}
	}
	source.put(inflateTail) // RFC 7692 7.2.2
	source.close(io.EOF)

	flate_reader := di.getFlateReader(source)
	n, err := di.readInflated(flate_reader.Read, buf)
	source.drop() // the pooled reader still points at source
	di.noContextFlateReaders.Put(flate_reader)
	if err == io.EOF { // the message ended
		return n, nil
	}
	return n, err
}

// inflateDataFramesReader returns a reader of frames, one whole message,
// inflated. Read returns io.EOF at the message's end, else compress/flate's
// errors. Close it when done.
func (c *noContextInflater) inflateDataFramesReader(frames DataFrames) io.ReadCloser {
	return noContextInflateDataFramesReader(frames, noContextInflateDataFramesReaderDI{
		newPipeSource:         newPipeSource,
		getFlateReader:        getNoContextFlateReader,
		noContextFlateReaders: &noContextFlateReaders,
	})
}

// noContextInflateDataFramesReaderDI is what noContextInflateDataFramesReader
// calls.
type noContextInflateDataFramesReaderDI struct {
	newPipeSource         func() inflaterPipeSource
	getFlateReader        func(source io.Reader) io.ReadCloser
	noContextFlateReaders flateReaderPool
}

// noContextInflateDataFramesReader is noContextInflater.inflateDataFramesReader.
func noContextInflateDataFramesReader(frames DataFrames, di noContextInflateDataFramesReaderDI) io.ReadCloser {
	source := di.newPipeSource()
	for _, f := range frames {
		if len(f.PayloadData) != 0 {
			source.put(f.PayloadData)
		}
	}
	source.put(inflateTail) // RFC 7692 7.2.2
	source.close(io.EOF)

	return &noContextInflateReader{
		source:                source,
		flateReader:           di.getFlateReader(source),
		noContextFlateReaders: di.noContextFlateReaders,
	}
}

// noContextInflateReader reads one message with a flate reader from
// noContextFlateReaders, and gives it back on Close.
type noContextInflateReader struct {
	source                inflaterPipeSource
	flateReader           io.ReadCloser // nil once closed
	noContextFlateReaders flateReaderPool
}

// Read inflates into p. After Close, it returns io.ErrClosedPipe: the flate
// reader may already read another message.
func (r *noContextInflateReader) Read(p []byte) (int, error) {
	if r.flateReader == nil {
		return 0, io.ErrClosedPipe
	}
	return r.flateReader.Read(p)
}

// Close drops source and gives the flate reader back. A second Close does
// nothing.
func (r *noContextInflateReader) Close() error {
	if r.flateReader == nil {
		return nil
	}
	r.source.drop() // the pooled reader still points at source
	r.noContextFlateReaders.Put(r.flateReader)
	r.source, r.flateReader = nil, nil
	return nil
}

// getNoContextFlateReader is a reader from noContextFlateReaders, or a new
// one, reading source from an empty window.
func getNoContextFlateReader(source io.Reader) io.ReadCloser {
	return getFlateReader(source, getFlateReaderDI{
		noContextFlateReaders: &noContextFlateReaders,
		newFlateReader:        flate.NewReader,
	})
}

// getFlateReaderDI is what getFlateReader calls.
type getFlateReaderDI struct {
	noContextFlateReaders flateReaderPool
	newFlateReader        func(source io.Reader) io.ReadCloser
}

// getFlateReader is getNoContextFlateReader.
func getFlateReader(source io.Reader, di getFlateReaderDI) io.ReadCloser {
	flate_reader, _ := di.noContextFlateReaders.Get().(io.ReadCloser)
	if flate_reader == nil {
		return di.newFlateReader(source)
	}
	flate_reader.(flate.Resetter).Reset(source, nil)
	return flate_reader
}

/*
readInflated reads one whole message into buf[:cap(buf)] with inflate_read,
which writes the next inflated bytes into the buf it gets, until io.EOF, and
returns how many bytes it wrote with that io.EOF: the message ended. cap(buf)
is the size allowed: a message that inflates past it returns
ErrInflatedSizeExceeded. Any other error is inflate_read's.
*/
func readInflated(inflate_read func(buf []byte) (int, error), buf []byte) (int, error) {
	n := 0
	for n < cap(buf) {
		m, err := inflate_read(buf[n:cap(buf)])
		n += m
		if err != nil { // io.EOF included: the message ended
			return n, err
		}
	}
	// buf is full: the message must end here.
	var probe [1]byte
	m, err := inflate_read(probe[:])
	if m > 0 {
		return n, ErrInflatedSizeExceeded
	}
	return n, err
}

/*
contextInflater is a Conn's receiving side of permessage-deflate for a peer
that keeps its context, and for every message inflated frame by frame. NewConn
sets both lockers; the flate reader is built on first use, then Reset for each
message.
*/
type contextInflater struct {
	// Held from startInflateFrames to endInflateFrames: one message at a time.
	framesLocker sync.Locker

	// The open message's token, from startInflateFrames. A call with another
	// token, or after the message ended, is stale. On pipeSourceLocker.
	currentFramesToken int64

	// Held by each put, end and dropContext, and by a pull's token check, so
	// they do not cross. A pull does not hold it while it reads: it may wait in
	// pipeSource for the next put.
	pipeSourceLocker sync.Locker
	// Reads pipeSource. Its errors are sticky: one ends the message.
	flateReader io.ReadCloser
	// The open message's frames, nil between messages.
	pipeSource inflaterPipeSource

	// At least the last 32 KB inflated, at most twice that, see appendWindow.
	// A message starts from its last 32 KB, so references into the messages
	// before it resolve. A peer may send with a smaller window (RFC 7692
	// 7.1.2); 32 KB holds any.
	history []byte
}

/*
inflateDataFrames inflates frames, one whole message ending with FIN, into
buf[:cap(buf)] from the messages before it, and returns how many bytes it
wrote. cap(buf) is the size allowed: a message that inflates past it returns
ErrInflatedSizeExceeded. Any other error is compress/flate's.
*/
func (c *contextInflater) inflateDataFrames(frames DataFrames, buf []byte) (int, error) {
	return contextInflateDataFrames(frames, buf, contextInflateDataFramesDI{
		startInflateFrames: c.startInflateFrames,
		putDeflatedFrame:   c.putDeflatedFrame,
		pullInflatedBytes:  c.pullInflatedBytes,
		endInflateFrames:   c.endInflateFrames,
		readInflated:       readInflated,
	})
}

// contextInflateDataFramesDI is what contextInflateDataFrames calls.
type contextInflateDataFramesDI struct {
	startInflateFrames func() int64
	putDeflatedFrame   func(token int64, f *Frame) error
	pullInflatedBytes  func(token int64, buf []byte) (int, error)
	endInflateFrames   func(token int64)
	readInflated       func(inflate_read func(buf []byte) (int, error), buf []byte) (int, error)
}

// contextInflateDataFrames is contextInflater.inflateDataFrames.
func contextInflateDataFrames(frames DataFrames, buf []byte, di contextInflateDataFramesDI) (int, error) {
	token := di.startInflateFrames()
	defer di.endInflateFrames(token)
	for _, f := range frames {
		if err := di.putDeflatedFrame(token, f); err != nil {
			return 0, err
		}
	}
	pull := func(buf []byte) (int, error) { return di.pullInflatedBytes(token, buf) }
	n, err := di.readInflated(pull, buf)
	if err == io.EOF { // the message ended
		return n, nil
	}
	return n, err
}

// startInflateFrames opens a message: an empty pipeSource, read by flateReader
// from the last 32 KB of history. It returns the message's token, for every
// call after. endInflateFrames must follow.
func (c *contextInflater) startInflateFrames() int64 {
	return startInflateFrames(startInflateFramesDI{
		inflater:       c,
		newPipeSource:  newPipeSource,
		newFlateReader: flate.NewReaderDict,
	})
}

// startInflateFramesDI is what startInflateFrames calls, and the inflater it
// opens the message on.
type startInflateFramesDI struct {
	inflater       *contextInflater
	newPipeSource  func() inflaterPipeSource
	newFlateReader func(source io.Reader, dict []byte) io.ReadCloser
}

// startInflateFrames is contextInflater.startInflateFrames.
func startInflateFrames(di startInflateFramesDI) int64 {
	inflater := di.inflater
	inflater.framesLocker.Lock()
	inflater.pipeSourceLocker.Lock()
	defer inflater.pipeSourceLocker.Unlock()

	inflater.currentFramesToken++
	inflater.pipeSource = di.newPipeSource()

	dict := inflater.history[max(len(inflater.history)-deflateWindowSize, 0):] // the last 32 KB
	if inflater.flateReader == nil {
		inflater.flateReader = di.newFlateReader(inflater.pipeSource, dict)
		return inflater.currentFramesToken
	}
	inflater.flateReader.(flate.Resetter).Reset(inflater.pipeSource, dict)
	return inflater.currentFramesToken
}

// putDeflatedFrame puts f's payload into the open message, then inflateTail
// and the close after the FIN frame. A stale token returns
// ErrInflateFramesTokenStale.
func (c *contextInflater) putDeflatedFrame(token int64, f *Frame) error {
	c.pipeSourceLocker.Lock()
	defer c.pipeSourceLocker.Unlock()
	if c.isStaleToken(token) {
		return ErrInflateFramesTokenStale
	}

	if len(f.PayloadData) != 0 {
		if err := c.pipeSource.put(f.PayloadData); err != nil {
			return err
		}
	}
	if f.FIN {
		if err := c.pipeSource.put(inflateTail); err != nil {
			return err
		}
		c.pipeSource.close(io.EOF)
	}

	return nil
}

/*
pullInflatedBytes inflates into buf[:cap(buf)] and adds it to history; io.EOF,
the message ended. Before the FIN frame, it waits in pipeSource for the next
put when flate runs out, so put from another goroutine. One pull at a time.
A stale token returns ErrInflateFramesTokenStale.
*/
func (c *contextInflater) pullInflatedBytes(token int64, buf []byte) (int, error) {
	c.pipeSourceLocker.Lock()
	stale := c.isStaleToken(token)
	c.pipeSourceLocker.Unlock()
	if stale {
		return 0, ErrInflateFramesTokenStale
	}
	if cap(buf) == 0 { // no room: flate would step into the pipe for nothing
		return 0, nil
	}
	n, err := c.flateReader.Read(buf[:cap(buf)])
	c.history = appendWindow(c.history, buf[:n])
	return n, err
}

// endInflateFrames closes the open message, drops its pipeSource and lets the
// next message start. Call it once no pull is running. A stale token does
// nothing, so a second end cannot end the next message.
func (c *contextInflater) endInflateFrames(token int64) {
	c.pipeSourceLocker.Lock()
	if c.isStaleToken(token) {
		c.pipeSourceLocker.Unlock()
		return
	}
	c.pipeSource.drop() // flateReader still points at it until the next start
	c.pipeSource = nil
	c.pipeSourceLocker.Unlock()
	c.framesLocker.Unlock()
}

/*
dropContext starts the next message from an empty window, as for a peer that
agreed to no context takeover (RFC 7692 7.1.1), and frees the history. Call it
while a message is open, after its last pull and before endInflateFrames, so no
other message starts in between. A stale token does nothing.
*/
func (c *contextInflater) dropContext(token int64) {
	c.pipeSourceLocker.Lock()
	defer c.pipeSourceLocker.Unlock()
	if c.isStaleToken(token) {
		return
	}
	c.history = nil
}

// isStaleToken is whether token is not the open message's: it ended, or
// another started. Hold pipeSourceLocker.
func (c *contextInflater) isStaleToken(token int64) bool {
	return c.pipeSource == nil || token != c.currentFramesToken
}

/*
pipeSource is what a flate reader reads a message from, frame by frame. put adds
a frame's bytes; Read and ReadByte are flate's side.

It is our own so a peer's long transmission inflates safely as it arrives
(library choice): put never waits, and Read waits for the next put while the
message is open, see pullInflatedBytes. io.Pipe would make put wait until flate
read it all, and a bytes.Reader ends at the bytes it holds.

The cost is memory: frames stay until flate reads them.

It is an io.ByteReader, so compress/flate reads it as it is (library choice),
not through one more bufio.Reader copy.
*/
type pipeSource struct {
	locker sync.Locker
	// Wakes a waiting Read or ReadByte on put and close. On locker.
	cond *sync.Cond
	// The payloads put and not yet read, in order. None is empty.
	pipe [][]byte
	// What Read and ReadByte return once pipe is empty and nothing more will
	// be put: io.EOF after the FIN frame, see close.
	err error
}

// newPipeSource is an empty, open pipeSource.
func newPipeSource() inflaterPipeSource {
	locker := &sync.Mutex{}
	return &pipeSource{
		locker: locker,
		cond:   sync.NewCond(locker),
	}
}

// put adds data after what pipe holds. It keeps data, so leave it unchanged
// until it is read.
func (s *pipeSource) put(data []byte) error {
	s.locker.Lock()
	defer s.locker.Unlock()
	if s.isClosed() {
		return s.err
	}
	s.pipe = append(s.pipe, data)
	s.cond.Broadcast()
	return nil
}

// close says nothing more will be put: once pipe is empty, Read and ReadByte
// return err.
func (s *pipeSource) close(err error) {
	s.locker.Lock()
	defer s.locker.Unlock()
	if s.isClosed() {
		return
	}
	s.err = err
	s.cond.Broadcast()
}

// drop frees what pipe holds, read or not, and closes it with
// io.ErrClosedPipe if it is still open.
func (s *pipeSource) drop() {
	s.locker.Lock()
	defer s.locker.Unlock()
	s.pipe = nil
	if !s.isClosed() {
		s.err = io.ErrClosedPipe
	}
	s.cond.Broadcast()
}

// isClosed is whether close was called. Hold locker, or own s alone.
func (s *pipeSource) isClosed() bool {
	return s.err != nil
}

// Read moves bytes from pipe into p. Empty, it waits for a put while open, and
// returns err once closed.
func (s *pipeSource) Read(p []byte) (int, error) {
	s.locker.Lock()
	defer s.locker.Unlock()
	for len(s.pipe) == 0 && !s.isClosed() {
		s.cond.Wait()
	}
	if len(s.pipe) == 0 {
		return 0, s.err
	}
	n := copy(p, s.pipe[0])
	s.pipe[0] = s.pipe[0][n:]
	if len(s.pipe[0]) == 0 {
		s.pipe[0] = nil // Let GC to handle memory early.
		s.pipe = s.pipe[1:]
	}
	return n, nil
}

// ReadByte takes one byte from pipe; empty, as Read.
func (s *pipeSource) ReadByte() (byte, error) {
	s.locker.Lock()
	defer s.locker.Unlock()
	for len(s.pipe) == 0 && !s.isClosed() {
		s.cond.Wait()
	}
	if len(s.pipe) == 0 {
		return 0, s.err
	}
	b := s.pipe[0][0]
	s.pipe[0] = s.pipe[0][1:]
	if len(s.pipe[0]) == 0 {
		s.pipe[0] = nil // Let GC to handle memory early.
		s.pipe = s.pipe[1:]
	}
	return b, nil
}
