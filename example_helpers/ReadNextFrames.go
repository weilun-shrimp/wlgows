package example_helpers

import (
	"github.com/weilun-shrimp/wlgows/v4"
)

// FrameReader is what *wlgows.Conn satisfies, so one helper serves every example.
type FrameReader interface {
	GetNextFrame(maxByteLength uint64) (*wlgows.Frame, error)
}

/*
ReadNextFrames accumulates frames until one arrives with FIN set, which is the
loop every caller writes now that the library hands out single frames.

maxByteLength caps each individual frame, refused at its header before anything
is allocated. Pass 0 for no limit.

A control frame (close, ping, pong) always arrives unfragmented, so it comes
back as a Frames of one — check frames[0].Opcode before treating the payload as
message data.
*/
func ReadNextFrames(conn FrameReader, maxByteLength uint64) (wlgows.Frames, error) {
	var frames wlgows.Frames
	for {
		f, err := conn.GetNextFrame(maxByteLength)
		if err != nil {
			return frames, err
		}
		frames = append(frames, f)
		if f.FIN {
			return frames, nil
		}
	}
}
