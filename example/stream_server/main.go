// Receives a streamed message without ever holding it whole. Each frame is
// written straight to disk as it arrives, so memory stays flat however large
// the message is.
//
//	go run ./example/stream_server   # in one terminal
//	go run ./example/stream_client   # in another
//
// This is the one example that does NOT use a wlgows.Listener. A Listener
// buffers every fragment until FIN and hands over the assembled message, which
// is exactly what streaming exists to avoid — so the read loop here is by hand,
// and everything the Listener would have done for you is visible below.
package main

import (
	"crypto/sha256"
	"fmt"
	"io"
	"os"

	"github.com/weilun-shrimp/wlgows/v3"
)

const (
	service  = ":8001"
	outPath  = "./received.bin"
	maxFrame = 1 * 1024 * 1024 // per frame, refused at the header before allocating
)

func main() {
	server, err := wlgows.Run(service)
	if err != nil {
		fmt.Println("server run:", err)
		return
	}
	defer server.Close()

	fmt.Println("stream server listening on " + service)
	for {
		conn, err := server.Accept()
		if err != nil {
			fmt.Println("accept:", err)
			continue
		}
		go handleConn(conn)
	}
}

func handleConn(conn *wlgows.ServerConn) {
	defer conn.Close()

	if _, err := conn.HandShake(); err != nil {
		fmt.Println("handshake:", err)
		return
	}
	fmt.Println("connected:", conn.RemoteAddr())

	out, err := os.Create(outPath)
	if err != nil {
		fmt.Println("create:", err)
		return
	}
	defer out.Close()

	// Hash as it streams past, so nothing has to be re-read to verify it.
	hasher := sha256.New()
	sink := io.MultiWriter(out, hasher)

	var frameCount int
	var byteCount int64

	for {
		f, err := conn.GetNextFrame(maxFrame)
		if err != nil {
			fmt.Println("read:", err)
			return
		}

		// 5.2: reserved bits belong to a negotiated extension, and none is.
		if f.RSV1 || f.RSV2 || f.RSV3 {
			conn.SendClose(&wlgows.ClosePayload{StatusCode: wlgows.CloseProtocolError})
			return
		}
		// 5.1: a client masks every frame it sends.
		if !f.Mask {
			conn.SendClose(&wlgows.ClosePayload{StatusCode: wlgows.CloseProtocolError})
			return
		}

		// 5.4: control frames may arrive between fragments, and must be handled
		// without disturbing the message being streamed.
		if wlgows.IsControlOpcode(f.Opcode) {
			switch f.Opcode {
			case wlgows.OpcodePing:
				conn.SendPong(f.PayloadData) // 5.5.2: MUST answer
			case wlgows.OpcodeClose:
				payload, _ := f.GetClosePayload()
				fmt.Printf("closed by peer: %+v\n", payload)
				conn.SendClose(payload) // 5.5.1: MUST answer
				return
			}
			continue // 5.5.3: a pong is never answered
		}

		// An opcode 5.2 reserves.
		if !wlgows.IsDataOpcode(f.Opcode) {
			conn.SendClose(&wlgows.ClosePayload{StatusCode: wlgows.CloseProtocolError})
			return
		}

		// 5.4: the first frame carries the message's opcode, every one after it
		// carries OpcodeContinuation. Anything else is a second message trying
		// to interleave with this one.
		wantContinuation := frameCount > 0
		if isContinuation := f.Opcode == wlgows.OpcodeContinuation; isContinuation != wantContinuation {
			conn.SendClose(&wlgows.ClosePayload{StatusCode: wlgows.CloseProtocolError})
			return
		}

		if _, err := sink.Write(f.PayloadData); err != nil {
			fmt.Println("write:", err)
			return
		}
		frameCount++
		byteCount += int64(len(f.PayloadData))
		fmt.Printf("frame %d: %d bytes, FIN=%v\n", frameCount, len(f.PayloadData), f.FIN)

		if f.FIN {
			fmt.Printf("message complete: %d frames, %d bytes\n", frameCount, byteCount)
			fmt.Printf("wrote %s\nsha256 %x\n", outPath, hasher.Sum(nil))

			// Ready for the next message on the same connection.
			frameCount, byteCount = 0, 0
			hasher.Reset()
		}
	}
}
