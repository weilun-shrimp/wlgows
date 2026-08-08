# Listener

`Listener` runs the read loop for you. It reads frames off a connection,
refuses the ones RFC 6455 forbids, assembles fragmented messages, and calls the
hook you configured for each opcode.

It knows the protocol's **shape**, not its **policy**. Every obligation the RFC
puts on a receiver lands on a hook, never inside the Listener.

## Contents

- [Quick start](#quick-start) — one connection, start to finish
- [Three things it will not do for you](#three-things-it-will-not-do-for-you) — [closing](#it-never-closes-the-connection) · [writing](#it-never-writes-anything) · [liveness](#it-has-no-pingpong-liveness)
- [Configuration](#configuration) — `SetConfig`, `GetConfig`, and which item to get right
- [Hooks](#hooks) — which frame reaches which, and what the RFC asks back
- [Errors](#errors) — four groups, and which of them want a close frame
- [Pausing and resuming](#pausing-and-resuming) — ending a run, and starting it again

The rest of the library — sending, streaming, handshakes, locks — is in the
[main README](./README.md).

## Quick start

`conn` is an already handshaken `*wlgows.ServerConn`. This handles one of them
start to finish, and covers every way `Listen` can return. `Pong` is the one
hook left nil on purpose — 5.5.3 says MUST NOT answer a pong, which is exactly
what a nil hook does.

`SetConfig` hands over the whole configuration at once, and is callable whenever
— before `Listen`, between two runs, or from inside a hook.

```go
func handleConn(conn *wlgows.ServerConn) {
	defer conn.Close() // yours: the Listener never closes anything

	listener := wlgows.NewListener(conn)
	listener.SetConfig(wlgows.ListenerConfig{
		PeerIsClient:         true,             // we are the server, so the peer masks
		MaxMsgPayloadByteLen: 10 * 1024 * 1024, // 10 MB per message
		MaxMsgFrameCount:     4000,             // see Configuration
		FrameReadTimeout:     60 * time.Second,

		Text: func(frames wlgows.Frames) {
			log.Printf("text: %s", frames.String())
		},
		Binary: func(frames wlgows.Frames) {
			log.Printf("binary: %d bytes", frames.ByteLen())
		},
		Ping: func(f *wlgows.Frame) {
			// 5.5.2: pong back, echoing f.PayloadData. Sending is yours.
		},
		Close: func(f *wlgows.Frame) {
			payload, _ := f.GetClosePayload() // the Listener already validated it
			// 5.5.1: answer with a close, whatever you want in it, then stop.
			listener.PauseListen()
		},
		Unknown: func(f *wlgows.Frame) {
			// An opcode 5.2 reserves: close 1002, then stop.
			listener.PauseListen()
		},
	})

	// Blocks until a read fails, a frame breaks a rule, or PauseListen is called.
	switch err := listener.Listen(); {
	// PauseListen was called. That says nothing about who called it — here the
	// only callers are the Close and Unknown hooks, so this is a clean shutdown.
	case err == nil:

	// Returned before a single byte was read, so the connection is not implicated.
	case errors.Is(err, wlgows.ErrListenerConnIsNil),
		errors.Is(err, wlgows.ErrListenerIsListening):
		log.Println("listener misuse:", err)

	// A frame broke a rule, or the socket died, or something unattributable. A
	// payload comes back only for the first — a dead socket has nothing to tell.
	default:
		if payload := wlgows.StandardClosePayloadFor(err); payload != nil {
			// Answer with it, then close.
		}
		log.Println("closing:", err)
	}
}
```

The Listener writes nothing and closes nothing, so every answer above is a
comment rather than a call: what goes on the wire is yours, and the hooks only
say when. `PauseListen` is the one thing it does for you there — it ends the run
so this function can return.

## Three things it will not do for you

### It never closes the connection

Not on a close frame, not on a read error, not on a payload over the limit, not
on an unknown opcode. It reports and returns. `Listen` returning an error is a
reason for **you** to close, never a sign that it already did.

That is not tidiness. RFC 6455 7.1.1 gives the two sides different obligations —
a server MUST close the TCP connection immediately once close frames have
crossed, while a client SHOULD wait for the server to close and may give up only
after a reasonable delay. A Listener does not know which side it is on.

### It never writes anything

No pongs, no close replies, no echoes. It reads. Every `Send*` call in the quick
start above is yours, in your hook.

So a Listener with **no hooks configured is a conforming reader of nothing** — it
will sit there quite happily while the peer waits for pongs that never come.

### It has no ping/pong liveness

The `Ping` and `Pong` hooks tell you a frame arrived. Detecting a dead peer is a
timer you own:

- send a ping on an interval from your own goroutine
- reset a deadline on **any** pong — ignore the payload
- if the deadline passes with no pong, the peer is gone; close

Ignore the payload because two conforming behaviours break matching: RFC 6455
5.5.3 permits unsolicited pongs, and 5.5.2 permits answering only the most recent
ping when several are outstanding.

`FrameReadTimeout` does not cover this. It fires when *no bytes arrive* — a peer
sending data while ignoring your pings looks perfectly alive to it.

## Configuration

`SetConfig` takes the whole `ListenerConfig` by copy, under a lock, so it is
safe from any goroutine and from a hook inside the read loop. The connection is
not in it — that is `NewListener`'s, and fixed for the life of the Listener.

| field | |
|---|---|
| `PeerIsClient` | which side the peer is on, which decides masking (5.1) |
| `MaxMsgPayloadByteLen` | payload budget for one message |
| `MaxMsgFrameCount` | how many frames one message may arrive in |
| `FrameReadTimeout` | per frame, armed before each read |

**All of it, every time.** What you leave out is set to its zero value, not left
alone, so changing one thing means reading the rest back first:

```go
config := listener.GetConfig() // a copy of what it is running on
config.MaxMsgFrameCount = 4000
listener.SetConfig(config)
```

That round trip is also how a hook changes something mid run. A frame already
being routed finishes on the values it started with, so a change lands on the
next frame rather than halfway through this one.

Nothing is required. Every item has a working zero value, so a Listener this was
never called on still reads — though `PeerIsClient` is the one to get right,
since its zero value says the peer is a server and a server that leaves it
refuses every frame a client sends.

`MaxMsgPayloadByteLen` is the one worth setting. A peer can claim a 10 GB
payload in a 10 byte header, and without a limit that claim becomes a 10 GB
allocation before a single payload byte arrives.

Each item carries its own detail — what the zero value means, which frames it
covers, and how to pick a number:

```bash
go doc github.com/weilun-shrimp/wlgows/v3.ListenerConfig
```

## Hooks

Called one at a time from the read loop. A hook **blocks the loop** for as long
as it runs — no frames are read meanwhile, including pings waiting to be
answered — so a slow one should hand off to a goroutine or a queue of its own.
They are not called concurrently, which keeps the message ordering TCP and RFC
6455 5.4 give you for free.

A nil hook drops those frames.

| hook | receives | what it asks of you |
|---|---|---|
| `Ping` | one frame | 5.5.2: MUST answer with a pong echoing the payload, unless a close already arrived |
| `Pong` | one frame | 5.5.3: MUST NOT answer |
| `Close` | one frame | 5.5.1: MUST answer with a close, then close. `Frame.GetClosePayload` decodes the status code and reason, both already checked |
| `Text` | a whole message | nothing — the payload is already checked as valid UTF-8 (5.6, 8.1) |
| `Binary` | a whole message | nothing — arbitrary bytes |
| `Data` | one data frame | **handle with care.** Watch for FIN, and check 5.6 UTF-8 yourself on a text message: answer 1007. Only for a message too large to hold — otherwise use `Text` or `Binary` |
| `Unknown` | one frame | an opcode 5.2 reserves. A protocol error: answer 1002 and close |

`Text` and `Binary` receive complete messages, assembled across every fragment.
You never see fragmentation, and a control frame arriving between two fragments
goes to its own hook without disturbing the message being assembled.

A text message is checked against RFC 6455 5.6 before it reaches `Text`, and the
check is on the **joined** bytes: a frame may end halfway through a rune, so per
frame validation would reject a conforming message. `Binary` is never checked — 5.6
makes binary payloads arbitrary bytes.

`Data` replaces that assembly: each data frame goes straight to it and none is
kept, so `Text` and `Binary` never run. It is the sharp edge of the config —
take it only when you want the frames themselves, a transmission too large to
hold or a stream you forward on, and only knowing exactly what comes with them.
A message that fits in memory belongs to `Text` or `Binary`, which discharge all
of it for you.

What comes with them: FIN is yours to watch for, and 5.6 cannot be judged on one
frame — it may end mid rune — so nothing checks it.
`Listener.GetCurrentMsgOpcode` says whether the message is text and owes you that
check, since a continuation frame does not carry the type. The budgets, 5.4 and
the empty continuation drop are unchanged, so these are the frames the message is
made of. Set it between messages, not during one.

Receiving a message straight to disk, however large — memory stays flat because
nothing is held between frames:

```go
out, err := os.Create("./received.bin")
if err != nil {
	return err
}
defer out.Close()

listener := wlgows.NewListener(conn)
listener.SetConfig(wlgows.ListenerConfig{
	PeerIsClient:         true,
	MaxMsgPayloadByteLen: 2 * 1024 * 1024 * 1024, // the whole message, so size it for the stream
	FrameReadTimeout:     60 * time.Second,

	Data: func(f *wlgows.Frame) {
		// Binary only. A text message would need 5.6 checked on the joined
		// bytes, which is why the opcode is worth asking for.
		if listener.GetCurrentMsgOpcode() != wlgows.OpcodeBinary {
			// Refuse it — close 1003, or whatever your protocol says.
			listener.PauseListen()
			return
		}
		if _, err := out.Write(f.PayloadData); err != nil {
			log.Println("write:", err)
			listener.PauseListen()
			return
		}
		if f.FIN { // nothing else marks the end
			log.Println("message complete")
		}
	},
	Ping: func(f *wlgows.Frame) {
		// 5.5.2: pong back, echoing f.PayloadData.
	},
	Close: func(f *wlgows.Frame) {
		// 5.5.1: answer with a close, then stop.
		listener.PauseListen()
	},
})

return listener.Listen()
```

`MaxMsgPayloadByteLen` is the one to think about here. It is still spent across
the whole message, so it has to cover the entire stream — and since it is also
what bounds a single frame at the header, a budget that large lets one frame
claim it all. Bounding every frame tightly while letting the message run long is
the one thing this cannot express — `Conn.GetNextFrame(max)` can, because that
limit is per read rather than per message.

[`stream_server`](./example/stream_server/main.go) is this hook end to end: a
file received frame by frame, hashed on the way past, with the budget sized for
the whole stream.

**After a close frame, stop reading.** RFC 6455 5.5.1 says an endpoint MUST NOT
process any further data frames once a Close has arrived. The Listener does not
enforce that — it hands the frame to `Close` and reads on — so your `Close` hook
must call `PauseListen`, or a message arriving after the close will still reach
`Text` or `Binary`.

## Errors

`Listen` returns `nil` in exactly one case: `PauseListen` was called. It does
not say **who** called it. If more than one place in your code pauses — a close
frame in one, a shutdown signal in another — `nil` cannot tell them apart, and
recording the reason is yours to do.

Every other return carries an error. `io.EOF` is not the polite goodbye it
looks like: it means the peer dropped the TCP connection **without** a close
frame, which is what §7.4.1 calls 1006.

Those errors fall into four groups, and they do not get the same answer.
Sorting them is the whole job.

### 1. A frame broke a rule

The peer violated RFC 6455. Answer with a close frame, then close.
`StandardClosePayloadFor` is the 7.4.1 table:

| error | answer with |
|---|---|
| `ErrReservedBitsSet` | 1002 |
| `ErrFrameNotMasked`, `ErrFrameMasked` | 1002 |
| `ErrControlFrameFragmented` | 1002 |
| `ErrControlFramePayloadTooLong` | 1002 |
| `ErrClosePayloadTooShort` | 1002 |
| `ErrContinuationFrameWithoutMsg`, `ErrDataFrameDuringMsg` | 1002 |
| `ErrInvalidCloseStatusCode` | 1002 |
| `ErrInvalidUTF8` | 1007 |
| `ErrFrameByteLengthExceeded` | 1009 |
| `ErrMsgFrameCountExceeded` | 1009 |

**The stream is unusable after any of them.** A refused frame was already partly
read, so the next read starts mid frame and parses payload bytes as a header.
Never resume — the write direction is independent, so the close frame still
goes out, but you must not read again.

### 2. The connection failed

`io.EOF`, `os.ErrDeadlineExceeded`, `net.ErrClosed`, connection reset. These
come from the socket, not from this package, and the peer broke no rule. There
is nothing to send a close frame to. §7.4.1 calls this **1006 abnormal closure**
and forbids 1006 on the wire, because it is what an endpoint records about
itself — so record it and close.

A read timeout is your own `FrameReadTimeout` policy rather than a peer fault.
The socket is usually still writable, so you *may* send 1000 or 1001 first. Only
you can decide which, so `StandardClosePayloadFor` will not decide it for you.

### 3. You misused the Listener

`ErrListenerConnIsNil` when the Listener was built by hand instead of by
`NewListener`, so it has no connection to read from, and
`ErrListenerIsListening` when a second `Listen` overlaps the first. Both are
returned before a single byte is read.

**Do not close the connection on these.** `ErrListenerIsListening` means another
goroutine holds a running `Listen` — closing would end its healthy session.
These are bugs in your code, not conditions to handle.

### 4. Something else entirely

A plain `errors.New("...")` from a custom `net.Conn`, a TLS layer or a mock,
surfacing through `GetNextFrame`. This package cannot say whose fault it is.

Treat it like group 2 — close, send nothing. §7.1.1 permits closing with no
close frame, so silence is always legal, while guessing 1002 blames a peer that
may have done nothing wrong, and the peer cannot recover from a wrong status
code. If you know what your own error means, map it yourself **before** calling
`StandardClosePayloadFor`, since it will only return `nil`.

### Putting it together

`StandardClosePayloadFor` returns a payload for group 1 and `nil` for everything
else. `nil` is *no attribution*, not *the connection is healthy* and not *close
the connection* — groups 2 and 4 want a close, group 3 must not get one:

```go
err := listener.Listen()
switch {
case err == nil: // PauseListen, nothing to answer

case errors.Is(err, wlgows.ErrListenerConnIsNil),
	errors.Is(err, wlgows.ErrListenerIsListening):
	log.Printf("listener misuse: %v", err) // group 3: leave the connection alone

default:
	if payload := wlgows.StandardClosePayloadFor(err); payload != nil {
		// group 1: answer with it before closing
	}
	// groups 1 and 2 both want the connection closed, and 7.1.1 says which side
	// does it first. Both calls are yours.
}
```

`Reason` is left empty. Whether to tell the peer what went wrong is a policy
this package has no opinion on — fill it in before sending if you want one.

A reserved opcode never reaches this path. It is not an error: the frame goes to
the `Unknown` hook untouched, and answering 1002 there is yours to do.

## Pausing and resuming

`PauseListen` ends the run. It is safe from another goroutine and safe to call
when nothing is listening.

The pause is only noticed **between frames**, because the loop spends its time
parked inside `GetNextFrame`. On a silent peer `PauseListen` returns at once
while the read stays blocked — closing the connection is the only thing that
unblocks it, and that is yours to do.

Starting again resumes where it left off. A message half assembled when you
paused is still open, so the configuration may be changed in between without
losing frames.
