package wlgows

import (
	"fmt"
	"io"
	"unicode/utf8"
)

// DataFramesBytesConfig is how DataFramesBytes reads one message.
type DataFramesBytesConfig struct {
	// InflateIfAgreed inflates the message when permessage-deflate was agreed
	// and its first frame has RSV1 set (RFC 7692 6: the sender chooses for each
	// message). Otherwise the payloads are copied as they are.
	InflateIfAgreed bool
}

/*
DataFramesBytes writes the payload of data_frames, one whole message ending
with FIN, into buf[:cap(buf)] from its start, and returns how many bytes it
wrote. cap(buf) is the size allowed: a message past it returns
ErrInflatedSizeExceeded when inflated, ErrDataFramesBytesSizeExceeded when not.

With config.InflateIfAgreed, a deflated message is inflated: from an empty
window for a peer that agreed to no context takeover, else from the messages
inflated before it (RFC 7692 7.1.1). Those must all have gone through the Conn
in order. One that does not inflate returns ErrInflateDataInvalid. After
ErrInflatedSizeExceeded from a peer that keeps its context, close the
connection (1009): the message was cut short, so the next one may inflate to
wrong bytes with no error.

A text message that is not UTF-8 once written returns ErrInvalidUTF8 (RFC 6455
8.1); a deflated one copied as it is is not checked. Frames that are not one
message (RFC 6455 5.4) return ErrContinuationFrameWithoutMsg for a
continuation first or after a FIN frame, ErrDataFrameDuringMsg for text or
binary after the first, and ErrDataFramesNotEnded for a last frame with FIN
clear. RSV1 on a frame after the first (RFC 7692 6.1), or on the first with
permessage-deflate not agreed (RFC 6455 5.2), returns ErrReservedBitsSet.
*/
func (c *Conn) DataFramesBytes(data_frames DataFrames, buf []byte, config DataFramesBytesConfig) (int, error) {
	return dataFramesBytes(data_frames, buf, config, dataFramesBytesDI{
		peerIsClient:        c.peerIsClient,
		deflateAgreedConfig: c.permessageDeflateAgreedConfig,
		contextInflater:     c.contextInflater,
		noContextInflater:   c.noContextInflater,
	})
}

type dataFramesBytesDI struct {
	peerIsClient        bool
	deflateAgreedConfig *PermessageDeflateConfig
	contextInflater     connContextInflater
	noContextInflater   connNoContextInflater
}

func dataFramesBytes(data_frames DataFrames, buf []byte, config DataFramesBytesConfig, di dataFramesBytesDI) (int, error) {
	if len(data_frames) == 0 {
		return 0, nil
	}
	if data_frames[0].Opcode == OpcodeContinuation { // RFC 6455 5.4: the first frame carries the message's opcode
		return 0, ErrContinuationFrameWithoutMsg
	}
	for i, f := range data_frames[1:] { // RFC 6455 5.4: every frame after the first continues it
		if f.Opcode != OpcodeContinuation {
			return 0, ErrDataFrameDuringMsg
		}
		if f.RSV1 { // RFC 7692 6.1: RSV1 goes on the first frame only
			return 0, ErrReservedBitsSet
		}
		if data_frames[i].FIN { // the message ended before f
			return 0, ErrContinuationFrameWithoutMsg
		}
	}
	if !data_frames[len(data_frames)-1].FIN { // RFC 6455 5.4: the last frame has FIN set
		return 0, ErrDataFramesNotEnded
	}
	if data_frames[0].RSV1 && di.deflateAgreedConfig == nil { // RFC 6455 5.2: no extension gives RSV1 a meaning
		return 0, ErrReservedBitsSet
	}

	deflated := config.InflateIfAgreed && di.deflateAgreedConfig != nil &&
		data_frames[0].RSV1 // RFC 7692 6: the first frame marks it
	var n int
	var err error
	switch {
	case !deflated:
		if data_frames.ByteLen() > cap(buf) {
			return 0, ErrDataFramesBytesSizeExceeded
		}
		for _, f := range data_frames {
			n += copy(buf[n:cap(buf)], f.PayloadData)
		}
	case peerNoContextTakeover(di.peerIsClient, di.deflateAgreedConfig):
		n, err = di.noContextInflater.inflateDataFrames(data_frames, buf)
	default:
		n, err = di.contextInflater.inflateDataFrames(data_frames, buf)
	}
	if err != nil {
		return n, inflateError(err)
	}

	// RFC 6455 8.1. A deflated message copied as it is holds no text to check.
	is_text := data_frames[0].Opcode == OpcodeText && (deflated || !data_frames[0].RSV1)
	if is_text && !utf8.Valid(buf[:n]) {
		return n, ErrInvalidUTF8
	}
	return n, nil
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
// in ErrInflateDataInvalid, the size limit and a stale token as they are.
func inflateError(err error) error {
	if err == nil || err == ErrInflatedSizeExceeded || err == ErrInflateFramesTokenStale {
		return err
	}
	return fmt.Errorf("%w: %w", ErrInflateDataInvalid, err)
}
