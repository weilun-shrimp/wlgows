package wlgows

import (
	"io"
	"strings"
)

/*
DataFrames is one WebSocket message: the frames it was split into, in wire order.

A message is only complete once a frame with FIN set arrives, and reading them
is the caller's job — a Conn hands out one frame at a time, so a caller that
does not want to hold a huge message in memory never has to:

	var frames wlgows.DataFrames
	for {
		conn.LockRead()
		frame, err := conn.GetNextFrameHeader()
		if err == nil && frame.GetDeclaredPayloadSize() > maxPayloadLength {
			err = errTooLarge // judged before the payload is allocated
		}
		if err == nil {
			err = conn.ReadFramePayload(frame)
		}
		conn.UnlockRead()
		if err != nil {
			return err
		}
		if !wlgows.IsDataOpcode(frame.Opcode) {
			continue // a control frame: answer it; a reserved opcode fails the connection (5.2)
		}
		frames = append(frames, frame)
		if frame.FIN {
			break
		}
	}
*/
type DataFrames []*Frame

/*
String joins every frame payload into one string.

Reference: https://studygolang.com/articles/12796
用傳統的string([]byte)方法可能會發生字串符段連接問題, 因為一個UTF-8中文是3bytes，如果剛好切一半在下一個frame就完了
而且很可能會有超大量字串，所以要用效能最好的strings.Builder底層自動分配資料至內部slice再組成string

Note this makes DataFrames a fmt.Stringer: a %v or %s of a DataFrames prints the whole
payload, so avoid logging one directly when the message may be large.
*/
func (f DataFrames) String() string {
	var builder strings.Builder
	builder.Grow(f.ByteLen()) // one exact allocation instead of regrowth
	for _, frame := range f {
		builder.Write(frame.PayloadData)
	}
	return builder.String()
}

/*
Bytes joins every frame payload into one slice.

Prefer it over []byte(String()) whenever the caller wants bytes — the conversion
copies the assembled payload a second time, so the byte path costs two full-size
buffers where this costs one. A binary (opcode 2) message has no reason to
become a string at all.

The result is a fresh copy, so mutating it never reaches the frames. Callers
willing to trade that safety for zero allocations can read a single frame
message straight off f[0].PayloadData, but must then treat the slice as owned by
the DataFrames.
*/
func (f DataFrames) Bytes() []byte {
	result := make([]byte, 0, f.ByteLen())
	for _, frame := range f {
		result = append(result, frame.PayloadData...)
	}
	return result
}

// ByteLen is the byte length of what String and Bytes return, without
// assembling them. Never a character count: "中文字" reports 9, not 3.
//
// Not a size guard — judge each frame's header before reading its payload; by the
// time a DataFrames exists its frames have already been read and allocated.
func (f DataFrames) ByteLen() int {
	total := 0
	for _, frame := range f {
		total += len(frame.PayloadData)
	}
	return total
}

/*
checkDataFramesFormat returns nil when data_frames are one message: a text or
binary frame, then continuations, the last with FIN set (RFC 6455 5.4). RSV1
goes only on the first frame (RFC 7692 6.1). No frames return
ErrDataFramesNotEnded.
*/
func checkDataFramesFormat(data_frames DataFrames) error {
	if len(data_frames) == 0 { // no FIN frame
		return ErrDataFramesNotEnded
	}
	if data_frames[0].Opcode == OpcodeContinuation { // RFC 6455 5.4: the first frame carries the message's opcode
		return ErrContinuationFrameWithoutMsg
	}
	for i, f := range data_frames[1:] { // RFC 6455 5.4: every frame after the first continues it
		if f.Opcode != OpcodeContinuation {
			return ErrDataFrameDuringMsg
		}
		if f.RSV1 { // RFC 7692 6.1: RSV1 goes on the first frame only
			return ErrReservedBitsSet
		}
		if data_frames[i].FIN { // the message ended before f
			return ErrContinuationFrameWithoutMsg
		}
	}
	if !data_frames[len(data_frames)-1].FIN { // RFC 6455 5.4: the last frame has FIN set
		return ErrDataFramesNotEnded
	}
	return nil
}

// newDataFramesReader returns a reader of data_frames' payloads as they are,
// one whole message not deflated. It reads them in place.
func newDataFramesReader(data_frames DataFrames) io.ReadCloser {
	return &dataFramesPayloadReader{dataFrames: data_frames}
}

// dataFramesPayloadReader reads the payloads of dataFrames in order, from
// where the last Read stopped.
type dataFramesPayloadReader struct {
	dataFrames   DataFrames
	frameIndex   int // the frame being read
	payloadIndex int // where the next Read starts in that frame's payload
	closed       bool
}

// Read copies the next payload bytes into p, across frames. io.EOF after the
// last frame. After Close, it returns io.ErrClosedPipe.
func (r *dataFramesPayloadReader) Read(p []byte) (int, error) {
	if r.closed {
		return 0, io.ErrClosedPipe
	}
	n := 0
	for n < len(p) && r.frameIndex < len(r.dataFrames) {
		payload := r.dataFrames[r.frameIndex].PayloadData
		copied := copy(p[n:], payload[r.payloadIndex:])
		n += copied
		r.payloadIndex += copied
		if r.payloadIndex == len(payload) { // this frame is read: go to the next
			r.frameIndex++
			r.payloadIndex = 0
		}
	}
	if n == 0 && r.frameIndex == len(r.dataFrames) {
		return 0, io.EOF
	}
	return n, nil
}

// Close lets go of the frames. A second Close does nothing.
func (r *dataFramesPayloadReader) Close() error {
	r.dataFrames = nil
	r.closed = true
	return nil
}
