// Receives a streamed message and writes it to a file as each frame arrives,
// so the message is never held in memory whole. Use it with stream_client.
//
//	cd example/stream_server && go run .   # in one terminal
//	cd example/stream_client && go run .   # in another
//
// Answer y to the TLS prompt to serve wss:// instead. Empty paths use the
// bundled ../server_fullchain.crt and ../server.key.
//
// How it works:
//  1. Accepts a connection and does the handshake.
//  2. Sets the Listener's Data hook, so data frames come one at a time
//     instead of as a whole message.
//  3. Writes each frame to received.bin, inflating it first when
//     permessage-deflate is agreed (RFC 7692). When the disk falls behind
//     the sender, it sleeps to slow the sender down, so memory stays near
//     1 MB. A text message is refused with close 1003 (RFC 6455 7.4.1).
//  4. At the FIN frame, sends the file's sha256 back as a text message.
//  5. Answers the client's close. If the close comes before FIN, the file
//     has only part of the message.
package main

import (
	"compress/flate"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"time"

	"github.com/weilun-shrimp/wlgows/v7"
	"github.com/weilun-shrimp/wlgows/v7/example/example_helpers"
)

const (
	service = ":8001"
	outPath = "./received.bin"

	// net/http sets no timeout by default, so a client that never finishes its
	// request would hold a goroutine forever.
	readHeaderTimeout = 10 * time.Second

	// readHeaderTimeout only starts once a request's first bytes arrive. This
	// bounds the wait for the next request on a kept-alive connection. It
	// does not touch a hijacked connection.
	idleTimeout = 60 * time.Second

	// The request line and headers. net/http allows 1 MB by default, and a
	// handshake is a few hundred bytes.
	maxHeaderBytes = 16 * 1024 // 16 KB

	// The budget for one message, spent frame by frame, so it has to cover the
	// whole stream rather than one fragment of it. Size it for the largest
	// transmission you are willing to receive.
	//
	// It bounds a single frame only by what the message has left, so early on
	// one frame may claim most of it. maxFrameSize bounds each frame.
	maxDataFramesSize = 2 * 1024 * 1024 * 1024 // 2 GB

	// One frame, header included, checked at the header before anything is
	// allocated, so one read never holds more than this.
	maxFrameSize = 64 * 1024 // 64 KB

	// What one deflated message may inflate to. A few compressed bytes can
	// inflate to far more, and the frame limits above never see that.
	maxInflatedSize = 2 * 1024 * 1024 * 1024 // 2 GB

	// One pull of inflated bytes.
	pullBufferSize = 32 * 1024 // 32 KB

	// Deflated frames wait in memory until they are inflated. When more than
	// this waits, the Data hook sleeps until none waits, so the Listener stops
	// reading and TCP slows the peer. Without it, a slow disk lets one
	// connection hold up to maxDataFramesSize.
	maxDeflatedBytesWaiting = 1 * 1024 * 1024 // 1 MB

	// How long the Data hook sleeps before it checks again.
	drainSleepInterval = 10 * time.Millisecond

	frameReadTimeout = 60 * time.Second

	// A ping every interval, answered by the peer's pong (5.5.2). It only asks;
	// deciding a silent peer is gone would be a deadline of our own.
	pingInterval = 30 * time.Second
)

// pullResult is how pulling one deflated message ended: the inflated bytes
// written to the file, and nil or why it stopped.
type pullResult struct {
	writtenByteCount int64
	err              error
}

// writeInflated pulls token's inflated bytes into out until io.EOF, up to
// maxInflatedSize. On an error out has only part of the message.
func writeInflated(conn *wlgows.Conn, token int64, out io.Writer) pullResult {
	buffer := make([]byte, pullBufferSize)
	var writtenByteCount int64
	for {
		n, err := conn.PullInflatedBytes(token, buffer)
		writtenByteCount += int64(n)
		if writtenByteCount > maxInflatedSize {
			return pullResult{writtenByteCount, wlgows.ErrSizeLimitExceeded}
		}
		if _, writeErr := out.Write(buffer[:n]); writeErr != nil {
			return pullResult{writtenByteCount, fmt.Errorf("writing %s: %w", outPath, writeErr)}
		}
		if err == io.EOF {
			return pullResult{writtenByteCount, nil}
		}
		if err != nil {
			// ErrInflateDataInvalid: StandardClosePayloadFor answers 1007.
			return pullResult{writtenByteCount, err}
		}
	}
}

func main() {
	serverConfig, err := example_helpers.ReadUserInputServerConfig()
	if err != nil {
		fmt.Println("error: server config:", err)
		return
	}

	server := &http.Server{
		Addr: service,
		Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			handler(w, r, serverConfig.SupportedSubprotocols)
		}),
		ReadHeaderTimeout: readHeaderTimeout,
		IdleTimeout:       idleTimeout,
		MaxHeaderBytes:    maxHeaderBytes,
	}

	fmt.Println("stream server listening on " + service)
	if serverConfig.ServerCertPath != "" && serverConfig.ServerKeyPath != "" {
		fmt.Println("tls mode — connect with wss://")
		err = server.ListenAndServeTLS(serverConfig.ServerCertPath, serverConfig.ServerKeyPath)
	} else {
		fmt.Println("plain mode — connect with ws://")
		err = server.ListenAndServe()
	}
	if err != nil {
		fmt.Println("error: starting server:", err)
	}
}

func handler(w http.ResponseWriter, r *http.Request, supportedSubprotocols []string) {
	handShakeConfig := wlgows.ServerHandShakeConfig{
		// permessage-deflate (RFC 7692). One answer with every field zero
		// accepts any offer this package can serve. Matching adds the offer's
		// server_no_context_takeover (RFC 7692 7.1.1.1) and
		// client_no_context_takeover (7.1.1.2), so the client picks the mode.
		// An offer of server_max_window_bits under 15 is skipped (library
		// choice). A client that offers nothing gets no compression.
		PermessageDeflates: []wlgows.PermessageDeflateConfig{{}},

		// Subprotocols (RFC 6455 4.2.2). When no offer is one the server
		// speaks, this is "" and the connection opens without one.
		Subprotocol: example_helpers.PickSubprotocol(wlgows.RequestSubprotocols(r), supportedSubprotocols),
	}

	hijacker, ok := w.(http.Hijacker)
	if !ok {
		http.Error(w, "could not hijack connection", http.StatusInternalServerError)
		fmt.Println("error: hijack: the ResponseWriter is not an http.Hijacker")
		return
	}
	netConn, bufRW, err := hijacker.Hijack()
	if err != nil {
		// Still an ordinary response: the hijack failed, so nothing was taken.
		http.Error(w, "could not hijack connection", http.StatusInternalServerError)
		fmt.Println("error: hijack:", err)
		return
	}
	defer netConn.Close()

	// r is already parsed by net/http, so no read step is needed here. Pass
	// bufRW.Reader, not a new reader: net/http may have buffered the start of
	// the first frame in it.
	conn, resp, err := wlgows.ServerHandShake(netConn, bufRW.Reader, bufRW.Writer, r, handShakeConfig)
	if err != nil {
		fmt.Println("error: handshake:", err)
		return
	}

	fmt.Println("connected:", conn.RemoteAddr())
	example_helpers.PrintAgreedDeflate(conn)
	example_helpers.PrintSubprotocol(resp.Header.Get("Sec-WebSocket-Protocol"))

	out, err := os.Create(outPath)
	if err != nil {
		fmt.Println("error: create:", err)
		return
	}
	defer out.Close()

	// Hash as it streams past, so nothing has to be re-read to verify it.
	hasher := sha256.New()
	fileAndHasherWriter := io.MultiWriter(out, hasher)

	var frameCount int
	var byteCount int64 // bytes written to the file, inflated when deflated

	// The deflated message open now. inflatePullResultChan is nil when none
	// is open. A value on it also means the pull returned, so
	// EndInflateDataFrames is safe to call.
	var inflateToken int64
	var inflatePullResultChan chan pullResult

	// Ping, pong, close and reserved opcodes are answered for you, and masking
	// (5.1) is settled from which side this connection is.
	listener := conn.NewStandardListener()

	config := listener.GetConfig()
	config.MaxDataFramesSize = maxDataFramesSize
	config.MaxFrameSize = maxFrameSize
	config.MaxDataFrameCount = 0 // Data keeps no frame, so the count bounds nothing
	config.FrameReadTimeout = frameReadTimeout

	// writePlainFrame writes f's payload to the file and the hasher.
	writePlainFrame := func(f *wlgows.Frame) error {
		if _, err := fileAndHasherWriter.Write(f.PayloadData); err != nil {
			// Nothing the peer did wrong — our disk. Ending the run with it
			// beats printing it here and returning nil from Listen.
			return fmt.Errorf("writing %s: %w", outPath, err)
		}
		byteCount += int64(len(f.PayloadData))
		fmt.Printf("frame %d: %d bytes, FIN=%v\n", frameCount, len(f.PayloadData), f.FIN)
		return nil
	}

	// writeDeflatedFrame puts f into the inflater. On the first frame it starts
	// the pull; when too much waits it sleeps until none waits; at the FIN
	// frame it waits for the pull to finish.
	writeDeflatedFrame := func(f *wlgows.Frame) error {
		if f.Opcode != wlgows.OpcodeContinuation { // the first frame
			inflateToken = conn.StartInflateDataFrames()
			inflatePullResultChan = make(chan pullResult, 1) // room for 1: the send never blocks
			go func(inflateToken int64, results chan<- pullResult) {
				results <- writeInflated(conn, inflateToken, fileAndHasherWriter)
			}(inflateToken, inflatePullResultChan)
		}
		if err := conn.PutDeflatedDataFrame(inflateToken, f); err != nil {
			return err
		}
		fmt.Printf("frame %d: %d deflated bytes, FIN=%v\n", frameCount, len(f.PayloadData), f.FIN)

		if !f.FIN {
			// Without this, a sender faster than our disk fills our memory:
			// PutDeflatedDataFrame never waits, so its frames pile up, up to
			// maxDataFramesSize. With it, memory stays under
			// maxDeflatedBytesWaiting plus two frames: the one just put, and
			// the one flate is reading.
			if conn.DeflatedBytesWaiting(inflateToken) > maxDeflatedBytesWaiting {
				// You are too fast. Sleep to slow it down: until none waits,
				// not just until it is under again. Stop early once the pull
				// has a result: when the peer sends deflate's last block
				// (BFINAL) before FIN, the bytes after it are never read, so
				// the count never drops to 0.
				for conn.DeflatedBytesWaiting(inflateToken) > 0 && len(inflatePullResultChan) == 0 {
					time.Sleep(drainSleepInterval)
				}
			}
			select {
			case result := <-inflatePullResultChan: // the pull stopped early: too big, not valid, or the file write failed
				conn.EndInflateDataFrames(inflateToken)
				inflatePullResultChan = nil
				if result.err == nil {
					// The deflate stream ended in a block marked final, before
					// the FIN frame: the frames after it cannot be inflated.
					result.err = fmt.Errorf("deflate stream ended before the FIN frame: %w", wlgows.ErrInflateDataInvalid)
				}
				return result.err
			default:
			}
			return nil
		}

		// The whole message is put: wait for the pull to finish.
		result := <-inflatePullResultChan
		conn.EndInflateDataFrames(inflateToken)
		inflatePullResultChan = nil
		if result.err != nil {
			return result.err
		}
		byteCount = result.writtenByteCount
		return nil
	}

	// Care: setting this replaces assembly outright. Text and Binary never run,
	// and no UTF-8 check runs for you — 5.6 is on the joined bytes and nothing
	// here joins them, so checking a text message is yours, and so is answering
	// 1007. A frame this hook drops is gone too. Only a message too large to
	// hold belongs here.
	config.Data = func(f *wlgows.Frame) {
		// A continuation frame does not carry the message's type, so this is the
		// only way to know what is arriving. Text would owe a 5.6 check on the
		// joined bytes, which is exactly what streaming refuses to hold.
		if listener.GetCurrentMsgOpcode() != wlgows.OpcodeBinary {
			// RFC 6455 7.4.1: 1003, a type this endpoint cannot accept.
			conn.SendClose(&wlgows.ClosePayload{StatusCode: wlgows.CloseUnsupportedData})
			// The reason travels out through Listen, so the one place that
			// reports a stopped connection reports this too.
			listener.PauseListen(errors.New("peer streamed text: 5.6 cannot be checked frame by frame"))
			return
		}

		frameCount++

		var err error
		if !listener.IsCurrentMsgDeflated() {
			err = writePlainFrame(f)
		} else {
			err = writeDeflatedFrame(f)
		}
		if err != nil {
			listener.PauseListen(err)
			return
		}
		if !f.FIN {
			return
		}

		// Nothing else marks the end — the Listener is not assembling, so there
		// is no completed message to be handed.
		sha256sum := hasher.Sum(nil)
		fmt.Printf("message complete: %d frames, %d bytes\n", frameCount, byteCount)
		fmt.Printf("wrote %s\nsha256 %x\n", outPath, sha256sum)

		// Sent back so our stream_client can compare it with what it sent. That
		// is this example's own convention, not WebSocket's: another client
		// may ignore it. Deflated when agreed; DeflateLevel must be set, its
		// zero is flate.NoCompression.
		reply := fmt.Sprintf("sha256 %x", sha256sum)
		if err := conn.SendText([]byte(reply), wlgows.SendDataConfig{
			DeflateIfAgreed: true,
			DeflateLevel:    flate.DefaultCompression,
		}); err != nil {
			listener.PauseListen(fmt.Errorf("sending sha256: %w", err))
			return
		}

		// Ready for the next message on the same connection.
		frameCount, byteCount = 0, 0
		hasher.Reset()
	}

	// The standard close hook answers and pauses; this only reports first.
	standardClose := config.Close
	config.Close = func(f *wlgows.Frame) {
		payload, _ := f.GetClosePayload()
		fmt.Printf("closed by peer: %+v\n", payload)

		standardClose(f)
	}

	listener.SetConfig(config)

	// A stream can be quiet for a long time between chunks, so the heartbeat
	// runs beside it — 5.4 lets a control frame through mid message, and
	// writeLocker keeps it from cutting into a fragment.
	go conn.StartPingLoop(pingInterval, nil)

	err = listener.Listen()

	// Stopped in the middle of a deflated message: an empty FIN frame makes the
	// pull return. Wait for it, then end the message. The file has only part of
	// it.
	if inflatePullResultChan != nil {
		conn.PutDeflatedDataFrame(inflateToken, &wlgows.Frame{FIN: true})
		<-inflatePullResultChan
		conn.EndInflateDataFrames(inflateToken)
	}

	if err != nil {
		if payload := wlgows.StandardClosePayloadFor(err); payload != nil {
			conn.SendClose(payload)
		}
		fmt.Println("error: listen:", err)
	}
}
