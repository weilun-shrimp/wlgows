package wlgows

import (
	"fmt"
	"net"
	"testing"
	"time"
)

/*
End to end over a real socket, through the exported API: SetConfig, Listen and
PauseListen, with a real Conn and real frames off the wire.

The unit tests stub every collaborator, so nothing else exercises the wiring —
a nil di field or a closure handed the wrong di would compile, pass all of them,
and only fail here.
*/
func TestListenerIntegration(t *testing.T) {
	// net.Pipe is a real net.Conn without a port or a network stack, so the
	// frame reader, Seal and masking all run for real while the test stays
	// synchronous.
	netConn, peer := net.Pipe()
	defer netConn.Close()

	// The peer is a client, so every frame it sends is masked (RFC 6455 5.1).
	go func() {
		defer peer.Close()

		write := func(config NewFrameConfig) {
			config.Mask = true
			frame, err := NewFrame(config)
			if err != nil {
				return
			}
			peer.Write(frame.Seal())
		}

		write(NewFrameConfig{Opcode: OpcodeText, FIN: true, PayloadData: []byte("hello")})
		// A message split in two, with a ping in between — 5.4 allows a control
		// frame to arrive mid message.
		write(NewFrameConfig{Opcode: OpcodeText, PayloadData: []byte("wl")})
		write(NewFrameConfig{Opcode: OpcodePing, FIN: true, PayloadData: []byte("hb")})
		write(NewFrameConfig{Opcode: OpcodeContinuation, FIN: true, PayloadData: []byte("gows")})
		write(NewFrameConfig{Opcode: OpcodeBinary, FIN: true, PayloadData: []byte{0x00, 0xFF}})
		write(NewFrameConfig{
			Opcode: OpcodeClose, FIN: true,
			PayloadData: (&ClosePayload{StatusCode: CloseNormalClosure, Reason: "bye"}).Bytes(),
		})
	}()

	conn := NewConn(netConn, nil, nil, false)
	defer conn.Close()

	listener := &Listener{}
	var got []string

	err := listener.SetConfig(ListenerConfig{
		Conn:                 conn,
		PeerIsClient:         true,
		MaxMsgPayloadByteLen: 1024,
		FrameReadTimeout:     5 * time.Second,

		Text:   func(frames Frames) { got = append(got, "Text:"+frames.String()) },
		Binary: func(frames Frames) { got = append(got, fmt.Sprintf("Binary:% x", frames.Bytes())) },
		Ping:   func(frame *Frame) { got = append(got, "Ping:"+string(frame.PayloadData)) },
		Close: func(frame *Frame) {
			payload, err := frame.GetClosePayload()
			if err != nil {
				t.Errorf("GetClosePayload: %v", err)
			}
			got = append(got, fmt.Sprintf("Close:%d:%s", payload.StatusCode, payload.Reason))
			listener.PauseListen()
		},
	})
	if err != nil {
		t.Fatalf("SetConfig: %v", err)
	}

	// PauseListen from the Close hook, so this returns nil rather than a read
	// error.
	if err := listener.Listen(); err != nil {
		t.Fatalf("Listen: %v", err)
	}

	want := []string{
		"Text:hello",
		"Ping:hb",
		"Text:wlgows",
		"Binary:00 ff",
		"Close:1000:bye",
	}
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("hook %d: got %q, want %q", i, got[i], want[i])
		}
	}
}
