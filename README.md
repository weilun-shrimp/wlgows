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

**Correctness first.** Unlike many packages that chase benchmark numbers,
correctness is our first priority. We always go for the fastest code, but only
when it stays correct and safe: no `unsafe`, no shortcuts past bounds checks.
Your own code may go further, if you know what you are doing — see
[Faster and less memory](./SENDING_README.md#faster-and-less-memory-sending-a-string-with-unsafe).

## Features

- **WebSocket Server** — raw TCP with HTTP handshake
- **WebSocket Client** — `ws://` and `wss://`
- **TLS/SSL** — both sides
- **HTTP Hijacking** — `http.Server` or Gin
- **Listener** — a read loop that validates against RFC 6455 and routes frames to your hooks
- **Frame-level control** — build and send your own frames when you need to
- **Streaming** — send a message larger than memory, fragment by fragment
- **Memory in your hands** — hand each connection its own write buffer, from 14 bytes up, and swap it at any time; pick the frame size on every send — see [SENDING_README.md](./SENDING_README.md#you-are-in-control)
- **Keepalive** — ping on an interval, with a payload you choose per ping
- **Concurrent sending** — lock-guarded, and control frames are never stuck behind a long message

## Contents

- [Installation](#installation)
- [Quick Start](#quick-start) — [Server](#server) · [Client](#client) · [TLS](#tls-wss) · [HTTP Hijacking](#http-hijacking)
- [Examples](#examples) — three echo servers, a client, and a streaming pair
- [Coming from v6](#coming-from-v6)
- [Design](#design)
- [Memory vs. speed](#memory-vs-speed-sizing-the-buffers-yourself) — [the reader](#the-reader) · [the writer](#the-writer) · [the smallest connection](#the-smallest-connection)
- [Benchmark comparison](#benchmark-comparison) — against gorilla/websocket and gobwas/ws
- [Reading](#reading) — `Listener`, or one frame at a time
- [Sending](#sending) — whole messages, control frames, streaming, raw frames
- [Keepalive](#keepalive) — pinging on an interval, and noticing silence
- [Concurrency](#concurrency) — which lock guards what
- [Errors](#errors) — sentinels and `StandardClosePayloadFor`
- [Testing](#testing) — [substituting a dependency](#substituting-a-dependency)
- [License](#license)

The `Listener` has a guide of its own: **[LISTENER_README.md](./LISTENER_README.md)**,
and so does sending: **[SENDING_README.md](./SENDING_README.md)**.

## Installation

```bash
go get github.com/weilun-shrimp/wlgows/v7
```

The import path carries the `/v7` suffix Go requires for major version 2 and
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

	"github.com/weilun-shrimp/wlgows/v7"
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
	conn, _, err := wlgows.ServerHandShake(netConn, r, bufio.NewWriter(netConn), req) // 4096 bytes/conn
	if err != nil {
		return
	}

	// Ping, pong, close and reserved opcodes already answered, masking settled
	// from which side this connection is.
	listener := conn.NewStandardListener()

	// SetConfig replaces all of it, so start from what the standard hooks left.
	config := listener.GetConfig()
	config.MaxDataFramesSize = 10 * 1024 * 1024
	config.Text = func(frames wlgows.Frames) {
		conn.SendText(frames.Bytes(), 0) // echo
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

	"github.com/weilun-shrimp/wlgows/v7"
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
	conn, _, err := wlgows.ClientHandShake(netConn, r, bufio.NewWriter(netConn), req) // 4096 bytes/conn
	if err != nil {
		panic(err)
	}

	listener := conn.NewStandardListener()

	config := listener.GetConfig() // what the standard hooks left
	config.MaxDataFramesSize = 10 * 1024 * 1024
	config.Text = func(frames wlgows.Frames) {
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

Masking needs no attention on either side: `ClientHandShake` and
`ServerHandShake` set `maskSendFrame` when they build the returned `Conn`, so
`SendText` and `SendPong` mask on the client and the server's do not.

Both handshakes take a `*bufio.Writer` next to the reader: every frame is sent
through it. See [Memory vs. speed](#memory-vs-speed-sizing-the-buffers-yourself)
to size your own.

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
	hijacker, ok := w.(http.Hijacker) // under Gin, c.Writer is one
	if !ok {
		return
	}
	netConn, bufRW, err := hijacker.Hijack()
	if err != nil {
		return
	}
	defer netConn.Close()

	// r is already parsed by net/http, so no read step is needed here. Pass
	// bufRW.Reader: net/http may have buffered the start of the first frame.
	// bufRW.Writer reuses net/http's writer; bufio.NewWriterSize(netConn, size) works too.
	conn, _, err := wlgows.ServerHandShake(netConn, bufRW.Reader, bufRW.Writer, r)
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
| [`hijack_http`](./example/hijack_http/main.go) | The same server behind `net/http`, via `http.Hijacker` |
| [`hijack_gin`](./example/hijack_gin/main.go) | The same server behind Gin, via `c.Writer.Hijack()`. `GET /ping` keeps answering JSON alongside the WebSocket |
| [`client`](./example/client/main.go) | Interactive client. Type a line to send it, `exit` to close cleanly. Prompts for a CA path, so it speaks `wss://` too |
| [`stream_server`](./example/stream_server/main.go) | Receives a streamed message frame by frame, straight to disk — the `Data` hook, for a message too large to hold |
| [`stream_client`](./example/stream_client/main.go) | Streams a file as one binary message, a chunk at a time |

The examples are their own module, so they run from `example/`:

```bash
cd example
go run ./echo      # or hijack_http, or hijack_gin
go run ./client    # in another terminal, then type
```

The streaming pair is its own demo — it prints a SHA-256 at each end, and they
match:

```bash
cd example
go run ./stream_server
go run ./stream_client   # in another terminal, enter twice for README.md
```

Both hijack servers prompt for a certificate and key at startup — press enter
twice for plain `ws://`, or give paths to serve `wss://`.

Every one of them uses a [`Listener`](./LISTENER_README.md), so they answer
pings, echo the close handshake, and reject frames RFC 6455 forbids without any
of that appearing in the example. What is left in each file is the part you
would write yourself: the hooks.

## Coming from v6

v7 reads a frame header straight out of your `bufio.Reader`, so reading a frame
allocates only the `Frame` and its payload, and renames the Listener's limits
after what they count. `Conn` code is unchanged. Direct callers of the frame
reader and code that names the renamed items see a compile error:

| v6 | v7 |
|---|---|
| `go get .../wlgows/v6` | `go get github.com/weilun-shrimp/wlgows/v7` |
| `wlgows.GetFrameFromReader(netConn, max)` | `wlgows.GetFrameFromReader(r, max)` with `r := bufio.NewReader(netConn)`. Keep one `r` per connection: it holds bytes already read. |
| `wlgows.ReadFromReader(r, length)` | gone: `buffer := make([]byte, length)`, then `io.ReadFull(r, buffer)` |
| `ListenerConfig{MaxMsgFrameCount: n}` | `ListenerConfig{MaxDataFrameCount: n}` |
| `ListenerConfig{MaxMsgPayloadByteLen: n}` | `ListenerConfig{MaxDataFramesSize: n}` |
| `wlgows.ErrMsgFrameCountExceeded` | `wlgows.ErrDataFrameCountExceeded` |

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
- **`TransmitData` sends an empty chunk as an empty frame.** In v6 it skipped
  it. Check the length first, as the streaming example does, if you do not want
  that frame.

## Design

**Single package.** Everything is in the root `wlgows` package:

```go
import "github.com/weilun-shrimp/wlgows/v7"

netConn, req, _ := wlgows.Dial(url, nil)
s, _             := wlgows.Run(":8001")
```

**`Conn` must be built by a constructor.** It carries unexported dependency
fields, so a hand-written struct literal panics on first use. `ClientHandShake`
and `ServerHandShake` already do the right thing — connect (or accept, or
hijack) with `Dial`/`Server.Accept`/`http.Hijacker`, then run one of those two
funcs to get a ready `Conn` back; only building one directly needs care:

```go
c, err := wlgows.NewConn(netConn, r, bufio.NewWriter(netConn), false) // false: a server does not mask (5.1)
```

The error comes only from a writer under 14 bytes: it is flushed before it is
replaced, and that flush can fail.

That last argument is RFC 6455 5.1 and follows from which side you are: a client
masks every frame it sends, a server masks none, and a peer fails the connection
on the wrong one. It is settled once, at construction, so no send call can pass
it wrong. `ClientHandShake` and `ServerHandShake` fill it in.

`Server`'s exported fields (`TCPAddr`, `TCPListener`) are readable and
settable.

## Memory vs. speed: sizing the buffers yourself

A `Conn` keeps two buffers: the `*bufio.Reader` and the `*bufio.Writer` you
hand it. Both are fixed, and both trade memory per
connection for fewer syscalls.

### The reader

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

### The writer

`bufio.NewWriter(netConn)` is 4096 bytes too. Size it the same way. The floor
is 14, the longest frame header: a smaller writer is flushed and replaced by a
14 byte one.

```go
w := bufio.NewWriterSize(netConn, 14) // 14 bytes a connection
conn, _, err := wlgows.ServerHandShake(netConn, r, w, req)
```

The trade is the same, from the other side: small frames that fit share one
socket write. On a server a large payload skips the buffer, so 14 bytes costs
little; a client masks through it, so a client sending large messages wants a
large one. `conn.RenewWriter(w)` swaps it at any time. Measured
write counts are in [Memory: the write buffer](./SENDING_README.md#memory-the-write-buffer).

### The smallest connection

Measured per idle connection: a `Conn`, its `NewStandardListener`, and the
goroutine running `Listen`. The `net.Conn` and the OS socket are not counted.

| | Per idle connection | 100,000 connections |
|---|---|---|
| Reader 4096, writer 4096 | ~14 KB | ~1.4 GB |
| Reader 16, writer 14 | ~5–6 KB | ~550 MB |
| Reader 16, writer 14, plus `StartPingLoop` | ~9 KB | ~900 MB |

At the smallest sizes the buffers are 30 bytes. Most of what is left is
goroutine stacks: about 4 KB for `Listen`, and about 3 KB more for each
`StartPingLoop`. wlgows cannot shrink those. What you can do:

- **Swap to a small writer while the connection is quiet.** If you know it will
  send nothing for a while, `conn.RenewWriter(bufio.NewWriterSize(conn, 14))`
  gives the big buffer back, and a bigger writer before the next burst takes it
  again. Each swap flushes and allocates the new writer, so do it when the
  quiet starts, not between messages. After a hijack, from Go 1.25 on,
  net/http keeps `bufRW.Writer` until the handler returns, so run the WebSocket
  on its own goroutine and return from the handler; then the swap frees it.
  That goroutine must not keep the `http.ResponseWriter`, which holds it too.
- **Ping from one goroutine, not one per connection.** `SendPing` is safe from
  any goroutine, so one loop of your own can ping every connection. A write to a
  stalled peer blocks that loop, so give each connection a write deadline.
- **Skip the copy of each message.** With `unsafe`, a message is held once
  instead of twice — see [sending](./SENDING_README.md#faster-and-less-memory-sending-a-string-with-unsafe)
  and [reading](./LISTENER_README.md#reading-text-without-a-copy-unsafe).
- **Stream a large message.** The `Data` hook holds one frame at a time instead
  of the whole message — see [LISTENER_README.md](./LISTENER_README.md#hooks).

## Benchmark comparison

Against [gorilla/websocket](https://github.com/gorilla/websocket) v1.5.3 and
[gobwas/ws](https://github.com/gobwas/ws) v1.4.0, with the same cases and a 4096
byte reader and writer on each:

| Area | vs gorilla | vs gobwas |
|---|---|---|
| Server send | **Win** at every size | **Win** on batched frames, tie on single ones |
| Send memory | **Win**: 0 allocs, always | **Win**: 0 allocs vs 1 per frame |
| Client send | Lose on small frames | Lose, on purpose for safety |
| Read | **Win**: 1.5x to 3.4x faster | **Win** on small server frames, tie on single client frames, lose on the rest |

The client send losses are on purpose. The masking key comes from
`crypto/rand`, never `math/rand`, and your payload is only read, never masked in
place. Both are measured faster paths turned down for
[Correctness first](#wlgows).

Environment, method, every number, and why each loss stays:
**[BENCHMARK_COMPARISON.md](./BENCHMARK_COMPARISON.md)**.

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
config.MaxDataFramesSize = 10 * 1024 * 1024
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

**Hint: `unsafe` skips the copy.** `frames.String()` copies the message. For a
message that arrived in one frame, `unsafe` can make the string with no copy:
about 2 ns instead of 60 µs on 1 MB, and half the memory. Only if you know what
you are doing — see
[Reading text without a copy](./LISTENER_README.md#reading-text-without-a-copy-unsafe).

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

Sending has a guide of its own: **[SENDING_README.md](./SENDING_README.md)** —
which call to use, streaming, managing memory, failures and locks.

| You have | Call |
|---|---|
| A message in memory | `SendText(text, chunkSize)`, `SendBinary(data, chunkSize)`, `SendData(opcode, payload, chunkSize)` |
| A message too large to hold | `StartLongDataTransmission` → `TransmitData` → `EndLongDataTransmission`, with `ReleaseLongDataTransmission` deferred |
| A close, ping or pong | `SendClose(payload)`, `SendPing(payloadData)`, `SendPong(payloadData)` |
| A frame you built yourself | `SendFrame(frame)` |

```go
conn.SendText([]byte("hello"), 0)  // one frame
conn.SendBinary(data, 4*1024)      // frames of 4 KB payload each
```

`chunkSize` is the payload of each frame. 0 or less sends one frame.

**You are in control, so memory is yours to manage.** Most packages fix a frame
size when the connection is set up; wlgows lets every send choose its own, and
every connection its own write buffer. The buffer is the `bufio.Writer` you
passed, fixed at its size — it never grows with what you send — and
`conn.RenewWriter(w)` swaps it at any time. See
[Memory: the write buffer](./SENDING_README.md#memory-the-write-buffer).

**Your payload is only read.** A client masks a copy, never your slice, so
sending the same bytes twice, or bytes you keep using, is safe.

**Hint: `unsafe` skips the copy.** `[]byte(text)` copies the string. Because
wlgows only reads your payload, `unsafe` can send a string with no copy: about
1 MB and 60–90 µs saved on a 1 MB message. Only if you know what you are doing —
see [Faster and less memory](./SENDING_README.md#faster-and-less-memory-sending-a-string-with-unsafe).

**No data after a close.** Once a close has gone out, data sends and a second
close return `ErrCloseAlreadySent` (5.5.1). A ping or pong still goes out
(5.5.2).

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
| `writeLocker` | the write buffer, one frame at a time | every `Send*`, for the length of one frame; `RenewWriter` |
| `dataFramesWriteLocker` | one data message at a time | `SendText`, `SendBinary`, `SendData`, and `Start`…`Release` |
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
if _, _, err := wlgows.ServerHandShake(netConn, r, w, req); errors.Is(err, wlgows.ErrHttpMethodNotAllowed) {
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
go test -cover .                           # 98.3% of statements
go test -bench BenchmarkFramesAssembly .   # benchmarks, which plain `go test` skips
```

One `_test.go` per source file, all in package `wlgows` so the `di` seams are
reachable. Two files have no source counterpart:

| File | Contents |
|---|---|
| [`Fakes_test.go`](./Fakes_test.go) | Shared doubles — `fakeConn` (in-memory `net.Conn`), `fakeIOWriter`/`fakeIOReader` (plain `io.Writer`/`io.Reader` doubles), `fakeFuncLocker` (runs a func on `Lock` and `Unlock`, so a test records them among its other steps), `fixedRandRead`, `fakeGetFrameFromReaderBufioReader` (scripts `Peek` and `Discard` for the frame reader) |
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
(`MaskPayload`, `Frames.String`, `ValidateHandShakeRequest`, …) have no seam and
are tested directly.

```go
// package-level func: pass a di struct
netConn, req, err := dial("ws://localhost:8001", nil, dialDI{...})

// method: overwrite the field the constructor set
conn, _ := NewConn(netConn, bufio.NewReader(netConn), bufio.NewWriter(netConn), false)
conn.di.newDataFrame = func(config NewFrameConfig) (*Frame, error) { ... }
```

Fixing the random source is what makes masked output assertable — a masked
frame's bytes cannot be pinned otherwise:

```go
f := NewFrame(NewFrameConfig{PayloadData: []byte("hi"), Opcode: 1, FIN: true})
prepareSendFrame(f, true, prepareSendFrameDI{
	fillMaskingKey: func(key *[4]byte) error { *key = [4]byte{1, 2, 3, 4}; return nil },
})
// f.appendSealedHeader(nil) == []byte{0x81, 0x82, 1, 2, 3, 4}
```

One test deliberately asserts current behaviour rather than correct behaviour,
and says so in its name and comments:

- `TestDial/an_unknown_scheme_yields_a_nil_conn_and_no_error` — the scheme
  switch in `dial` has no default branch. Unreachable in production because
  `ValidateWebsocketUrl` gates it; only a permissive fake exposes it.

The gaps left in coverage are the thin `Conn` wrappers that only bind fields
into a `di` struct (`SendText`, `SendData`, `TransmitData`, `RenewWriter`, …),
whose logic is covered through the pure function each wraps, and the line where
`ClientHandShake` returns its `Conn`. Its `NewConn` error is tested over a
`net.Pipe`, with a goroutine answering as the server, since the real client
picks a fresh `crypto/rand` key on every call.

## License

MIT
