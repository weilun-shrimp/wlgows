**English** · [繁體中文](./READING.zh-TW.md)

# Reading

This guide shows how to read messages from the peer.

Reading has two steps:

1. A `Listener` reads frames and gives you each whole message as `DataFrames`.
2. `conn.DataFramesReader(frames)` gives you the bytes of that message. It
   decompresses the message when it is compressed, and checks text as UTF-8.

Getting a `Conn` is in [SERVER.md](./SERVER.md) and [CLIENT.md](./CLIENT.md).
Sending is in [SENDING.md](./SENDING.md). How the Listener works is in
[LISTENER.md](./LISTENER.md).

## Contents

- [Quick start](#quick-start) — read every message of one connection
- [DataFramesReader](#dataframesreader) — the rules for using the reader
- [Limiting the size](#limiting-the-size) — `NewSizeLimitReader` and close code 1009
- [Two recommended ways to read a message](#two-recommended-ways-to-read-a-message) — `io.ReadAll`, `ReadMessageInto`, and step by step (careful)
- [Errors](#errors) — what each error means and which close code answers it
- [Compressed messages](#compressed-messages) — what you must do when the peer compresses
- [Large messages: the Data hook](#large-messages-the-data-hook) — one frame at a time, compressed or not
- [Reading without a copy](#reading-without-a-copy) — advanced: binary, and text with `unsafe`
- [Reading without a Listener](#reading-without-a-listener) — not recommended
- [Memory: the connection's read buffer](#memory-the-connections-read-buffer)
- [Concurrency](#concurrency) — one reader, messages in order

## Quick start

`conn` is a `*wlgows.Conn` after the handshake. This code reads text and binary
messages up to 10 MB each, and closes the connection when anything goes wrong.

```go
const maxMessageSize = 10 * 1024 * 1024 // 10 MB, after decompressing

// readMessage reads one message into buf, and returns the part of buf it
// filled.
func readMessage(conn *wlgows.Conn, frames wlgows.DataFrames, buf []byte) ([]byte, error) {
	reader, err := conn.DataFramesReader(frames)
	if err != nil {
		return nil, err
	}
	defer reader.Close() // always close it

	n, err := wlgows.ReadMessageInto(
		wlgows.NewSizeLimitReader(reader, maxMessageSize), 
		buf,
	)
	if err != nil {
		return nil, err // ErrSizeLimitExceeded, ErrInvalidUTF8, ...
	}
	return buf[:n], nil
}

func handleConn(conn *wlgows.Conn) {
	defer conn.Close()

	listener := conn.NewStandardListener() // answers ping and close for you

	// One buffer for every message of this connection. A message lives in it
	// only until the next one: copy what you keep.
	buf := make([]byte, maxMessageSize)

	config := listener.GetConfig()
	config.MaxDataFramesSize = maxMessageSize // bytes as they arrive
	config.MaxFrameSize = maxMessageSize
	config.MaxDataFrameCount = maxMessageSize / 256
	config.Text = func(frames wlgows.DataFrames) {
		message, err := readMessage(conn, frames, buf)
		if err != nil {
			listener.PauseListen(err) // Listen returns err
			return
		}
		log.Printf("text: %s", message)
	}
	config.Binary = func(frames wlgows.DataFrames) {
		message, err := readMessage(conn, frames, buf)
		if err != nil {
			listener.PauseListen(err)
			return
		}
		log.Printf("binary: %d bytes", len(message))
	}
	listener.SetConfig(config)

	err := listener.Listen()
	if err == nil {
		return // the peer closed
	}
	// A close code for errors that the peer caused, nil for the others.
	if payload := wlgows.StandardClosePayloadFor(err); payload != nil {
		conn.SendClose(payload)
	}
	log.Println("closing:", err)
}
```

The full list of `Listen` errors is in
[LISTENER.md](./LISTENER.md#errors).

## DataFramesReader

`conn.DataFramesReader(frames)` returns an `io.ReadCloser`. Follow these rules:

- **Read until `io.EOF`.** Only then is the message complete and checked.
- **Always call `Close`**, also after `Read` returns an error. `defer` is the
  easy way.
- Do not call `Close` while a `Read` is running.
- Do not call `Read` after `Close`.
- Do not change the frames until `Close`. The reader reads their payloads in
  place, without a copy.

The reader works for every message:

| The message is | `Read` gives you |
|---|---|
| not compressed | the payloads, one after the other |
| compressed ([permessage-deflate](./PERMESSAGE_DEFLATE.md)) | the decompressed bytes |
| text | the same bytes, checked as UTF-8 |

You do not need to know which one it is. The reader finds out from the frames.

## Limiting the size

`MaxDataFramesSize` in the Listener counts the bytes that arrive. A compressed
message can be much bigger after decompressing. 1 KB of compressed data can
become many MB.

So limit the bytes you read. Put the reader inside
`wlgows.NewSizeLimitReader`:

```go
reader, err := conn.DataFramesReader(frames)
if err != nil {
	return err
}
defer reader.Close() // always close it

sizeLimitReader := wlgows.NewSizeLimitReader(reader, maxMessageSize)
```

The size limit reader:

- gives you the bytes of the message, up to `maxMessageSize`;
- returns `ErrSizeLimitExceeded` from `Read` when the message has more. It
  reads one byte more to know this, and never gives you that byte;
- returns `io.EOF` only when the whole message fits.

[`ReadMessageInto`](#readmessageinto) has the same limit: `len(buf)`.

When you get `ErrSizeLimitExceeded`:

1. Give it to `listener.PauseListen(err)`. `Listen` returns it.
2. `StandardClosePayloadFor(err)` gives close code 1009 (RFC 6455 7.4.1).
3. Send that close, then close the connection.

The [quick start](#quick-start) does all three.

## Two recommended ways to read a message

All three read from `sizeLimitReader`, as in
[Limiting the size](#limiting-the-size).

| Way | Allocates | Memory used | You get the message |
|---|---|---|---|
| `io.ReadAll` | many times, as it grows | about the message size | whole, after it is checked |
| `ReadMessageInto` | once | `maxMessageSize`, for any message | whole, after it is checked |
| Step by step (careful) | once | only your buffer, smaller than the message | in parts, before it is checked |

### `io.ReadAll`

```go
sizeLimitReader := wlgows.NewSizeLimitReader(reader, maxMessageSize)
message, err := io.ReadAll(sizeLimitReader)
if err != nil {
	return err // ErrSizeLimitExceeded, ErrInvalidUTF8, ...
}
```

The simple way. But `io.ReadAll` does not know the size up front: for a big
message, it allocates again each time its buffer is full.

### `ReadMessageInto`

```go
sizeLimitReader := wlgows.NewSizeLimitReader(reader, maxMessageSize)
buf := make([]byte, maxMessageSize)
n, err := wlgows.ReadMessageInto(sizeLimitReader, buf)
if err != nil {
	return err // ErrSizeLimitExceeded, ErrInvalidUTF8, ...
}
message := buf[:n] // the whole message
```

It allocates once. You can use `buf` again for every message, as the
[quick start](#quick-start) does. But it always takes `maxMessageSize` bytes,
even for a 10 byte message.

`json.Unmarshal` from `encoding/json` copies what it decodes. So you can use
`buf` again for the next message right after it returns. Other decoders may
keep `buf`: check before you use them.

`nil` means the whole message fits in `buf`. On an error, do not use
`buf[:n]`: it may be cut short, or it failed a check.

### Careful: step by step, into a buffer smaller than the message

**Only when you know exactly what you are doing.** You get parts before the
message is checked. It can still fail after you handled some parts: too big,
not UTF-8 (this comes at the end), or not decompressing. So use this way only
when you can throw away what you handled, for example delete a half-written
file.

```go
sizeLimitReader := wlgows.NewSizeLimitReader(reader, maxMessageSize)
buf := make([]byte, 32*1024) // 32 KB, used again for every part
for {
	n, err := sizeLimitReader.Read(buf)
	handle(buf[:n]) // the next part of the message
	if err == io.EOF {
		return nil // the whole message, within the size
	}
	if err != nil {
		return err // ErrSizeLimitExceeded: stop here
	}
}
```

Memory stays at one buffer, whatever the message size. You get each part as it
comes, and you stop as soon as the message is too big.

Handle `buf[:n]` before you check `err`. A `Read` can give bytes and an error
together, `io.EOF` included.

`io.Copy(file, sizeLimitReader)` does the same loop for you. When the message
is too big, it returns `ErrSizeLimitExceeded`, and the file has only the first
`maxMessageSize` bytes.

## Errors

Errors from reading:

| Error | Meaning | Close code |
|---|---|---|
| `ErrInvalidUTF8` | a text message is not valid UTF-8. It comes in place of `io.EOF` | 1007 |
| `ErrInflateDataInvalid` | a compressed message cannot be decompressed | 1007 |
| `ErrSizeLimitExceeded` | the message is bigger than you allow. From `ReadMessageInto` or `NewSizeLimitReader` | 1009 |

`StandardClosePayloadFor(err)` gives the close payload for all three.

Errors from `DataFramesReader` itself:

| Error | Meaning |
|---|---|
| `ErrDataFramesNotEnded` | no frames, or the last frame has no FIN |
| `ErrContinuationFrameWithoutMsg`, `ErrDataFrameDuringMsg` | the frames are not one message |
| `ErrReservedBitsSet` | RSV1 is set where it is not allowed |

Frames from a Listener's `Text` or `Binary` hook never cause these. They only
come when you collect the frames yourself.

## Compressed messages

When the handshake agreed on [permessage-deflate](./PERMESSAGE_DEFLATE.md), the
peer may compress its messages. `DataFramesReader` decompresses them for you.

The Listener does not decompress. So for a compressed text message:

- the `Text` hook gets it **without** a UTF-8 check;
- `DataFramesReader` checks it after decompressing.

Use `DataFramesReader` for every message, and every text message gets checked.

### When the peer keeps its context

The peer can compress each message with the help of the messages before it.
This is called context takeover (RFC 7692 7.1.1). The peer decides this in the
handshake. Then:

- **Every compressed message must go through `DataFramesReader`, in the order
  it arrived.** Skipping one breaks the next one.
- **Read each one to `io.EOF`.** If you call `Close` before `io.EOF`, the next
  compressed message may decompress to wrong bytes, with no error.
- **Finish one message before you start the next.** `DataFramesReader` for the
  next compressed message waits until the reader before it returns an error
  (`io.EOF` included) or is closed. If you call it in the same goroutine before
  that, it waits forever.

Reading inside the `Text` and `Binary` hooks, as in the
[quick start](#quick-start), follows all three rules.

When the peer agreed to no context takeover, each message stands alone. Closing
early is then safe.

To know how compression uses your memory, and how much, see
[PERMESSAGE_DEFLATE.md](./PERMESSAGE_DEFLATE.md#memory).

## Large messages: the Data hook

The `Text` and `Binary` hooks get a whole message, so it must fit in memory.
For a message too large to hold, set the `Data` hook. It gets each data frame
as it arrives, and the Listener keeps none of them.

Then some work is yours:

- Watch `frame.FIN`. Nothing else marks the end of the message.
- A continuation frame has no type. Ask `listener.GetCurrentMsgOpcode()`.
- A text message must be valid UTF-8 (RFC 6455 5.6). One frame alone cannot be
  checked, because a character can be split across two frames. Check it
  yourself.
- `MaxDataFramesSize` now covers the whole stream. Set `MaxFrameSize` too, so
  one frame stays small.

### Not compressed

This writes a binary message straight to a file. Memory stays small, whatever
the message size:

```go
out, err := os.Create("./received.bin")
if err != nil {
	return err
}
defer out.Close()

listener := conn.NewStandardListener()
config := listener.GetConfig()
config.MaxDataFramesSize = 2 * 1024 * 1024 * 1024 // the whole stream
config.MaxFrameSize = 64 * 1024                    // one frame
config.MaxDataFrameCount = 0                       // no limit: frames are not kept
config.Data = func(frame *wlgows.Frame) {
	if listener.GetCurrentMsgOpcode() != wlgows.OpcodeBinary {
		listener.PauseListen(errors.New("peer sent text"))
		return
	}
	if listener.IsCurrentMsgDeflated() {
		listener.PauseListen(errors.New("peer sent a compressed message"))
		return
	}
	if _, err := out.Write(frame.PayloadData); err != nil {
		listener.PauseListen(err)
		return
	}
	if frame.FIN {
		log.Println("message complete")
	}
}
listener.SetConfig(config)

// When Listen returns before a FIN frame, out has only a part of the message.
// Drop it, for example delete the file.
return listener.Listen()
```

[`stream_server`](./example/stream_server/main.go) does this with a real file:
it receives a file frame by frame and hashes it.

### Compressed

A compressed message is decompressed in four steps:

1. `token := conn.StartInflateDataFrames()` on the first frame.
2. `conn.PutDeflatedDataFrame(token, frame)` for each frame.
3. `conn.PullInflatedBytes(token, buf)` to get decompressed bytes, until
   `io.EOF`.
4. `conn.EndInflateDataFrames(token)` at the end. Always call it, also after an
   error.

Pull in another goroutine, while the frames still arrive. Each frame is let go
once it is decompressed, so memory stays small. When `PullInflatedBytes` needs
more, it waits for the next frame.

This writes a compressed binary message to a file:

```go
var token int64
// pullResult tells the hook how the pull ended: nil when the whole message
// was written, else why it stopped. A value also means the pull returned, so
// EndInflateDataFrames is safe to call.
// The channel itself is nil when no message is open.
var pullResult chan error

config.Data = func(frame *wlgows.Frame) {
	if !listener.IsCurrentMsgDeflated() || listener.GetCurrentMsgOpcode() != wlgows.OpcodeBinary {
		listener.PauseListen(errors.New("expected a compressed binary message"))
		return
	}
	if frame.Opcode != wlgows.OpcodeContinuation { // the first frame
		token = conn.StartInflateDataFrames()
		pullResult = make(chan error, 1) // room for 1: the send never blocks
		go func(token int64, result chan<- error) {
			result <- writeInflated(conn, token, out)
		}(token, pullResult)
	}
	if err := conn.PutDeflatedDataFrame(token, frame); err != nil {
		listener.PauseListen(err)
		return
	}

	if !frame.FIN {
		select {
		case err := <-pullResult: // the pull stopped early: too big, or not valid
			conn.EndInflateDataFrames(token)
			pullResult = nil
			listener.PauseListen(err)
		default:
		}
		return
	}

	err := <-pullResult // the whole message is put: wait for the pull to finish
	conn.EndInflateDataFrames(token)
	pullResult = nil
	if err != nil {
		listener.PauseListen(err)
	}
}
listener.SetConfig(config)

err := listener.Listen()
if pullResult != nil { // stopped in the middle of a message
	conn.PutDeflatedDataFrame(token, &wlgows.Frame{FIN: true}) // the pull returns
	<-pullResult
	conn.EndInflateDataFrames(token)
}
return err
```

```go
// writeInflated pulls the decompressed message into out, up to maxMessageSize.
func writeInflated(conn *wlgows.Conn, token int64, out io.Writer) error {
	buf := make([]byte, 32*1024)
	written := 0
	for {
		n, err := conn.PullInflatedBytes(token, buf)
		written += n
		if written > maxMessageSize {
			return wlgows.ErrSizeLimitExceeded
		}
		if _, writeErr := out.Write(buf[:n]); writeErr != nil {
			return writeErr
		}
		if err == io.EOF {
			return nil
		}
		if err != nil {
			// ErrInflateDataInvalid: close with 1007.
			// On any error, or when the message stops before FIN, out has only
			// a part of the message: drop it, for example delete the file.
			return err
		}
	}
}
```

**Stopping in the middle of a message.** When `Listen` returns before the FIN
frame, the pull still waits for the next frame. Put a FIN frame with no
payload: the pull then returns. Wait for it, then call `EndInflateDataFrames`.
What it wrote is not the whole message.

The rules from [When the peer keeps its context](#when-the-peer-keeps-its-context)
apply here too. If you end a message before `io.EOF`, the next compressed
message may decompress to wrong bytes.

`StartInflateDataFrames` waits while another message is open, so end each
message before the next one starts.

## Reading without a copy

**Advanced. Only if you know exactly what you are doing.** If you are not
sure, use `DataFramesReader`.

A message of **one frame, not compressed**, is already in memory: the frame's
payload. wlgows never writes to a payload after reading it, and each frame has
its own. So you can use the payload without `DataFramesReader` and without a
copy.

Other messages, of several frames or compressed, must go through
`DataFramesReader`.

### Binary

```go
config.Binary = func(frames wlgows.DataFrames) {
	if len(frames) != 1 || frames[0].RSV1 {
		message, err := readMessage(conn, frames, buf) // from the quick start
		if err != nil {
			listener.PauseListen(err)
			return
		}
		handle(message)
		return
	}
	handle(frames[0].PayloadData) // the whole message, no copy
}
```

No `unsafe` is needed: the payload is already a `[]byte`. Do not write to it.

### Text: `unsafe`

The warning in
[the sending guide](./SENDING.md#faster-and-less-memory-sending-a-string-with-unsafe)
applies here too.

A text message can become a string without a copy:

```go
config.Text = func(frames wlgows.DataFrames) {
	if len(frames) != 1 || frames[0].RSV1 {
		message, err := readMessage(conn, frames, buf) // from the quick start
		if err != nil {
			listener.PauseListen(err)
			return
		}
		handle(string(message))
		return
	}
	payload := frames[0].PayloadData
	handle(unsafe.String(unsafe.SliceData(payload), len(payload)))
}
```

The message is then held once in memory, not twice.

- **Never write to `payload` afterwards.** The string would change too. Go
  expects strings never to change, so a map that uses the string as a key
  breaks.
- **The string keeps the whole payload in memory.** To keep a small part of a
  big message, copy it with `strings.Clone`.
- The Listener already checked the bytes as UTF-8 before `Text` runs. This is
  true only because the message is not compressed.

## Reading without a Listener

**Not recommended.** Without a Listener, every rule RFC 6455 gives a reader is
yours.

A frame is read in two steps:

1. `conn.GetNextFrameHeader()` returns the frame with its header and no
   payload. You can check the size before anything is allocated.
2. `conn.ReadFramePayload(frame)` reads the payload.

Hold `conn.LockRead()` across both steps, so no other reader takes bytes in
between:

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
	if wlgows.IsDataOpcode(frame.Opcode) {
		frames = append(frames, frame)
		if frame.FIN {
			break
		}
	} else {
		// A control frame or a reserved opcode: your code here.
	}
}

message, err := readMessage(conn, frames, buf) // from the quick start
```

## Memory: the connection's read buffer

Each connection reads its frames through one `*bufio.Reader`: the reader given
to the handshake. Its size is the memory each connection keeps for reading.

- **Server:** we recommend net/http's reader from `Hijack`. Its size is up to
  net/http, not wlgows. See [SERVER.md](./SERVER.md#http-hijacking).
- **Client:** you create the reader yourself. See [CLIENT.md](./CLIENT.md).

Any size works. A smaller one saves memory and costs a little speed:

```go
connReader := bufio.NewReaderSize(netConn, 16) // 16 is the smallest: bufio raises anything under it to 16
```

16 is enough. The longest frame header is 14 bytes (2 + 8 length + 4 mask), so
it still fits in one read. A bigger buffer only lets small payloads come in the
same read. A large payload needs its own read at any buffer size.

Memory grows with the number of open connections:

| | 16 bytes | 4096 bytes (default) |
|---|---|---|
| Per connection | 16 B | 4096 B |
| 1,000 connections | ~16 KB | ~4 MB |
| 100,000 connections | ~1.6 MB | ~400 MB |

Reads grow with the number of frames that arrive together:

| | 16 bytes | 4096 bytes (default) |
|---|---|---|
| One 10 B frame | 1 read | 1 read |
| 100 frames of 10 B from a client in a row<br>(16 B each with the header, 1600 B total) | ~100 reads | as few as 1 read |
| One 1 MB frame | the payload skips the buffer | the payload skips the buffer |

The 100-frame row assumes all the bytes have already arrived. With a slow peer,
the difference is smaller.

For the smallest memory of a whole idle connection, see
[The smallest connection](./SENDING.md#the-smallest-connection).

## Concurrency

**Read frames from one goroutine.** A lock keeps two readers from mixing bytes,
but each reader would still get random frames. A Listener is one reader, so do
not read frames yourself while it runs.

`conn.Read` comes from the embedded `net.Conn` and skips the lock. Read frames
through a Listener, or with `GetNextFrameHeader` and `ReadFramePayload` under
`LockRead`.

**Call `DataFramesReader` in the order the messages arrived.** For a peer that
keeps its context, this order is required (see
[When the peer keeps its context](#when-the-peer-keeps-its-context)). If you
give messages to other goroutines, read them there in order, one after another.
The simple way: read inside the hook.
