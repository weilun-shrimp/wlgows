// Streams a file as one binary message, a chunk at a time, without ever holding
// it whole.
//
//	cd example
//	go run ./stream_server   # in one terminal
//	go run ./stream_client   # in another, then press enter twice
//
// chunkByteLen is deliberately small so an ordinary file still fragments: at
// 4 KB this repository's README goes out as one fragment per 4 KB, plus the empty
// frame End sends to carry FIN. RFC 6455 5.4 puts the opcode on the first frame
// and OpcodeContinuation on the rest, and the transmission API does that for
// you — the loop below only supplies bytes, and never has to keep them.
//
// Sending is only half of it. A Listener runs the whole time, because the server
// may ping mid stream (5.5.2 says answer) and its close has to be waited for
// (5.5.1, 7.1.2) — reading one frame and hoping it is the close is how a client
// misses both.
package main

import (
	"bufio"
	"context"
	"crypto/sha256"
	"fmt"
	"io"
	"net"
	"os"
	"time"

	"github.com/weilun-shrimp/wlgows/v7"
	"github.com/weilun-shrimp/wlgows/v7/example/example_helpers"
)

const (
	defaultURL  = "ws://localhost:8001"
	defaultPath = "../README.md"

	// One chunk is one frame. Small enough here that a modest file fragments;
	// 32 KB is a fair default for real use.
	chunkByteLen = 4 * 1024

	frameReadTimeout = 60 * time.Second

	// Bound the TCP connect and, for wss://, the TLS handshake: dialTimeout
	// through the context, dialerTimeout through the net.Dialer.
	dialTimeout   = 15 * time.Second
	dialerTimeout = 15 * time.Second

	// A ping every interval, answered by the peer's pong (5.5.2). It only asks;
	// deciding a silent peer is gone would be a deadline of our own.
	pingInterval = 30 * time.Second
)

func main() {
	fmt.Print("url (enter for " + defaultURL + ") :")
	url, err := example_helpers.ReadUserInput()
	if err != nil {
		fmt.Println("reading url:", err)
		return
	}
	if url == "" {
		url = defaultURL
	}

	fmt.Print("file to stream (enter for " + defaultPath + ") :")
	path, err := example_helpers.ReadUserInput()
	if err != nil {
		fmt.Println("reading path:", err)
		return
	}
	if path == "" {
		path = defaultPath
	}

	file, err := os.Open(path)
	if err != nil {
		fmt.Println("open:", err)
		return
	}
	defer file.Close()

	// Either bounds the TCP connect and the TLS handshake; the earlier one wins.
	ctx, cancel := context.WithTimeout(context.Background(), dialTimeout)
	defer cancel()
	dialer := &net.Dialer{Timeout: dialerTimeout}
	netConn, req, err := wlgows.DialContext(ctx, url, dialer, nil)
	if err != nil {
		fmt.Println("dial:", err)
		return
	}
	defer netConn.Close()

	// Subprotocols (RFC 6455 4.1). To offer chat.v2 and require the server to
	// pick it, uncomment this line and the check after the handshake. A list
	// goes in one header: "chat.v2, chat.v1".
	// req.Header.Set("Sec-WebSocket-Protocol", "chat.v2")

	r := bufio.NewReader(netConn)
	conn, _, err := wlgows.ClientHandShake(netConn, r, bufio.NewWriter(netConn), req)
	if err != nil {
		fmt.Println("handshake:", err)
		return
	}

	// The server answers with one value or none. A value never offered already
	// failed the handshake above (4.1). Change the _ above to resp to use this.
	// An empty answer means the server picked none. The connection is open, so
	// start the closing handshake with a close frame before returning (7.1.2).
	// if resp.Header.Get("Sec-WebSocket-Protocol") != "chat.v2" {
	// 	fmt.Println("server did not pick chat.v2")
	// 	conn.SendClose(&wlgows.ClosePayload{StatusCode: wlgows.ClosePolicyViolation, Reason: "subprotocol chat.v2 required"})
	// 	return
	// }

	// Ping, pong and close answered for us. The close hook also pauses the loop,
	// which is what ends the wait at the bottom.
	listener := conn.NewStandardListener()

	config := listener.GetConfig()
	config.FrameReadTimeout = frameReadTimeout

	standardClose := config.Close
	config.Close = func(f *wlgows.Frame) {
		// 5.5.1: a client must not show the reason to its user, so only the
		// code. No body means no code, which 7.1.5 records as 1005.
		code := uint16(1005)
		if payload, _ := f.GetClosePayload(); payload != nil { // already validated
			code = payload.StatusCode
		}
		fmt.Printf("server closed: code %d\n", code)

		standardClose(f)
	}
	listener.SetConfig(config)

	// Reading runs beside the sending, not after it: a ping arriving mid stream
	// is answered while the chunks keep going out.
	reading := make(chan struct{})
	go func() {
		defer close(reading)

		if err := listener.Listen(); err != nil {
			fmt.Println("reader stopping:", err)
		}
	}()

	go conn.StartPingLoop(pingInterval, nil)

	sum, chunks, bytes, err := stream(conn, file)
	if err != nil {
		fmt.Println("stream:", err)
		return
	}
	fmt.Printf("sent %s: %d chunks, %d bytes\nsha256 %x\n", path, chunks, bytes, sum)

	// 5.5.1, 7.1.2: send close, then wait for the server's reply — the close hook
	// pauses the loop, so this returns once it has arrived.
	conn.SendClose(&wlgows.ClosePayload{StatusCode: wlgows.CloseNormalClosure, Reason: "done"})
	<-reading
}

func stream(conn *wlgows.Conn, r io.Reader) (sum []byte, chunks int, bytes int64, err error) {
	if err := conn.StartLongDataTransmission(wlgows.OpcodeBinary); err != nil {
		return nil, 0, 0, err
	}
	defer conn.ReleaseLongDataTransmission()

	hasher := sha256.New()
	buf := make([]byte, chunkByteLen)
	for {
		n, readErr := r.Read(buf)

		// Read may return bytes AND io.EOF together. Handling n before readErr
		// is what stops the last chunk being dropped.
		if n > 0 {
			if err := conn.TransmitData(buf[:n]); err != nil {
				return nil, chunks, bytes, err
			}
			hasher.Write(buf[:n])
			chunks++
			bytes += int64(n)
			fmt.Printf("chunk %d: %d bytes\n", chunks, n)
		}

		if readErr == io.EOF {
			// FIN tells the server the message ended. Any return before this
			// leaves it unterminated, so a partial file is never taken as whole.
			if err := conn.EndLongDataTransmission(nil); err != nil {
				return nil, chunks, bytes, err
			}
			return hasher.Sum(nil), chunks, bytes, nil
		}
		if readErr != nil {
			return nil, chunks, bytes, readErr
		}
	}
}
