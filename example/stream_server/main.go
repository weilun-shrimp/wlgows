// Receives a streamed message without ever holding it whole. Each frame is
// written straight to disk as it arrives, so memory stays flat however large
// the message is.
//
//	go run ./example/stream_server   # in one terminal
//	go run ./example/stream_client   # in another
//
// The Data hook is what makes this possible: set it and the Listener stops
// assembling, handing over each data frame instead of the message it would have
// built. Nothing is retained between frames, so the payload only ever exists
// once — here, on its way to the file.
//
// What you give up is what assembly bought. FIN is yours to watch for, since
// nothing else says where the message ends, and 5.6 UTF-8 validity is not
// checked because it cannot be judged one frame at a time. This only accepts
// binary for that reason.
//
// So handle it with care, and use it only for this. A message that fits in
// memory belongs to Text or Binary, which check 5.6 and hand you the whole
// thing. Every rule the hook stops keeping becomes yours quietly: a frame your
// hook drops is a corrupt file nothing reports, and text accepted unchecked
// buys a close 1007 from the peer that you cannot diagnose locally.
package main

import (
	"crypto/sha256"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/weilun-shrimp/wlgows/v3"
)

const (
	service = ":8001"
	outPath = "./received.bin"

	// The budget for one message, spent frame by frame, so it has to cover the
	// whole stream rather than one fragment of it. Size it for the largest
	// transmission you are willing to receive.
	//
	// It bounds a single frame too, at the header before anything is allocated —
	// but only by what the message has left, so early on a frame may claim most
	// of this. Bounding every frame tightly while letting the message run long is
	// what reading with conn.GetNextFrame(max) yourself still does better.
	maxMsgPayloadByteLen = 2 * 1024 * 1024 * 1024 // 2 GB

	frameReadTimeout = 60 * time.Second
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

	// Ping, pong, close and reserved opcodes are answered for you, and masking
	// (5.1) is settled from which side this connection is.
	listener := conn.NewStandardListener()

	config := listener.GetConfig()
	config.MaxMsgPayloadByteLen = maxMsgPayloadByteLen
	config.FrameReadTimeout = frameReadTimeout

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
			fmt.Println("refusing a text stream: 5.6 cannot be checked frame by frame")
			conn.SendClose(&wlgows.ClosePayload{StatusCode: wlgows.CloseUnsupportedData})
			listener.PauseListen()
			return
		}

		if _, err := sink.Write(f.PayloadData); err != nil {
			fmt.Println("write:", err)
			listener.PauseListen()
			return
		}
		frameCount++
		byteCount += int64(len(f.PayloadData))
		fmt.Printf("frame %d: %d bytes, FIN=%v\n", frameCount, len(f.PayloadData), f.FIN)

		// Nothing else marks the end — the Listener is not assembling, so there
		// is no completed message to be handed.
		if f.FIN {
			fmt.Printf("message complete: %d frames, %d bytes\n", frameCount, byteCount)
			fmt.Printf("wrote %s\nsha256 %x\n", outPath, hasher.Sum(nil))

			// Ready for the next message on the same connection.
			frameCount, byteCount = 0, 0
			hasher.Reset()
		}
	}

	// The standard close hook answers and pauses; this only reports first.
	standardClose := config.Close
	config.Close = func(f *wlgows.Frame) {
		payload, _ := f.GetClosePayload()
		fmt.Printf("closed by peer: %+v\n", payload)

		standardClose(f)
	}

	listener.SetConfig(config)

	if err := listener.Listen(); err != nil {
		if payload := wlgows.StandardClosePayloadFor(err); payload != nil {
			conn.SendClose(payload)
		}
		fmt.Println("closing:", err)
	}
}
