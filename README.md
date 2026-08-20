**English** · [繁體中文](./README.zh-TW.md)

# WLGOWS

A simple, intuitive and powerful WebSocket library for Go — quick to start, and
honest about the protocol underneath.

Paste the Quick Start below and you have a working echo server: one constructor,
one hook, and `conn.NewStandardListener()` answering ping, pong and close for
you. When you need more, it is all still there — stream a message larger than
memory, take every data frame yourself, ping on your own schedule, or build and
send a frame by hand. Nothing is hidden, because it works in frames rather than
abstractions: server and client, manual handshake, and every rule RFC 6455 puts
on you kept where you can see it.

## Features

- **WebSocket Server** — raw TCP with HTTP handshake
- **WebSocket Client** — `ws://` and `wss://`
- **TLS/SSL** — both sides
- **HTTP Hijacking** — `http.Server` or Gin
- **Listener** — a read loop that validates against RFC 6455 and routes frames to your hooks
- **Frame-level control** — build and send your own frames when you need to
- **Streaming** — send a message larger than memory, fragment by fragment
- **Keepalive** — ping on an interval, with a payload you choose per ping
- **Concurrent sending** — lock-guarded, and control frames are never stuck behind a long message

## Contents

- [Quick Start](#quick-start) — [Server](#server) · [Client](#client) · [TLS](#tls-wss) · [HTTP Hijacking](#http-hijacking)
- [Examples](#examples) — three echo servers, a client, and a streaming pair
- [Coming from v3](#coming-from-v3)
- [Design](#design)
- [Memory vs. speed](#memory-vs-speed-sizing-the-reader-yourself) — sizing the reader yourself
- [Reading](#reading) — `Listener`, or one frame at a time
- [Sending](#sending) — whole messages, control frames, streaming, raw frames
- [Keepalive](#keepalive) — pinging on an interval, and noticing silence
- [Concurrency](#concurrency) — which lock guards what
- [Errors](#errors) — sentinels and `StandardClosePayloadFor`
- [Testing](#testing)

The `Listener` has a guide of its own: **[LISTENER_README.md](./LISTENER_README.md)**.

## Installation

```bash
go get github.com/weilun-shrimp/wlgows/v4
```

The import path carries the `/v4` suffix Go requires for major version 2 and
above; the package name is still `wlgows`, so call sites read `wlgows.Dial(...)`.

## Quick Start

### Server

```go
package main

import (
	"bufio"
	"log"
	"net"
	"net/http"
	"time"

	"github.com/weilun-shrimp/wlgows/v4"
)

func main() {
	server, err := wlgows.Run(":8001")
	if err != nil {
		panic(err)
	}
	defer server.Close()

	for {
		netConn, err := server.Accept()
		if err != nil {
			continue
		}
		go handle(netConn)
	}
}

func handle(netConn net.Conn) {
	defer netConn.Close()

	// Accept only takes the TCP connection — reading the request and running
	// the handshake are yours, so nothing hides which bytes reach the wire.
	r := bufio.NewReader(netConn) // 4096 bytes/conn; see Memory vs. speed to shrink it
	req, err := http.ReadRequest(r)
	if err != nil {
		return
	}
	conn, _, err := wlgows.ServerHandShake(netConn, r, req)
	if err != nil {
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

	// Heartbeat, on a goroutine of its own. It ends itself when the connection
	// does — see Keepalive for noticing a peer that never pongs back.
	go conn.StartPingLoop(30*time.Second, nil)

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
	"bufio"
	"log"
	"time"

	"github.com/weilun-shrimp/wlgows/v4"
)

func main() {
	// Dial only connects and builds the request — running the handshake is
	// yours, so you can still add headers to req before it goes out.
	netConn, req, err := wlgows.Dial("ws://localhost:8001", nil)
	if err != nil {
		panic(err)
	}
	defer netConn.Close()

	r := bufio.NewReader(netConn) // 4096 bytes/conn; see Memory vs. speed to shrink it
	conn, _, err := wlgows.ClientHandShake(netConn, r, req)
	if err != nil {
		panic(err)
	}

	listener := conn.NewStandardListener()

	config := listener.GetConfig() // what the standard hooks left
	config.MaxMsgPayloadByteLen = 10 * 1024 * 1024
	config.Text = func(frames wlgows.Frames) {
		log.Println("received:", frames.String())
	}
	listener.SetConfig(config)

	go conn.StartPingLoop(30*time.Second, nil) // heartbeat; ends itself

	conn.SendText([]byte("Hello, WebSocket!"))

	if err := listener.Listen(); err != nil {
		if payload := wlgows.StandardClosePayloadFor(err); payload != nil {
			conn.SendClose(payload)
		}
		log.Println("closing:", err)
	}
}
```

Masking needs no attention on either side: `ClientHandShake` and
`ServerHandShake` set `maskSendFrame` when they build the returned `Conn`, so
`SendText` and `SendPong` mask on the client and the server's do not.

### TLS (wss://)

```go
caCert, _ := os.ReadFile("ca.crt")
caCertPool := x509.NewCertPool()
caCertPool.AppendCertsFromPEM(caCert)

netConn, req, err := wlgows.Dial("wss://localhost:8001", &tls.Config{RootCAs: caCertPool})
```

### HTTP Hijacking

```go
func handler(w http.ResponseWriter, r *http.Request) {
	netConn, bufReader, err := wlgows.HijackFromHttp(w) // or HijackFromGin(c)
	if err != nil {
		return
	}
	defer netConn.Close()

	// r is already parsed by net/http, so no read step is needed here.
	conn, _, err := wlgows.ServerHandShake(netConn, bufReader, r)
	if err != nil {
		return
	}
	// ... read and send
}
```

## Examples

Six. Three servers are the same echo program reached three different ways, so
the interactive client drives any of them, and the streaming pair drives itself.

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

## Coming from v3

`ClientConn` and `ServerConn` are gone. There is one `Conn`, for both sides,
and it carries no handshake state — the handshake is a function call, not a
stored `ClientRequest`/`ServerResponse`.

| v3 | v4 |
|---|---|
| `conn, err := wlgows.Dial(url, tls)` then `conn.HandShake()` | `netConn, req, err := wlgows.Dial(url, tls)` then `conn, res, err := wlgows.ClientHandShake(netConn, r, req)` |
| `conn, err := server.Accept()` then `conn.HandShake()` | `netConn, err := server.Accept()`, read the request yourself, then `conn, res, err := wlgows.ServerHandShake(netConn, r, req)` |
| `sc, err := wlgows.HijackFromHttp(w, r)` then `sc.HandShake()` | `netConn, r, err := wlgows.HijackFromHttp(w)` then `conn, res, err := wlgows.ServerHandShake(netConn, r, req)` |
| `wlgows.NewClientConn(c, req)` / `wlgows.NewServerConn(c, req)` | gone — build a `Conn` via a handshake func above, or `wlgows.NewConn(c, r, maskSendFrame)` directly |
| `conn.ClientRequest` / `conn.ServerResponse` | gone — the handshake funcs return the response directly instead of storing it |

The point of the split is the `*bufio.Reader`: `http.ReadRequest` and
`http.ReadResponse` can buffer bytes past the header block — the start of the
first frame — so the handshake and the `Conn` that reads frames afterward
must share the exact same reader. `ClientHandShake`/`ServerHandShake` take it
as a parameter and build the returned `Conn` from it, so this can no longer be
gotten wrong by accident. `Dial`/`Server.Accept`/`HijackFromHttp` stay thin —
connect (or accept, or hijack) and hand back the raw materials — so you still
decide when the handshake actually runs, and can still add headers to a
request before it goes out.

## Design

**Single package.** Everything is in the root `wlgows` package:

```go
import "github.com/weilun-shrimp/wlgows/v4"

netConn, req, _ := wlgows.Dial(url, nil)
s, _             := wlgows.Run(":8001")
hjConn, r, _     := wlgows.HijackFromHttp(w)
```

**`Conn` must be built by a constructor.** It carries unexported dependency
fields, so a hand-written struct literal panics on first use. `ClientHandShake`
and `ServerHandShake` already do the right thing — connect (or accept, or
hijack) with `Dial`/`Server.Accept`/`HijackFromHttp`, then run one of those two
funcs to get a ready `Conn` back; only building one directly needs care:

```go
c := wlgows.NewConn(netConn, r, false) // false: a server does not mask (5.1)
```

That last argument is RFC 6455 5.1 and follows from which side you are: a client
masks every frame it sends, a server masks none, and a peer fails the connection
on the wrong one. It is settled once, at construction, so no send call can pass
it wrong. `ClientHandShake` and `ServerHandShake` fill it in.

`Server`'s exported fields (`TCPAddr`, `TCPListener`) are readable and
settable.

## Memory vs. speed: sizing the reader yourself

`bufio.NewReader(netConn)` — what every example above uses — defaults to a
4096 byte buffer per connection. `ClientHandShake`/`ServerHandShake`/`NewConn`
don't care about that size; they just use whatever `*bufio.Reader` you hand
them. If you'd rather trade a little speed for far less memory per
connection, size it yourself:

```go
r := bufio.NewReaderSize(netConn, 16) // 16 is the floor — bufio clamps anything under it up to 16
```

16 is not an arbitrary minimum here: a frame's header (2 bytes) plus the
largest extended length (8) plus a mask key (4) is 14 bytes, just under it —
so the whole non-payload part of a frame still fills in one read either way.
What a bigger buffer buys you beyond that is small payloads riding along in
the same read; past 16 bytes, a large payload needs its own read regardless
of buffer size, so the gap narrows the bigger the message.

The memory side scales with however many connections are open at once:

| | 16 bytes | 4096 bytes (default) |
|---|---|---|
| Per connection | 16 B | 4096 B |
| 1,000 connections | ~16 KB | ~4 MB |
| 100,000 connections | ~1.6 MB | ~400 MB |

And the read-count side scales with frame size and how many arrive back to back:

| | 16 bytes | 4096 bytes (default) |
|---|---|---|
| One 10 B frame | 1 read | 1 read |
| 100 back-to-back 10 B frames (1000 B total) | ~63 reads | as few as 1 read |
| One 1 MB frame | payload bypasses the buffer — same read count either way | payload bypasses the buffer — same read count either way |

(the 100-frame row assumes the bytes have already arrived when `Read` is
called, the normal case for a steady stream — a peer trickling data in slowly
narrows the gap.)

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

## Keepalive

A dead peer looks exactly like a quiet one. RFC 6455 5.5.2 makes a ping the
question and a pong the answer, so liveness is two halves: something asking on
an interval, and something judging the replies.

`StartPingLoop` is the asking half. It blocks, so the goroutine is yours:

```go
go conn.StartPingLoop(30*time.Second, nil)   // nil: an empty ping
```

It ends itself — on a ping that cannot be sent, and once a close has gone out
from either side of the handshake. Nothing retries, so a failed ping is the last
one, and there is no handle to remember.

`payload` is called for each ping, not once. 5.5.2 has the peer echo those bytes
back verbatim, so varying them is what lets you tell which ping came back:

```go
var seq atomic.Uint64
go conn.StartPingLoop(30*time.Second, func() []byte {
	return []byte(fmt.Sprintf("ping-%d", seq.Add(1)))
})
```

The pong carries that same `ping-4` back, so a `Pong` hook can see which one it
answers. Bytes are bytes to the protocol — a number, a timestamp, anything you
can recognise on the way back.

**Judging the replies is yours**, and it has to be. Stamp a time in your `Pong`
hook, compare it against a deadline of your own, and close when it passes:

```go
config.Pong = func(*wlgows.Frame) { lastPong.Store(time.Now()) }
```

Ignore the payload when stamping. 5.5.3 permits unsolicited pongs and 5.5.2
permits answering only the most recent of several outstanding pings, so a pong
that matches nothing you sent is still proof the peer is alive.

`Loop` underneath it is exported and takes any work at all — it calls a func
on an interval until that func signals stop:

```go
wlgows.Loop(func(stop chan<- struct{}) {
	if done() {
		stop <- struct{}{}   // send once; checked the moment this returns
		return
	}
	work()
}, time.Second)
```

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
if _, _, err := wlgows.ServerHandShake(netConn, r, req); errors.Is(err, wlgows.ErrHttpMethodNotAllowed) {
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

`nil` is the absence of an attribution, not an instruction to close. An error of
your own reaches here the same way — `PauseListen(err)` ends a run and `Listen`
returns it, joined with the socket's own error when closing is what ended the
read — and gets `nil` too, since this package cannot answer for a rule it does
not know. Map yours before calling. See the Errors section of
[LISTENER_README.md](./LISTENER_README.md).

## Testing

```bash
go test ./...                              # full suite
go test -race ./...                        # the integration tests spawn goroutines
go test -cover .                           # 99.7% of statements
go test -bench BenchmarkFramesAssembly .   # benchmarks, which plain `go test` skips
```

One `_test.go` per source file, all in package `wlgows` so the `di` seams are
reachable. Two files have no source counterpart:

| File | Contents |
|---|---|
| [`Fakes_test.go`](./Fakes_test.go) | Shared doubles — `fakeConn` (in-memory `net.Conn`), `fakeIOWriter`/`fakeIOReader` (plain `io.Writer`/`io.Reader` doubles), `fakeLocker` (counts and catches misuse), `fixedRandRead`, `scriptedReadFromReader` |
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
netConn, req, err := dial("ws://localhost:8001", nil, dialDI{...})

// method: overwrite the field the constructor set
conn := NewConn(netConn, bufio.NewReader(netConn), false)
conn.di.newDataFrame = func(config NewFrameConfig) (*Frame, error) { ... }
```

Fixing the random source is what makes masked output assertable — a masked
frame's bytes cannot be pinned otherwise:

```go
f, _ := newFrame(NewFrameConfig{PayloadData: []byte("hi"), Opcode: 1, Mask: true, FIN: true},
	newFrameDI{generateMaskingKey: func() ([]byte, error) { return []byte{1, 2, 3, 4}, nil }})
// f.Seal() == []byte{0x81, 0x82, 1, 2, 3, 4, 'h'^1, 'i'^2}
```

One test deliberately asserts current behaviour rather than correct behaviour,
and says so in its name and comments:

- `TestDial/an_unknown_scheme_yields_a_nil_conn_and_no_error` — the scheme
  switch in `dial` has no default branch. Unreachable in production because
  `ValidateWebsocketUrl` gates it; only a permissive fake exposes it.

`ClientHandShake`'s own success path is the main gap left in coverage: the
real client picks a fresh `crypto/rand` key on every call, so a canned wire
response can't be made to match it without a live two-ended connection —
`clientHandShake` (the DI-injected step it wraps) and `NewConn` (what it
builds on success) are both separately covered at 100%.

## License

MIT
