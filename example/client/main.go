// An interactive client for the echo server. Type a line, it goes out as a text
// message, and whatever comes back is printed. Type "exit" to close cleanly.
//
//	cd example/echo && go run .     # in one terminal
//	cd example/client && go run .   # in another
//
// Reading is a wlgows.Listener on its own goroutine, so pings are answered while
// the main goroutine sits blocked on the keyboard. Only PeerIsClient differs
// from the server's configuration: a server does not mask what it sends (5.1).
package main

import (
	"bufio"
	"compress/flate"
	"context"
	"fmt"
	"io"
	"net"
	"sync"
	"time"

	"github.com/weilun-shrimp/wlgows/v7"
	"github.com/weilun-shrimp/wlgows/v7/example/example_helpers"
)

const (
	// A peer can claim a 10 GB payload in a 10 byte header. Without this that
	// claim becomes a 10 GB allocation before a byte of payload arrives.
	maxDataFramesSize = 10 * 1024 * 1024 // 10 MB

	// Armed before each frame read, and only fires when no bytes arrive at all.
	frameReadTimeout = 60 * time.Second

	// Bound the TCP connect and, for wss://, the TLS handshake: dialTimeout
	// through the context, dialerTimeout through the net.Dialer.
	dialTimeout   = 15 * time.Second
	dialerTimeout = 15 * time.Second

	// ClientHandShake sets no timeout, so a server that never answers the
	// request would hold the client forever.
	responseReadTimeout = 10 * time.Second

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

// Sent text is deflated when the handshake agreed permessage-deflate, and sent
// as is otherwise. DeflateLevel must be set: its zero is flate.NoCompression.
var sendDataConfig = wlgows.SendDataConfig{
	DeflateIfAgreed: true,
	DeflateLevel:    flate.DefaultCompression,
}

func main() {
	clientConfig, err := example_helpers.ReadUserInputClientConfig()
	if err != nil {
		fmt.Println("error: client config:", err)
		return
	}

	// Either bounds the TCP connect and the TLS handshake; the earlier one wins.
	ctx, cancel := context.WithTimeout(context.Background(), dialTimeout)
	defer cancel()
	dialer := &net.Dialer{Timeout: dialerTimeout}
	netConn, req, err := wlgows.DialContext(ctx, clientConfig.WebsocketUrl, dialer, clientConfig.TlsConfig)
	if err != nil {
		fmt.Println("error: dial:", err)
		return
	}
	defer netConn.Close()

	reader := bufio.NewReader(netConn)
	writer := bufio.NewWriter(netConn)
	if err := netConn.SetReadDeadline(time.Now().Add(responseReadTimeout)); err != nil {
		fmt.Println("error: setting response deadline:", err)
		return
	}
	conn, resp, err := wlgows.ClientHandShake(netConn, reader, writer, req, clientConfig.HandShakeConfig)
	if err != nil {
		fmt.Println("error: handshake:", err)
		return
	}
	// Cleared, or it would cut off the frame reads too. The Listener sets its
	// own deadline before each frame.
	if err := netConn.SetReadDeadline(time.Time{}); err != nil {
		fmt.Println("error: clearing response deadline:", err)
		return
	}

	example_helpers.PrintAgreedDeflate(conn)
	// The server answers with one value or none. A value never offered already
	// failed the handshake above (RFC 6455 4.1).
	example_helpers.PrintSubprotocol(resp.Header.Get("Sec-WebSocket-Protocol"))
	fmt.Println("Client with server handshaked. Type a line to send it, or \"exit\" to quit.")

	// Closed once, by whichever side finishes first: the reader when the server
	// goes away, or the keyboard loop on "exit".
	stopChan := make(chan struct{})
	var stopOnce sync.Once
	stop := func() { stopOnce.Do(func() { close(stopChan) }) }

	// Ping (5.5.2), pong (5.5.3), close (5.5.1) and reserved opcodes (5.2) are
	// answered for you, and masking is settled from which side this is — here a
	// client, so the peer does not mask.
	listener := conn.NewStandardListener()

	// SetConfig replaces all of it, so start from what the standard hooks left.
	config := listener.GetConfig()
	config.MaxDataFramesSize = maxDataFramesSize
	config.MaxFrameSize = maxDataFramesSize            // one frame may carry the whole message
	config.MaxDataFrameCount = maxDataFramesSize / 256 // fragments of 256 bytes or more
	config.FrameReadTimeout = frameReadTimeout

	config.Text = func(frames wlgows.DataFrames) {
		message, err := readMessage(conn, frames)
		if err != nil {
			listener.PauseListen(err) // Listen returns err
			return
		}
		fmt.Println("echo server return: ", string(message))
	}
	config.Binary = func(frames wlgows.DataFrames) {
		message, err := readMessage(conn, frames)
		if err != nil {
			listener.PauseListen(err) // Listen returns err
			return
		}
		fmt.Printf("echo server return %d binary bytes\n", len(message))
	}

	// The standard close hook answers and pauses; this only reports first.
	standardClose := config.Close
	config.Close = func(f *wlgows.Frame) {
		// 5.5.1: a client must not show the reason to its user, so only the
		// code. No body means no code, which 7.1.5 records as 1005.
		code := uint16(1005)
		if payload, _ := f.GetClosePayload(); payload != nil { // already validated
			code = payload.StatusCode
		}
		fmt.Printf("closed by server: code %d\n", code)

		standardClose(f)
	}

	listener.SetConfig(config)

	// Answers the server's pings by itself; this asks the server in turn. It
	// ends when the connection does, so nothing here has to stop it.
	go conn.StartPingLoop(pingInterval, nil)

	go func() { // read from the server
		defer stop()

		// nil means PauseListen(nil) — the close hook above. The unknown hook
		// pauses with ErrReservedOpcode.
		if err := listener.Listen(); err != nil {
			if payload := wlgows.StandardClosePayloadFor(err); payload != nil {
				conn.SendClose(payload)
			}
			fmt.Println("error: listen:", err)
		}
	}()

	go func() { // read from the keyboard
		for {
			input, err := example_helpers.ReadUserInput()
			if err != nil {
				fmt.Println("error: reading input:", err)
				stop()
				return
			}
			if input == "exit" {
				// 5.5.1, 7.1.2: a client sends close and waits for the server's
				// close frame. So this goroutine steps back without stopping
				// anything — the server's reply reaches the Close hook, which
				// pauses the reader, and that is what ends the program.
				conn.SendClose(&wlgows.ClosePayload{
					StatusCode: wlgows.CloseNormalClosure, Reason: "bye",
				})
				fmt.Println("Client reader bye. Waiting for the server to close.")
				return
			}
			if err := conn.SendText([]byte(input), sendDataConfig); err != nil {
				fmt.Println("error: send:", err)
				stop()
				return
			}
		}
	}()

	// Block until either goroutine finishes. A select with a default branch here
	// would spin a whole core doing nothing.
	<-stopChan
	fmt.Println("main process detect the stop sign. Bye.")
}
