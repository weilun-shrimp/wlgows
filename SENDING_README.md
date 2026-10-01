**English** · [繁體中文](./SENDING_README.zh-TW.md)

# Sending

A `Conn` sends whole messages, streams of chunks, control frames, and frames you
build yourself.

## You are in control

Most WebSocket packages fix a frame size or a buffer size when the connection is
set up, and every send lives with it.

wlgows does not. Nothing is fixed. Every send decides its own frame size, and you
decide when the `Conn` gives memory back. You get full power over both, and full
responsibility: if memory use grows ugly, that is the sizes you chose. Two habits
keep it clean:

- **Pick one chunk size and stick to it.** See [Use a fixed chunk size](#1-use-a-fixed-chunk-size).
- **Call `RenewWriteBuffer` at the right moments.** See [Call `RenewWriteBuffer`](#2-call-renewwritebuffer-when-you-want-memory-back).

The same control lets you make a server's sending memory extremely low, for a
small price: one allocation on the next send. See
[The lowest memory setup](#the-lowest-memory-setup).

## Contents

- [You are in control](#you-are-in-control)
- [Choosing a call](#choosing-a-call)
- [Whole messages](#whole-messages) — `SendText`, `SendBinary`, `SendData`
- [Streaming](#streaming) — `Start`, `TransmitData`, `End`, `Release`
- [Memory: the write buffer](#memory-the-write-buffer) — a fixed chunk size, `RenewWriteBuffer`, and the lowest memory setup
- [When a send fails](#when-a-send-fails)
- [Concurrency](#concurrency)
- [Control frames and your own frames](#control-frames-and-your-own-frames)

The rest of the library is in the [main README](./README.md). Coming from v4?
See [Coming from v4](./README.md#coming-from-v4).

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
| `TransmitData(chunk)` | Sends one chunk as one frame. Empty chunks are skipped. |
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
- **Your buffer is yours again** as soon as `TransmitData` returns. Nothing is
  held between calls, so reading into the same buffer is safe.
- **Text must be valid UTF-8.** Only `SendText` checks it. A text stream is yours
  to keep valid.
- **One goroutine.** Make all four calls from the goroutine that called Start.

[`stream_client`](./example/stream_client/main.go) streams a file this way, and
[`stream_server`](./example/stream_server/main.go) receives it frame by frame.

## Memory: the write buffer

Every frame a `Conn` sends is sealed into one write buffer. The buffer grows to
fit the largest **frame** sent and stays that size. It never shrinks by itself.

Nothing here caps it for you — see [You are in control](#you-are-in-control).
You have two tools.

### 1. Use a fixed chunk size

This is the good practice. The buffer follows the largest frame, not the largest
message. So a fixed `chunkSize` caps the buffer at `chunkSize` plus at most 14
header bytes, however large the message is.

| Sending a 16 MB message | Write buffer after |
|---|---|
| `SendBinary(data, 0)` | about 16 MB |
| `SendBinary(data, 4*1024)` | about 4 KB |
| Streaming with a 4 KB read buffer | about 4 KB |

Pick one size and use it everywhere: as `chunkSize`, and as the buffer you read
into before `TransmitData`. Then every send produces the same frames, and the
buffer stays the same size.

4 KB is a fair default. Check it against your peer's limits:

- **Too large**, and a frame may pass a peer's per-frame or per-message limit.
- **Too small**, and a large message becomes many frames. A peer may cap frames
  per message. wlgows' own `Listener` can, with `MaxMsgFrameCount` (no limit by
  default), and refuses the message with `ErrMsgFrameCountExceeded`. Keep message size ÷ `chunkSize`
  under that cap. For example, 16 MB in 4 KB chunks is 4,096 frames, more than
  the 4,000 the [Listener guide](./LISTENER_README.md#configuration) uses as its
  example.

### 2. Call `RenewWriteBuffer` when you want memory back

```go
conn.RenewWriteBuffer(4 * 1024) // a fresh buffer of 4 KB; the old one goes to the GC
conn.RenewWriteBuffer(0)        // no buffer at all; every byte goes back
```

When to call it is your call. Good times:

- **After a large frame.** One big message grew the buffer, and you do not
  expect another soon.
- **Before a long quiet time.** You know the connection will not send for a
  while. On a server holding many connections, each idle connection holding a
  large buffer adds up.

How to call it:

- **Pass your usual frame size**, such as your `chunkSize`, so the next send
  does not have to grow the buffer again. Or pass 0 — see below.
- **Not between chunks of one message.** The next chunk grows the buffer straight
  back.
- **Any goroutine is fine.** It takes the write lock, so it waits for a frame
  being written to finish.

With a fixed chunk size the buffer never grows past one chunk, so you may never
need `RenewWriteBuffer` at all.

#### 0: the lowest memory cost

`RenewWriteBuffer(0)` gives the whole write buffer back. Until the next send, the
`Conn` holds no write memory at all. On a server with many connections that
mostly sit idle, this keeps the server clean: an idle connection costs nothing
to write with.

The price is one allocation: the next send grows the buffer again, to the size
of its frame. Nothing breaks, because the buffer is dynamic and grows whenever a
frame needs it. So 0 is safe to pass at any time, and it is the right choice when
you know the connection will be quiet. If it sends again straight away, passing
your usual frame size saves that allocation.

### The lowest memory setup

Because nothing is fixed, sending can cost a server almost no memory. Two
settings together:

```go
conn.SendText(text, 4*1024) // a small, fixed chunk size
conn.RenewWriteBuffer(0)    // no write buffer while idle
```

- **While sending:** the write buffer is at most one chunk plus a 14-byte header,
  however large the message.
- **While idle:** the write buffer holds nothing at all.

So a connection that is not sending costs no write memory. For a server holding
100,000 mostly idle connections, that is the difference between nothing and
100,000 buffers each the size of the largest frame they ever sent. The price is
one allocation on the next send, so use it when memory matters more than that.

## When a send fails

| Error | Meaning |
|---|---|
| `ErrCloseAlreadySent` | A close has gone out, and RFC 6455 allows no data frame after one (5.5.1). Nothing of this frame was sent. |
| `ErrInvalidUTF8` | `SendText` got invalid UTF-8. Nothing was sent. |
| `ErrNotDataFrameOpcode`, `ErrContinuationFrameWithoutMsg` | `Start` or `SendData` got an opcode that cannot open a message. Nothing was locked. |
| `ErrLongDataTransmissionNotStarted` | `TransmitData` or `End` with no transmission open. |
| Anything else | The frame could not be built, or the socket write failed. |

**A failure in the middle of a message leaves it unterminated.** Earlier chunks
already reached the peer, and RFC 6455 has no way to end a message early. Close
the connection. If you send another data message instead, the peer sees a
protocol error.

In a stream you may call End after a failure instead. The peer then receives what
was sent as a complete message. Do that only if a partial message is what you
want.

## Concurrency

- **Many goroutines can send.** Data messages go out one at a time: a message
  holds the connection from its first frame to its last.
- **Control frames still get through.** A ping, pong or close waits only for the
  frame being written, so it can go out between two chunks of a long message
  (5.4, 5.5.2).
- **A slow stream blocks other data sends.** While a stream is open, every
  `SendText`, `SendBinary` and `SendData` on that connection waits for it.
- **Do not send a data message from inside your own stream.** Calling `SendText`
  between your `Start` and `Release` waits on the lock you hold, forever.

## Control frames and your own frames

`SendClose`, `SendPing` and `SendPong` send one control frame each. RFC 6455 caps
a control payload at 125 bytes (5.5). For pinging on an interval, see
[Keepalive](./README.md#keepalive).

`SendFrame` writes a frame you built with `NewFrame`. It checks almost nothing:
not the close rule, and not the order of a fragmented message. It only fixes the
masking to match the `Conn`. Read its doc before you reach for it.
