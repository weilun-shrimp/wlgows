package benchmarks

import (
	"bufio"
	"bytes"
	"errors"
	"io"
	"slices"
	"testing"

	"github.com/weilun-shrimp/wlgows/v7"
)

// The calls a wlgows Send case can measure.
const (
	wlgowsSendFrameCall     = "SendFrame"
	wlgowsSendFrameLoopCall = "SendFrame loop"
	wlgowsSendDataCall      = "SendData"
)

// wlgowsReadCall is what every wlgows Read case measures. The Listener checks
// every frame header against RFC 6455 and takes LockRead for each frame.
const wlgowsReadCall = "Listener (Data hook)"

/*
wlgowsSendCalls is the public call each Send case measures, the fastest one
measured for it:

  - One sends its frame with SendFrame.
  - Many64 sends one message of 64 frames with SendData and ChunkSize. It
    flushes once, so small frames share Writes. Frames after the first are
    continuation frames (RFC 6455 5.4).
  - Many8 calls SendFrame for each frame. Each frame is larger than the
    writer, so one flush would not save a Write.
*/
var wlgowsSendCalls = []struct {
	id   string
	call string
}{
	{
		id:   "Server-Send:Frame-One-16B",
		call: wlgowsSendFrameCall,
	},
	{
		id:   "Server-Send:Frame-Many64-16B",
		call: wlgowsSendDataCall,
	},
	{
		id:   "Server-Send:Frame-One-1KB",
		call: wlgowsSendFrameCall,
	},
	{
		id:   "Server-Send:Frame-Many64-1KB",
		call: wlgowsSendDataCall,
	},
	{
		id:   "Server-Send:Frame-One-16KB",
		call: wlgowsSendFrameCall,
	},
	{
		id:   "Server-Send:Frame-Many8-16KB",
		call: wlgowsSendFrameLoopCall,
	},
	{
		id:   "Server-Send:Frame-One-1MB",
		call: wlgowsSendFrameCall,
	},
	{
		id:   "Server-Send:Frame-Many8-1MB",
		call: wlgowsSendFrameLoopCall,
	},
	{
		id:   "Client-Send:Frame-One-16B",
		call: wlgowsSendFrameCall,
	},
	{
		id:   "Client-Send:Frame-Many64-16B",
		call: wlgowsSendDataCall,
	},
	{
		id:   "Client-Send:Frame-One-1KB",
		call: wlgowsSendFrameCall,
	},
	{
		id:   "Client-Send:Frame-Many64-1KB",
		call: wlgowsSendDataCall,
	},
	{
		id:   "Client-Send:Frame-One-16KB",
		call: wlgowsSendFrameCall,
	},
	{
		id:   "Client-Send:Frame-Many8-16KB",
		call: wlgowsSendFrameLoopCall,
	},
	{
		id:   "Client-Send:Frame-One-1MB",
		call: wlgowsSendFrameCall,
	},
	{
		id:   "Client-Send:Frame-Many8-1MB",
		call: wlgowsSendFrameLoopCall,
	},
}

// TestWlgows runs every case on wlgows and writes results/wlgows.json.
func TestWlgows(t *testing.T) {
	sendCalls := map[string]string{}
	for _, sendCall := range wlgowsSendCalls {
		sendCalls[sendCall.id] = sendCall.call
	}

	cases := []libraryCase{}
	for _, benchmarkCase := range benchmarkCases {
		if benchmarkCase.operation == readOperation {
			cases = append(cases, libraryCase{
				id:        benchmarkCase.id,
				call:      wlgowsReadCall,
				benchmark: wlgowsListen(benchmarkCase),
			})
			continue
		}
		call := sendCalls[benchmarkCase.id]
		var benchmark func(b *testing.B)
		switch call {
		case wlgowsSendFrameCall, wlgowsSendFrameLoopCall:
			benchmark = wlgowsSendFrames(benchmarkCase)
		case wlgowsSendDataCall:
			benchmark = wlgowsSendData(benchmarkCase)
		default:
			t.Fatalf("%s has no call in wlgowsSendCalls", benchmarkCase.id)
		}
		cases = append(cases, libraryCase{
			id:        benchmarkCase.id,
			call:      call,
			benchmark: benchmark,
		})
	}

	runLibrary(t, "wlgows", "github.com/weilun-shrimp/wlgows/v7", cases)
}

// newWlgowsSendConn is a Conn on the case's side that writes to a
// countingConn through a bufferSize writer.
func newWlgowsSendConn(b *testing.B, benchmarkCase benchmarkCase) (*wlgows.Conn, *countingConn) {
	netConn := &countingConn{}
	conn, err := wlgows.NewConn(
		netConn,
		nil,
		bufio.NewWriterSize(netConn, bufferSize),
		wlgows.NewConnConfig{
			PeerIsClient: benchmarkCase.side == serverSide,
		},
	)
	if err != nil {
		b.Fatal(err)
	}
	return conn, netConn
}

// wlgowsSendFrames calls SendFrame once for each frame of the case.
func wlgowsSendFrames(benchmarkCase benchmarkCase) func(b *testing.B) {
	return func(b *testing.B) {
		conn, netConn := newWlgowsSendConn(b, benchmarkCase)
		frame := &wlgows.Frame{
			FIN:         true,
			Opcode:      wlgows.OpcodeBinary,
			PayloadData: casePayload(benchmarkCase.frameSize, benchmarkCase.side),
		}
		b.ResetTimer()

		for i := 0; i < b.N; i++ {
			for j := 0; j < benchmarkCase.frameCount; j++ {
				if err := conn.SendFrame(frame); err != nil {
					b.Fatal(err)
				}
			}
		}

		b.ReportMetric(float64(netConn.writes)/float64(b.N), "writes/op")
	}
}

// wlgowsSendData sends the case's frames as one message with SendData,
// one frame per ChunkSize.
func wlgowsSendData(benchmarkCase benchmarkCase) func(b *testing.B) {
	return func(b *testing.B) {
		conn, netConn := newWlgowsSendConn(b, benchmarkCase)
		payload := casePayload(benchmarkCase.frameSize, benchmarkCase.side)
		message := bytes.Repeat(payload, benchmarkCase.frameCount)
		config := wlgows.SendDataConfig{
			ChunkSize: len(payload),
		}
		b.ResetTimer()

		for i := 0; i < b.N; i++ {
			if err := conn.SendData(wlgows.OpcodeBinary, message, config); err != nil {
				b.Fatal(err)
			}
		}

		b.ReportMetric(float64(netConn.writes)/float64(b.N), "writes/op")
	}
}

// wireReplay reads wire replays times over, then returns io.EOF. Start offset
// at len(wire): each time the wire runs out, one replay starts it over.
type wireReplay struct {
	wire    []byte
	offset  int
	replays int
}

func (replay *wireReplay) Read(data []byte) (int, error) {
	if replay.offset == len(replay.wire) {
		if replay.replays == 0 {
			return 0, io.EOF
		}
		replay.replays--
		replay.offset = 0
	}
	n := copy(data, replay.wire[replay.offset:])
	replay.offset += n
	return n, nil
}

// newWlgowsListener is a Listener on the case's side, with every limit at 0
// (no limit), reading the recorded wire replays times through a bufferSize
// reader. data is its Data hook.
func newWlgowsListener(b *testing.B, benchmarkCase benchmarkCase, wire []byte, replays int, data func(data_frame *wlgows.Frame)) *wlgows.Listener {
	netConn := &countingConn{}
	replay := &wireReplay{
		wire:    wire,
		offset:  len(wire),
		replays: replays,
	}
	conn, err := wlgows.NewConn(
		netConn,
		bufio.NewReaderSize(replay, bufferSize),
		bufio.NewWriterSize(netConn, bufferSize),
		wlgows.NewConnConfig{
			PeerIsClient: benchmarkCase.side == serverSide,
		},
	)
	if err != nil {
		b.Fatal(err)
	}
	listener := wlgows.NewListener(conn, wlgows.NewListenerConfig{
		PeerIsClient: benchmarkCase.side == serverSide,
	})
	listener.SetConfig(wlgows.ListenerConfig{
		Data: data,
	})
	return listener
}

/*
wlgowsListen reads the case's frames from the recorded wire with one Listen,
which ends with io.EOF when the wire runs out.

  - Before timing, a Listener reads the wire once and checks every message.
  - Timed, a Listener reads the wire b.N times over, and the Data hook counts
    the messages by their FIN frame, so one Listen covers every op.
*/
func wlgowsListen(benchmarkCase benchmarkCase) func(b *testing.B) {
	return func(b *testing.B) {
		sender := benchmarkCase.side.peer()
		wire, err := recordedWire(sender, benchmarkCase.frameSize, benchmarkCase.frameCount)
		if err != nil {
			b.Fatal(err)
		}
		payload := casePayload(benchmarkCase.frameSize, sender)

		checkedMessages := 0
		checkListener := newWlgowsListener(b, benchmarkCase, wire, 1, func(data_frame *wlgows.Frame) {
			if !data_frame.FIN || data_frame.Opcode != wlgows.OpcodeBinary || !bytes.Equal(data_frame.PayloadData, payload) {
				b.Fatal("the frame read differs from the one sent")
			}
			checkedMessages++
		})
		if err := checkListener.Listen(); !errors.Is(err, io.EOF) {
			b.Fatalf("Listen returned %v, want io.EOF", err)
		}
		if checkedMessages != benchmarkCase.frameCount {
			b.Fatalf("Listen read %d messages, want %d", checkedMessages, benchmarkCase.frameCount)
		}

		messages := 0
		listener := newWlgowsListener(b, benchmarkCase, wire, b.N, func(data_frame *wlgows.Frame) {
			if data_frame.FIN {
				messages++
			}
		})
		b.ResetTimer()

		err = listener.Listen()

		b.StopTimer()
		if !errors.Is(err, io.EOF) {
			b.Fatalf("Listen returned %v, want io.EOF", err)
		}
		if messages != b.N*benchmarkCase.frameCount {
			b.Fatalf("Listen read %d messages, want %d", messages, b.N*benchmarkCase.frameCount)
		}
	}
}

func TestWlgowsSendCalls(t *testing.T) {
	want := []string{}
	for _, benchmarkCase := range benchmarkCases {
		if benchmarkCase.operation == sendOperation {
			want = append(want, benchmarkCase.id)
		}
	}
	got := []string{}
	for _, sendCall := range wlgowsSendCalls {
		got = append(got, sendCall.id)
	}
	if !slices.Equal(got, want) {
		t.Errorf("wlgowsSendCalls has the IDs %v, want every Send case once, in order: %v", got, want)
	}
}
