package wlgows

import (
	"fmt"
	"io"
)

/*
DataFramesReader returns a reader of the payload of data_frames, one whole
message ending with FIN. Read it until io.EOF: only then is the message whole.
Close it when done, even after Read returns an error, and not during a Read.
Do not Read after Close. The frames' payloads are read in place, so leave
them unchanged until Close.

A message whose first frame has RSV1 set is deflated (RFC 7692 6) and is read
inflated: from an empty window for a peer that agreed to no context takeover,
else from the messages inflated before it (RFC 7692 7.1.1). Those must all
have gone through the Conn in order. One that does not inflate returns
ErrInflateDataInvalid from Read. For a peer that keeps its context:
  - DataFramesReader for the next deflated message waits until Read returns
    an error, io.EOF included, or Close. From the same goroutine, it never
    returns.
  - Closed before io.EOF, close the connection: the next message may inflate
    to wrong bytes with no error.

To limit the size, read through io.LimitReader(reader, max+1). More than max
bytes read: the message is too big, close the connection with 1009 (RFC 6455
7.4.1), as StandardClosePayloadFor(ErrInflatedSizeExceeded) gives.

A text message that is not UTF-8 returns ErrInvalidUTF8 in place of io.EOF
(RFC 6455 8.1). Frames that are not one message (RFC 6455 5.4) make
DataFramesReader return ErrContinuationFrameWithoutMsg for a continuation
first or after a FIN frame, ErrDataFrameDuringMsg for text or binary after the
first, and ErrDataFramesNotEnded for no frames or a last frame with FIN clear.
RSV1 on a frame after the first (RFC 7692 6.1), or on the first with
permessage-deflate not agreed (RFC 6455 5.2), makes it return
ErrReservedBitsSet.
*/
func (c *Conn) DataFramesReader(data_frames DataFrames) (io.ReadCloser, error) {
	return dataFramesReader(data_frames, dataFramesReaderDI{
		peerIsClient:          c.peerIsClient,
		deflateAgreedConfig:   c.permessageDeflateAgreedConfig,
		contextInflater:       c.contextInflater,
		noContextInflater:     c.noContextInflater,
		checkDataFramesFormat: checkDataFramesFormat,
		newDataFramesReader:   newDataFramesReader,
	})
}

type dataFramesReaderDI struct {
	peerIsClient          bool
	deflateAgreedConfig   *PermessageDeflateConfig
	contextInflater       connContextInflater
	noContextInflater     connNoContextInflater
	checkDataFramesFormat func(data_frames DataFrames) error
	newDataFramesReader   func(data_frames DataFrames) io.ReadCloser
}

func dataFramesReader(data_frames DataFrames, di dataFramesReaderDI) (io.ReadCloser, error) {
	if err := di.checkDataFramesFormat(data_frames); err != nil {
		return nil, err
	}
	if data_frames[0].RSV1 && di.deflateAgreedConfig == nil { // RFC 6455 5.2: no extension gives RSV1 a meaning
		return nil, ErrReservedBitsSet
	}

	var reader io.ReadCloser
	switch {
	case !data_frames[0].RSV1: // RFC 7692 6: the first frame marks a deflated message
		reader = di.newDataFramesReader(data_frames)
	case peerNoContextTakeover(di.peerIsClient, di.deflateAgreedConfig):
		reader = di.noContextInflater.inflateDataFramesReader(data_frames)
	default:
		reader = di.contextInflater.inflateDataFramesReader(data_frames)
	}

	if data_frames[0].Opcode == OpcodeText { // RFC 6455 8.1
		reader = &utf8CheckReader{reader: reader}
	}
	return reader, nil
}

/*
StartInflateDataFrames opens a deflated message inflated frame by frame, as in
a Data hook. It returns the message's token: pass it to PutDeflatedDataFrame,
PullInflatedBytes and EndInflateDataFrames.

Call it only when permessage-deflate was agreed and the message's first frame
has RSV1 set (RFC 7692 6), then put that frame first. It holds the inflater
until EndInflateDataFrames, which must follow, even on an error. The frames'
payloads are read in place, so leave them unchanged until they are pulled.
*/
func (c *Conn) StartInflateDataFrames() int64 {
	return c.contextInflater.startInflateFrames()
}

/*
PutDeflatedDataFrame puts the next frame of token's message. After the FIN
frame, nothing more goes in. A token whose message ended returns
ErrInflateFramesTokenStale.
*/
func (c *Conn) PutDeflatedDataFrame(token int64, f *Frame) error {
	return c.contextInflater.putDeflatedFrame(token, f)
}

/*
PullInflatedBytes writes what is inflated so far into buf[:cap(buf)] from its
start, and returns how many bytes it wrote. Pull until io.EOF.

Before the FIN frame, it waits for the next PutDeflatedDataFrame when the
frames put run out, so put from another goroutine, or pull only after the FIN
frame. One pull at a time, and EndInflateDataFrames only once it returned.

A token whose message ended returns ErrInflateFramesTokenStale. The message is
lost on any other error: ErrInflateDataInvalid when it does not inflate.
*/
func (c *Conn) PullInflatedBytes(token int64, buf []byte) (int, error) {
	n, err := c.contextInflater.pullInflatedBytes(token, buf)
	if err == io.EOF {
		return n, err
	}
	return n, inflateError(err)
}

/*
EndInflateDataFrames ends token's message and frees the inflater. For a peer
that agreed to no context takeover, the next message starts from an empty
window (RFC 7692 7.1.1). Ended before io.EOF for a peer that keeps its context,
close the connection: the next message may inflate to wrong bytes with no
error. A token whose message ended does nothing.
*/
func (c *Conn) EndInflateDataFrames(token int64) {
	if peerNoContextTakeover(c.peerIsClient, c.permessageDeflateAgreedConfig) {
		c.contextInflater.dropContext(token)
	}
	c.contextInflater.endInflateFrames(token)
}

/*
peerNoContextTakeover is whether the peer starts every message it sends from
an empty window. RFC 7692 7.1.1: client_no_context_takeover binds a client,
server_no_context_takeover a server. False when nothing was agreed.
*/
func peerNoContextTakeover(peer_is_client bool, agreed *PermessageDeflateConfig) bool {
	if agreed == nil {
		return false
	}
	if peer_is_client {
		return agreed.ClientNoContextTakeover
	}
	return agreed.ServerNoContextTakeover
}

// inflateError is err as the Conn returns it: compress/flate's errors wrapped
// in ErrInflateDataInvalid, a stale token as it is.
func inflateError(err error) error {
	if err == nil || err == ErrInflateFramesTokenStale {
		return err
	}
	return fmt.Errorf("%w: %w", ErrInflateDataInvalid, err)
}
