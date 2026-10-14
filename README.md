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
rule RFC 6455 gives you is documented.

**Correctness first.** We want the fastest code, but only when it stays correct
and safe: no `unsafe`, and no skipped bounds checks. Your own code may go
further if you know what you are doing. See
[Faster and less memory](./SENDING_README.md#faster-and-less-memory-sending-a-string-with-unsafe).

## Features

- **WebSocket Server** — behind `net/http` or Gin, or on raw TCP when you know what you are doing
- **WebSocket Client** — `ws://` and `wss://`
- **TLS/SSL** — both sides
- **Subprotocols** — offer, pick or require one, on either side
- **HTTP Hijacking** — `http.Server` or Gin
- **Listener** — a read loop that validates against RFC 6455 and routes frames to your hooks
- **Frame-level control** — build and send your own frames when you need to
- **Streaming** — send a message larger than memory, fragment by fragment
- **Memory in your hands** — hand each connection its own write buffer, from 14 bytes up, and swap it at any time. Pick the frame size on every send. See [SENDING_README.md](./SENDING_README.md#you-are-in-control)
- **Keepalive** — ping on an interval, with a payload you choose per ping
- **Concurrent sending** — lock-guarded, and control frames are never stuck behind a long message

## Contents

- [Installation](#installation)
- [Quick Start](#quick-start) — [Server](#server) · [Client](#client)
- [Guides](#guides) — server, client, reading, sending, benchmarks
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
	"log"
	"net/http"
	"time"

	"github.com/weilun-shrimp/wlgows/v7"
)

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

	// Ping, pong, close and reserved opcodes already answered, masking settled
	// from which side this connection is.
	listener := conn.NewStandardListener()

	// SetConfig replaces all of it, so start from what the standard hooks left.
	config := listener.GetConfig()
	config.MaxDataFramesSize = 10 * 1024 * 1024
	config.MaxFrameSize = 10 * 1024 * 1024
	config.MaxDataFrameCount = 10 * 1024 * 1024 / 256
	config.Text = func(frames wlgows.DataFrames) {
		conn.SendText(frames.Bytes(), 0) // echo
	}
	listener.SetConfig(config)

	// Heartbeat, on a goroutine of its own. It ends itself when the connection
	// does — see LISTENER_README.md for noticing a peer that never pongs back.
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
[HTTP hijacking](./SERVER_README.md#http-hijacking).

### Client

You never set masking: the `Conn` knows which side it is.

```go
package main

import (
	"bufio"
	"context"
	"log"
	"net"
	"time"

	"github.com/weilun-shrimp/wlgows/v7"
)

func main() {
	// DialContext only connects and builds the request — running the handshake
	// is yours, so you can still add headers to request before it goes out. ctx
	// and dialer each bound the connect; the earlier one wins. See CLIENT_README.md.
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	dialer := &net.Dialer{Timeout: 15 * time.Second}
	netConn, request, err := wlgows.DialContext(ctx, "ws://localhost:8001", dialer, nil)
	if err != nil {
		panic(err)
	}
	defer netConn.Close()

	reader := bufio.NewReader(netConn) // 4096 bytes/conn; see LISTENER_README.md to shrink it
	conn, _, err := wlgows.ClientHandShake(netConn, reader, bufio.NewWriter(netConn), request) // 4096 bytes/conn; see SENDING_README.md
	if err != nil {
		panic(err)
	}

	listener := conn.NewStandardListener()

	config := listener.GetConfig() // what the standard hooks left
	config.MaxDataFramesSize = 10 * 1024 * 1024
	config.MaxFrameSize = 10 * 1024 * 1024
	config.MaxDataFrameCount = 10 * 1024 * 1024 / 256
	config.Text = func(frames wlgows.DataFrames) {
		log.Println("received:", frames.String())
	}
	listener.SetConfig(config)

	go conn.StartPingLoop(30*time.Second, nil) // heartbeat; ends itself

	conn.SendText([]byte("Hello, WebSocket!"), 0)

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
| **[SERVER_README.md](./SERVER_README.md)** | Accepting a connection, the handshake and its refusals, HTTP hijacking from `net/http` or Gin, TLS, subprotocols |
| **[CLIENT_README.md](./CLIENT_README.md)** | Dialing, TLS, the handshake, adding headers, subprotocols |
| **[LISTENER_README.md](./LISTENER_README.md)** | Reading: hooks, limits, errors, noticing a dead peer, one frame at a time, the read buffer |
| **[SENDING_README.md](./SENDING_README.md)** | Sending: whole messages, streaming, control frames, pinging on an interval, the write buffer, locks |
| **[BENCHMARK_COMPARISON.md](./BENCHMARK_COMPARISON.md)** | Against gorilla/websocket and gobwas/ws: every number, and why each loss stays |

## Examples

Three servers run the same echo program in three ways, and the client works
with all of them. The streaming pair runs on its own.

| Example | |
|---|---|
| [`hijack_http`](./example/hijack_http/main.go) | Echo server behind `net/http`, via `http.Hijacker` |
| [`hijack_gin`](./example/hijack_gin/main.go) | The same server behind Gin, via `c.Writer.Hijack()`. `GET /ping` keeps answering JSON alongside the WebSocket |
| [`echo`](./example/echo/main.go) | The same server over raw TCP, with `wlgows.Run` and `Accept`. Adds a timeout on the request, but no size limit |
| [`client`](./example/client/main.go) | Interactive client. Type a line to send it, `exit` to close cleanly. Prompts for a CA path, so it speaks `wss://` too |
| [`stream_server`](./example/stream_server/main.go) | Receives a streamed message frame by frame, straight to disk — the `Data` hook, for a message too large to hold |
| [`stream_client`](./example/stream_client/main.go) | Streams a file as one binary message, a chunk at a time |

The examples are their own module, so they run from `example/`:

```bash
cd example
go run ./echo      # or hijack_http, or hijack_gin
go run ./client    # in another terminal, then type
```

The streaming pair prints a SHA-256 at each end, and the two match:

```bash
cd example
go run ./stream_server
go run ./stream_client   # in another terminal, enter twice for README.md
```

Both hijack servers ask for a certificate and key at startup. Press enter twice
for `ws://`, or give paths for `wss://`.

Each example uses a [`Listener`](./LISTENER_README.md), which answers pings and
closes and refuses bad frames. What is left in each file is the part you write
yourself: the hooks.

## Coming from v6

v7 reads frame headers straight from your `bufio.Reader`, so a frame allocates
only itself and its payload. It also renames the Listener's limits. Other `Conn`
code is unchanged. These no longer compile:

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
| `wlgows.ServerHandShake(netConn, reader, writer, request)` | `wlgows.ServerHandShake(netConn, reader, writer, request, wlgows.ServerHandShakeConfig{})` |
| your own `ListenerConn` with `GetNextFrame(max)` | `LockRead()`, `UnlockRead()`, `GetNextFrameHeader()` and `ReadFramePayload(frame)`, as `*Conn` has them |

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
- **`ClientHandShake` closes the connection on a response RFC 6455 4.1 fails.**
  A wrong `Upgrade`, `Connection`, `Sec-WebSocket-Accept` or
  `Sec-WebSocket-Protocol` closes `netConn` before the error returns (7.1.7).
  Other errors still leave it to you. In v6 none closed it. A `defer
  netConn.Close()` keeps working: a second close only returns `net.ErrClosed`.
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
