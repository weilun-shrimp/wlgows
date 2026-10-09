**English** · [繁體中文](./SENDING_README.zh-TW.md)

# Sending

A `Conn` sends whole messages, streams of chunks, control frames, and frames you
build yourself.

## You are in control

Most WebSocket packages fix a frame size or a buffer size when the connection is
set up, and every send lives with it.

wlgows hands you both, and they are separate knobs:

- **The write buffer** is yours, per connection: the `*bufio.Writer` you hand
  the `Conn` when it is built, and `RenewWriter` to swap it at any time. It is
  the only memory a `Conn` keeps for sending, and it never grows past what you
  gave it. See [Memory: the write buffer](#memory-the-write-buffer).
- **The frame size** is yours on every send: `chunkSize`. It decides where a
  message is cut into frames, never how much memory sending takes. See
  [Choosing a chunk size](#choosing-a-chunk-size).

Full power, and full responsibility: the trade between memory and speed is the
size you pick. The same control lets a server's sending memory go as low as 14
bytes a connection, for a price in writes. See
[The lowest memory setup](#the-lowest-memory-setup).

## Contents

- [You are in control](#you-are-in-control)
- [Choosing a call](#choosing-a-call)
- [Whole messages](#whole-messages) — `SendText`, `SendBinary`, `SendData`
- [Streaming](#streaming) — `Start`, `TransmitData`, `End`, `Release`
- [Memory: the write buffer](#memory-the-write-buffer) — [changing it: `RenewWriter`](#changing-it-renewwriter) · [the lowest memory setup](#the-lowest-memory-setup)
- [Choosing a chunk size](#choosing-a-chunk-size)
- [Faster and less memory: sending a string with `unsafe`](#faster-and-less-memory-sending-a-string-with-unsafe) — only if you know what you are doing
- [When a send fails](#when-a-send-fails)
- [Concurrency](#concurrency)
- [Control frames and your own frames](#control-frames-and-your-own-frames)

The rest of the library is in the [main README](./README.md).

## Choosing a call

| You have | Call |
|---|---|
| A text message in memory | `SendText(text, chunkSize)` |
| A binary message in memory | `SendBinary(data, chunkSize)` |
| A message whose opcode you pick at run time, like an echo | `SendData(opcode, payload, chunkSize)` |
| A message too large to hold in memory | `StartLongDataTransmission` → `TransmitData` → `EndLongDataTransmission`, with `ReleaseLongDataTransmission` deferred |
| A close, ping or pong | `SendClose(payload)`, `SendPing(payloadData)`, `SendPong(payloadData)` |
| A frame you built yourself | `SendFrame(frame)` |

You never set masking. A client masks every frame and a server masks none
(RFC 6455 5.1), and the `Conn` already knows which side it is.

## Whole messages

```go
conn.SendText([]byte("hello"), 0)                  // one frame
conn.SendBinary(data, 4*1024)                      // frames of 4 KB
conn.SendData(wlgows.OpcodeText, []byte("abc"), 0) // like SendText, without the UTF-8 check
conn.SendData(frames[0].Opcode, frames.Bytes(), 0) // echo back what arrived
```

`chunkSize` is the payload of each frame, in bytes:

- **0 or less** sends the message as one frame.
- **A positive size** splits it into frames of that many payload bytes. Only the
  last frame can be shorter, and only the last frame carries FIN.
- The frame header (2 to 14 bytes) is not counted. So the same `chunkSize` means
  the same thing on a client and a server.
- An empty message goes out as one empty frame. RFC 6455 allows it.

`SendText` checks the whole message is valid UTF-8 before it sends anything, and
refuses it with `ErrInvalidUTF8` if not. A peer would close the connection over
invalid text (8.1). The check is on the whole message, so a chunk boundary in the
middle of a character is fine. `SendBinary` and `SendData` check nothing.

The frames of one message go through the write buffer together, and it is
flushed once at the end, so many small frames share one socket write. The call
returns once the last frame has been written.

**Your bytes are only read.** A client masks a copy, in the write buffer, never
your slice. So sending the same slice twice, sending bytes you keep using, or
sending a `[]byte` converted from a string is safe, on either side.

That promise is also what lets you send a string with no copy at all, through
`unsafe`. Only if you know what you are doing: see
[Faster and less memory](#faster-and-less-memory-sending-a-string-with-unsafe).

## Streaming

For a message too large to hold, send it chunk by chunk. Each `TransmitData`
call sends one frame.

```go
if err := conn.StartLongDataTransmission(wlgows.OpcodeBinary); err != nil {
	return err
}
defer conn.ReleaseLongDataTransmission()

buffer := make([]byte, 4*1024)
for {
	length, err := file.Read(buffer)
	if length > 0 {
		if err := conn.TransmitData(buffer[:length]); err != nil {
			return err // no FIN: the message is left unterminated
		}
	}
	if err == io.EOF {
		return conn.EndLongDataTransmission(nil)
	}
	if err != nil {
		return err
	}
}
```

The four calls:

| Call | What it does |
|---|---|
| `StartLongDataTransmission(opcode)` | Opens a text or binary message and takes the connection for it. |
| `TransmitData(chunk)` | Sends one chunk as one frame. An empty chunk is sent as an empty frame. |
| `EndLongDataTransmission(last)` | Sends `last` as the final frame, with FIN. |
| `ReleaseLongDataTransmission()` | Frees the connection. Sends nothing. |

The rules:

- **Start and Release pair like `Lock` and `Unlock`.** Defer Release right after
  Start returns nil. Never call Release without a Start, or twice: like
  `sync.Mutex.Unlock`, that is a fatal error.
- **Call End once.** After End, only Release is left. This is true even when End
  fails.
- **End sends a frame.** `End(nil)` sends an empty last frame, so four chunks go
  out as five frames. If you know which chunk is last, pass it to End instead and
  it carries FIN itself. If nothing was sent before End, its data is the whole
  message, even when it is empty.
- **Your buffer is yours again** as soon as `TransmitData` returns: the frame is
  already flushed to the socket. Nothing is held between calls, so reading into
  the same buffer is safe.
- **Text must be valid UTF-8.** Only `SendText` checks it. A text stream is yours
  to keep valid.
- **One goroutine.** Make all four calls from the goroutine that called Start.

[`stream_client`](./example/stream_client/main.go) streams a file this way, and
[`stream_server`](./example/stream_server/main.go) receives it frame by frame.

## Memory: the write buffer

Every frame a `Conn` sends goes through one write buffer: the `*bufio.Writer`
you hand it when it is built. Its size is the buffer's size:

```go
serverConn, _, err := wlgows.ServerHandShake(netConn, r, bufio.NewWriterSize(netConn, 16*1024), req) // 16 KB
clientConn, _, err := wlgows.ClientHandShake(netConn, r, bufio.NewWriter(netConn), req)        // bufio's 4096
```

- **It must write to the same connection.** Build it on `netConn`.
- **Under 14 bytes** it is flushed and replaced by a new 14 byte writer on the
  connection. 14 is the longest frame header (2 + 8 extended length + 4 masking
  key). If that flush fails, you get the error and no `Conn`.
- Hijacked from `net/http`? Pass `bufRW.Writer` and keep net/http's writer.

**It is fixed.** It never grows and never shrinks by itself, whatever you send.
A `Conn` holds exactly the writer's size for sending, from the first frame to
the last, so memory per connection is known before any message is sent:

| Writer size | Per connection | 100,000 connections |
|---|---|---|
| 14 | 14 B | ~1.4 MB |
| 4096 (`bufio.NewWriter`) | 4 KB | ~400 MB |
| 64 KB | 64 KB | ~6.4 GB |

A frame larger than the buffer still goes out whole, in pieces:

- **A server** writes a large payload straight from your slice. No copy, and the
  buffer size hardly matters.
- **A client** must mask what it sends, and never touches your slice, so it masks
  a copy in the buffer, one buffer at a time. Here the size is the number of
  socket writes.

What the size buys is fewer socket writes: frames that fit share one. Measured,
counting writes to the socket:

| Send | 14, server | 14, client | 4096 | 64 KB |
|---|---|---|---|---|
| One 10 B message | 1 | 2 | 1 | 1 |
| 1000 B in 10 B frames (`chunkSize` 10) | 86 | 134 | 1 | 1 |
| One 1 MB message, server | 2 | | 2 | 2 |
| One 1 MB message, client | | 87,383 | 257 | 17 |

So a server can run a small buffer cheaply: only many small frames feel it. A
client sending large messages wants a large one.

### Changing it: `RenewWriter`

```go
conn.RenewWriter(bufio.NewWriterSize(conn, 64*1024)) // a 64 KB buffer from here on
conn.RenewWriter(bufio.NewWriterSize(conn, 14))      // the smallest there is
```

It flushes what the old writer holds, then sends every frame after it through
the new one, and the old one goes to the GC. The new writer is taken as the
handshake takes it: one under 14 bytes is flushed and replaced. When to call it
is your call:

- **Before a bulk send**, on a client: a larger buffer cuts the writes of every
  large message after it.
- **Before a long quiet time**, on a server holding many connections: a small
  buffer keeps an idle connection cheap.

How to call it:

- **Any goroutine is fine.** It takes the write lock, so it waits for a frame
  being written to finish.
- **Build the new writer on the same connection.** `conn` itself works: it is a
  `net.Conn`.
- **It returns an error.** A failed flush leaves the old writer in place, and
  means the socket write failed — see [When a send fails](#when-a-send-fails).

### The lowest memory setup

Because you size it, sending can cost a server almost no memory:

```go
conn, _, err := wlgows.ServerHandShake(netConn, r, bufio.NewWriterSize(netConn, 14), req) // 14 bytes, for the life of the Conn
```

Or keep a normal buffer while busy and drop to 14 when the connection goes
quiet:

```go
conn.RenewWriter(bufio.NewWriterSize(conn, 14))
```

For a server holding 100,000 connections, that is about 1.4 MB of write buffers
instead of about 400 MB at 4096. The price is socket writes, as measured above:
about one per frame on a server, which large messages barely notice. Do not do
this on a client that sends large messages: it masks 12 bytes per write.

**After a hijack, return from the handler first.** From Go 1.25 on, net/http
keeps its own 4 KB writer, the one in `bufRW.Writer`, until the handler returns.
Run the WebSocket on a goroutine of its own and return from the handler; then a
14 byte writer frees the 4 KB. Keep the `http.ResponseWriter` out of that
goroutine: it holds the writer too. If the handler runs the WebSocket itself, a
smaller writer frees nothing, so keep `bufRW.Writer`. Before Go 1.25,
`bufRW.Writer` is a new writer net/http does not keep, so this does not apply.

## Choosing a chunk size

`chunkSize` decides where a message is cut into frames. It does not change
memory: the write buffer is fixed, and a whole message given to `SendData` is
already in memory. What it changes:

- **How soon a control frame gets out.** A ping, pong or close can only go
  between two frames of a message, so a 100 MB message sent as one frame holds
  a pong back until all of it is written. Smaller frames let it in sooner.
- **Your peer's limits.** Too large, and a frame may pass a peer's per-frame or
  per-message limit. Too small, and a large message becomes many frames, and a
  peer may cap frames per message. wlgows' own `Listener` can, with
  `MaxDataFrameCount` (no limit by default), and refuses the message with
  `ErrDataFrameCountExceeded`. Keep message size ÷ `chunkSize` under that cap.
  For example, 16 MB in 4 KB chunks is 4,096 frames, more than the 4,000 the
  [Listener guide](./LISTENER_README.md#configuration) uses as its example.

0 is fine for messages that are small next to the peer's limits. For large ones,
4 KB to 64 KB is a fair range. A stream's chunk is the buffer you read into
before `TransmitData`.

## Faster and less memory: sending a string with `unsafe`

**Only if you know exactly what you are doing.** A mistake here is not caught by
the compiler, the race detector or your tests. It shows up later as a crash, or
as a string that changes under you, far from the line that caused it. If you
are not sure, use `[]byte(text)`: the price is one copy.

wlgows itself uses no `unsafe`. But it promises it **never writes to a payload
you send**, on either side: a client masks a copy in the write buffer. So you
can hand it a string's own memory instead of a copy, as long as **your** code
never writes to it either.

```go
payload := unsafe.Slice(unsafe.StringData(text), len(text))
err := conn.SendText(payload, 0)
```

Measured on a 1 MB text message:

| | Time | Allocated |
|---|---|---|
| `conn.SendText([]byte(text), 0)` | ~80–110 µs | 1 MB |
| `conn.SendText(payload, 0)`, through `unsafe` | ~23 µs | 0.3 KB |
| `conn.SendBinary(payload, 0)`, through `unsafe` | ~1.3 µs | 0.3 KB |

The copy is what `[]byte(text)` costs: Go must copy a string to give you bytes
you could write to. Of the 23 µs left, about 20 µs is `SendText`'s UTF-8 check;
the send itself is about 1 µs.

**It is also the lowest memory.** Without `unsafe`, the 1 MB string exists twice,
the string and its copy, until the send ends. With it, once.

The rules:

- **Never write to `payload`.** It is the string's own memory. A string literal
  lives in read-only memory, so writing to it crashes the program; any other
  string silently changes everywhere it is used.
- **Never pass `payload` to anything else that might write to it**, or `append`
  to it.
- **It works with every send**: `SendText`, `SendBinary`, `SendData`,
  `TransmitData`, `EndLongDataTransmission`, `SendPing`, `SendPong` and
  `SendFrame`.

Receiving has the same trick the other way round: see
[Reading text without a copy](./LISTENER_README.md#reading-text-without-a-copy-unsafe).

## When a send fails

| Error | Meaning |
|---|---|
| `ErrCloseAlreadySent` | A close has gone out, and RFC 6455 allows no data frame and no second close after one (5.5.1). Nothing of this frame was sent. A ping or pong is never refused for this. |
| `ErrControlFramePayloadTooLong` | A close, ping or pong payload over 125 bytes (5.5). Nothing was sent. |
| `ErrInvalidUTF8` | `SendText` got invalid UTF-8. Nothing was sent. |
| `ErrNotDataFrameOpcode`, `ErrContinuationFrameWithoutMsg` | `Start` or `SendData` got an opcode that cannot open a message. Nothing was locked. |
| `ErrLongDataTransmissionNotStarted` | `TransmitData` or `End` with no transmission open. |
| Anything else | A masking key could not be drawn, or the socket write failed. |

**A failed socket write is final.** The write buffer keeps its first error, so
every send after it fails the same way, and so does `RenewWriter`. Close
the connection.

**A failure in the middle of a message leaves it unterminated.** Earlier chunks
may already have reached the peer, and RFC 6455 has no way to end a message
early. Close the connection. If you send another data message instead, the peer
sees a protocol error.

In a stream you may call End after a failure instead. The peer then receives what
was sent as a complete message. Do that only if a partial message is what you
want.

## Concurrency

- **Many goroutines can send.** Data messages go out one at a time: a message
  holds the connection from its first frame to its last.
- **Control frames still get through.** A ping, pong or close waits only for the
  frame being written, so it can go out between two chunks of a long message
  (5.4, 5.5.2). It is flushed at once, along with any frames of the message
  buffered ahead of it.
- **A slow stream blocks other data sends.** While a stream is open, every
  `SendText`, `SendBinary` and `SendData` on that connection waits for it.
- **Do not send a data message from inside your own stream.** Calling `SendText`
  between your `Start` and `Release` waits on the lock you hold, forever.

## Control frames and your own frames

`SendClose`, `SendPing` and `SendPong` send one control frame each, flushed
before they return. RFC 6455 caps a control payload at 125 bytes (5.5). Only one
close goes out; a ping or pong still goes out after it (5.5.2). For pinging on
an interval, see [Keepalive](./README.md#keepalive).

`SendFrame` writes a frame you built with `NewFrame`, flushed before it returns.
It settles what the `Conn` knows and leaves the rest to you:

- **Settled, in your frame:** `Mask` and a fresh `MaskingKey` on every send from
  a client (5.1, 5.3), the length fields from `len(PayloadData)`, and FIN on a
  close, ping or pong (5.5). So your frame is modified. `PayloadData` never is.
- **Refused, with nothing written:** a control payload over 125 bytes, and a data
  frame or second close after this side's close.
- **Yours:** the order of a fragmented message (5.4), keeping other senders out
  of it, the opcode and the RSV bits, which go out as you set them.

Read its doc before you reach for it.
