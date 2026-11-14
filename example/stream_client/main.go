// Streams a file to stream_server as one binary message, a chunk at a time,
// so the file is never held in memory whole.
//
//	cd example/stream_server && go run .   # in one terminal
//	cd example/stream_client && go run .   # in another, then press enter for every default
//
// How it works:
//  1. Asks for the server, the file, and how many chunks to send. The
//     defaults send this repository's README, all of it.
//  2. Dials and does the handshake.
//  3. Starts a Listener. It answers the server's pings, and it receives the
//     server's text message with the sha256 of what the server got.
//  4. Sends the file in chunks of chunkByteLen, one frame each, then an
//     ending frame with FIN. The transmission API sets the opcode on the
//     first frame and OpcodeContinuation on the rest (RFC 6455 5.4).
//  5. Compares the file's sha256 with the server's.
//  6. Sends close and waits for the server's close (5.5.1, 7.1.2).
//
// You can also stop in the middle: at the chunk prompt, enter how many
// chunks to send. This is for seeing how the server handles a message that
// never ends. The client sends close without ever sending FIN, which RFC 6455
// 5.4 allows between fragments, and skips the sha256 check.
//
// The Listener runs beside the sending, not after it:
//   - A ping can arrive mid stream. RFC 6455 5.5.2 says to answer it with a
//     pong, which only works if something is reading.
//   - After we send close, we must wait for the server's close. Other frames
//     can arrive before it, so reading one frame is not enough.
//
// The printed byte counts are bytes read from the file. With
// permessage-deflate, a different number of bytes go over the connection.
package main

import (
	"bufio"
	"compress/flate"
	"context"
	"crypto/sha256"
	"fmt"
	"io"
	"math"
	"net"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/weilun-shrimp/wlgows/v7"
	"github.com/weilun-shrimp/wlgows/v7/example/example_helpers"
)

const (
	// This repository's README, from the folder the client runs in.
	defaultFilePath = "../../README.md"

	// One chunk is one frame. Small enough here that a modest file fragments;
	// 32 KB is a fair default for real use.
	chunkByteLen = 4 * 1024

	// A chunk limit that sends the whole file.
	noChunkLimit = math.MaxInt

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

	// How long to wait for stream_server's sha256 after the whole file is sent.
	serverSha256sumTimeout = 10 * time.Second

	// stream_server's only message is "sha256 " and 64 hex characters.
	maxServerMessageSize = 1024 // 1 KB
)

func main() {
	clientConfig, err := example_helpers.ReadUserInputClientConfig()
	if err != nil {
		fmt.Println("error: client config:", err)
		return
	}

	fmt.Print("Please input the file to stream, empty means " + defaultFilePath + " :")
	path, err := example_helpers.ReadUserInput()
	if err != nil {
		fmt.Println("error: reading path:", err)
		return
	}
	if path == "" {
		path = defaultFilePath
	}
	// Opened before dialing, so a bad path is reported before connecting.
	file, err := os.Open(path)
	if err != nil {
		fmt.Println("error: open:", err)
		return
	}
	defer file.Close()

	// Stopping in the middle sends close before the FIN frame (RFC 6455 5.4
	// lets a control frame go out between fragments), so the server has to
	// handle a message that never ends.
	fmt.Print("Please input the most chunks to send before stopping, empty means send the whole file :")
	chunkLimitInput, err := example_helpers.ReadUserInput()
	if err != nil {
		fmt.Println("error: reading chunk count:", err)
		return
	}
	chunkLimit := noChunkLimit
	if chunkLimitInput != "" {
		chunkLimit, err = strconv.Atoi(chunkLimitInput)
		if err != nil || chunkLimit < 1 {
			fmt.Println("error: chunk count must be a whole number of 1 or more:", chunkLimitInput)
			return
		}
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

	// Ping, pong and close answered for us. The close hook also pauses the loop,
	// which is what ends the wait at the bottom.
	listener := conn.NewStandardListener()

	config := listener.GetConfig()
	config.FrameReadTimeout = frameReadTimeout

	// Our stream_server answers a whole file with its sha256 as one text
	// message. That is this example's own convention, not WebSocket's: another
	// server may send nothing, and then the wait below times out.
	// Room for 1, so the hook never waits on main.
	serverSha256sumChan := make(chan string, 1)
	config.Text = func(frames wlgows.DataFrames) {
		reader, err := conn.DataFramesReader(frames)
		if err != nil {
			listener.PauseListen(err) // Listen returns err
			return
		}
		defer reader.Close() // always close it
		message, err := io.ReadAll(wlgows.NewSizeLimitReader(reader, maxServerMessageSize))
		if err != nil {
			listener.PauseListen(err) // Listen returns err
			return
		}
		select {
		case serverSha256sumChan <- string(message):
		default: // one already waits; main only reads one
		}
	}

	standardClose := config.Close
	config.Close = func(f *wlgows.Frame) {
		// 5.5.1: a client must not show the reason to its user, so only the
		// code. No body means no code, which 7.1.5 records as 1005.
		closeStatusCode := uint16(1005)
		if closePayload, _ := f.GetClosePayload(); closePayload != nil { // already validated
			closeStatusCode = closePayload.StatusCode
		}
		fmt.Printf("server closed: code %d\n", closeStatusCode)

		standardClose(f)
	}
	listener.SetConfig(config)

	// Reading runs beside the sending, not after it: a ping arriving mid stream
	// is answered while the chunks keep going out. listenerStopped closes once
	// Listen returns.
	listenerStopped := make(chan struct{})
	go func() {
		defer close(listenerStopped)

		if err := listener.Listen(); err != nil {
			fmt.Println("error: listen:", err)
		}
	}()

	go conn.StartPingLoop(pingInterval, nil)

	if err := conn.StartLongDataTransmission(wlgows.OpcodeBinary, startLongDataTransmissionConfig); err != nil {
		fmt.Println("error: stream:", err)
		return
	}
	sha256sum, chunkCount, byteCount, err := sendChunks(conn, file, chunkLimit)
	if err == nil && chunkLimit == noChunkLimit {
		// FIN tells the server the message ended. Without it the message is
		// left unterminated, so a partial file is never taken as whole.
		// With permessage-deflate agreed, this frame is not empty: deflating
		// nothing still sends the single byte 00 (RFC 7692 7.2.3.6).
		err = conn.EndLongDataTransmission(nil, transmitDataConfig)
	}
	// Before End this leaves the message unterminated, which the close below
	// then ends (RFC 6455 5.5.1).
	conn.ReleaseLongDataTransmission()
	if err != nil {
		fmt.Println("error: stream:", err)
		return
	}

	closeReason := "done"
	if chunkLimit == noChunkLimit {
		fmt.Printf("sent %s: %d chunks, %d bytes\nsha256 %x\n", path, chunkCount, byteCount, sha256sum)
		compareServerSha256sum(sha256sum, serverSha256sumChan, listenerStopped)
	} else {
		fmt.Printf("stopped after %d chunks, %d bytes: the message is not ended\n", chunkCount, byteCount)
		closeReason = "stopped in the middle"
	}

	// 5.5.1, 7.1.2: send close, then wait for the server's reply — the close hook
	// pauses the loop, so this returns once it has arrived.
	conn.SendClose(&wlgows.ClosePayload{StatusCode: wlgows.CloseNormalClosure, Reason: closeReason})
	<-listenerStopped
}

// The stream is deflated when the handshake agreed permessage-deflate, and sent
// as is otherwise. DeflateLevel must be set: its zero is flate.NoCompression.
var (
	startLongDataTransmissionConfig = wlgows.StartLongDataTransmissionConfig{DeflateIfAgreed: true}
	transmitDataConfig              = wlgows.TransmitDataConfig{DeflateLevel: flate.DefaultCompression}
)

// sendChunks sends reader as chunks of the open transmission, at most
// chunkLimit of them, and returns at the reader's end or at the limit, with
// the sha256 of what it sent. It never sends FIN: EndLongDataTransmission does.
func sendChunks(
	conn *wlgows.Conn,
	reader io.Reader,
	chunkLimit int,
) (
	sha256sum []byte,
	chunkCount int,
	byteCount int64,
	err error,
) {
	hasher := sha256.New()
	buf := make([]byte, chunkByteLen)
	for chunkCount < chunkLimit {
		n, readErr := reader.Read(buf)

		// Read may return bytes AND io.EOF together. Handling n before readErr
		// is what stops the last chunk being dropped.
		if n > 0 {
			if err := conn.TransmitData(buf[:n], transmitDataConfig); err != nil {
				return nil, chunkCount, byteCount, err
			}
			hasher.Write(buf[:n])
			chunkCount++
			byteCount += int64(n)
			fmt.Printf("chunk %d: %d bytes\n", chunkCount, n)
		}

		if readErr == io.EOF {
			break
		}
		if readErr != nil {
			return nil, chunkCount, byteCount, readErr
		}
	}
	return hasher.Sum(nil), chunkCount, byteCount, nil
}

// compareServerSha256sum waits for stream_server's sha256 and prints whether
// it matches sha256sum. It gives up after serverSha256sumTimeout, or when
// listenerStopped closes because the connection stopped.
func compareServerSha256sum(
	sha256sum []byte,
	serverSha256sumChan <-chan string,
	listenerStopped <-chan struct{},
) {
	select {
	case reply := <-serverSha256sumChan:
		serverSha256sum := strings.TrimPrefix(reply, "sha256 ")
		if serverSha256sum == fmt.Sprintf("%x", sha256sum) {
			fmt.Println("sha256 matches the server's")
		} else {
			fmt.Printf("sha256 MISMATCH: sent %x, server got %s\n", sha256sum, serverSha256sum)
		}
	case <-time.After(serverSha256sumTimeout):
		fmt.Println("the server sent no sha256")
	case <-listenerStopped:
		fmt.Println("the server sent no sha256: the connection stopped")
	}
}
