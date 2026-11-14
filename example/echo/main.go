// An echo server. Every text or binary message comes back unchanged.
//
//	cd example/echo && go run .
//
// Answer y to the TLS prompt to serve wss:// instead. Empty paths use the
// bundled ../server_fullchain.crt and ../server.key.
//
// The read loop is a wlgows.Listener: it validates each frame against RFC 6455,
// assembles fragmented messages, and calls the hook for the opcode. It never
// writes and never closes, so every Send below is this program's, in a hook.
//
// This serves raw TCP, which is fast, with crypto/tls on top when TLS is on.
// wlgows.Run has no TLS of its own. The timeout and size limit on the
// request are left to the caller. This example adds the timeout but no size
// limit, so a client can send headers at full speed until requestReadTimeout
// runs out. Add one yourself if you know what you are doing, or use net/http
// or Gin as hijack_http and hijack_gin do.
package main

import (
	"bufio"
	"compress/flate"
	"crypto/tls"
	"fmt"
	"io"
	"net"
	"net/http"
	"time"
	"unicode/utf8"

	"github.com/weilun-shrimp/wlgows/v7"
	"github.com/weilun-shrimp/wlgows/v7/example/example_helpers"
)

const (
	service = ":8001"

	// Run sets no timeout, so a client that never sends its request would
	// hold a goroutine forever.
	requestReadTimeout = 10 * time.Second

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

// readMessage returns the bytes of one message, up to maxDataFramesSize. It
// decompresses a compressed message and checks text as UTF-8. For a larger
// message, it returns ErrSizeLimitExceeded. See READING.md.
func readMessage(conn *wlgows.Conn, frames wlgows.DataFrames) ([]byte, error) {
	reader, err := conn.DataFramesReader(frames)
	if err != nil {
		return nil, err
	}
	defer reader.Close() // always close it
	return io.ReadAll(wlgows.NewSizeLimitReader(reader, maxDataFramesSize))
}

// Echoes are deflated when the handshake agreed permessage-deflate, and sent as
// is otherwise. DeflateLevel must be set: its zero is flate.NoCompression.
var sendDataConfig = wlgows.SendDataConfig{
	DeflateIfAgreed: true,
	DeflateLevel:    flate.DefaultCompression,
}

func main() {
	serverConfig, err := example_helpers.ReadUserInputServerConfig()
	if err != nil {
		fmt.Println("error: server config:", err)
		return
	}

	// Loaded once, so a bad path or a mismatched pair stops the server before
	// it listens.
	var tlsConfig *tls.Config
	if serverConfig.ServerCertPath != "" && serverConfig.ServerKeyPath != "" {
		certificate, err := tls.LoadX509KeyPair(
			serverConfig.ServerCertPath,
			serverConfig.ServerKeyPath,
		)
		if err != nil {
			fmt.Println("error: loading tls certificate:", err)
			return
		}
		tlsConfig = &tls.Config{Certificates: []tls.Certificate{certificate}}
	}

	server, err := wlgows.Run(service)
	if err != nil {
		fmt.Println("error: server run:", err)
		return
	}
	defer server.Close()

	fmt.Println("echo server listening on " + service)
	if tlsConfig != nil {
		fmt.Println("tls mode — connect with wss://")
	} else {
		fmt.Println("plain mode — connect with ws://")
	}
	for {
		netConn, err := server.Accept()
		if err != nil {
			fmt.Println("error: accept:", err)
			continue
		}
		// The TLS handshake runs on the first read, so the request deadline
		// in handleConn bounds it too.
		if tlsConfig != nil {
			netConn = tls.Server(netConn, tlsConfig)
		}
		go handleConn(netConn, serverConfig.SupportedSubprotocols)
	}
}

func handleConn(netConn net.Conn, supportedSubprotocols []string) {
	// The only close in this program. The hooks pause the loop and let this
	// function return rather than closing underneath it.
	defer netConn.Close()

	reader := bufio.NewReader(netConn)
	if err := netConn.SetReadDeadline(time.Now().Add(requestReadTimeout)); err != nil {
		fmt.Println("error: setting request deadline:", err)
		return
	}
	req, err := http.ReadRequest(reader)
	if err != nil {
		// RFC 6455 4.2.1: a malformed request is answered with 400.
		respWriter := wlgows.NewResponseWriter()
		respWriter.DeclineByError(wlgows.ErrHttpMsgFormationInvalid)
		wlgows.SendHandShakeResponse(netConn, respWriter.GenerateResponse())
		fmt.Println("error: reading request:", err)
		return
	}
	// Cleared, or it would cut off the frame reads too. The Listener sets its
	// own deadline before each frame.
	if err := netConn.SetReadDeadline(time.Time{}); err != nil {
		fmt.Println("error: clearing request deadline:", err)
		return
	}
	handShakeConfig := wlgows.ServerHandShakeConfig{
		// permessage-deflate (RFC 7692). One answer with every field zero
		// accepts any offer this package can serve. Matching adds the offer's
		// server_no_context_takeover (RFC 7692 7.1.1.1) and
		// client_no_context_takeover (7.1.1.2), so the client picks the mode.
		// An offer of server_max_window_bits under 15 is skipped (library
		// choice). A client that offers nothing gets no compression.
		PermessageDeflates: []wlgows.PermessageDeflateConfig{{}},

		// Subprotocols (RFC 6455 4.2.2). When no offer is one the server
		// speaks, this is "" and the connection opens without one.
		Subprotocol: example_helpers.PickSubprotocol(wlgows.RequestSubprotocols(req), supportedSubprotocols),
	}

	writer := bufio.NewWriter(netConn)
	conn, resp, err := wlgows.ServerHandShake(netConn, reader, writer, req, handShakeConfig)
	if err != nil {
		fmt.Println("error: handshake:", err)
		return
	}

	fmt.Println("connected:", conn.RemoteAddr())
	example_helpers.PrintAgreedDeflate(conn)
	example_helpers.PrintSubprotocol(resp.Header.Get("Sec-WebSocket-Protocol"))

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
		message, err := readMessage(conn, frames)
		if err != nil {
			listener.PauseListen(err) // Listen returns err
			return
		}
		fmt.Printf("text: frames=%d bytes=%d runes=%d: %s\n",
			len(frames), len(message), utf8.RuneCount(message), message)

		if err := conn.SendText(message, sendDataConfig); err != nil {
			fmt.Println("error: echo text:", err)
		}
	}

	// Arbitrary bytes — 5.6 gives binary no encoding at all.
	config.Binary = func(frames wlgows.DataFrames) {
		message, err := readMessage(conn, frames)
		if err != nil {
			listener.PauseListen(err) // Listen returns err
			return
		}
		fmt.Printf("binary: frames=%d bytes=%d\n", len(frames), len(message))

		if err := conn.SendBinary(message, sendDataConfig); err != nil {
			fmt.Println("error: echo binary:", err)
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
		fmt.Println("error: listen:", err)
	}
}
