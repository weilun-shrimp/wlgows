package benchmarks

import (
	"bufio"
	"bytes"
	"crypto/sha1"
	"encoding/base64"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync"
	"testing"

	"github.com/gorilla/websocket"
)

// The calls a gorilla case can measure. gorilla allows one writer and one
// reader at a time, so each call takes a sync.Mutex, as a caller sharing the
// Conn between goroutines must.
const (
	gorillaWriteMessageCall     = "sync.Mutex + WriteMessage"
	gorillaWriteMessageLoopCall = "sync.Mutex + WriteMessage loop"
	gorillaReadMessageCall      = "sync.Mutex + ReadMessage"
)

// gorillaSplitFramesNote is added to the call of a client Send case whose
// payload is larger than gorilla's write buffer. gorilla's client then sends
// the message as 4 KB frames, not one frame of the case size (RFC 6455 5.4).
const gorillaSplitFramesNote = ", split into 4 KB frames"

/*
gorillaNetConn is the net.Conn under a gorilla Conn. It drops and counts Writes
like countingConn. Read reads from source, and returns io.EOF with no source.

A client Conn needs the handshake answered. While request is not nil, Writes
are also kept in request, and the first Read answers the handshake found there.
*/
type gorillaNetConn struct {
	countingConn
	request *bytes.Buffer
	source  io.Reader
}

func (conn *gorillaNetConn) Write(data []byte) (int, error) {
	if conn.request != nil {
		conn.request.Write(data)
	}
	return conn.countingConn.Write(data)
}

func (conn *gorillaNetConn) Read(data []byte) (int, error) {
	if conn.source == nil && conn.request != nil {
		response, err := gorillaHandshakeResponse(conn.request)
		if err != nil {
			return 0, err
		}
		conn.source = bytes.NewReader(response)
	}
	if conn.source == nil {
		return 0, io.EOF
	}
	return conn.source.Read(data)
}

// gorillaHandshakeResponse is a server's 101 response to the client handshake
// request. RFC 6455 4.2.2: Sec-WebSocket-Accept is the base64 SHA-1 of the
// key and the GUID 258EAFA5-E914-47DA-95CA-C5AB0DC85B11.
func gorillaHandshakeResponse(request *bytes.Buffer) ([]byte, error) {
	handshakeRequest, err := http.ReadRequest(bufio.NewReader(request))
	if err != nil {
		return nil, err
	}
	hash := sha1.New()
	hash.Write([]byte(handshakeRequest.Header.Get("Sec-WebSocket-Key")))
	hash.Write([]byte("258EAFA5-E914-47DA-95CA-C5AB0DC85B11"))
	accept := base64.StdEncoding.EncodeToString(hash.Sum(nil))
	return []byte("HTTP/1.1 101 Switching Protocols\r\n" +
		"Upgrade: websocket\r\n" +
		"Connection: Upgrade\r\n" +
		"Sec-WebSocket-Accept: " + accept + "\r\n" +
		"\r\n"), nil
}

// gorillaHijackResponseWriter hands netConn to Upgrader.Upgrade.
type gorillaHijackResponseWriter struct {
	netConn net.Conn
	header  http.Header
}

func (writer *gorillaHijackResponseWriter) Header() http.Header            { return writer.header }
func (writer *gorillaHijackResponseWriter) Write(data []byte) (int, error) { return len(data), nil }
func (writer *gorillaHijackResponseWriter) WriteHeader(statusCode int)     {}

func (writer *gorillaHijackResponseWriter) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	readWriter := bufio.NewReadWriter(
		bufio.NewReaderSize(writer.netConn, bufferSize),
		bufio.NewWriterSize(writer.netConn, bufferSize),
	)
	return writer.netConn, readWriter, nil
}

/*
newGorillaConn is a gorilla Conn on caseSide over a gorillaNetConn, with
bufferSize buffers. A server Conn comes from Upgrader.Upgrade, a client Conn
from NewClient. The handshake's Writes are not counted.
*/
func newGorillaConn(b *testing.B, caseSide side) (*websocket.Conn, *gorillaNetConn) {
	netConn := &gorillaNetConn{}
	var conn *websocket.Conn
	var err error
	if caseSide == serverSide {
		upgrader := websocket.Upgrader{
			ReadBufferSize:  bufferSize,
			WriteBufferSize: bufferSize,
		}
		request := httptest.NewRequest(http.MethodGet, "/", nil)
		request.Header.Set("Connection", "Upgrade")
		request.Header.Set("Upgrade", "websocket")
		request.Header.Set("Sec-WebSocket-Version", "13")
		// The sample key from RFC 6455 1.3.
		request.Header.Set("Sec-WebSocket-Key", "dGhlIHNhbXBsZSBub25jZQ==")
		writer := &gorillaHijackResponseWriter{
			netConn: netConn,
			header:  http.Header{},
		}
		conn, err = upgrader.Upgrade(writer, request, nil)
	} else {
		netConn.request = &bytes.Buffer{}
		serverURL := &url.URL{
			Scheme: "ws",
			Host:   "localhost",
			Path:   "/",
		}
		conn, _, err = websocket.NewClient(netConn, serverURL, nil, bufferSize, bufferSize)
		netConn.request = nil
		netConn.source = nil
	}
	if err != nil {
		b.Fatal(err)
	}
	netConn.writes = 0
	return conn, netConn
}

// TestGorilla runs every case on gorilla/websocket and writes
// results/gorilla.json.
func TestGorilla(t *testing.T) {
	cases := []libraryCase{}
	for _, benchmarkCase := range benchmarkCases {
		if benchmarkCase.operation == readOperation {
			cases = append(cases, libraryCase{
				id:        benchmarkCase.id,
				call:      gorillaReadMessageCall,
				benchmark: gorillaReadMessage(benchmarkCase),
			})
			continue
		}
		call := gorillaWriteMessageCall
		if benchmarkCase.frameCount > 1 {
			call = gorillaWriteMessageLoopCall
		}
		payloadLength := len(casePayload(benchmarkCase.frameSize, benchmarkCase.side))
		if benchmarkCase.side == clientSide && payloadLength > bufferSize {
			call += gorillaSplitFramesNote
		}
		cases = append(cases, libraryCase{
			id:        benchmarkCase.id,
			call:      call,
			benchmark: gorillaWriteMessage(benchmarkCase),
		})
	}

	runLibrary(t, "gorilla", "github.com/gorilla/websocket", cases)
}

// gorillaWriteMessage calls WriteMessage under a sync.Mutex once for each frame
// of the case. gorilla flushes every message, so each one is at least one
// Write. As a client, a payload larger than the write buffer goes out as 4 KB
// frames.
func gorillaWriteMessage(benchmarkCase benchmarkCase) func(b *testing.B) {
	return func(b *testing.B) {
		conn, netConn := newGorillaConn(b, benchmarkCase.side)
		payload := casePayload(benchmarkCase.frameSize, benchmarkCase.side)
		writeLocker := &sync.Mutex{}
		b.ResetTimer()

		for i := 0; i < b.N; i++ {
			for j := 0; j < benchmarkCase.frameCount; j++ {
				writeLocker.Lock()
				err := conn.WriteMessage(websocket.BinaryMessage, payload)
				writeLocker.Unlock()
				if err != nil {
					b.Fatal(err)
				}
			}
		}

		b.ReportMetric(float64(netConn.writes)/float64(b.N), "writes/op")
	}
}

// gorillaReadMessage reads the case's frames from the recorded wire with
// ReadMessage under a sync.Mutex, after checking them once. Its read limit
// stays at 0, no limit.
func gorillaReadMessage(benchmarkCase benchmarkCase) func(b *testing.B) {
	return func(b *testing.B) {
		conn, netConn := newGorillaConn(b, benchmarkCase.side)
		sender := benchmarkCase.side.peer()
		wire, err := recordedWire(sender, benchmarkCase.frameSize, benchmarkCase.frameCount)
		if err != nil {
			b.Fatal(err)
		}
		payload := casePayload(benchmarkCase.frameSize, sender)
		source := bytes.NewReader(wire)
		netConn.source = source

		for j := 0; j < benchmarkCase.frameCount; j++ {
			messageType, message, err := conn.ReadMessage()
			if err != nil {
				b.Fatal(err)
			}
			if messageType != websocket.BinaryMessage || !bytes.Equal(message, payload) {
				b.Fatal("the message read differs from the one sent")
			}
		}
		readLocker := &sync.Mutex{}
		b.ResetTimer()

		// Every frame is read whole, so gorilla's reader is empty before
		// source starts over.
		for i := 0; i < b.N; i++ {
			source.Reset(wire)
			for j := 0; j < benchmarkCase.frameCount; j++ {
				readLocker.Lock()
				_, _, err := conn.ReadMessage()
				readLocker.Unlock()
				if err != nil {
					b.Fatal(err)
				}
			}
		}
	}
}
