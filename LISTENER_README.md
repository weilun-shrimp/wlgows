# Listener

`Listener` runs the read loop for you. It reads frames off a connection,
refuses the ones RFC 6455 forbids, assembles fragmented messages, and calls the
hook you configured for each opcode.

It knows the protocol's **shape**, not its **policy**. Every obligation the RFC
puts on a receiver lands on a hook, never inside the Listener.

## Quick start

`conn` is an already handshaken `*wlgows.ServerConn`. This handles one of them
start to finish, and covers every way `Listen` can return. `Pong` is the one
hook left nil on purpose — 5.5.3 says MUST NOT answer a pong, which is exactly
what a nil hook does.

```go
func handleConn(conn *wlgows.ServerConn) {
	defer conn.Close() // the only place this connection is closed

	listener := &wlgows.Listener{}
	if err := listener.SetConfig(wlgows.ListenerConfig{
		Conn:                 conn,
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
			conn.SendPong(f.PayloadData) // 5.5.2: MUST answer, echoing
		},
		Close: func(f *wlgows.Frame) {
			payload, _ := f.GetClosePayload() // the Listener already validated it
			conn.SendClose(payload)           // 5.5.1: MUST answer
			listener.PauseListen()            // 5.5.1: and read nothing further
		},
		Unknown: func(f *wlgows.Frame) {
			conn.SendClose(&wlgows.ClosePayload{StatusCode: wlgows.CloseProtocolError})
			listener.PauseListen()
		},
	}); err != nil {
		log.Println("config:", err)
		return
	}

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
			conn.SendClose(payload)
		}
		log.Println("closing:", err)
	}
}
```

Every `Send*` call above is yours, in a hook — the Listener writes nothing. The
one `defer conn.Close()` is the only close, since the hooks pause the loop and
let this function return rather than closing underneath it.

This server never sends a message of its own: v3 has `SendClose`, `SendPing` and
`SendPong` and no data frame API yet.

## Three things it will not do for you

### It never closes the connection

Not on a close frame, not on a read error, not on a payload over the limit, not
on an unknown opcode. It reports and returns. `Listen` returning an error is a
reason for **you** to close, never a sign that it already did.

That is not tidiness. RFC 6455 7.1.1 gives the two sides different obligations —
a server MUST close the TCP connection immediately once close frames have
crossed, while a client SHOULD wait for the server to close and may give up only
after a reasonable delay. A Listener does not know which side it is on.

Closing is also not free to get wrong: `Conn.Close` does not take the write
lock, so calling it while another goroutine is mid-write truncates that frame on
the wire and the peer records 1006 instead of the status code you meant to send.
Sequencing the close against your own writes is something only you can do.

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

Set through `SetConfig`, which refuses while a loop is running. Pause, set,
start again.

| field | |
|---|---|
| `Conn` | where frames come from. Required. |
| `PeerIsClient` | which side the peer is on, which decides masking (5.1) |
| `MaxMsgPayloadByteLen` | payload budget for one message |
| `MaxMsgFrameCount` | how many frames one message may arrive in |
| `FrameReadTimeout` | per frame, armed before each read |

`MaxMsgPayloadByteLen` is the one worth setting. A peer can claim a 10 GB
payload in a 10 byte header, and without a limit that claim becomes a 10 GB
allocation before a single payload byte arrives.

Each field carries its own detail — what the zero value means, which frames it
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

| hook | receives | what the RFC asks of you |
|---|---|---|
| `Ping` | one frame | 5.5.2: MUST answer with a pong echoing the payload, unless a close already arrived |
| `Pong` | one frame | 5.5.3: MUST NOT answer |
| `Close` | one frame | 5.5.1: MUST answer with a close, then close. `Frame.GetClosePayload` decodes the status code and reason, both already checked |
| `Text` | a whole message | nothing — the payload is already checked as valid UTF-8 (5.6, 8.1) |
| `Binary` | a whole message | nothing — arbitrary bytes |
| `Unknown` | one frame | an opcode 5.2 reserves. A protocol error: answer 1002 and close |

`Text` and `Binary` receive complete messages, assembled across every fragment.
You never see fragmentation, and a control frame arriving between two fragments
goes to its own hook without disturbing the message being assembled.

A text message is checked against RFC 6455 5.6 before it reaches `Text`, and the
check is on the **joined** bytes: a frame may end halfway through a rune, so per
frame validation would reject a conforming message. `Binary` is never checked — 5.6
makes binary payloads arbitrary bytes.

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

`ErrListenerConnIsNil` when `SetConfig` was never called, and
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
		conn.SendClose(payload) // group 1
	}
	conn.Close() // groups 1 and 2, and the server is the one that closes
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
