package wlgows

import (
	"strings"
)

/*
Frames is one WebSocket message: the frames it was split into, in wire order.

A message is only complete once a frame with FIN set arrives, and reading them
is the caller's job — GetNextFrame hands out one frame at a time, so a caller
that does not want to hold a huge message in memory never has to:

	var frames wlgows.Frames
	for {
		f, err := conn.GetNextFrame(maxByteLength)
		if err != nil {
			return err
		}
		frames = append(frames, f)
		if f.FIN {
			break
		}
	}
*/
type Frames []*Frame

/*
String joins every frame payload into one string.

Reference: https://studygolang.com/articles/12796
用傳統的string([]byte)方法可能會發生字串符段連接問題, 因為一個UTF-8中文是3bytes，如果剛好切一半在下一個frame就完了
而且很可能會有超大量字串，所以要用效能最好的strings.Builder底層自動分配資料至內部slice再組成string

Note this makes Frames a fmt.Stringer: a %v or %s of a Frames prints the whole
payload, so avoid logging one directly when the message may be large.
*/
func (f Frames) String() string {
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
the Frames.
*/
func (f Frames) Bytes() []byte {
	result := make([]byte, 0, f.ByteLen())
	for _, frame := range f {
		result = append(result, frame.PayloadData...)
	}
	return result
}

// ByteLen is the byte length of what String and Bytes return, without
// assembling them. Never a character count: "中文字" reports 9, not 3.
//
// Not a size guard — pass a max byte length to GetNextFrame for that; by the
// time a Frames exists its frames have already been read and allocated.
func (f Frames) ByteLen() int {
	total := 0
	for _, frame := range f {
		total += len(frame.PayloadData)
	}
	return total
}
