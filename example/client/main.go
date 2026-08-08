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

	listener := &wlgows.Listener{}
	if err := listener.SetConfig(wlgows.ListenerConfig{
		Conn:                 conn,
		PeerIsClient:         false, // we are the client, so the peer does not mask (5.1)
		MaxMsgPayloadByteLen: maxMsgPayloadByteLen,
		FrameReadTimeout:     frameReadTimeout,

		Text: func(frames wlgows.Frames) {
			fmt.Println("echo server return: ", frames.String())
		},
		Binary: func(frames wlgows.Frames) {
			fmt.Printf("echo server return %d binary bytes\n", frames.ByteLen())
		},

		// 5.5.2: MUST answer with a pong carrying the same payload. Without this
		// hook a server watching for liveness eventually drops us.
		Ping: func(f *wlgows.Frame) {
			if err := conn.SendPong(f.PayloadData); err != nil {
				fmt.Println("pong:", err)
			}
		},

		// 5.5.1: MUST answer with a close, then stop reading.
		Close: func(f *wlgows.Frame) {
			payload, _ := f.GetClosePayload() // already validated by the Listener
			fmt.Printf("closed by server: %+v\n", payload)

			conn.SendClose(payload)
			listener.PauseListen()
		},

		// An opcode 5.2 reserves. A protocol error: answer 1002 and stop.
		Unknown: func(f *wlgows.Frame) {
			fmt.Printf("unknown opcode %#x\n", f.Opcode)

			conn.SendClose(&wlgows.ClosePayload{StatusCode: wlgows.CloseProtocolError})
			listener.PauseListen()
		},

		// Pong is nil on purpose: 5.5.3 says MUST NOT answer one.
	}); err != nil {
		fmt.Println("listener config:", err)
		return
	}

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
