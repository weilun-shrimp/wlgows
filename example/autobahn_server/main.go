// An echo server for the Autobahn|Testsuite (wstest -m fuzzingclient). Every
// text or binary message comes back unchanged. See README.md.
//
//	cd example/autobahn_server && go run .
//
// It uses net/http, as SERVER.md recommends, and asks nothing on stdin.
package main

import (
	"compress/flate"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/weilun-shrimp/wlgows/v7"
)

const (
	service = ":9001"

	// One message's byte budget, headers included. Autobahn's largest message
	// is 16 MB (cases 9.x), so twice that leaves room for the headers.
	maxDataFramesSize = 32 * 1024 * 1024 // 32 MB

	// The large fragmented messages (cases 9.3.x, 9.4.x) use fragments of 64
	// bytes or more. The 1-byte fragments (cases 6.2.x, 6.4.x) come in short
	// messages.
	maxDataFrameCount = maxDataFramesSize / 64

	// The time limit for reading one whole frame, the wait for it included.
	frameReadTimeout = 60 * time.Second

	// net/http sets no timeout by default.
	readHeaderTimeout = 10 * time.Second
	idleTimeout       = 60 * time.Second
	maxHeaderBytes    = 16 * 1024 // 16 KB
)

// Echoes are deflated when the handshake agreed permessage-deflate (RFC 7692).
// DeflateLevel must be set: its zero is flate.NoCompression.
var sendDataConfig = wlgows.SendDataConfig{
	DeflateIfAgreed: true,
	DeflateLevel:    flate.DefaultCompression,
}

func main() {
	server := &http.Server{
		Addr:              service,
		Handler:           http.HandlerFunc(handler),
		ReadHeaderTimeout: readHeaderTimeout,
		IdleTimeout:       idleTimeout,
		MaxHeaderBytes:    maxHeaderBytes,
	}

	fmt.Println("autobahn_server listening on ws://127.0.0.1" + service)
	if err := server.ListenAndServe(); err != nil {
		fmt.Println("error: starting server:", err)
	}
}

func handler(w http.ResponseWriter, r *http.Request) {
	hijacker, ok := w.(http.Hijacker)
	if !ok {
		http.Error(w, "could not hijack connection", http.StatusInternalServerError)
		return
	}
	netConn, bufRW, err := hijacker.Hijack()
	if err != nil {
		http.Error(w, "could not hijack connection", http.StatusInternalServerError)
		return
	}
	defer netConn.Close()

	// permessage-deflate (RFC 7692). One answer with every field zero accepts
	// any offer this package can serve, so the cases 12.x and 13.x run. An offer
	// of server_max_window_bits under 15 (RFC 7692 7.1.2.1) is skipped (library
	// choice), so cases 13.3.x and 13.5.x run without compression.
	handShakeConfig := wlgows.ServerHandShakeConfig{
		PermessageDeflates: []wlgows.PermessageDeflateConfig{{}},
	}
	conn, _, err := wlgows.ServerHandShake(netConn, bufRW.Reader, bufRW.Writer, r, handShakeConfig)
	if err != nil {
		fmt.Println("error: handshake:", err)
		return
	}

	// Ping, pong, close and reserved opcodes are answered for you.
	listener := conn.NewStandardListener()

	// SetConfig replaces all of it, so start from the standard config.
	config := listener.GetConfig()
	config.MaxDataFramesSize = maxDataFramesSize
	config.MaxFrameSize = maxDataFramesSize // one frame may carry the whole message
	config.MaxDataFrameCount = maxDataFrameCount
	config.FrameReadTimeout = frameReadTimeout

	config.Text = func(frames wlgows.DataFrames) {
		message, err := readMessage(conn, frames)
		if err != nil {
			listener.PauseListen(err) // Listen returns err
			return
		}
		if err := conn.SendText(message, sendDataConfig); err != nil {
			listener.PauseListen(err)
		}
	}
	config.Binary = func(frames wlgows.DataFrames) {
		message, err := readMessage(conn, frames)
		if err != nil {
			listener.PauseListen(err) // Listen returns err
			return
		}
		if err := conn.SendBinary(message, sendDataConfig); err != nil {
			listener.PauseListen(err)
		}
	}

	listener.SetConfig(config)

	// No ping loop: Autobahn does not expect pings it did not ask for.

	// nil means the peer closed. Otherwise answer with the close code for err,
	// when there is one (RFC 6455 7.4.1).
	if err := listener.Listen(); err != nil {
		if payload := wlgows.StandardClosePayloadFor(err); payload != nil {
			conn.SendClose(payload)
		}
		fmt.Println("error: listen:", err)
	}
}

// readMessage returns the bytes of one message, up to maxDataFramesSize. It
// decompresses a compressed message and checks text as UTF-8. See READING.md.
func readMessage(conn *wlgows.Conn, frames wlgows.DataFrames) ([]byte, error) {
	reader, err := conn.DataFramesReader(frames)
	if err != nil {
		return nil, err
	}
	defer reader.Close() // always close it
	return io.ReadAll(wlgows.NewSizeLimitReader(reader, maxDataFramesSize))
}
