// An echo server. Every text or binary message comes back unchanged.
//
//	cd example
//	go run ./echo
//
// The read loop is a wlgows.Listener: it validates each frame against RFC 6455,
// assembles fragmented messages, and calls the hook for the opcode. It never
// writes and never closes, so every Send below is this program's, in a hook.
package main

import (
	"bufio"
	"fmt"
	"net"
	"net/http"
	"time"
	"unicode/utf8"

	"github.com/weilun-shrimp/wlgows/v7"
)

const (
	service = ":8001"

	// One message's byte budget, headers included. A peer can claim a 10 GB
	// payload in a 10 byte header, and without this that claim becomes a 10 GB
	// allocation before a single payload byte arrives.
	maxDataFramesSize = 10 * 1024 * 1024 // 10 MB

	// Armed before each frame read. It fires when no bytes arrive, so it does
	// not detect a peer that sends data while ignoring pings.
	frameReadTimeout = 60 * time.Second

	// A ping every interval, answered by the peer's pong (5.5.2). It only asks;
	// deciding a silent peer is gone would be a deadline of our own.
	pingInterval = 30 * time.Second
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
		netConn, err := server.Accept()
		if err != nil {
			fmt.Println("accept:", err)
			continue
		}
		go handleConn(netConn)
	}
}

func handleConn(netConn net.Conn) {
	// The only close in this program. The hooks pause the loop and let this
	// function return rather than closing underneath it.
	defer netConn.Close()

	r := bufio.NewReader(netConn)
	req, err := http.ReadRequest(r)
	if err != nil {
		// RFC 6455 4.2.1: a malformed request is answered with 400.
		respWriter := wlgows.NewResponseWriter()
		respWriter.DeclineByError(wlgows.ErrHttpMsgFormationInvalid)
		wlgows.SendHandShakeResponse(netConn, respWriter.GenerateResponse())
		fmt.Println("reading request:", err)
		return
	}
	// Subprotocols (RFC 6455 4.2.2). To require one, uncomment the block below
	// and add "slices" to the imports. A server that only speaks chat.v2 has
	// nothing to say to a client that did not offer it, so it refuses before
	// the handshake. To prefer chat.v2 without requiring it, uncomment only the
	// last line: a client that did not offer it still connects, with no
	// subprotocol.
	handShakeConfig := wlgows.ServerHandShakeConfig{}
	// if !slices.Contains(wlgows.RequestSubprotocols(req), "chat.v2") {
	// 	respWriter := wlgows.NewResponseWriter()
	// 	respWriter.WriteHeader(http.StatusBadRequest)
	// 	respWriter.Write([]byte("subprotocol chat.v2 required"))
	// 	wlgows.SendHandShakeResponse(netConn, respWriter.GenerateResponse())
	// 	return
	// }
	// handShakeConfig.Subprotocol = "chat.v2"

	conn, _, err := wlgows.ServerHandShake(netConn, r, bufio.NewWriter(netConn), req, handShakeConfig)
	if err != nil {
		fmt.Println("handshake:", err)
		return
	}

	fmt.Println("connected:", conn.RemoteAddr())

	// Ping, pong, close and reserved opcodes are answered for you, and masking
	// (5.1) is settled from which side this connection is.
	listener := conn.NewStandardListener()

	// SetConfig replaces all of it, so start from what the standard hooks left
	// rather than from an empty ListenerConfig.
	config := listener.GetConfig()
	config.MaxDataFramesSize = maxDataFramesSize
	config.MaxFrameSize = maxDataFramesSize            // one frame may carry the whole message
	config.MaxDataFrameCount = maxDataFramesSize / 256 // fragments of 256 bytes or more
	config.FrameReadTimeout = frameReadTimeout

	// A whole message, assembled across every fragment. The payload is already
	// checked as valid UTF-8 (5.6, 8.1).
	config.Text = func(frames wlgows.DataFrames) {
		text := frames.String()
		fmt.Printf("text: frames=%d bytes=%d runes=%d: %s\n",
			len(frames), frames.ByteLen(), utf8.RuneCountInString(text), text)

		if err := conn.SendText(frames.Bytes(), 0); err != nil {
			fmt.Println("echo text:", err)
		}
	}

	// Arbitrary bytes — 5.6 gives binary no encoding at all.
	config.Binary = func(frames wlgows.DataFrames) {
		fmt.Printf("binary: frames=%d bytes=%d\n", len(frames), frames.ByteLen())

		if err := conn.SendBinary(frames.Bytes(), 0); err != nil {
			fmt.Println("echo binary:", err)
		}
	}

	// The standard hooks already answer and pause, so these only report what
	// arrived and hand over. Reading them back out of the config is what makes
	// that possible.
	standardClose, standardUnknown := config.Close, config.Unknown
	config.Close = func(f *wlgows.Frame) {
		payload, _ := f.GetClosePayload() // already validated by the Listener
		fmt.Printf("closed by peer: %+v\n", payload)

		standardClose(f)
	}
	config.Unknown = func(f *wlgows.Frame) {
		fmt.Printf("unknown opcode %#x\n", f.Opcode)

		standardUnknown(f)
	}

	listener.SetConfig(config)

	// Answers the peer's pings by itself; this asks the peer in turn. It ends
	// when the connection does, so nothing here has to stop it.
	go conn.StartPingLoop(pingInterval, nil)

	// Blocks until a read fails, a frame breaks a rule, or a hook pauses it.
	// nil means the close hook paused it: the peer closed, the ordinary
	// shutdown. The unknown hook pauses with ErrReservedOpcode, already
	// answered with 1002, so the SendClose below gets ErrCloseAlreadySent.
	if err := listener.Listen(); err != nil {
		// No payload when no close frame can answer err: a dead socket, or
		// something this package cannot attribute to the peer.
		if payload := wlgows.StandardClosePayloadFor(err); payload != nil {
			conn.SendClose(payload)
		}
		fmt.Println("closing:", err)
	}
}
