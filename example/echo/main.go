// An echo server. Every text or binary message comes back unchanged.
//
//	go run ./example/echo
//
// The read loop is a wlgows.Listener: it validates each frame against RFC 6455,
// assembles fragmented messages, and calls the hook for the opcode. It never
// writes and never closes, so every Send below is this program's, in a hook.
package main

import (
	"fmt"
	"time"
	"unicode/utf8"

	"github.com/weilun-shrimp/wlgows/v3"
)

const (
	service = ":8001"

	// One message's payload budget. A peer can claim a 10 GB payload in a 10
	// byte header, and without this that claim becomes a 10 GB allocation
	// before a single payload byte arrives.
	maxMsgPayloadByteLen = 10 * 1024 * 1024 // 10 MB

	// Armed before each frame read. It fires when no bytes arrive, so it does
	// not detect a peer that sends data while ignoring pings.
	frameReadTimeout = 60 * time.Second
)

func main() {
	server, err := wlgows.Run(service)
	if err != nil {
		fmt.Println("server run:", err)
		return
	}
	defer server.Close()

	fmt.Println("echo server listening on " + service)
	for {
		conn, err := server.Accept()
		if err != nil {
			fmt.Println("accept:", err)
			continue
		}
		go handleConn(conn)
	}
}

func handleConn(conn *wlgows.ServerConn) {
	// The only close in this program. The hooks pause the loop and let this
	// function return rather than closing underneath it.
	defer conn.Close()

	if _, err := conn.HandShake(); err != nil {
		fmt.Println("handshake:", err)
		return
	}
	fmt.Println("connected:", conn.RemoteAddr())

	// Ping, pong, close and reserved opcodes are answered for you, and masking
	// (5.1) is settled from which side this connection is.
	listener := conn.NewStandardListener()
	listener.SetMaxMsgPayloadByteLen(maxMsgPayloadByteLen)
	listener.SetFrameReadTimeout(frameReadTimeout)

	// A whole message, assembled across every fragment. The payload is already
	// checked as valid UTF-8 (5.6, 8.1).
	listener.SetText(func(frames wlgows.Frames) {
		text := frames.String()
		fmt.Printf("text: frames=%d bytes=%d runes=%d: %s\n",
			len(frames), frames.ByteLen(), utf8.RuneCountInString(text), text)

		if err := conn.SendText(frames.Bytes()); err != nil {
			fmt.Println("echo text:", err)
		}
	})

	// Arbitrary bytes — 5.6 gives binary no encoding at all.
	listener.SetBinary(func(frames wlgows.Frames) {
		fmt.Printf("binary: frames=%d bytes=%d\n", len(frames), frames.ByteLen())

		if err := conn.SendBinary(frames.Bytes()); err != nil {
			fmt.Println("echo binary:", err)
		}
	})

	// The standard close hook already answers and pauses; this only reports what
	// arrived, then hands over to it.
	standardClose := conn.RFC6455CloseHook(listener)
	listener.SetClose(func(f *wlgows.Frame) {
		payload, _ := f.GetClosePayload() // already validated by the Listener
		fmt.Printf("closed by peer: %+v\n", payload)

		standardClose(f)
	})

	// Blocks until a read fails, a frame breaks a rule, or a hook pauses it.
	// nil means PauseListen was called — here only the close and unknown hooks
	// do that, so it is the ordinary shutdown.
	if err := listener.Listen(); err != nil {
		// No payload when no close frame can answer err: a dead socket, or
		// something this package cannot attribute to the peer.
		if payload := wlgows.StandardClosePayloadFor(err); payload != nil {
			conn.SendClose(payload)
		}
		fmt.Println("closing:", err)
	}
}
