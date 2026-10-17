**English** · [繁體中文](./PERMESSAGE_DEFLATE.zh-TW.md)

# permessage-deflate

What each field of `PermessageDeflateConfig` means, and how a config is written
in `Sec-WebSocket-Extensions` (RFC 7692). How each side uses it is in
[CLIENT_README.md](./CLIENT_README.md#compression-permessage-deflate) and
[SERVER_README.md](./SERVER_README.md#compression-permessage-deflate).

## Contents

- [The window and context takeover](#the-window-and-context-takeover)
- [PermessageDeflateConfig](#permessagedeflateconfig)
- [The header format](#the-header-format) — [the grammar](#the-grammar) · [writing a config](#writing-a-config) · [invalid entries](#invalid-entries)
- [Windows under 15 bits](#windows-under-15-bits)

## The window and context takeover

permessage-deflate compresses each message with DEFLATE (RFC 1951). DEFLATE
replaces a repeat with a back-reference: copy this many bytes from that far
back. The window is how far back a reference may point, 2^N bytes for N from 8
to 15. Without a limit, it is 15 bits, 32 KB.

Context takeover decides what a reference may point into (RFC 7692 7.2.1):

- **With context**, the default: the current message and earlier ones, as one
  stream. Small repeated messages compress well, but the sender and the
  receiver both keep the window between messages.
- **Without context**: the current message only. Each message starts with an
  empty window, so nothing is kept between messages.

Each side's sending is set on its own. The `client_` params limit what the
client sends, and the `server_` params limit what the server sends.

## PermessageDeflateConfig

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

`client_max_window_bits` is the only param that may come without a value, and
only in an offer. There it means the client supports the param and lets the
server pick the value. An answer must give it a value (RFC 7692 7.1.2.2).
`server_max_window_bits` is never sent without a value. `ServerMaxWindowBits`
0 leaves the param out.

## The header format

### The grammar

`Sec-WebSocket-Extensions` is a comma list of entries (RFC 6455 9.1). Each entry
is a name followed by `;`-separated params. Each param is a name with an
optional `=` value:

```
Sec-WebSocket-Extensions: permessage-deflate; server_max_window_bits=10; client_max_window_bits, x-other
```

- **A value may be quoted**: `server_max_window_bits="10"` is the same as
  `server_max_window_bits=10`. A `,` or `;` inside quotes does not split
  (RFC 7230 3.2.6).
- **Spaces** around names, values, `;` and `,` are ignored.
- **Several lines** of the header read as one comma list, in order
  (RFC 7230 3.2.2).
- **Empty entries**, such as `a, , b`, are skipped (RFC 7230 7).

### Writing a config

`Format()` writes a config as one entry, with its params in this order:

```
permessage-deflate; server_no_context_takeover; client_no_context_takeover; server_max_window_bits=N; client_max_window_bits[=N]
```

`Format()` is low-level: the handshakes call it for you, and putting its result
on the wire yourself is not recommended unless you know what you are doing.
Printing it, such as in a log or with fmt, is fine:

```go
log.Printf("offering %s", deflateConfig.Format())
```

A param whose field is `false` or 0 is left out:

| Config | Entry |
|---|---|
| `{}` | `permessage-deflate` |
| `{ServerNoContextTakeover: true}` | `permessage-deflate; server_no_context_takeover` |
| `{ServerMaxWindowBits: 10}` | `permessage-deflate; server_max_window_bits=10` |
| `{ClientMaxWindowBitsEnabled: true}` | `permessage-deflate; client_max_window_bits` |
| `{ClientMaxWindowBitsEnabled: true, ClientMaxWindowBits: 12}` | `permessage-deflate; client_max_window_bits=12` |
| `{ClientMaxWindowBits: 12}` | `permessage-deflate` |

Several configs are written as several entries on one line, in order:

```
permessage-deflate; client_no_context_takeover, permessage-deflate
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

## Windows under 15 bits

wlgows compresses with Go's `compress/flate`, which cannot send with a window
under 15 bits. So a wlgows peer never agrees to send with 8 to 14 bits (library
choice). Which configs each side refuses for this is in its README.

Receiving has no such limit. Go's inflater reads any window up to 15 bits, so a
wlgows peer accepts any window the other side sends with.
