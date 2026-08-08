# WLGOWS

A WebSocket library for Go that works in frames, not abstractions. Server and
client, manual handshake, and every rule RFC 6455 puts on you kept where you can
see it.

## Features

- **WebSocket Server** — raw TCP with HTTP handshake
- **WebSocket Client** — `ws://` and `wss://`
- **TLS/SSL** — both sides
- **HTTP Hijacking** — `http.Server` or Gin
- **Listener** — a read loop that validates against RFC 6455 and routes frames to your hooks
- **Frame-level control** — build and send your own frames when you need to
- **Streaming** — send a message larger than memory, fragment by fragment
- **Concurrent sending** — lock-guarded, and control frames are never stuck behind a long message

## Contents

- [Quick Start](#quick-start) — [Server](#server) · [Client](#client) · [TLS](#tls-wss) · [HTTP Hijacking](#http-hijacking)
- [Examples](#examples) — three servers and a client, ready to run
- [Coming from v2](#coming-from-v2)
- [Design](#design)
- [Reading](#reading) — `Listener`, or one frame at a time
- [Sending](#sending) — whole messages, control frames, streaming, raw frames
- [Concurrency](#concurrency) — which lock guards what
- [Errors](#errors) — sentinels and `StandardClosePayloadFor`
- [Testing](#testing)

The `Listener` has a guide of its own: **[LISTENER_README.md](./LISTENER_README.md)**.

## Installation

```bash
go get github.com/weilun-shrimp/wlgows/v3
```

The import path carries the `/v3` suffix Go requires for major version 2 and
above; the package name is still `wlgows`, so call sites read `wlgows.Dial(...)`.

## Quick Start

### Server

```go
package main

import (
	"log"

	"github.com/weilun-shrimp/wlgows/v3"
)

func main() {
	server, err := wlgows.Run(":8001")
	if err != nil {
		panic(err)
	}
	defer server.Close()

	for {
		conn, err := server.Accept()
		if err != nil {
			continue
		}
		go handle(conn)
	}
}

func handle(conn *wlgows.ServerConn) {
	defer conn.Close()

	if _, err := conn.HandShake(); err != nil {
		return
	}

	// Ping, pong, close and reserved opcodes already answered, masking settled
	// from which side this connection is.
	listener := conn.NewStandardListener()

	// SetConfig replaces all of it, so start from what the standard hooks left.
	config := listener.GetConfig()
	config.MaxMsgPayloadByteLen = 10 * 1024 * 1024
	config.Text = func(frames wlgows.Frames) {
		conn.SendText(frames.Bytes()) // echo
	}
	listener.SetConfig(config)

	if err := listener.Listen(); err != nil {
		if payload := wlgows.StandardClosePayloadFor(err); payload != nil {
			conn.SendClose(payload)
		}
		log.Println("closing:", err)
	}
}
```

### Client

Same shape as the server. Masking flips — a server does not mask what it sends —
but nothing here says so: a `Conn` knows which side it is, and
`NewStandardListener` takes it from there.

```go
package main

import (
	"log"

	"github.com/weilun-shrimp/wlgows/v3"
)

func main() {
	conn, err := wlgows.Dial("ws://localhost:8001", nil)
	if err != nil {
		panic(err)
	}
	defer conn.Close()

	if err := conn.HandShake(); err != nil {
		panic(err)
	}

	listener := conn.NewStandardListener()

	config := listener.GetConfig() // what the standard hooks left
	config.MaxMsgPayloadByteLen = 10 * 1024 * 1024
	config.Text = func(frames wlgows.Frames) {
		log.Println("received:", frames.String())
	}
	listener.SetConfig(config)

	conn.SendText([]byte("Hello, WebSocket!"))

	if err := listener.Listen(); err != nil {
		if payload := wlgows.StandardClosePayloadFor(err); payload != nil {
			conn.SendClose(payload)
		}
		log.Println("closing:", err)
	}
}
```

Masking needs no attention on either side: `Dial` and `NewClientConn` set
`maskSendFrame`, so `SendText` and `SendPong` mask, and the server's do not.

### TLS (wss://)

```go
caCert, _ := os.ReadFile("ca.crt")
caCertPool := x509.NewCertPool()
caCertPool.AppendCertsFromPEM(caCert)

conn, err := wlgows.Dial("wss://localhost:8001", &tls.Config{RootCAs: caCertPool})
```

### HTTP Hijacking

```go
func handler(w http.ResponseWriter, r *http.Request) {
	conn, err := wlgows.HijackFromHttp(w, r) // or HijackFromGin(c)
	if err != nil {
		return
	}
	defer conn.Close()

	conn.HandShake()
	// ... read and send
}
```

## Examples

Three servers and one client. The servers are the same echo program reached
three different ways, so the client drives any of them:

| Example | |
|---|---|
| [`echo`](./example/echo/main.go) | Echo server over raw TCP — `wlgows.Run` and `Accept` |
| [`hijack_http`](./example/hijack_http/main.go) | The same server behind `net/http`, via `HijackFromHttp` |
| [`hijack_gin`](./example/hijack_gin/main.go) | The same server behind Gin, via `HijackFromGin`. `GET /ping` keeps answering JSON alongside the WebSocket |
| [`client`](./example/client/main.go) | Interactive client. Type a line to send it, `exit` to close cleanly. Prompts for a CA path, so it speaks `wss://` too |
| [`stream_server`](./example/stream_server/main.go) | Receives a streamed message frame by frame, straight to disk — the `Data` hook, for a message too large to hold |
| [`stream_client`](./example/stream_client/main.go) | Streams a file as one binary message, a chunk at a time |

```bash
go run ./example/echo      # or hijack_http, or hijack_gin
go run ./example/client    # in another terminal, then type
```

The streaming pair is its own demo — it prints a SHA-256 at each end, and they
match:

```bash
go run ./example/stream_server
go run ./example/stream_client   # in another terminal, enter twice for README.md
```

Both hijack servers prompt for a certificate and key at startup — press enter
twice for plain `ws://`, or give paths to serve `wss://`.

Every one of them uses a [`Listener`](./LISTENER_README.md), so they answer
pings, echo the close handshake, and reject frames RFC 6455 forbids without any
of that appearing in the example. What is left in each file is the part you
would write yourself: the hooks.

## Coming from v2

`Msg` is gone. A message is a `Frames`, which is `[]*Frame`, so what you hold is
what arrived.

| v2 | v3 |
|---|---|
| `conn.GetNextMsg()` | `conn.GetNextFrame(max)`, or a `Listener` that assembles for you |
| `msg.GetStr()` / `msg.GetBytes()` | `frames.String()` / `frames.Bytes()` |
| `conn.SendByte(b)` | `conn.SendBinary(b)` |
| `Error{Type, Msg}` | sentinel errors — `errors.Is(err, wlgows.ErrInvalidUTF8)` |
| `NewConn(c, req, res)` | `NewConn(c, req, res, maskSendFrame)` |

`GetNextFrame` takes a byte limit, which `GetNextMsg` had no way to express: a
peer can claim a 10 GB payload in a 10 byte header, and the limit refuses it at
the header before anything is allocated.

## Design

**Single package.** Everything is in the root `wlgows` package:

```go
import "github.com/weilun-shrimp/wlgows/v3"

conn, _ := wlgows.Dial(url, nil)
s, _    := wlgows.Run(":8001")
sc, _   := wlgows.HijackFromHttp(w, r)
```

**Connections must be built by their constructors.** `Conn`, `ClientConn`,
`ServerConn` and `Server` carry unexported dependency fields, so a hand-written
struct literal panics on first use. `Dial`, `Run`, `Accept` and `HijackFrom*`
already do the right thing; only direct construction needs care:

```go
cc := wlgows.NewClientConn(netConn, req)
sc := wlgows.NewServerConn(netConn, req)
c  := wlgows.NewConn(netConn, req, res, false) // false: a server does not mask (5.1)
```

That last argument is RFC 6455 5.1 and follows from which side you are: a client
masks every frame it sends, a server masks none, and a peer fails the connection
on the wrong one. It is settled once, at construction, so no send call can pass
it wrong. `NewClientConn` and `NewServerConn` fill it in.

Exported fields (`ClientRequest`, `ServerResponse`, `TCPAddr`, `TCPListener`)
are readable and settable.

## Reading

Two ways, depending on how much you want to own.

**A `Listener`** reads, validates against RFC 6455, assembles fragmented
messages and calls the hook for each opcode. It never writes and never closes —
every obligation the RFC puts on a receiver lands on a hook. `SetConfig` takes
the configuration whole, by copy, and is callable whenever — including from
inside a hook:

```go
listener := conn.NewStandardListener() // or wlgows.NewListener(conn), no hooks

config := listener.GetConfig()
config.MaxMsgPayloadByteLen = 10 * 1024 * 1024
config.Text = func(frames wlgows.Frames) { log.Print(frames.String()) }
listener.SetConfig(config)

err := listener.Listen()
```

`NewStandardListener` is the same Listener with the protocol's own duties already
answered — ping, pong, close and reserved opcodes — and `PeerIsClient` taken from
which side the connection is. Every one of those hooks can be replaced.

See **[LISTENER_README.md](./LISTENER_README.md)** — hooks, limits, pausing, and
what each error means.

**`GetNextFrame`** hands you one frame at a time, and you assemble:

```go
var frames wlgows.Frames
for {
	f, err := conn.GetNextFrame(10 * 1024 * 1024) // 0 for no limit
	if err != nil {
		return err
	}
	frames = append(frames, f)
	if f.FIN {
		break
	}
}
```

One frame at a time is what lets a huge message be streamed somewhere else
instead of held whole. `Frames` assembles when you want it to:

| Call | Returns |
|---|---|
| `frames.String()` | the joined payload as a `string` |
| `frames.Bytes()` | the joined payload as a fresh `[]byte` you own |
| `frames.ByteLen()` | the joined size in **bytes**, allocating nothing |

Both assemblers join every fragment, so a multi-byte rune split across a frame
boundary comes back whole. `ByteLen` is bytes, never characters: `"中文字"`
reports 9, not 3.

The opcode lives on the first frame — continuation frames carry 0:

```go
switch frames[0].Opcode {
case wlgows.OpcodeText:   // 0x1
case wlgows.OpcodeBinary: // 0x2
case wlgows.OpcodeClose:  // 0x8 — stop reading
case wlgows.OpcodePing:   // 0x9
case wlgows.OpcodePong:   // 0xA
}
```

## Sending

Four levels. Use the highest one that fits.

| | Call |
|---|---|
| A whole message | `SendText(b)`, `SendBinary(b)` |
| Control frames | `SendClose(payload)`, `SendPing(b)`, `SendPong(b)` |
| Too large to hold | `StartLongDataTransmission(opcode)` / `TransmitData(chunk)` / `EndLongDataTransmission()` |
| Your own frame | `NewFrame(config)` then `SendFrame(f)` |

`SendText` checks UTF-8 (5.6) and refuses invalid bytes with `ErrInvalidUTF8`,
because a peer that validates answers close 1007. `SendBinary` checks nothing —
5.6 gives binary no encoding at all.

**The close ends it.** 5.5.1 puts the closing handshake at one close each way and
allows no data frame after one, so once `SendClose` has gone out, `SendClose`,
`SendText`, `SendBinary` and `StartLongDataTransmission` all return
`ErrCloseAlreadySent` and write nothing. Each checks under the same lock that
records the close, so two goroutines racing to answer a peer's close cannot both
put a frame on the wire — the loser is told its frame was not needed. `SendFrame`
is exempt: it checks nothing by design, so a close you build yourself is yours to
sequence.

Streaming a message too big for memory — one chunk is one frame, so read into a
buffer and pass it as many times as you like:

```go
if err := conn.StartLongDataTransmission(wlgows.OpcodeBinary); err != nil {
	return err
}
defer conn.EndLongDataTransmission()

buf := make([]byte, 32*1024)
for {
	n, err := file.Read(buf)
	if n > 0 {
		if err := conn.TransmitData(buf[:n]); err != nil {
			return err
		}
	}
	if err == io.EOF {
		return nil
	}
	if err != nil {
		return err
	}
}
```

**`TransmitData` does not retain `buf`.** It seals and writes before returning,
so the next `Read` into the same buffer is safe — memory stays flat at one chunk
however large the message is.

5.4 is kept for you: the opcode goes on the first frame and `OpcodeContinuation`
on every one after, and no other message interleaves. `End` terminates with an
empty frame carrying FIN, which is why a four chunk message goes out as five
frames.

[`stream_client`](./example/stream_client/main.go) streams a file this way, and
[`stream_server`](./example/stream_server/main.go) receives it without holding
it. A `Listener` normally assembles the whole message before your hook runs, so
that end uses the `Data` hook, which hands over each data frame instead and
retains nothing — see [LISTENER_README.md](./LISTENER_README.md).

`SendFrame` is the escape hatch — it writes what you built and checks almost
nothing beyond masking. Read its doc before reaching for it.

## Concurrency

`Conn` holds three `sync.Locker` values, all defaulting to `*sync.Mutex`:

| Locker | Guards | Taken by |
|---|---|---|
| `writeLocker` | one frame on the wire at a time | every `Send*`, for the length of one frame |
| `dataFramesWriteLocker` | one data message at a time | `SendText`, `SendBinary`, and `Start`…`End` |
| `readLocker` | one reader | `GetNextFrame` |

**Sending from many goroutines is safe.** Two locks rather than one is what
keeps a pong from waiting behind a 10 GB transfer: a control frame takes only
`writeLocker`, so it slips between fragments — which 5.4 permits on purpose and
5.5.2 asks for.

**Use one reader goroutine.** `readLocker` stops two readers stealing each
other's bytes, but they would still each get an arbitrary subset of frames.

**`conn.Write` and `conn.Read` bypass every lock** — they are promoted from the
embedded `net.Conn`. Send through the `Send*` methods.

`Close` takes no lock on purpose: closing the fd is what unblocks a read or
write parked on a dead peer.

## Errors

Every error wraps a package-level sentinel with `%w`, so compare with
`errors.Is` rather than `==`:

```go
if _, err := sc.HandShake(); errors.Is(err, wlgows.ErrHttpMethodNotAllowed) {
	// ...
}
```

`StandardClosePayloadFor` maps a `Listen` error to the close code RFC 6455 7.4.1
wants — 1007 for bad UTF-8, 1009 for a message over a limit, 1002 for the rest,
and `nil` for anything a close frame cannot answer:

```go
if payload := wlgows.StandardClosePayloadFor(err); payload != nil {
	conn.SendClose(payload)
}
```

`nil` is the absence of an attribution, not an instruction to close. See the
Errors section of [LISTENER_README.md](./LISTENER_README.md).

## Testing

```bash
go test ./...                              # full suite
go test -race ./...                        # the integration tests spawn goroutines
go test -cover .                           # 99.8% of statements
go test -bench BenchmarkFramesAssembly .   # benchmarks, which plain `go test` skips
```

One `_test.go` per source file, all in package `wlgows` so the `di` seams are
reachable. Two files have no source counterpart:

| File | Contents |
|---|---|
| [`Fakes_test.go`](./Fakes_test.go) | Shared doubles — `fakeConn` (in-memory `net.Conn`), `fakeLocker` (counts and catches misuse), `fixedRandRead`, `scriptedReadTCPConn` |
| [`ListenerIntegration_test.go`](./ListenerIntegration_test.go) | `Listen` end to end over a real `net.Pipe`, with **no** `di` substitution |

The integration tests catch wiring mistakes the unit tests structurally cannot —
a constructor that forgets a `di` field, or a default pointing at the wrong
function.

`BenchmarkFramesAssembly` justifies `Bytes()` over `[]byte(String())` on a
70000 byte message:

```
[]byte(String())   10774 ns/op   147458 B/op   2 allocs/op
Bytes()             5722 ns/op    73729 B/op   1 allocs/op
```

Half the time, half the memory — the `string` header only ever existed to be
converted away.

### Substituting a dependency

Every function that performs I/O or uses randomness is a thin exported wrapper
over an implementation taking a `di` struct of function values; types hold their
dependencies in a `di` field populated by the constructor. All of it is
unexported, so only in-package tests can reach it. Deterministic functions
(`Frame.Seal`, `Frames.String`, `ValidateHandShakeRequest`, …) have no seam and
are tested directly.

```go
// package-level func: pass a di struct
cc, err := dial("ws://localhost:8001", nil, dialDI{...})

// method: overwrite the field the constructor set
conn := NewConn(netConn, nil, nil, false)
conn.di.newDataFrame = func(config NewFrameConfig) (*Frame, error) { ... }
```

Fixing the random source is what makes masked output assertable — a masked
frame's bytes cannot be pinned otherwise:

```go
f, _ := newFrame(NewFrameConfig{PayloadData: []byte("hi"), Opcode: 1, Mask: true, FIN: true},
	newFrameDI{generateMaskingKey: func() ([]byte, error) { return []byte{1, 2, 3, 4}, nil }})
// f.Seal() == []byte{0x81, 0x82, 1, 2, 3, 4, 'h'^1, 'i'^2}
```

Two tests deliberately assert current behaviour rather than correct behaviour,
and say so in their names and comments:

- `TestResponseWriterWrittenBodyIsLostWithoutFlush` — `Write` fills a
  `bufio.Writer` that is never flushed, while `GenerateResponse` reads the
  underlying buffer, so a short body never reaches the response and
  `Content-Length` stays 0. Bodies over 4096 bytes bypass the buffer and do
  survive. When this is fixed, the test fails and tells you to update it.
- `TestDial/an_unknown_scheme_yields_a_connection_with_no_socket` — the scheme
  switch in `dial` has no default branch. Unreachable in production because
  `ValidateWebsocketUrl` gates it; only a permissive fake exposes it.

`RequestToPlainHTTPMsg` is the one function below 100% coverage.

## License

MIT
