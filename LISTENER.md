**English** · [繁體中文](./LISTENER.zh-TW.md)

# Listener

A `Listener` runs the read loop for you. It reads frames, refuses the ones RFC
6455 forbids, joins the frames of a message, and calls your hook for each type
of frame.

It checks frames against the RFC. It does not decide what to do with them. Your
hooks do that.

Getting a `Conn` is in [SERVER.md](./SERVER.md) and [CLIENT.md](./CLIENT.md).
Sending is in [SENDING.md](./SENDING.md). Reading the bytes of a message is in
[READING.md](./READING.md).

## Contents

- [Quick start](#quick-start) — one connection, from start to end
- [The standard Listener](#the-standard-listener) — `conn.NewStandardListener()`: what it sets, and how to add to it
- [Three things a pure Listener will not do for you](#three-things-a-pure-listener-will-not-do-for-you) — [closing](#it-never-closes-the-connection) · [writing](#it-never-writes-anything) · [liveness](#it-has-no-pingpong-liveness)
- [Configuration](#configuration) — `NewListenerConfig`, `SetConfig`, and the limits
- [Hooks](#hooks) — which frame goes to which hook, and what the RFC asks of you
- [Errors](#errors) — four groups, and which of them need a close frame · [1. a frame broke a rule](#1-a-frame-broke-a-rule) · [2. the connection failed](#2-the-connection-failed) · [3. you used the Listener wrong](#3-you-used-the-listener-wrong) · [4. something else](#4-something-else) · [putting it together](#putting-it-together)
- [Pausing and resuming](#pausing-and-resuming) — ending a run, and starting it again
- [Concurrency](#concurrency) — one reader

## Quick start

`conn` is a `*wlgows.Conn` after the handshake. This example is for a server.
The code handles one connection and every way `Listen` can return.

`readMessage` and `maxMessageSize` are from the
[READING quick start](./READING.md#quick-start). `readMessage` reads the bytes
of one message, and decompresses them when the peer compressed the message.

```go
func handleConn(conn *wlgows.Conn) {
	defer conn.Close() // the Listener never closes anything, so you do

	listener := wlgows.NewListener(conn, wlgows.NewListenerConfig{
		PeerIsClient:                  true, // we are the server, so the peer is a client
		PermessageDeflateAgreedConfig: conn.GetPermessageDeflateAgreedConfig(),
	})

	buf := make([]byte, maxMessageSize) // one buffer for every message

	listener.SetConfig(wlgows.ListenerConfig{
		MaxDataFramesSize: 10 * 1024 * 1024, // 10 MB per message, as it arrives
		MaxFrameSize:      10 * 1024 * 1024, // one frame, header included
		MaxDataFrameCount: 4000,             // see Configuration
		FrameReadTimeout:  60 * time.Second,

		Text: func(frames wlgows.DataFrames) {
			message, err := readMessage(conn, frames, buf)
			if err != nil {
				listener.PauseListen(err) // Listen returns err
				return
			}
			log.Printf("text: %s", message)
		},
		Binary: func(frames wlgows.DataFrames) {
			message, err := readMessage(conn, frames, buf)
			if err != nil {
				listener.PauseListen(err)
				return
			}
			log.Printf("binary: %d bytes", len(message))
		},
		Ping: func(frame *wlgows.Frame) {
			conn.SendPong(frame.PayloadData) // 5.5.2, 5.5.3: answer with a pong, same payload
		},
		Close: func(frame *wlgows.Frame) {
			payload, _ := frame.GetClosePayload() // the Listener already checked it
			conn.SendClose(payload)               // 5.5.1: answer with a close, then stop
			listener.PauseListen(nil)             // nil: Listen returns nil
		},
		Unknown: func(frame *wlgows.Frame) {
			// An opcode that 5.2 reserves: close with 1002, then stop.
			conn.SendClose(&wlgows.ClosePayload{StatusCode: wlgows.CloseProtocolError})
			listener.PauseListen(wlgows.ErrReservedOpcode) // Listen returns this
		},
	})

	// Blocks until a read fails, a frame breaks a rule, or PauseListen is called.
	switch err := listener.Listen(); {
	// PauseListen(nil). Here only the Close hook does that: the peer closed.
	case err == nil:

	// Returned before any byte was read. The connection did nothing wrong.
	case errors.Is(err, wlgows.ErrListenerConnIsNil),
		errors.Is(err, wlgows.ErrListenerIsListening):
		log.Println("listener misuse:", err)

	// A frame or a message broke a rule, the connection failed, or something
	// else. Only the first gets a close payload.
	default:
		if payload := wlgows.StandardClosePayloadFor(err); payload != nil {
			conn.SendClose(payload) // does nothing if a hook already sent a close (ErrCloseAlreadySent)
		}
		log.Println("closing:", err)
	}
}
```

The Listener never writes or closes. Every `Send*` above is your own call.
`Pong` is nil on purpose: 5.5.3 expects no answer to a pong, and a nil hook does
nothing.

`conn.NewStandardListener()` sets the same hooks and the limits for you. See
[The standard Listener](#the-standard-listener).

## The standard Listener

`conn.NewStandardListener()` already answers every frame that RFC 6455 asks a
reader to answer. You must still do two things: close the connection, and send
a close frame for an error that `Listen` returns.

Start here, and add your message hooks:

```go
listener := conn.NewStandardListener()

buf := make([]byte, maxMessageSize) // one buffer for every message

config := listener.GetConfig() // start from what it set: SetConfig replaces all of it
config.MaxDataFramesSize = 10 * 1024 * 1024
config.MaxFrameSize = 10 * 1024 * 1024
config.MaxDataFrameCount = 10 * 1024 * 1024 / 256
config.Text = func(frames wlgows.DataFrames) {
	message, err := readMessage(conn, frames, buf) // from the READING quick start
	if err != nil {
		listener.PauseListen(err)
		return
	}
	log.Printf("text: %s", message)
}
listener.SetConfig(config)

err := listener.Listen()
```

What it sets:

| Item | Set to | What it does |
|---|---|---|
| `PeerIsClient` | the side `conn` is not | expects the right masking from the peer (5.1) |
| `PermessageDeflateAgreedConfig` | what the handshake agreed | allows RSV1 on compressed messages ([RFC 7692](./PERMESSAGE_DEFLATE.md)) |
| `Ping` | `conn.RFC6455PingHook` | answers with a pong with the same payload (5.5.2, 5.5.3) |
| `Pong` | `conn.RFC6455PongHook` | nothing: a pong expects no answer (5.5.3) |
| `Close` | `conn.RFC6455CloseHook(listener)` | answers with the same close, unless this side sent one first. Then stops `Listen` with `nil` (5.5.1) |
| `Unknown` | `conn.RFC6455UnknownHook(listener)` | sends a close with 1002, then stops `Listen` with `ErrReservedOpcode` (5.2, 7.4.1) |
| `MaxDataFramesSize` | `StandardMaxDataFramesSize`, 4 KB | a bigger message ends `Listen` with an error. `StandardClosePayloadFor` maps it to 1009 |
| `MaxFrameSize` | `StandardMaxFrameSize`, 4 KB | one frame, header included. Raise it with the size, or a bigger one-frame message is refused |
| `MaxDataFrameCount` | `StandardMaxDataFrameCount`, 16 | comes from the size, so raise both together |
| `FrameReadTimeout` | `StandardFrameReadTimeout`, 60 seconds | ends `Listen` when the peer sends nothing for 60 seconds. Send a ping every 30 seconds or less |

What it leaves to you: the `Text`, `Binary` or `Data` hook, and closing the
connection after `Listen` returns.

**You can change any item.** To add to a standard hook, save it in a variable.
Then call it from your own hook:

```go
standardClose := config.Close
config.Close = func(frame *wlgows.Frame) {
	payload, _ := frame.GetClosePayload()
	log.Printf("peer closed: %+v", payload)
	standardClose(frame)
}
```

The hooks are exported. You can also use any of them in a Listener from
`wlgows.NewListener`, for example `Ping: conn.RFC6455PingHook`.

## Three things a pure Listener will not do for you

A pure Listener is one from `wlgows.NewListener` with no hooks. The standard
Listener's hooks do the writing for you. Closing and liveness are always your
job.

### It never closes the connection

It does not close after a close frame, an error, a message over the limit, or
an unknown opcode. It returns, and **you** close.

RFC 6455 5.5.1 asks the two sides to close at different times. A server MUST
close right after the close frames. A client SHOULD wait for the server to
close first. The Listener does not know which side it is on, so your code
closes.

To wait as a client: keep reading after the close frames, with a deadline,
until the read fails. Then close. `conn.Close()` on a TLS connection also ends
the TLS session, which 7.1.1 asks for.

### It never writes anything

No pongs, no close answers, no echoes. Every `Send*` in the quick start is
your code.

So a Listener **with no hooks answers nothing**. The peer waits for pongs that
never come. A reserved opcode is dropped, and the Listener keeps reading. But
5.2 says it fails the connection.

### It has no ping/pong liveness

The `Ping` and `Pong` hooks tell you that a frame arrived. A dead peer looks the
same as a quiet one. To find a dead peer, you need your own timer:

- send a ping at a fixed interval: [`StartPingLoop`](./SENDING.md#pinging-on-an-interval) does this
- reset a deadline on **any** pong, whatever its payload
- if the deadline passes with no pong, the peer is gone: close

Save the time in your `Pong` hook, and close when your deadline passes:

```go
var lastPong atomic.Value // time.Time
config.Pong = func(*wlgows.Frame) { lastPong.Store(time.Now()) }
```

Do not check the payload. RFC 6455 5.5.3 allows a pong that nobody asked for,
and one pong for only the latest of several pings. Any pong shows the peer is
alive.

`FrameReadTimeout` does not do this. It only ends `Listen` when *no whole frame
arrives in time*. A peer that sends data but never answers pings still looks alive.

## Configuration

A Listener has two configs:

| Config | Set by | Can change |
|---|---|---|
| `NewListenerConfig` | `wlgows.NewListener(conn, config)` | no: fixed for the Listener's life |
| `ListenerConfig` | `listener.SetConfig(config)` | yes: any time, from any goroutine |

### `NewListenerConfig`

| Item | Zero value | What it is |
|---|---|---|
| `PeerIsClient` | the peer is a **server** | which side the peer is on. This decides masking (5.1) |
| `PermessageDeflateAgreedConfig` | permessage-deflate not agreed | pass `conn.GetPermessageDeflateAgreedConfig()` |

**Get `PeerIsClient` right.** Its zero value says the peer is a server. A
server that leaves it unset refuses every frame from its client.

**Pass `PermessageDeflateAgreedConfig`.** When it is nil, a compressed message
has RSV1 set, and the Listener refuses it with `ErrReservedBitsSet`.

`conn.NewStandardListener()` sets both from the connection.

### `ListenerConfig`

`SetConfig` copies the whole `ListenerConfig` under a lock. You can call it
before `Listen`, between runs, or inside a hook.

| Item | Type | Zero value | What it is |
|---|---|---|---|
| `MaxDataFramesSize` | `uint64` | no limit | bytes of one message's frames as they arrive, headers included. Checked at each header |
| `MaxDataFrameCount` | `uint64` | no limit | how many frames one message may have |
| `MaxFrameSize` | `uint64` | no limit | bytes of one data or reserved-opcode frame, header included. Checked at its header |
| `FrameReadTimeout` | `time.Duration` | no timeout | the time limit to read one frame |
| `Ping` | `func(*Frame)` | frames dropped | [5.5.2, 5.5.3](#hooks): answer with a pong with the same payload |
| `Pong` | `func(*Frame)` | frames dropped | [5.5.3](#hooks): no answer expected, so nil is fine |
| `Close` | `func(*Frame)` | frames dropped | [5.5.1](#hooks): answer with a close unless this side sent one, then `PauseListen` |
| `Text` | `func(DataFrames)` | messages dropped | a whole text message |
| `Binary` | `func(DataFrames)` | messages dropped | a whole binary message |
| `Data` | `func(*Frame)` | `Text` and `Binary` are used | [each data frame](#the-data-hook), none kept |
| `Unknown` | `func(*Frame)` | frames dropped | [5.2](#hooks) reserved opcode: answer 1002, then stop |

**It replaces everything.** Items you leave out become zero values. To change
one item, get the config first:

```go
config := listener.GetConfig() // a copy of the config in use
config.MaxDataFrameCount = 4000
listener.SetConfig(config)
```

A hook changes the config the same way. The change applies from the next frame,
not the current one.

Every zero value works, but **always set `MaxDataFramesSize` and
`MaxFrameSize`.** A 10 byte header can claim a 10 GB payload. Without a limit,
that becomes a 10 GB allocation. A reserved-opcode frame is limited only by
`MaxFrameSize`.

`MaxDataFramesSize` counts the bytes as they arrive. A compressed message can
be much bigger after decompressing. To limit that size, see
[Limiting the size](./READING.md#limiting-the-size).

**Picking `MaxDataFrameCount`.** The peer decides how many frames to use. Start
from the smallest frame you accept:

	MaxDataFrameCount = MaxDataFramesSize / smallest frame you accept

10 MB in frames of 4 KB is 2560 frames, so 4000 leaves room. Pick a high number
rather than a low one. Too high costs little, because `MaxDataFramesSize`
already limits memory. Too low refuses good messages.

Why the count exists: an empty frame adds only 2 bytes to `MaxDataFramesSize`
(6 bytes from a client). But it still uses the memory of a whole `Frame`.

**With the `Data` hook, `MaxDataFramesSize` must fit the whole stream**, not
one frame. See [The Data hook](#the-data-hook).

Each item's full doc:

```bash
go doc github.com/weilun-shrimp/wlgows/v7.ListenerConfig
```

## Hooks

Hooks run one at a time, inside the read loop. While a hook runs, **the loop
waits**: no frames are read, not even pings. Give slow work to a goroutine or a
queue. Hooks never run at the same time, so messages arrive in order (5.4).

A nil hook drops its frames.

| Hook | Gets | What it asks of you |
|---|---|---|
| `Ping` | one frame | 5.5.2, 5.5.3: MUST answer with a pong with the same payload, unless a close already arrived |
| `Pong` | one frame | 5.5.3: no answer expected |
| `Close` | one frame | 5.5.1: MUST answer with a close unless this side already sent one, then close. `Frame.GetClosePayload` gives the status code and reason, both already checked. A client must not show the reason to its user |
| `Text` | a whole message | read it with [`DataFramesReader`](./READING.md#dataframesreader) |
| `Binary` | a whole message | read it with [`DataFramesReader`](./READING.md#dataframesreader) |
| `Data` | one data frame | **use with care.** See [The Data hook](#the-data-hook) |
| `Unknown` | one frame | an opcode that 5.2 reserves. A protocol error: answer 1002 and close |

### `Text` and `Binary`

`Text` and `Binary` get a whole message as `DataFrames`: every frame of the
message, in order. A control frame between two frames of a message goes to its
own hook, and does not break the message.

Read the bytes with `conn.DataFramesReader(frames)`. It joins the payloads,
decompresses a compressed message, and checks text as UTF-8. See
[READING.md](./READING.md).

The UTF-8 check on text (5.6):

| The text message is | Checked by |
|---|---|
| not compressed | the Listener, before `Text` runs |
| compressed | `DataFramesReader`, after decompressing |

The Listener checks the **whole message**, not each frame, because a frame may
end in the middle of a character. `Binary` is never checked.

### The `Data` hook

With `Data` set, the Listener does not join frames into messages. Each data
frame goes straight to `Data`, none is kept, and `Text` and `Binary` never run.
Use it only for a message too large to hold in memory. A message that fits
belongs to `Text` or `Binary`.

With `Data`, your code must do more:

- Watch `frame.FIN`. Nothing else marks the end of the message.
- A continuation frame has no type. Ask `listener.GetCurrentMsgOpcode()`.
- Ask `listener.IsCurrentMsgDeflated()` whether the message is compressed.
- Check UTF-8 on text yourself. One frame cannot be checked alone.
- The limits still apply, and empty frames still arrive.
- Set `Data` between messages, not during one.

`MaxDataFramesSize` covers the whole message, so it must fit the whole stream.
A single frame could then use all of that limit. So also set `MaxFrameSize`: it
limits the size of each frame.

Examples, for messages compressed or not:
[Large messages: the Data hook](./READING.md#large-messages-the-data-hook).

### After a close frame, stop reading

RFC 6455 1.4 (not normative) says a peer drops any data that comes after a
close. The Listener keeps reading after `Close`, so your `Close` hook must call
`PauseListen`. If it does not, a later message still reaches `Text` or
`Binary`.

## Errors

`Listen` returns `nil` after `PauseListen(nil)`, if nothing else went wrong.
`PauseListen(err)` makes `Listen` return `err`, so you can say why you stopped,
for example a shutdown. The first pause wins. A later one is ignored.

After a pause, you often close the connection to end a blocked read. The read
then fails too. `Listen` returns both errors **joined**, and `errors.Is` finds
either one. So an error does not always mean the peer did something
wrong.

`StandardClosePayloadFor` does not know your own errors. For them, it returns
`nil` (see group 4). If your error needs a close code, check for it before you
call `StandardClosePayloadFor`.

`io.EOF` does not mean a normal close. The peer dropped the TCP connection
**without** a close frame. 7.4.1 calls this 1006.

The errors fall into four groups, and each needs a different answer.

### 1. A frame broke a rule

The peer broke RFC 6455. Answer with a close frame, then close.
`StandardClosePayloadFor` gives the close code from 7.4.1:

| Error | Close code |
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

Reading a message can fail too, with `ErrInvalidUTF8` (1007),
`ErrInflateDataInvalid` (1007) or `ErrSizeLimitExceeded` (1009). Your hook gives
them to `PauseListen`, and `Listen` returns them. `StandardClosePayloadFor`
knows them too. See [READING errors](./READING.md#errors).

**Never read again after any of these.** 7.1.7 forbids processing anything more
from the peer once the connection has failed. After some of them, the refused
frame was only partly read, so the next read would start in the middle of it.
Writing still works, so the close frame can go out.

### 2. The connection failed

`io.EOF`, `io.ErrUnexpectedEOF`, `os.ErrDeadlineExceeded`, `net.ErrClosed`,
connection reset. They come from the socket, and the peer broke no rule. There
is no one to send a close frame to. 7.4.1 calls this **1006** and forbids
sending it. So log it and close.

A read timeout comes from your own `FrameReadTimeout`. It is not the peer's
fault. The connection can usually still write, so you *may* send 1000 or 1001
first. You decide, so `StandardClosePayloadFor` returns `nil`.

### 3. You used the Listener wrong

- `ErrListenerConnIsNil`: `NewListener` was called with a nil `conn`.
- `ErrListenerIsListening`: a second `Listen` started while one was running.

Both come back before anything is read.

**Do not close the connection on these.** After `ErrListenerIsListening`,
another `Listen` is still running on the connection. These are bugs in your
code. Fix them.

### 4. Something else

A plain error from your own `net.Conn`, a TLS layer or a mock. This package
cannot tell whose fault it is.

Handle it like group 2: close, and send nothing. 7.1.7 says a close frame
SHOULD be sent, not MUST, so sending none breaks no rule. Sending 1002 would
blame a peer that may have done nothing wrong. If you want to send one, 1011
says the problem is on this side. If you know what your error means, map it
yourself **before** you call `StandardClosePayloadFor`.

### Putting it together

`StandardClosePayloadFor` returns a payload for group 1, and `nil` for the
others. `nil` only means "no close frame". Groups 2 and 4 still need the
connection closed. Group 3 must not close it:

```go
err := listener.Listen()
switch {
case err == nil: // PauseListen(nil): nothing to answer

case errors.Is(err, wlgows.ErrListenerConnIsNil),
	errors.Is(err, wlgows.ErrListenerIsListening):
	log.Printf("listener misuse: %v", err) // group 3: leave the connection open

default:
	if payload := wlgows.StandardClosePayloadFor(err); payload != nil {
		// group 1: send it before you close
	}
	// Groups 1, 2 and 4 all need the connection closed. For group 1,
	// 7.1.7 makes it a MUST. Your code sends and closes.
}
```

`Reason` in the payload is empty. To tell the peer why, set `Reason` before you
send it.

A reserved opcode (5.2) goes to the `Unknown` hook, not to `Listen`. The
standard one answers 1002 and stops with `ErrReservedOpcode`. With your own
hook, your code does both. A nil hook drops it, like any nil hook.

## Pausing and resuming

`PauseListen(err)` ends the run, and `Listen` returns `err`. Pass `nil` to stop
without an error. You can call it from any goroutine. When nothing is
listening, it does nothing.

The Listener sees a pause only **between frames**. While it waits for the next
frame, the pause has no effect. If the peer is silent, the read stays blocked.
Only closing the connection unblocks it.

Calling `Listen` again continues where it stopped. A message that has not
received all its frames is kept, so you can change the config in between
without losing frames.

## Concurrency

**Read frames from one goroutine.** A lock keeps two readers from mixing bytes,
but each reader would still get random frames. A Listener is one reader, so do
not read frames yourself while it runs.

`conn.Read` comes from the embedded `net.Conn` and skips the lock. Read frames
through a Listener.

Reading without a Listener, and the memory of the read buffer, are in
[READING.md](./READING.md#reading-without-a-listener).
