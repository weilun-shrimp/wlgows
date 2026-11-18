**English** · [繁體中文](./README.zh-TW.md)

# WLGOWS

A simple, intuitive, powerful and fast WebSocket library for Go: zero
allocations on send, and faster than gorilla/websocket almost everywhere
([benchmarks](./BENCHMARK_COMPARISON.md)).
It is quick to start with, and it does not hide how WebSocket works.

The Quick Start below is a working echo server. `conn.NewStandardListener()`
answers ping, pong and close for you. When you need more, you can stream a
message larger than memory, handle each frame yourself, ping on your own
schedule, or build a frame by hand. You run the handshake yourself, and every
rule from RFC 6455 and RFC 7692 is documented.

**Correctness first.** We want the fastest code, but only when it stays correct
and safe: no `unsafe`, and no skipped bounds checks. Your own code may go
further if you know what you are doing. See
[Faster and less memory](./SENDING.md#faster-and-less-memory-sending-a-string-with-unsafe).

## Features

- **WebSocket Server** — behind `net/http` or Gin, or on raw TCP when you know what you are doing
- **WebSocket Client** — `ws://` and `wss://`
- **TLS/SSL** — both sides
- **Subprotocols** — offer, pick or require one, on either side
- **HTTP Hijacking** — `http.Server` or Gin
- **Listener** — a read loop that validates against RFC 6455 and routes frames to your hooks
- **Frame-level control** — build and send your own frames when you need to
- **Streaming** — send a message larger than memory, fragment by fragment
- **Compression** — permessage-deflate (RFC 7692), on both sides. See [PERMESSAGE_DEFLATE.md](./PERMESSAGE_DEFLATE.md)
- **Memory in your hands** — hand each connection its own write buffer, from 14 bytes up, and swap it at any time. Pick the frame size on every send. See [SENDING.md](./SENDING.md#you-are-in-control)
- **Keepalive** — ping on an interval, with a payload you choose per ping
- **Concurrent sending** — lock-guarded, and control frames are never stuck behind a long message

## Contents

- [Installation](#installation)
- [Quick Start](#quick-start) — [Server](#server) · [Client](#client)
- [Guides](#guides) — server, client, reading, the Listener, sending, compression, benchmarks
- [Examples](#examples) — three echo servers, a client, and a streaming pair
- [Coming from v6](#coming-from-v6)
- [Testing](#testing)
- [License](#license)

## Installation

```bash
go get github.com/weilun-shrimp/wlgows/v7
```

The import path carries the `/v7` suffix Go requires for major version 2 and
above; the package name is still `wlgows`, so call sites read `wlgows.DialContext(...)`.

## Quick Start

### Server

```go
package main

import (
	"io"
	"log"
	"net/http"
	"time"

	"github.com/weilun-shrimp/wlgows/v7"
)

const maxMessageSize = 10 * 1024 * 1024 // 10 MB

// readMessage returns the bytes of one message, up to maxMessageSize. It
// decompresses a compressed message and checks text as UTF-8. For a larger
// message, it returns ErrSizeLimitExceeded. See READING.md.
func readMessage(conn *wlgows.Conn, frames wlgows.DataFrames) ([]byte, error) {
	reader, err := conn.DataFramesReader(frames)
	if err != nil {
		return nil, err
	}
	defer reader.Close() // always close it
	return io.ReadAll(wlgows.NewSizeLimitReader(reader, maxMessageSize))
}

func main() {
	server := &http.Server{
		Addr:              ":8001",
		Handler:           http.HandlerFunc(handle),
		ReadHeaderTimeout: 10 * time.Second, // net/http sets no timeout by default
		IdleTimeout:       60 * time.Second, // a kept-alive connection waiting for its next request
		MaxHeaderBytes:    16 * 1024,        // 1 MB by default; a handshake is a few hundred bytes
	}
	log.Fatal(server.ListenAndServe())
}

func handle(w http.ResponseWriter, r *http.Request) {
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

	// Pass bufRW.Reader: net/http may have read the start of the first frame into it.
	conn, _, err := wlgows.ServerHandShake(netConn, bufRW.Reader, bufRW.Writer, r, wlgows.ServerHandShakeConfig{})
	if err != nil {
		return
	}

	// Ping, pong, close and reserved opcodes already answered.
	listener := conn.NewStandardListener()

	// SetConfig replaces all of it, so start from what the standard hooks left.
	config := listener.GetConfig()
	config.MaxDataFramesSize = maxMessageSize
	config.MaxFrameSize = maxMessageSize
	config.MaxDataFrameCount = maxMessageSize / 256
	config.Text = func(frames wlgows.DataFrames) {
		message, err := readMessage(conn, frames)
		if err != nil {
			listener.PauseListen(err) // Listen returns err
			return
		}
		conn.SendText(message, wlgows.SendDataConfig{}) // echo
	}
	listener.SetConfig(config)

	// Heartbeat, on a goroutine of its own. It ends itself when the connection
	// does — see LISTENER.md for noticing a peer that never pongs back.
	go conn.StartPingLoop(30*time.Second, nil)

	if err := listener.Listen(); err != nil {
		if payload := wlgows.StandardClosePayloadFor(err); payload != nil {
			conn.SendClose(payload)
		}
		log.Println("closing:", err)
	}
}
```

Under Gin it is the same, with `c.Writer.Hijack()` and `c.Request`. Serve the
Gin engine with your own `http.Server`, since Gin's `Run` sets no timeout. See
[HTTP hijacking](./SERVER.md#http-hijacking).

### Client

```go
package main

import (
	"bufio"
	"context"
	"io"
	"log"
	"net"
	"time"

	"github.com/weilun-shrimp/wlgows/v7"
)

const maxMessageSize = 10 * 1024 * 1024 // 10 MB

// readMessage returns the bytes of one message, up to maxMessageSize. It
// decompresses a compressed message and checks text as UTF-8. For a larger
// message, it returns ErrSizeLimitExceeded. See READING.md.
func readMessage(conn *wlgows.Conn, frames wlgows.DataFrames) ([]byte, error) {
	reader, err := conn.DataFramesReader(frames)
	if err != nil {
		return nil, err
	}
	defer reader.Close() // always close it
	return io.ReadAll(wlgows.NewSizeLimitReader(reader, maxMessageSize))
}

func main() {
	// DialContext only connects and builds the request. You run the handshake
	// yourself, so you can still add headers to `request` before it is sent.
	// ctx and dialer both limit the TCP connect and the TLS handshake. The
	// shorter one applies. See CLIENT.md.
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	dialer := &net.Dialer{Timeout: 15 * time.Second}
	netConn, request, err := wlgows.DialContext(ctx, "ws://localhost:8001", dialer, nil)
	if err != nil {
		panic(err)
	}
	defer netConn.Close()

	reader := bufio.NewReader(netConn) // 4096 bytes/conn; see READING.md to shrink it
	conn, _, err := wlgows.ClientHandShake(netConn, reader, bufio.NewWriter(netConn), request, wlgows.ClientHandShakeConfig{}) // 4096 bytes/conn; see SENDING.md
	if err != nil {
		panic(err)
	}

	listener := conn.NewStandardListener()

	config := listener.GetConfig() // what the standard hooks left
	config.MaxDataFramesSize = maxMessageSize
	config.MaxFrameSize = maxMessageSize
	config.MaxDataFrameCount = maxMessageSize / 256
	config.Text = func(frames wlgows.DataFrames) {
		message, err := readMessage(conn, frames)
		if err != nil {
			listener.PauseListen(err) // Listen returns err
			return
		}
		log.Printf("received: %s", message)
	}
	listener.SetConfig(config)

	go conn.StartPingLoop(30*time.Second, nil) // heartbeat; ends itself

	conn.SendText([]byte("Hello, WebSocket!"), wlgows.SendDataConfig{})

	if err := listener.Listen(); err != nil {
		if payload := wlgows.StandardClosePayloadFor(err); payload != nil {
			conn.SendClose(payload)
		}
		log.Println("closing:", err)
	}
}
```

Masking needs no attention on either side: each handshake tells the `Conn`
which side it is, so a client masks every frame it sends and a server masks
none (RFC 6455 5.1).

## Guides

| Guide | What is in it |
|---|---|
| **[SERVER.md](./SERVER.md)** | Accepting a connection, the handshake and its refusals, HTTP hijacking from `net/http` or Gin, TLS, subprotocols |
| **[CLIENT.md](./CLIENT.md)** | Dialing, TLS, the handshake, adding headers, subprotocols, compression |
| **[READING.md](./READING.md)** | Reading a message: limiting its size, errors and their close codes, compressed messages, one frame at a time, the read buffer |
| **[LISTENER.md](./LISTENER.md)** | The read loop: the standard Listener, hooks, limits, errors, noticing a dead peer, pausing |
| **[SENDING.md](./SENDING.md)** | Sending: whole messages, streaming, compression, control frames, pinging on an interval, the write buffer, locks |
| **[PERMESSAGE_DEFLATE.md](./PERMESSAGE_DEFLATE.md)** | Compression: each config field, context takeover, memory, compressing large data yourself |
| **[BENCHMARK_COMPARISON.md](./BENCHMARK_COMPARISON.md)** | Against gorilla/websocket and gobwas/ws: every number, and why each loss stays |

## Examples

Three servers run the same echo program in three ways, and the client works
with all of them. The streaming pair runs on its own.

| Example | |
|---|---|
| [`hijack_http`](./example/hijack_http/main.go) | Echo server behind `net/http`, via `http.Hijacker` |
| [`hijack_gin`](./example/hijack_gin/main.go) | The same server behind Gin, via `c.Writer.Hijack()`. `GET /ping` keeps answering JSON alongside the WebSocket |
| [`echo`](./example/echo/main.go) | The same server over raw TCP, with `wlgows.Run` and `Accept`. Adds a timeout on the request, but no size limit. TLS comes from `crypto/tls` |
| [`client`](./example/client/main.go) | Interactive client. Type a line to send it, `exit` to close cleanly |
| [`stream_server`](./example/stream_server/main.go) | Receives a streamed message frame by frame, straight to disk — the `Data` hook, for a message too large to hold. Inflates a compressed message frame by frame too |
| [`stream_client`](./example/stream_client/main.go) | Streams a file as one binary message, a chunk at a time |

Each example runs from its own folder. Start a server, then a client in
another terminal:

```bash
cd example/echo && go run .     # or hijack_http, or hijack_gin
cd example/client && go run .   # then type
```

```bash
cd example/stream_server && go run .
cd example/stream_client && go run .
```

Each program asks a few questions at startup. Press enter to take the default.

- **Compression** is the client's choice: the servers accept any mode it offers.
- **Subprotocols:** the server picks the client's first offer that it speaks.
  With no match, the connection opens without one.
- **The stream client** can stop after a few chunks, to see how the server
  handles a message that never ends.

Both sides print what was agreed. After a whole file, the stream server sends
its SHA-256 back, and the stream client checks it against what it sent.

The certificate in `example/` is for testing only. Do not use it for anything
real.

Each example uses a [`Listener`](./LISTENER.md), which answers pings and
closes and refuses bad frames. What is left in each file is the part you write
yourself: the hooks.

## Coming from v6

v7 reads frame headers straight from your `bufio.Reader`, so a frame allocates
only itself and its payload. It also renames the Listener's limits. Sending,
the handshakes, `NewConn` and `NewListener` now take a config. These no longer
compile:

| v6 | v7 |
|---|---|
| `go get .../wlgows/v6` | `go get github.com/weilun-shrimp/wlgows/v7` |
| Go 1.22 or newer | Go 1.24 or newer: from 1.24 on, `crypto/rand.Read` cannot fail |
| `wlgows.GetFrameFromReader(netConn, max)` | two steps: `frame, err := wlgows.GetFrameHeaderFromReader(reader)`, check `frame.GetDeclaredPayloadSize()` yourself, then `frame.FillFramePayloadFromReader(reader)`, with `reader := bufio.NewReader(netConn)`. Keep one `reader` per connection: it holds bytes already read. |
| `conn.GetNextFrame(max)` | the same two steps under `conn.LockRead()`: `conn.GetNextFrameHeader()`, then `conn.ReadFramePayload(frame)`, then `conn.UnlockRead()` |
| `frame.GetMaxPayloadLength()` | `frame.GetDeclaredPayloadSize()` |
| `wlgows.ReadFromReader(reader, length)` | gone: `buffer := make([]byte, length)`, then `io.ReadFull(reader, buffer)` |
| `err := wlgows.FillMaskingKey(&key)` | `wlgows.FillMaskingKey(&key)`: it returns nothing |
| `ListenerConfig{MaxMsgFrameCount: n}` | `ListenerConfig{MaxDataFrameCount: n}` |
| `ListenerConfig{MaxMsgPayloadByteLen: n}` | `ListenerConfig{MaxDataFramesSize: n, MaxFrameSize: n}` |
| `wlgows.ErrMsgFrameCountExceeded` | `wlgows.ErrDataFrameCountExceeded` |
| `wlgows.Frames`, `func(frames wlgows.Frames)` hooks | `wlgows.DataFrames`, `func(frames wlgows.DataFrames)` |
| `frames.String()`, `frames.Bytes()` | `reader, err := conn.DataFramesReader(frames)`, then `defer reader.Close()`, then `io.ReadAll(wlgows.NewSizeLimitReader(reader, maxSize))`, with `maxSize` the largest message you accept. The reader decompresses a compressed message and checks text as UTF-8. See [READING.md](./READING.md) |
| `conn.SendText(data, chunkSize)`, also `SendBinary` | `conn.SendText(data, wlgows.SendDataConfig{ChunkSize: chunkSize})` |
| `conn.SendData(opcode, payload, chunkSize)` | `conn.SendData(opcode, payload, wlgows.SendDataConfig{ChunkSize: chunkSize})` |
| `conn.StartLongDataTransmission(opcode)` | `conn.StartLongDataTransmission(opcode, wlgows.StartLongDataTransmissionConfig{})` |
| `conn.TransmitData(data)`, also `EndLongDataTransmission` | `conn.TransmitData(data, wlgows.TransmitDataConfig{})` |
| `wlgows.NewConn(netConn, reader, writer, maskSendFrame)` | `wlgows.NewConn(netConn, reader, writer, wlgows.NewConnConfig{PeerIsClient: ...})`. The meaning is the opposite: v6 passed `true` on a client, v7 passes `true` on a server |
| `wlgows.NewListener(conn)` | `wlgows.NewListener(conn, wlgows.NewListenerConfig{PeerIsClient: ..., PermessageDeflateAgreedConfig: ...})`. Set `PeerIsClient` to `true` on a server. If it is wrong, every frame fails with `ErrFrameMasked` or `ErrFrameNotMasked`. `PermessageDeflateAgreedConfig` is `nil` when compression was not agreed |
| `wlgows.UpgradeRequest(request)` | `wlgows.UpgradeRequest(request, wlgows.ClientHandShakeConfig{})` |
| `wlgows.ServerHandShake(netConn, reader, writer, request)` | `wlgows.ServerHandShake(netConn, reader, writer, request, wlgows.ServerHandShakeConfig{})` |
| `wlgows.ClientHandShake(netConn, reader, writer, request)` | `wlgows.ClientHandShake(netConn, reader, writer, request, wlgows.ClientHandShakeConfig{})` |
| your own `ListenerConn` with `GetNextFrame(max)` | `LockRead()`, `UnlockRead()`, `GetNextFrameHeader()` and `ReadFramePayload(frame)`, as `*Conn` has them |

`wlgows.Dial(url, tlsConfig)` still works. `DialContext` is new: it also takes
a context and a `*net.Dialer`. See [CLIENT.md](./CLIENT.md#dialing).

Behaviour that changes without a compile error:

- **`io.EOF` now only means the stream ended between frames.** A stream that
  ends after a frame's first byte returns `io.ErrUnexpectedEOF`. In v6 a frame
  cut right after its first 2 bytes, or right after its header, returned
  `io.EOF`.
- **A 64 bit length with its most significant bit set returns
  `ErrPayloadLengthMSBSet`.** RFC 6455 5.2 forbids that bit.
  `StandardClosePayloadFor` already maps it to 1002, so a loop that sends its
  result, as the examples do, needs no change. In v6 such a frame made `make`
  panic.
- **`MaxDataFramesSize` counts frame headers too.** Every data frame spends
  its 2 to 14 header bytes as well as its payload, empty continuations included.
  In v6 only the payload counted, so a message that just fitted may now be
  refused. Leave a little room above the largest message you accept.
- **Empty continuation frames are no longer dropped.** `Text`, `Binary` and
  `Data` get every frame of the message as it arrived, and each one counts
  toward `MaxDataFrameCount`. In v6 an empty frame in the middle of a message
  was silently skipped.
- **`ClientHandShake` refuses a subprotocol the client did not offer.** It
  returns `ErrHandshakeResponseSubprotocolNotOffered` for a
  `Sec-WebSocket-Protocol` never offered, as RFC 6455 4.1 asks, and
  `ErrHandshakeResponseSubprotocolMultiple` for more than one value. In v6 both
  were accepted.
- **`ClientHandShake` refuses an extension the client did not offer.** It
  returns `ErrHandshakeResponseExtensionNotOffered` for a
  `Sec-WebSocket-Extensions` entry never offered, as RFC 6455 4.1 asks. In v6
  the header was not read.
- **`ClientHandShake` closes the connection when RFC 6455 4.1 or RFC 7692 5
  says to fail it.** For example, a wrong `Upgrade`, `Connection`,
  `Sec-WebSocket-Accept`, `Sec-WebSocket-Protocol` or
  `Sec-WebSocket-Extensions` closes `netConn` before the error returns
  (RFC 6455 7.1.7). Other errors leave it open, and you close it. The full
  list is in [What it returns](./CLIENT.md#what-it-returns). In v6 none closed
  it. A `defer netConn.Close()` keeps working: a second close only returns
  `net.ErrClosed`.
- **The 101 response no longer sends `Sec-WebSocket-Version`.** RFC 6455 4.2.2
  does not list it there; the server sends it back only when refusing a
  version, with a 426. A client that wrongly required it on a 101 now fails.
- **`TransmitData` sends an empty chunk as an empty frame.** In v6 it skipped
  it. Check the length first, as the streaming example does, if you do not want
  that frame.
- **`NewStandardListener` now sets limits.** 4 KB per message and per frame, 16
  frames, and a 60 second read timeout. A larger message ends `Listen` with
  `ErrFrameByteLengthExceeded` (1009), and an idle peer times out. Raise them as
  the quick start does, and ping at 30 seconds or less. In v6 it set no limit.
- **`RFC6455UnknownHook` stops `Listen` with `ErrReservedOpcode`.** In v6 it
  paused with `nil`.
- **A payload length not in its shortest form returns
  `ErrPayloadLengthNotMinimal`** (RFC 6455 5.2, close 1002). In v6 it was
  accepted.
- **`ServerHandShake` refuses a `Sec-WebSocket-Key` that is not base64 of 16
  bytes**, with 400 and `ErrHttpSecWebSocketKeyInvalid`. In v6 any value was
  accepted.

## Testing

```bash
go test ./...                                  # full suite
go test -race ./...                            # the integration tests spawn goroutines
go test -cover .                               # 97.6% of statements
go test -bench BenchmarkDataFramesAssembly .   # benchmarks, which plain `go test` skips
```

One `_test.go` per source file, all in package `wlgows`, so the `di` seams are
reachable. Every function that does I/O or uses randomness is a thin exported
wrapper over a function taking a `di` struct of function values, and the tests
target that function with fakes. Three files have no source counterpart:

| File | Contents |
|---|---|
| [`Benchmark_test.go`](./Benchmark_test.go) | Send and read benchmarks, and `TestBenchmarkReport`, behind BENCHMARK_COMPARISON.md |
| [`Fakes_test.go`](./Fakes_test.go) | Shared fakes, such as `fakeConn`, an in-memory `net.Conn` |
| [`ListenerIntegration_test.go`](./ListenerIntegration_test.go) | `Listen` end to end over a real `net.Pipe`, with **no** `di` substitution |

## License

MIT
