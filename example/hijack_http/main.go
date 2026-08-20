// The same echo server, but reached through net/http instead of raw TCP.
// HijackFromHttp takes the connection off http.Server after routing, so the
// WebSocket lives on a handler's own goroutine.
//
//	go run ./example/hijack_http   # in one terminal
//	go run ./example/client        # in another
//
// Set the cert paths that LoadServerTlsInfo reads to serve wss:// instead.
package main

import (
	"fmt"
	"net/http"
	"time"
	"unicode/utf8"

	"github.com/weilun-shrimp/wlgows/v4"
	"github.com/weilun-shrimp/wlgows/v4/example_helpers"
)

const (
	service = ":8001"

	// A peer can claim a 10 GB payload in a 10 byte header. Without this that
	// claim becomes a 10 GB allocation before a byte of payload arrives.
	maxMsgPayloadByteLen = 10 * 1024 * 1024 // 10 MB

	// Armed before each frame read, and only fires when no bytes arrive at all.
	frameReadTimeout = 60 * time.Second

	// A ping every interval, answered by the peer's pong (5.5.2). It only asks;
	// deciding a silent peer is gone would be a deadline of our own.
	pingInterval = 30 * time.Second
)

func main() {
	serverCrtPath, serverKeyPath, err := example_helpers.LoadServerTlsInfo()
	if err != nil {
		fmt.Println("load server tls info:", err)
		return
	}

	http.HandleFunc("/", handler)

	fmt.Println("hijack_http server listening on " + service)
	if serverCrtPath != "" && serverKeyPath != "" {
		fmt.Println("tls mode — connect with wss://")
		err = http.ListenAndServeTLS(service, serverCrtPath, serverKeyPath, nil)
	} else {
		fmt.Println("plain mode — connect with ws://")
		err = http.ListenAndServe(service, nil)
	}
	if err != nil {
		fmt.Println("starting server:", err)
	}
}

func handler(w http.ResponseWriter, r *http.Request) {
	netConn, bufReader, err := wlgows.HijackFromHttp(w)
	if err != nil {
		// Still an ordinary response: the hijack failed, so nothing was taken.
		http.Error(w, "could not hijack connection", http.StatusInternalServerError)
		return
	}

	// The only close in this handler. The hooks pause the loop and let this
	// function return rather than closing underneath it.
	defer netConn.Close()

	// r is already parsed by net/http, so no read step is needed here.
	conn, _, err := wlgows.ServerHandShake(netConn, bufReader, r)
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
	config.MaxMsgPayloadByteLen = maxMsgPayloadByteLen
	config.FrameReadTimeout = frameReadTimeout

	// A whole message, assembled across every fragment. The payload is already
	// checked as valid UTF-8 (5.6, 8.1).
	config.Text = func(frames wlgows.Frames) {
		text := frames.String()
		fmt.Printf("text: frames=%d bytes=%d runes=%d: %s\n",
			len(frames), frames.ByteLen(), utf8.RuneCountInString(text), text)

		if err := conn.SendText(frames.Bytes()); err != nil {
			fmt.Println("echo text:", err)
		}
	}

	// Arbitrary bytes — 5.6 gives binary no encoding at all.
	config.Binary = func(frames wlgows.Frames) {
		fmt.Printf("binary: frames=%d bytes=%d\n", len(frames), frames.ByteLen())

		if err := conn.SendBinary(frames.Bytes()); err != nil {
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
	// nil means PauseListen(nil) — here only the close and unknown hooks pause,
	// and neither has anything to report, so it is the ordinary shutdown.
	if err := listener.Listen(); err != nil {
		// No payload when no close frame can answer err: a dead socket, or
		// something this package cannot attribute to the peer.
		if payload := wlgows.StandardClosePayloadFor(err); payload != nil {
			conn.SendClose(payload)
		}
		fmt.Println("closing:", err)
	}
}
