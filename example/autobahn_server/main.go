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

	// permessage-deflate (RFC 7692). One response config with every field zero
	// accepts any valid offer, so the cases 12.x and 13.x run. An offered
	// server_max_window_bits is kept as it is (RFC 7692 7.1.2.1).
	handShakeConfig := wlgows.ServerHandShakeConfig{
		PermessageDeflates: []wlgows.PermessageDeflateConfig{{}},
	}
	conn, _, err := wlgows.ServerHandShake(netConn, bufRW.Reader, bufRW.Writer, r, handShakeConfig)
	if err != nil {
		fmt.Println("error: handshake:", err)
		return
	}

	// Echoes are deflated at flate.BestSpeed when the handshake agreed
	// permessage-deflate (RFC 7692). Under a server_max_window_bits of 8 to 14,
	// as in the cases 13.3.x, 13.5.x and 13.7.x, wlgows allows only
	// flate.NoCompression and flate.HuffmanOnly (library choice), so the
	// echoes use flate.HuffmanOnly. ServerMaxWindowBits 0 means 15 bits
	// (RFC 7692 7.1.2.1).
	sendDataConfig := wlgows.SendDataConfig{
		DeflateIfAgreed: true,
		DeflateLevel:    flate.BestSpeed,
	}
	if agreed := conn.GetPermessageDeflateAgreedConfig(); agreed != nil &&
		agreed.ServerMaxWindowBits != 0 && agreed.ServerMaxWindowBits < 15 {
		sendDataConfig.DeflateLevel = flate.HuffmanOnly
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
