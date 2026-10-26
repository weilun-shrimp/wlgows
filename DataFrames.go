package wlgows

import (
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
