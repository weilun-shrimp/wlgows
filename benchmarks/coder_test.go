package benchmarks

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha1"
	"encoding/base64"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/coder/websocket"
)

// The calls a coder case can measure. coder allows Write from several
// goroutines at once, but Read from one at a time, so each Read takes a
// sync.Mutex, as a caller sharing the Conn between goroutines must.
const (
	coderWriteCall     = "Write"
	coderWriteLoopCall = "Write loop"
	coderReadCall      = "sync.Mutex + Read"
)

/*
coderNetConn is the net.Conn under a coder Conn. It drops and counts Writes
like countingConn. Read reads from source, and returns io.EOF with no source.
*/
type coderNetConn struct {
	countingConn
	source io.Reader
}

func (conn *coderNetConn) Read(data []byte) (int, error) {
	if conn.source == nil {
		return 0, io.EOF
	}
	return conn.source.Read(data)
}

// coderHijackResponseWriter hands netConn to Accept, with bufferSize buffers.
type coderHijackResponseWriter struct {
	netConn net.Conn
	header  http.Header
}

func (writer *coderHijackResponseWriter) Header() http.Header            { return writer.header }
func (writer *coderHijackResponseWriter) Write(data []byte) (int, error) { return len(data), nil }
func (writer *coderHijackResponseWriter) WriteHeader(statusCode int)     {}

func (writer *coderHijackResponseWriter) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	readWriter := bufio.NewReadWriter(
		bufio.NewReaderSize(writer.netConn, bufferSize),
		bufio.NewWriterSize(writer.netConn, bufferSize),
	)
	return writer.netConn, readWriter, nil
}

// coderHandshakeTransport answers the client handshake with a 101 response
// whose Body is netConn, which Dial then uses as the connection.
type coderHandshakeTransport struct {
	netConn *coderNetConn
}

// RoundTrip answers request. RFC 6455 4.2.2: Sec-WebSocket-Accept is the
// base64 SHA-1 of the key and the GUID 258EAFA5-E914-47DA-95CA-C5AB0DC85B11.
func (transport *coderHandshakeTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	hash := sha1.New()
	hash.Write([]byte(request.Header.Get("Sec-WebSocket-Key")))
	hash.Write([]byte("258EAFA5-E914-47DA-95CA-C5AB0DC85B11"))
	header := http.Header{}
	header.Set("Upgrade", "websocket")
	header.Set("Connection", "Upgrade")
	header.Set("Sec-WebSocket-Accept", base64.StdEncoding.EncodeToString(hash.Sum(nil)))
	response := &http.Response{
		Status:     "101 Switching Protocols",
		StatusCode: http.StatusSwitchingProtocols,
		Proto:      "HTTP/1.1",
		ProtoMajor: 1,
		ProtoMinor: 1,
		Header:     header,
		Body:       transport.netConn,
		Request:    request,
	}
	return response, nil
}

/*
newCoderConn is a coder Conn on caseSide over a coderNetConn, with no read
limit. A server Conn comes from Accept with bufferSize buffers. A client Conn
comes from Dial, whose buffers are bufio's default 4 KB, the same as
bufferSize. Compression stays off, coder's default. The handshake's Writes are
not counted.
*/
func newCoderConn(b *testing.B, caseSide side) (*websocket.Conn, *coderNetConn) {
	netConn := &coderNetConn{}
	var conn *websocket.Conn
	var err error
	if caseSide == serverSide {
		request := httptest.NewRequest(http.MethodGet, "/", nil)
		request.Header.Set("Connection", "Upgrade")
		request.Header.Set("Upgrade", "websocket")
		request.Header.Set("Sec-WebSocket-Version", "13")
		// The sample key from RFC 6455 1.3.
		request.Header.Set("Sec-WebSocket-Key", "dGhlIHNhbXBsZSBub25jZQ==")
		writer := &coderHijackResponseWriter{
			netConn: netConn,
			header:  http.Header{},
		}
		conn, err = websocket.Accept(writer, request, nil)
	} else {
		client := &http.Client{
			Transport: &coderHandshakeTransport{
				netConn: netConn,
			},
		}
		conn, _, err = websocket.Dial(context.Background(), "ws://localhost/", &websocket.DialOptions{
			HTTPClient: client,
		})
	}
	if err != nil {
		b.Fatal(err)
	}
	conn.SetReadLimit(-1)
	netConn.writes = 0
	return conn, netConn
}

// TestCoder runs every case on coder/websocket and writes results/coder.json.
func TestCoder(t *testing.T) {
	cases := []libraryCase{}
	for _, benchmarkCase := range benchmarkCases {
		if benchmarkCase.operation == readOperation {
			cases = append(cases, libraryCase{
				id:        benchmarkCase.id,
				call:      coderReadCall,
				benchmark: coderRead(benchmarkCase),
			})
			continue
		}
		call := coderWriteCall
		if benchmarkCase.frameCount > 1 {
			call = coderWriteLoopCall
		}
		cases = append(cases, libraryCase{
			id:        benchmarkCase.id,
			call:      call,
			benchmark: coderWrite(benchmarkCase),
		})
	}

	runLibrary(t, "coder", "github.com/coder/websocket", cases)
}

// coderWrite calls Write once for each frame of the case. With compression
// off, Write sends a message as one frame and flushes it.
func coderWrite(benchmarkCase benchmarkCase) func(b *testing.B) {
	return func(b *testing.B) {
		conn, netConn := newCoderConn(b, benchmarkCase.side)
		payload := casePayload(benchmarkCase.frameSize, benchmarkCase.side)
		ctx := context.Background()
		b.ResetTimer()

		for i := 0; i < b.N; i++ {
			for j := 0; j < benchmarkCase.frameCount; j++ {
				if err := conn.Write(ctx, websocket.MessageBinary, payload); err != nil {
					b.Fatal(err)
				}
			}
		}

		b.ReportMetric(float64(netConn.writes)/float64(b.N), "writes/op")
	}
}

// coderRead reads the case's frames from the recorded wire with Read under a
// sync.Mutex, after checking them once.
func coderRead(benchmarkCase benchmarkCase) func(b *testing.B) {
	return func(b *testing.B) {
		conn, netConn := newCoderConn(b, benchmarkCase.side)
		sender := benchmarkCase.side.peer()
		wire, err := recordedWire(sender, benchmarkCase.frameSize, benchmarkCase.frameCount)
		if err != nil {
			b.Fatal(err)
		}
		payload := casePayload(benchmarkCase.frameSize, sender)
		source := bytes.NewReader(wire)
		netConn.source = source
		ctx := context.Background()

		for j := 0; j < benchmarkCase.frameCount; j++ {
			messageType, message, err := conn.Read(ctx)
			if err != nil {
				b.Fatal(err)
			}
			if messageType != websocket.MessageBinary || !bytes.Equal(message, payload) {
				b.Fatal("the message read differs from the one sent")
			}
		}
		readLocker := &sync.Mutex{}
		b.ResetTimer()

		// Every frame is read whole, so coder's reader is empty before
		// source starts over.
		for i := 0; i < b.N; i++ {
			source.Reset(wire)
			for j := 0; j < benchmarkCase.frameCount; j++ {
				readLocker.Lock()
				_, _, err := conn.Read(ctx)
				readLocker.Unlock()
				if err != nil {
					b.Fatal(err)
				}
			}
		}
	}
}
