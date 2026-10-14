**English** · [繁體中文](./LISTENER_README.zh-TW.md)

# Listener

`Listener` runs the read loop for you. It reads frames, refuses the ones RFC
6455 forbids, joins fragmented messages, and calls your hook for each opcode. A
reserved opcode goes to your `Unknown` hook.

It checks frames against the RFC, but does not decide what to do with them. Your
hooks do that.

## Contents

- [Quick start](#quick-start) — one connection, start to finish
- [The standard Listener](#the-standard-listener) — `conn.NewStandardListener()`: what it sets, and how to build on it
- [Three things a pure Listener will not do for you](#three-things-a-pure-listener-will-not-do-for-you) — [closing](#it-never-closes-the-connection) · [writing](#it-never-writes-anything) · [liveness](#it-has-no-pingpong-liveness)
- [Configuration](#configuration) — `SetConfig`, `GetConfig`, and which item to get right
- [Hooks](#hooks) — which frame reaches which, and what the RFC asks back · [reading text without a copy: `unsafe`](#reading-text-without-a-copy-unsafe)
- [Errors](#errors) — four groups, and which of them want a close frame · [1. a frame broke a rule](#1-a-frame-broke-a-rule) · [2. the connection failed](#2-the-connection-failed) · [3. you misused the Listener](#3-you-misused-the-listener) · [4. something else](#4-something-else-entirely) · [putting it together](#putting-it-together)
- [Pausing and resuming](#pausing-and-resuming) — ending a run, and starting it again
- [Reading without a Listener](#reading-without-a-listener) — `GetNextFrameHeader` and `ReadFramePayload`, one frame at a time; not recommended
- [Memory: the read buffer](#memory-the-read-buffer)
- [Concurrency](#concurrency) — one reader

Getting a `Conn` is in [SERVER_README.md](./SERVER_README.md) and
[CLIENT_README.md](./CLIENT_README.md), and sending in
[SENDING_README.md](./SENDING_README.md).

## Quick start

`conn` is an already handshaken `*wlgows.Conn`, from
[the server](./SERVER_README.md#the-handshake) or
[the client](./CLIENT_README.md#the-handshake) side. This one is a server's.
It handles one connection and every way `Listen` can return. `Pong` is nil on
purpose: 5.5.3 expects no answer to a pong, and a nil hook does nothing.

`SetConfig` sets the whole config at once. Call it any time: before `Listen`,
between runs, or inside a hook.

```go
func handleConn(conn *wlgows.Conn) {
	defer conn.Close() // yours: the Listener never closes anything

	listener := wlgows.NewListener(conn)
	listener.SetConfig(wlgows.ListenerConfig{
		PeerIsClient:      true,             // we are the server, so the peer masks
		MaxDataFramesSize: 10 * 1024 * 1024, // 10 MB per message
		MaxFrameSize:      10 * 1024 * 1024, // one frame, header included
		MaxDataFrameCount: 4000,             // see Configuration
		FrameReadTimeout:  60 * time.Second,

		Text: func(frames wlgows.DataFrames) {
			log.Printf("text: %s", frames.String())
		},
		Binary: func(frames wlgows.DataFrames) {
			log.Printf("binary: %d bytes", frames.ByteLen())
		},
		Ping: func(frame *wlgows.Frame) {
			conn.SendPong(frame.PayloadData) // 5.5.2, 5.5.3: pong back, echoing the payload
		},
		Close: func(frame *wlgows.Frame) {
			payload, _ := frame.GetClosePayload() // the Listener already validated it
			conn.SendClose(payload)               // 5.5.1: answer with a close, then stop
			listener.PauseListen(nil)             // nil: the peer said why, Listen returns nil
		},
		Unknown: func(frame *wlgows.Frame) {
			// An opcode 5.2 reserves: close 1002, then stop.
			conn.SendClose(&wlgows.ClosePayload{StatusCode: wlgows.CloseProtocolError})
			listener.PauseListen(wlgows.ErrReservedOpcode) // Listen returns this
		},
	})

	// Blocks until a read fails, a frame breaks a rule, or PauseListen is called.
	switch err := listener.Listen(); {
	// PauseListen(nil) — here only the Close hook does that, so the peer closed
	// and there is nothing to report.
	case err == nil:

	// Returned before a single byte was read, so the connection is not implicated.
	case errors.Is(err, wlgows.ErrListenerConnIsNil),
		errors.Is(err, wlgows.ErrListenerIsListening):
		log.Println("listener misuse:", err)

	// A frame broke a rule, or the socket died, or something unattributable. A
	// payload comes back only for the first — a dead socket has nothing to tell.
	default:
		if payload := wlgows.StandardClosePayloadFor(err); payload != nil {
			conn.SendClose(payload) // answer with it; the deferred Close follows
		}
		log.Println("closing:", err)
	}
}
```

The Listener never writes or closes, so every `Send*` above is your own call.
`conn.NewStandardListener()` sets equivalent hooks and the limits for you:
see [The standard Listener](#the-standard-listener). `PauseListen` ends the run,
and `Listen` returns the reason you pass it.

## The standard Listener

`conn.NewStandardListener()` already answers every frame RFC 6455 asks a reader
to answer. Closing, and the close frame for an error `Listen` returns, stay
yours.
Start here, and add your message hooks:

```go
listener := conn.NewStandardListener()

config := listener.GetConfig() // start from what it set: SetConfig replaces it all
config.MaxDataFramesSize = 10 * 1024 * 1024
config.MaxFrameSize = 10 * 1024 * 1024
config.MaxDataFrameCount = 10 * 1024 * 1024 / 256
config.Text = func(frames wlgows.DataFrames) {
	log.Print(frames.String())
}
listener.SetConfig(config)

err := listener.Listen()
```

What it sets:

| Item | Set to | What it does |
|---|---|---|
| `PeerIsClient` | the side `conn` is not | expects the peer's masking right (5.1) |
| `Ping` | `conn.RFC6455PingHook` | answers with a pong carrying the same payload (5.5.2, 5.5.3) |
| `Pong` | `conn.RFC6455PongHook` | nothing: a pong expects no answer (5.5.3) |
| `Close` | `conn.RFC6455CloseHook(listener)` | echoes the close, unless this side sent one first, then stops `Listen` with `nil` (5.5.1) |
| `Unknown` | `conn.RFC6455UnknownHook(listener)` | sends a close with 1002, then stops `Listen` with `ErrReservedOpcode` (5.2, 7.4.1) |
| `MaxDataFramesSize` | `StandardMaxDataFramesSize`, 4 KB | a message over it ends `Listen` with an error, which `StandardClosePayloadFor` maps to 1009 |
| `MaxFrameSize` | `StandardMaxFrameSize`, 4 KB | one frame, header included; raise it with the size, or a larger single-frame message is refused |
| `MaxDataFrameCount` | `StandardMaxDataFrameCount`, 16 | derived from the size, so raise both together |
| `FrameReadTimeout` | `StandardFrameReadTimeout`, 60 seconds | cuts off an idle peer, so ping at 30 seconds or less |

What it leaves to you: the `Text`, `Binary` or `Data` hook, and closing the
connection after `Listen` returns.

**You can change any item.** To add to a standard hook, keep it and call it from
yours:

```go
standardClose := config.Close
config.Close = func(frame *wlgows.Frame) {
	payload, _ := frame.GetClosePayload()
	log.Printf("peer closed: %+v", payload)
	standardClose(frame)
}
```

The hooks are exported, so a `wlgows.NewListener(conn)` can use only some of
them, such as `Ping: conn.RFC6455PingHook`.

## Three things a pure Listener will not do for you

A pure Listener is one from `wlgows.NewListener(conn)` with no hooks. The
standard Listener's hooks do the second for you. The first and third are always
yours.

### It never closes the connection

Not on a close frame, an error, a message over the limit, or an unknown opcode.
It returns, and **you** close.

RFC 6455 5.5.1 asks the two sides to close at different times. A server MUST
close at once after the close frames. A client SHOULD wait for the server to
close first. Closing is left to you because the Listener does not know which
side it is.

To wait as a client, keep reading after the close frames until the read fails,
with a deadline, then close. `netConn.Close()` on a TLS connection also ends the
TLS session, which 7.1.1 asks for.

### It never writes anything

No pongs, no close replies, no echoes. Every `Send*` in the quick start is yours.

So a Listener **with no hooks answers nothing**. The peer waits for pongs that
never come, and a reserved opcode is dropped while reading goes on, though 5.2
says it fails the connection.

### It has no ping/pong liveness

The `Ping` and `Pong` hooks tell you a frame arrived. A dead peer looks exactly
like a quiet one, and detecting it is a timer you own:

- send a ping on an interval: [`StartPingLoop`](./SENDING_README.md#pinging-on-an-interval) does it
- reset a deadline on **any** pong — ignore the payload
- if the deadline passes with no pong, the peer is gone; close

Record the time in your `Pong` hook, and close when your deadline passes:

```go
var lastPong atomic.Value // time.Time
config.Pong = func(*wlgows.Frame) { lastPong.Store(time.Now()) }
```

Ignore the payload. RFC 6455 allows a pong nobody asked for (5.5.3), and a pong
for only the latest of several pings (5.5.3). Any pong proves the peer is alive.

`FrameReadTimeout` does not cover this. It only fires when *no whole frame arrives in time*, so
a peer that sends data but ignores pings still looks alive.

## Configuration

`SetConfig` copies the whole `ListenerConfig` under a lock. It is safe from any
goroutine, and from inside a hook. The connection is not in it: `NewListener`
fixes it for the Listener's life.

Every item, and what leaving it out means:

| item | type | zero value | |
|---|---|---|---|
| `PeerIsClient` | `bool` | the peer is a **server** | which side the peer is on, which decides masking (5.1) |
| `MaxDataFramesSize` | `uint64` | no limit | byte budget for one message's data frames, headers included, checked at each header |
| `MaxDataFrameCount` | `uint64` | no limit | how many frames one message may arrive in |
| `MaxFrameSize` | `uint64` | no limit | byte limit for one data or reserved-opcode frame, header included, checked at its header |
| `FrameReadTimeout` | `time.Duration` | no timeout | per frame, armed before each read |
| `Ping` | `func(*Frame)` | frames dropped | [5.5.2, 5.5.3](#hooks): answer with a pong echoing the payload |
| `Pong` | `func(*Frame)` | frames dropped | [5.5.3](#hooks): no answer expected, so nil conforms |
| `Close` | `func(*Frame)` | frames dropped | [5.5.1](#hooks): answer with a close unless this side sent one, then `PauseListen` |
| `Text` | `func(DataFrames)` | messages dropped | a whole message, already checked as UTF-8 |
| `Binary` | `func(DataFrames)` | messages dropped | a whole message, arbitrary bytes |
| `Data` | `func(*Frame)` | `Text`/`Binary` assemble instead | [each data frame raw](#hooks), nothing retained |
| `Unknown` | `func(*Frame)` | frames dropped | [5.2](#hooks) reserved opcode: answer 1002, then stop |

**It replaces everything.** Items you leave out become zero values, so read the
config first to change one thing:

```go
config := listener.GetConfig() // a copy of what it is running on
config.MaxDataFrameCount = 4000
listener.SetConfig(config)
```

A hook changes the config the same way. The change applies from the next frame,
not the current one.

Nothing is required: every zero value works. But get `PeerIsClient` right. Its
zero value says the peer is a server, so a server that leaves it unset refuses
every frame from its client.

Always set `MaxDataFramesSize` and `MaxFrameSize`. A 10 byte header can claim a
10 GB payload, and without a limit that becomes a 10 GB allocation. A
reserved-opcode frame is bound only by `MaxFrameSize`.

**Picking `MaxDataFrameCount`.** The peer decides how to fragment, so start from
the smallest fragment you accept:

	MaxDataFrameCount = MaxDataFramesSize / smallest fragment expected

10 MB in 4 KB fragments is 2560 frames, so 4000 leaves room. Choose high rather
than low. Too high costs little, since `MaxDataFramesSize` already caps memory.
Too low refuses valid messages, and you will not see why. The count exists
because an empty fragment costs 2 bytes of the budget but a whole `Frame` of
memory.

**With `Data` set, size the byte budget for the whole message**, not one frame.
See `Data` in [Hooks](#hooks).

Each item's full detail is in its doc:

```bash
go doc github.com/weilun-shrimp/wlgows/v7.ListenerConfig
```

## Hooks

Hooks run one at a time, inside the read loop. A hook **blocks the loop** while it
runs, so no frames are read, not even pings. Hand slow work to a goroutine or a
queue. Because hooks never run at once, messages arrive in order (5.4).

A nil hook drops those frames.

| hook | receives | what it asks of you |
|---|---|---|
| `Ping` | one frame | 5.5.2, 5.5.3: MUST answer with a pong echoing the payload, unless a close already arrived |
| `Pong` | one frame | 5.5.3: no answer expected |
| `Close` | one frame | 5.5.1: MUST answer with a close unless this side already sent one, then close. `Frame.GetClosePayload` decodes the status code and reason, both already checked. A client must not show the reason to its user |
| `Text` | a whole message | nothing — the payload is already checked as valid UTF-8 (5.6, 8.1) |
| `Binary` | a whole message | nothing — arbitrary bytes |
| `Data` | one data frame | **handle with care.** Watch for FIN, and check 5.6 UTF-8 yourself on a text message: answer 1007. Only for a message too large to hold — otherwise use `Text` or `Binary` |
| `Unknown` | one frame | an opcode 5.2 reserves. A protocol error: answer 1002 and close |

`Text` and `Binary` get whole messages, joined from every fragment. A control
frame between two fragments goes to its own hook and does not break the message.

A text message is checked as UTF-8 (5.6) before `Text` gets it. The check runs
on the **joined** bytes, because a frame may end in the middle of a character.
`Binary` is never checked.

`Data` replaces the joining. Each data frame goes straight to it, none is kept,
and `Text` and `Binary` never run. Use it only for a message too large to hold,
or a stream you pass on. A message that fits in memory belongs to `Text` or
`Binary`, which do the rest for you.

With `Data`, watch FIN yourself, and check UTF-8 yourself, since one frame
cannot be checked alone. A continuation frame has no type, so ask
`Listener.GetCurrentMsgOpcode` whether the message is text. The limits still
apply, and empty frames still arrive. Set `Data` between messages.

Writing a message straight to disk. Memory stays flat, whatever its size:

```go
out, err := os.Create("./received.bin")
if err != nil {
	return err
}
defer out.Close()

listener := wlgows.NewListener(conn)
listener.SetConfig(wlgows.ListenerConfig{
	PeerIsClient:      true,
	MaxDataFramesSize: 2 * 1024 * 1024 * 1024, // the whole message, so size it for the stream
	MaxFrameSize:      64 * 1024,              // one frame, so one read stays small
	FrameReadTimeout:  60 * time.Second,

	Data: func(frame *wlgows.Frame) {
		// Binary only. A text message would need 5.6 checked on the joined
		// bytes, which is why the opcode is worth asking for.
		if listener.GetCurrentMsgOpcode() != wlgows.OpcodeBinary {
			// Refuse it — close 1003, or whatever your protocol says.
			listener.PauseListen(errors.New("peer streamed text"))
			return
		}
		if _, err := out.Write(frame.PayloadData); err != nil {
			listener.PauseListen(err) // your disk, not the peer's fault
			return
		}
		if frame.FIN { // nothing else marks the end
			log.Println("message complete")
		}
	},
	Ping: func(frame *wlgows.Frame) {
		conn.SendPong(frame.PayloadData) // 5.5.2, 5.5.3: pong back, echoing the payload
	},
	Close: func(frame *wlgows.Frame) {
		payload, _ := frame.GetClosePayload()
		conn.SendClose(payload) // 5.5.1: answer with a close, then stop
		listener.PauseListen(nil)
	},
	Unknown: conn.RFC6455UnknownHook(listener), // 5.2: a reserved opcode fails the connection
})

// Whatever ended it: a read error, a rule broken, or one of the pauses above.
return listener.Listen()
```

Think about `MaxDataFramesSize` here. It covers the whole message, so it must
fit the whole stream. That also lets one frame use all of it, so set
`MaxFrameSize` too: it limits each frame while the message runs long.

[`stream_server`](./example/stream_server/main.go) does this end to end: it
receives a file frame by frame and hashes it.

**After a close frame, stop reading.** RFC 6455 1.4 (non-normative) says a peer
discards any data received after a close. The Listener keeps reading after `Close`, so your `Close` hook
must call `PauseListen`. Otherwise a later message still reaches `Text` or
`Binary`.

### Reading text without a copy: `unsafe`

**Only if you know exactly what you are doing.** The warning in
[the sending guide](./SENDING_README.md#faster-and-less-memory-sending-a-string-with-unsafe)
applies. If unsure, use `frames.String()`, which costs one copy.

wlgows never writes to a payload after reading it, and each frame has its own.
So a one-frame message can become a string without a copy:

```go
config.Text = func(frames wlgows.DataFrames) {
	if len(frames) != 1 {
		handle(frames.String()) // fragmented: joining needs a copy anyway
		return
	}
	payload := frames[0].PayloadData
	handle(unsafe.String(unsafe.SliceData(payload), len(payload)))
}
```

On 1 MB, `frames.String()` takes about 60 µs and allocates 1 MB. The `unsafe`
version takes about 2 ns and allocates nothing, so the message is held once, not
twice.

- **Only for a one-frame message.** A fragmented one must be joined, and
  `frames.String()` joins it in one allocation.
- **Never write to `payload` afterwards**, or the string changes too. Go assumes
  strings never change, so a map keyed by it breaks.
- **The string keeps the whole payload alive.** To keep a small piece of a huge
  message, copy it with `strings.Clone`.
- The bytes are already checked as valid UTF-8 before `Text` runs.

A binary message needs no `unsafe`: `frames[0].PayloadData` is already a
`[]byte`. Just do not write to it.

## Errors

`Listen` returns `nil` after `PauseListen(nil)`, if nothing else went wrong.
`PauseListen(err)` makes `Listen` return `err`, so you can say why you stopped,
such as a shutdown. The first pause wins. A later one is ignored.

A pause often comes with a failed read, because closing the connection is what
wakes a blocked read. Then both errors come back **joined**, and `errors.Is`
finds either. So a non-nil error does not always mean the peer did wrong.

`StandardClosePayloadFor` does not know your own errors. It returns `nil` for
them, as in group 4 below, unless you map them first.

Other errors come from this package. `io.EOF` is not a polite goodbye: the peer
dropped the TCP connection **without** a close frame, which 7.4.1 calls 1006.

The errors fall into four groups, and each needs a different answer.

### 1. A frame broke a rule

The peer violated RFC 6455. Answer with a close frame, then close.
`StandardClosePayloadFor` is the 7.4.1 table:

| error | answer with |
|---|---|
| `ErrReservedBitsSet` | 1002 |
| `ErrFrameNotMasked`, `ErrFrameMasked` | 1002 |
| `ErrReservedOpcode` | 1002 |
| `ErrControlFrameFragmented` | 1002 |
| `ErrControlFramePayloadTooLong` | 1002 |
| `ErrClosePayloadTooShort` | 1002 |
| `ErrPayloadLengthMSBSet` | 1002 |
| `ErrPayloadLengthNotMinimal` | 1002 |
| `ErrContinuationFrameWithoutMsg`, `ErrDataFrameDuringMsg` | 1002 |
| `ErrInvalidCloseStatusCode` | 1002 |
| `ErrInvalidUTF8` | 1007 |
| `ErrFrameByteLengthExceeded` | 1009 |
| `ErrDataFrameCountExceeded` | 1009 |

**Never read again after any of them.** 7.1.7 forbids processing anything more
from the peer once the connection is failed, and after some of them the refused
frame was only partly read, so the next read would start in the middle of it.
Writing still works, so the close frame can go out.

### 2. The connection failed

`io.EOF`, `io.ErrUnexpectedEOF`, `os.ErrDeadlineExceeded`, `net.ErrClosed`,
connection reset. They come from the socket, and the peer broke no rule. There is
no one to send a close frame to. 7.4.1 calls this **1006** and forbids sending it,
so log it and close.

A read timeout is your own `FrameReadTimeout`, not the peer's fault. The socket
can usually still write, so you *may* send 1000 or 1001 first. That choice is
yours, so `StandardClosePayloadFor` returns `nil`.

### 3. You misused the Listener

`ErrListenerConnIsNil`: the Listener was not built by `NewListener`, so it has
no connection. `ErrListenerIsListening`: a second `Listen` started while one was
running. Both return before anything is read.

**Do not close the connection on these.** Another `Listen` may be running fine
on it. These are bugs in your code. Fix them.

### 4. Something else entirely

A plain error from a custom `net.Conn`, a TLS layer or a mock. This package
cannot tell whose fault it is.

Treat it like group 2: close and send nothing. 7.1.7 only says a close frame
SHOULD be sent, so sending none is not a violation, while sending 1002 would
blame a peer that may have done nothing wrong. If you want to send one, 1011
says the fault is on this side. If you know what your error means, map it yourself **before**
`StandardClosePayloadFor`, which returns `nil` for it.

### Putting it together

`StandardClosePayloadFor` returns a payload for group 1 and `nil` for the rest.
`nil` only means "no close frame". Groups 2 and 4 still need the connection
closed, and group 3 must not close it:

```go
err := listener.Listen()
switch {
case err == nil: // PauseListen(nil), nothing to answer

case errors.Is(err, wlgows.ErrListenerConnIsNil),
	errors.Is(err, wlgows.ErrListenerIsListening):
	log.Printf("listener misuse: %v", err) // group 3: leave the connection alone

default:
	if payload := wlgows.StandardClosePayloadFor(err); payload != nil {
		// group 1: answer with it before closing
	}
	// groups 1, 2 and 4 all want the connection closed (7.1.7: MUST, whichever
	// side you are). Both calls are yours.
}
```

`Reason` is empty. Fill it in before sending if you want to tell the peer why.

A reserved opcode (5.2) goes to the `Unknown` hook, not to `Listen`. The
standard one answers 1002 and stops with `ErrReservedOpcode`. With your own
hook, both are yours. A nil hook drops it, like any nil hook.

## Pausing and resuming

`PauseListen(err)` ends the run, and `Listen` returns `err`. Pass `nil` to stop
without an error. It is safe from any goroutine. When nothing is listening, it
does nothing.

The pause takes effect **between frames**, because the loop waits inside
`GetNextFrameHeader`. If the peer is silent, the read stays blocked. Only closing the
connection unblocks it.

`Listen` again resumes where it stopped. A half-joined message is kept, so you
can change the config in between without losing frames.

## Reading without a Listener

**Not recommended. Only do this if you have a better reason than the Listener
gives you.** The Listener answers and refuses what RFC 6455 asks of a reader.
Without it, all of that is yours, and a missed rule breaks the protocol. Even a
message too large to hold does not need this: the `Data` hook streams it one
frame at a time.

A frame is read in two steps. `conn.GetNextFrameHeader()` returns the frame with
its header and no payload, so you can judge its size before anything is
allocated. `conn.ReadFramePayload(frame)` then reads the payload. Hold
`conn.LockRead()` across both, so no other reader takes bytes in between. You
assemble the message:

```go
var frames wlgows.DataFrames
for {
	conn.LockRead()
	frame, err := conn.GetNextFrameHeader()
	if err == nil && frame.GetDeclaredPayloadSize() > 10*1024*1024 {
		err = errors.New("frame too large") // nothing allocated yet
	}
	if err == nil {
		err = conn.ReadFramePayload(frame)
	}
	conn.UnlockRead()
	if err != nil {
		return err
	}
	if !wlgows.IsDataOpcode(frame.Opcode) {
		continue // a control frame: answer it yourself; a reserved opcode fails the connection (5.2)
	}
	frames = append(frames, frame)
	if frame.FIN {
		break
	}
}
```

Everything the Listener does is then yours. Some of it comes as helpers you can
still call:

| Duty | Helper |
|---|---|
| Answer a ping with a pong (5.5.2) | `conn.RFC6455PingHook(frame)` |
| Pick the close code for an error (7.4.1) | `wlgows.StandardClosePayloadFor(err)` |

Nothing else has a helper. `RFC6455CloseHook` and `RFC6455UnknownHook` need a
Listener to stop.
Answering a close is yours, and so are the checks: reserved bits, masking
direction, reserved opcodes, control frame rules, fragment order, the limits,
UTF-8 in text, and the close payload.

`DataFrames` assembles when you want it to:

| Call | Returns |
|---|---|
| `frames.String()` | the joined payload as a `string` |
| `frames.Bytes()` | the joined payload as a fresh `[]byte` you own |
| `frames.ByteLen()` | the joined size in **bytes**, allocating nothing |

`String` and `Bytes` join every fragment, so a character split across two frames comes back
whole. `ByteLen` counts bytes, not characters: `"中文字"` is 9, not 3.

Only the first frame has the opcode. Continuation frames carry 0:

```go
switch frames[0].Opcode {
case wlgows.OpcodeText:   // 0x1
case wlgows.OpcodeBinary: // 0x2
}
```

## Memory: the read buffer

Frames are read through the `*bufio.Reader` you give the handshake.
`bufio.NewReader(netConn)` is 4096 bytes per connection. Any size works, so a
smaller one saves memory for a little speed:

```go
reader := bufio.NewReaderSize(netConn, 16) // 16 is the floor: bufio raises anything under it to 16
```

16 is enough: the longest frame header is 14 bytes (2 + 8 length + 4 mask), so
it still fits in one read. A bigger buffer only lets small payloads share that
read. A large payload needs its own read at any size.

Memory grows with the number of open connections:

| | 16 bytes | 4096 bytes (default) |
|---|---|---|
| Per connection | 16 B | 4096 B |
| 1,000 connections | ~16 KB | ~4 MB |
| 100,000 connections | ~1.6 MB | ~400 MB |

Reads grow with the number of frames arriving together:

| | 16 bytes | 4096 bytes (default) |
|---|---|---|
| One 10 B frame | 1 read | 1 read |
| 100 back-to-back 10 B frames (1000 B total) | ~63 reads | as few as 1 read |
| One 1 MB frame | the payload skips the buffer, same either way | the payload skips the buffer, same either way |

The 100-frame row assumes the bytes have already arrived. A slow peer narrows
the gap.

For a whole idle connection, see
[The smallest connection](./SENDING_README.md#the-smallest-connection).

## Concurrency

**Read from one goroutine.** A lock keeps two readers from mixing bytes, but
each would still get random frames. A Listener is one reader, so do not read
frames yourself while it runs.

`conn.Read` comes from the embedded `net.Conn` and skips the lock. Read through
a Listener, or `GetNextFrameHeader` and `ReadFramePayload` under `LockRead`.
