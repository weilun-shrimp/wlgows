// Streams a file as one binary message, a chunk at a time, without ever holding
// it whole.
//
//	go run ./example/stream_server   # in one terminal
//	go run ./example/stream_client   # in another, then press enter twice
//
// chunkByteLen is deliberately small so an ordinary file still fragments: at
// 4 KB this repository's README goes out as four fragments, plus the empty
// frame End sends to carry FIN. RFC 6455 5.4 puts the opcode on the first frame
// and OpcodeContinuation on the rest, and the transmission API does that for
// you — the loop below only supplies bytes, and never has to keep them.
package main

import (
	"crypto/sha256"
	"fmt"
	"io"
	"os"

	"github.com/weilun-shrimp/wlgows/v3"
	"github.com/weilun-shrimp/wlgows/v3/example_helpers"
)

const (
	defaultURL  = "ws://localhost:8001"
	defaultPath = "./README.md"

	// One chunk is one frame. Small enough here that a modest file fragments;
	// 32 KB is a fair default for real use.
	chunkByteLen = 4 * 1024
)

func main() {
	fmt.Print("url (enter for " + defaultURL + ") :")
	url, err := example_helpers.ReadUserInput()
	if err != nil {
		fmt.Println("reading url:", err)
		return
	}
	if url == "" {
		url = defaultURL
	}

	fmt.Print("file to stream (enter for " + defaultPath + ") :")
	path, err := example_helpers.ReadUserInput()
	if err != nil {
		fmt.Println("reading path:", err)
		return
	}
	if path == "" {
		path = defaultPath
	}

	file, err := os.Open(path)
	if err != nil {
		fmt.Println("open:", err)
		return
	}
	defer file.Close()

	conn, err := wlgows.Dial(url, nil)
	if err != nil {
		fmt.Println("dial:", err)
		return
	}
	defer conn.Close()

	if err := conn.HandShake(); err != nil {
		fmt.Println("handshake:", err)
		return
	}

	sum, chunks, bytes, err := stream(conn, file)
	if err != nil {
		fmt.Println("stream:", err)
		return
	}
	fmt.Printf("sent %s: %d chunks, %d bytes\nsha256 %x\n", path, chunks, bytes, sum)

	// 7.1.1: send close, then wait for the server's reply before letting the
	// deferred Close take the socket down.
	conn.SendClose(&wlgows.ClosePayload{StatusCode: wlgows.CloseNormalClosure, Reason: "done"})
	if f, err := conn.GetNextFrame(0); err == nil {
		payload, _ := f.GetClosePayload()
		fmt.Printf("server closed: %+v\n", payload)
	}
}

func stream(conn *wlgows.ClientConn, r io.Reader) (sum []byte, chunks int, bytes int64, err error) {
	if err := conn.StartLongDataTransmission(wlgows.OpcodeBinary); err != nil {
		return nil, 0, 0, err
	}
	// Releases the connection and sends the last chunk with FIN, which is what
	// tells the server the message ended.
	defer conn.EndLongDataTransmission()

	hasher := sha256.New()
	buf := make([]byte, chunkByteLen)
	for {
		n, readErr := r.Read(buf)

		// Read may return bytes AND io.EOF together. Handling n before readErr
		// is what stops the last chunk being dropped.
		if n > 0 {
			if err := conn.TransmitData(buf[:n]); err != nil {
				return nil, chunks, bytes, err
			}
			hasher.Write(buf[:n])
			chunks++
			bytes += int64(n)
			fmt.Printf("chunk %d: %d bytes\n", chunks, n)
		}

		if readErr == io.EOF {
			return hasher.Sum(nil), chunks, bytes, nil
		}
		if readErr != nil {
			return nil, chunks, bytes, readErr
		}
	}
}
