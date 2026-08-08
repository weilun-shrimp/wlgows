// An interactive client for the echo server. Type a line, it goes out as a text
// message, and whatever comes back is printed. Type "exit" to close cleanly.
//
//	go run ./example/echo     # in one terminal
//	go run ./example/client   # in another
//
// Reading is a wlgows.Listener on its own goroutine, so pings are answered while
// the main goroutine sits blocked on the keyboard. Only PeerIsClient differs
// from the server's configuration: a server does not mask what it sends (5.1).
package main

import (
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"os"
	"sync"
	"time"

	"github.com/weilun-shrimp/wlgows/v3"
	"github.com/weilun-shrimp/wlgows/v3/example_helpers"
)

const (
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
	fmt.Print("Please input the url (eg. ws://localhost:8001) :")
	url, err := example_helpers.ReadUserInput()
	if err != nil {
		fmt.Println("reading url:", err)
		return
	}

	fmt.Print("Please input the trust ca.crt path (eg. ./ca.crt), empty means no need :")
	caPath, err := example_helpers.ReadUserInput()
	if err != nil {
		fmt.Println("reading ca path:", err)
		return
	}
	var tlsConfig *tls.Config
	if caPath != "" {
		tlsConfig = loadCA(caPath)
	}

	conn, err := wlgows.Dial(url, tlsConfig)
	if err != nil {
		fmt.Println("dial:", err)
		return
	}
	defer conn.Close()

	if err := conn.HandShake(); err != nil {
		fmt.Println("handshake:", err)
		return
	}
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
	config.MaxMsgPayloadByteLen = maxMsgPayloadByteLen
	config.FrameReadTimeout = frameReadTimeout

	config.Text = func(frames wlgows.Frames) {
		fmt.Println("echo server return: ", frames.String())
	}
	config.Binary = func(frames wlgows.Frames) {
		fmt.Printf("echo server return %d binary bytes\n", frames.ByteLen())
	}

	// The standard close hook answers and pauses; this only reports first.
	standardClose := config.Close
	config.Close = func(f *wlgows.Frame) {
		payload, _ := f.GetClosePayload() // already validated by the Listener
		fmt.Printf("closed by server: %+v\n", payload)

		standardClose(f)
	}

	listener.SetConfig(config)

	// Answers the server's pings by itself; this asks the server in turn. It
	// ends when the connection does, so nothing here has to stop it.
	go conn.StartPingLoop(pingInterval, nil)

	go func() { // read from the server
		defer stop()

		// nil means PauseListen was called — the Close or Unknown hook above.
		if err := listener.Listen(); err != nil {
			if payload := wlgows.StandardClosePayloadFor(err); payload != nil {
				conn.SendClose(payload)
			}
			fmt.Println("reader stopping:", err)
		}
	}()

	go func() { // read from the keyboard
		for {
			input, err := example_helpers.ReadUserInput()
			if err != nil {
				fmt.Println("reading input:", err)
				stop()
				return
			}
			if input == "exit" {
				// 7.1.1: a client sends close and waits for the server to close
				// the socket. So this goroutine steps back without stopping
				// anything — the server's reply reaches the Close hook, which
				// pauses the reader, and that is what ends the program.
				conn.SendClose(&wlgows.ClosePayload{
					StatusCode: wlgows.CloseNormalClosure, Reason: "bye",
				})
				fmt.Println("Client reader bye. Waiting for the server to close.")
				return
			}
			if err := conn.SendText([]byte(input)); err != nil {
				fmt.Println("send:", err)
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

func loadCA(caPath string) *tls.Config {
	caCert, err := os.ReadFile(caPath)
	if err != nil {
		panic(err)
	}

	caCertPool := x509.NewCertPool()
	if ok := caCertPool.AppendCertsFromPEM(caCert); !ok {
		panic("failed to append CA certificate")
	}

	return &tls.Config{RootCAs: caCertPool}
}
