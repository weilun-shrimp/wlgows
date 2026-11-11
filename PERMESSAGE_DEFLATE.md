**English** · [繁體中文](./PERMESSAGE_DEFLATE.zh-TW.md)

# permessage-deflate

What each field of `PermessageDeflateConfig` means, and how a config is written
in `Sec-WebSocket-Extensions` (RFC 7692). How each side uses it is in
[CLIENT.md](./CLIENT.md#compression-permessage-deflate) and
[SERVER.md](./SERVER.md#compression-permessage-deflate). Sending compressed
messages is in [SENDING.md](./SENDING.md#compression-permessage-deflate).
Reading them is in [READING.md](./READING.md#compressed-messages).

## Contents

- [The window and context takeover](#the-window-and-context-takeover)
- [PermessageDeflateConfig](#permessagedeflateconfig)
- [The header format](#the-header-format) — [invalid entries](#invalid-entries)
- [Windows under 15 bits](#windows-under-15-bits)
- [Memory](#memory) — [per connection](#per-connection) · [many connections](#many-connections)
- [Large data: compress it yourself](#large-data-compress-it-yourself)

## The window and context takeover

The window is how far back a compressed message may point to repeat earlier
bytes: 2^N bytes, N from 8 to 15. Without a limit, it is 15 bits, 32 KB.

Context takeover decides what a message may point back into (RFC 7692 7.2.1):

- **With context**, the default: the current message and earlier ones, as one
  stream. Small repeated messages compress well, but the sender and the
  receiver both keep the window between messages.
- **Without context**: the current message only. Each message starts with an
  empty window, so nothing is kept between messages.

Each side's sending is set on its own. The `client_` params limit what the
client sends, and the `server_` params limit what the server sends.

## PermessageDeflateConfig

**We recommend setting the header with `PermessageDeflateConfig`.** The
handshakes write `Sec-WebSocket-Extensions` from your config and check it, so
it follows RFC 7692. A header you write yourself can break those rules.

Each field is named after one param:

| Field | Param | Means |
|---|---|---|
| `ClientNoContextTakeover` | `client_no_context_takeover` | The client sends without context (RFC 7692 7.1.1.2). |
| `ServerNoContextTakeover` | `server_no_context_takeover` | The server sends without context (RFC 7692 7.1.1.1). |
| `ClientMaxWindowBitsEnabled` | `client_max_window_bits` | The param is sent (RFC 7692 7.1.2.2). |
| `ClientMaxWindowBits` | its value | The client's window is at most 2^N bytes. 8 to 15, or 0 for no value. Ignored when `ClientMaxWindowBitsEnabled` is false. |
| `ServerMaxWindowBits` | `server_max_window_bits` | The server's window is at most 2^N bytes. 8 to 15, or 0 to leave the param out (RFC 7692 7.1.2.1). |

The zero value is plain `permessage-deflate`: both sides keep context, and both
may use 15 bits.

**We recommend sending without context, on both sides, in every case.** With
context, each connection keeps its own writer of about 1 MB. Without context,
connections share writers, and an idle connection holds none. See
[Memory](#memory). But it is still up to you: do what you want if you know what
you are doing.

```go
wlgows.PermessageDeflateConfig{
	ServerNoContextTakeover: true, // the server sends without context
	ClientNoContextTakeover: true, // the client sends without context
}
```

Use the same config on both sides: in the client's offer, and in the server's
list.

`client_max_window_bits` is the only param that may come without a value, and
only in an offer. There it means the client supports the param and lets the
server pick the value. An answer must give it a value (RFC 7692 7.1.2.2).
`server_max_window_bits` is never sent without a value. `ServerMaxWindowBits`
0 leaves the param out.

## The header format

`Format()` gives the header text of a config. Use it for logs. We don't
recommend using it to set the header yourself:

```go
log.Printf("offering %s", deflateConfig.Format())
```

### Invalid entries

An entry with any of these breaks RFC 7692 7. A server declines such an offer,
and a client fails the connection on such an answer:

- a param other than the four above;
- the same param twice;
- a value on `server_no_context_takeover` or `client_no_context_takeover`;
- `server_max_window_bits` with no value;
- window bits that are not a decimal 8 to 15 with no leading zero, such as `7`,
  `16` or `010` (RFC 7692 7.1.2.1).

`PermessageDeflateConfig` and the handshakes never write such an entry. This is
why we recommend setting the header with them, see
[PermessageDeflateConfig](#permessagedeflateconfig).

## Windows under 15 bits

wlgows compresses with Go's `compress/flate`, which cannot send with a window
under 15 bits. So a wlgows peer never agrees to send with 8 to 14 bits (library
choice). Which configs each side refuses for this is in
[CLIENT.md](./CLIENT.md#compression-permessage-deflate) and
[SERVER.md](./SERVER.md#compression-permessage-deflate).

Receiving has no such limit. Go's inflater reads any window up to 15 bits, so a
wlgows peer accepts any window the other side sends with.

## Memory

Compression costs memory. These sizes come from Go's `compress/flate`, measured
with Go 1.26. They change a little between Go versions.

| Part | Size |
|---|---|
| A writer, for sending | about 1170 KB at `BestSpeed`, about 790 KB at levels 2 to 9, about 715 KB at `HuffmanOnly` or `NoCompression` |
| A reader, for receiving | about 40 KB |
| The history, with context | up to about 64 KB |

The history is a copy of the last bytes sent or received. With context, a
message may point back into it (RFC 7692 7.2.1).

### Per connection

What one connection keeps after it has sent and received one compressed
message, at `BestSpeed`:

| This side sends | The peer sends | One connection keeps |
|---|---|---|
| with context | with context | about 1340 KB: writer 1170 + history 64 + reader 40 + history 64 |
| with context | without context | about 1234 KB: writer 1170 + history 64. Add 40 KB for the reader if you use the Data hook. |
| without context | with context | about 104 KB: reader 40 + history 64 |
| without context | without context | 0. Add 40 KB for the reader if you use the Data hook. |

With context, one connection keeps over 1 MB. Without context, it keeps almost
nothing. This is why we recommend no context takeover on both sides. See
[PermessageDeflateConfig](#permessagedeflateconfig).

- At another level, use that level's writer size from the table above.
- Not counted:
  - the result buffer, which grows to your largest compressed message, see
    [SENDING.md](./SENDING.md#memory-the-result-buffer);
  - frames waiting to be read;
  - a writer or reader taken from the pool while a message is sent or read.
- While a stream is open, a connection that sends without context also holds a
  writer and a history, as in the "with context" rows.

### Many connections

With context, each connection holds a writer once it has sent one compressed
message. 10 000 connections at `BestSpeed` hold about 12 GB.

Without context, writers are shared. Only the connections sending at the same
moment hold one.

- A server sends without context when its config sets
  `ServerNoContextTakeover`, or when the client's offer asks for it
  (RFC 7692 7.1.1.1).
- A wlgows client sends without context when its offer has
  `ClientNoContextTakeover`, or when the server's answer has it
  (RFC 7692 7.1.1.2).

Each param limits only the sending of the side it names. The cost of sending
without context: small messages that repeat compress less well.

## Large data: compress it yourself

**We don't recommend permessage-deflate for large data.** Compress it before you
send it, and send it without deflate. The receiver decompresses it itself.

Why:

- A stream with deflate always uses the connection's own writer, about 1 MB,
  even without context. See [Per connection](#per-connection).
- permessage-deflate compresses again for each connection. Data you compress
  yourself is compressed once, and the same bytes can go to every connection.
- You choose the format, such as gzip or zstd. The receiver can store it as it
  is, without decompressing.

Both sides must agree on the format. That is up to your app, not the WebSocket
protocol.
