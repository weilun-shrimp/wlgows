// An echo client for the Autobahn|Testsuite (wstest -m fuzzingserver). It runs
// every case, and every text or binary message goes back unchanged. See
// README.md.
//
//	cd example/autobahn_client && go run .
//
// It asks nothing on stdin.
package main

import (
	"bufio"
	"compress/flate"
	"context"
	"errors"
	"fmt"
	"io"
	"net/url"
	"strconv"
	"time"

	"github.com/weilun-shrimp/wlgows/v7"
)

const (
	serverUrl = "ws://127.0.0.1:9001"

	// The name of the tested side in the report.
	agent = "wlgows"

	// One message's byte budget, headers included. Autobahn's largest message
	// is 16 MB (cases 9.x), so twice that leaves room for the headers.
	maxDataFramesSize = 32 * 1024 * 1024 // 32 MB

	// The large fragmented messages (cases 9.3.x, 9.4.x) use fragments of 64
	// bytes or more. The 1-byte fragments (cases 6.2.x, 6.4.x) come in short
	// messages.
	maxDataFrameCount = maxDataFramesSize / 64

	// The time limit for reading one whole frame, the wait for it included.
	frameReadTimeout = 60 * time.Second

	// Bounds the TCP connect.
	dialTimeout = 15 * time.Second

	// ClientHandShake sets no timeout, so a server that never sends the
	// response would hold the client forever.
	responseReadTimeout = 10 * time.Second
)

// permessage-deflate (RFC 7692). The offer has client_max_window_bits with no
// value (RFC 7692 7.1.2.2), so the cases 13.4.x and 13.6.x run.
var handShakeConfig = wlgows.ClientHandShakeConfig{
	PermessageDeflates: []wlgows.PermessageDeflateConfig{
		{ClientMaxWindowBitsEnabled: true},
	},
}

// A response of client_max_window_bits under 15 fails the handshake with
// ErrHandshakeResponseDeflateUnsupported (library choice). Then runCase
// dials again with this offer, which has no client_max_window_bits, so the
// server cannot send it (RFC 7692 7.1.2.2).
var retryHandShakeConfig = wlgows.ClientHandShakeConfig{
	PermessageDeflates: []wlgows.PermessageDeflateConfig{{}},
}

// Echoes are deflated when the handshake agreed permessage-deflate (RFC 7692).
// DeflateLevel must be set: its zero is flate.NoCompression.
var sendDataConfig = wlgows.SendDataConfig{
	DeflateIfAgreed: true,
	DeflateLevel:    flate.DefaultCompression,
}

func main() {
	caseCount, err := readCaseCount()
	if err != nil {
		fmt.Println("error: case count:", err)
		return
	}
	fmt.Println("running", caseCount, "cases on", serverUrl)

	for caseIndex := 1; caseIndex <= caseCount; caseIndex++ {
		runCase(caseIndex)
	}

	if err := updateReports(); err != nil {
		fmt.Println("error: update reports:", err)
		return
	}
	fmt.Println("done. The report is in reports/index.html.")
}

// readCaseCount asks Autobahn how many cases it has. It sends the number as
// one text message, then closes.
func readCaseCount() (int, error) {
	conn, err := connect("/getCaseCount", nil, handShakeConfig)
	if err != nil {
		return 0, err
	}
	defer conn.Close()

	var text []byte
	listener := newListener(conn)
	config := listener.GetConfig()
	config.Text = func(frames wlgows.DataFrames) {
		message, err := readMessage(conn, frames)
		if err != nil {
			listener.PauseListen(err) // Listen returns err
			return
		}
		text = message
	}
	listener.SetConfig(config)
	if err := listen(conn, listener); err != nil {
		return 0, err
	}
	return strconv.Atoi(string(text))
}

// runCase runs the case at caseIndex, from 1 to the case count. It is not the
// case number in the report, such as 13.3.1. Every message goes back until
// Autobahn closes. A failure is printed, and the next case runs: the report
// shows the result.
//
// On ErrHandshakeResponseDeflateUnsupported, the case runs again with
// retryHandShakeConfig. The first run is logged as Fail. Autobahn keeps the
// last result of a case, so the report shows the second run.
func runCase(caseIndex int) {
	query := url.Values{"case": {strconv.Itoa(caseIndex)}, "agent": {agent}}
	conn, err := connect("/runCase", query, handShakeConfig)
	if errors.Is(err, wlgows.ErrHandshakeResponseDeflateUnsupported) {
		conn, err = connect("/runCase", query, retryHandShakeConfig)
	}
	if err != nil {
		fmt.Printf("error: case index %d: %v\n", caseIndex, err)
		return
	}
	defer conn.Close()

	listener := newListener(conn)
	config := listener.GetConfig()
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
	if err := listen(conn, listener); err != nil {
		fmt.Printf("error: case index %d: %v\n", caseIndex, err)
	}
}

// updateReports tells Autobahn to write the report. Autobahn writes it when
// the connection opens, then closes.
func updateReports() error {
	conn, err := connect("/updateReports", url.Values{"agent": {agent}}, handShakeConfig)
	if err != nil {
		return err
	}
	defer conn.Close()

	return listen(conn, newListener(conn))
}

// connect dials serverUrl + path and does the handshake with config.
func connect(path string, query url.Values, config wlgows.ClientHandShakeConfig) (*wlgows.Conn, error) {
	rawUrl := serverUrl + path
	if query != nil {
		rawUrl += "?" + query.Encode()
	}

	ctx, cancel := context.WithTimeout(context.Background(), dialTimeout)
	defer cancel()
	netConn, req, err := wlgows.DialContext(ctx, rawUrl, nil, nil)
	if err != nil {
		return nil, fmt.Errorf("dial: %w", err)
	}

	reader := bufio.NewReader(netConn)
	writer := bufio.NewWriter(netConn)
	if err := netConn.SetReadDeadline(time.Now().Add(responseReadTimeout)); err != nil {
		netConn.Close()
		return nil, fmt.Errorf("setting response deadline: %w", err)
	}
	conn, _, err := wlgows.ClientHandShake(netConn, reader, writer, req, config)
	if err != nil {
		netConn.Close()
		return nil, fmt.Errorf("handshake: %w", err)
	}
	// Cleared, or it would cut off the frame reads too. The Listener sets its
	// own deadline before each frame.
	if err := netConn.SetReadDeadline(time.Time{}); err != nil {
		netConn.Close()
		return nil, fmt.Errorf("clearing response deadline: %w", err)
	}
	return conn, nil
}

// newListener returns a standard Listener with this client's limits. Ping,
// pong, close and reserved opcodes are answered for you.
func newListener(conn *wlgows.Conn) *wlgows.Listener {
	listener := conn.NewStandardListener()

	// SetConfig replaces all of it, so start from the standard config.
	config := listener.GetConfig()
	config.MaxDataFramesSize = maxDataFramesSize
	config.MaxFrameSize = maxDataFramesSize // one frame may carry the whole message
	config.MaxDataFrameCount = maxDataFrameCount
	config.FrameReadTimeout = frameReadTimeout
	listener.SetConfig(config)
	return listener
}

// listen reads until the connection ends. nil means the peer closed.
// Otherwise it sends the close code for err, when there is one
// (RFC 6455 7.4.1), and returns err.
func listen(conn *wlgows.Conn, listener *wlgows.Listener) error {
	err := listener.Listen()
	if err != nil {
		if payload := wlgows.StandardClosePayloadFor(err); payload != nil {
			conn.SendClose(payload)
		}
	}
	return err
}

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
