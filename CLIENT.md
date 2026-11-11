**English** · [繁體中文](./CLIENT.zh-TW.md)

# Client

How a client goes from a URL to a ready `*wlgows.Conn`: dialing, TLS, the
handshake, subprotocols and compression.

## Contents

- [Dialing](#dialing)
- [TLS (wss://)](#tls-wss)
- [The handshake](#the-handshake) — [the config](#the-config) · [adding headers](#adding-headers) · [what it returns](#what-it-returns)
- [Subprotocols](#subprotocols) — [offering](#offering) · [checking the answer](#checking-the-answer) · [requiring one](#requiring-one)
- [Compression (permessage-deflate)](#compression-permessage-deflate) — the config in [PERMESSAGE_DEFLATE.md](./PERMESSAGE_DEFLATE.md)
- [Running the handshake yourself](#running-the-handshake-yourself) — not recommended
- [Next: reading and sending](#next-reading-and-sending)

Reading is in [READING.md](./READING.md), sending in
[SENDING.md](./SENDING.md), and the server side in
[SERVER.md](./SERVER.md).

## Dialing

`DialContext` connects and builds the opening request, and nothing more. It
does not run the handshake, so you can still add headers to the request first.

```go
// Both limit the TCP connect and the TLS handshake. The shorter one applies.
ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
defer cancel()
dialer := &net.Dialer{Timeout: 15 * time.Second}
netConn, request, err := wlgows.DialContext(ctx, "ws://localhost:8001", dialer, nil)
if err != nil {
	panic(err)
}
defer netConn.Close()
```

- **The URL** is `ws://` or `wss://`. `http://` and `https://` work the same as
  `ws://` and `wss://`.
  Without a port, `ws://` dials 80 and `wss://` dials 443 (RFC 6455 3).
- **The context** bounds the TCP connect and, for `wss://`, the TLS handshake.
  It stops mattering once `DialContext` returns. To bound the WebSocket
  handshake and the frames, use `netConn.SetDeadline`.
- **The dialer** sets options such as `Timeout` and `KeepAlive`. `nil` means a
  default one.

`Dial(url, tlsConfig)` is the shorter form: `DialContext` with
`context.Background()` and a default dialer. It has no timeout, so if a `wss://`
server accepts the connection but never answers the TLS handshake, `Dial` waits
forever.

Two rules for opening several connections, both yours:

- **One handshake at a time per server.** RFC 6455 4.1 allows only one
  connection to the same IP address and port to be in the handshake at once.
  Open the next one after the last one's `ClientHandShake` returns.
- **Back off when reconnecting.** After a dropped connection, wait a random
  0 to 5 seconds before the first retry, and longer after each failure (7.2.3).

## TLS (wss://)

Pass a `*tls.Config`. `nil` trusts the system's roots. To trust your own CA:

```go
caCert, _ := os.ReadFile("ca.crt")
caCertPool := x509.NewCertPool()
caCertPool.AppendCertsFromPEM(caCert)

netConn, request, err := wlgows.DialContext(ctx, "wss://localhost:8001", dialer, &tls.Config{RootCAs: caCertPool})
```

## The handshake

`ClientHandShake(netConn, reader, writer, request, config)` adds the upgrade
headers and what `config` asks for, sends the request, reads the answer, checks
it against RFC 6455 4.1, and builds the `Conn`.

```go
reader := bufio.NewReader(netConn)
writer := bufio.NewWriter(netConn)
conn, resp, err := wlgows.ClientHandShake(netConn, reader, writer, request, wlgows.ClientHandShakeConfig{})
if err != nil {
	netConn.Close() // recommended on every handshake error
	return
}
```

- **`reader`** must be the one you go on to read frames from. Reading the answer
  may pull the start of the first frame into it.
- **`writer`** is what every frame is sent through. Its size decides how many
  socket writes a large message takes. See [Memory: the write buffer](./SENDING.md#memory-the-write-buffer).
- **`config`** is what the handshake offers. `ClientHandShakeConfig{}` offers
  nothing and runs a plain handshake. See [The config](#the-config).

### The config

`ClientHandShakeConfig` holds what the handshake adds to the request:

| Field | Adds | See |
|---|---|---|
| `Subprotocols` | `Sec-WebSocket-Protocol`, most preferred first | [Subprotocols](#subprotocols) |
| `PermessageDeflates` | `Sec-WebSocket-Extensions`, one permessage-deflate offer each | [Compression](#compression-permessage-deflate) |

```go
config := wlgows.ClientHandShakeConfig{
	Subprotocols:       []string{"chat.v2", "chat.v1"},
	PermessageDeflates: []wlgows.PermessageDeflateConfig{
		{ServerNoContextTakeover: true, ClientNoContextTakeover: true},
	},
}
conn, resp, err := wlgows.ClientHandShake(netConn, reader, writer, request, config)
```

Each field is added on its own line, after any line of the same header
already in `request`. Your own lines are sent as they are. Setting the same
header by hand and in the config is not recommended unless you know what you
are doing.

### Adding headers

Set them on `request` between `DialContext` and `ClientHandShake`. Every header
you set is sent:

```go
request.Header.Set("Origin", "https://example.com")
request.Header.Set("Authorization", "Bearer "+token)
```

`ClientHandShake` sets only what RFC 6455 4.1 fixes: the method `GET`, the
version `HTTP/1.1`, no body, and `Upgrade`, `Connection`, `Sec-WebSocket-Key` and
`Sec-WebSocket-Version`. It also sets `Host` from the URL when you have not set
one, and adds what `config` asks for.

### What it returns

| Outcome | `conn` | `resp` | `err` |
|---|---|---|---|
| Upgraded | the `Conn` | the 101 | `nil` |
| `request` is `nil` | `nil` | `nil` | `ErrHandshakeRequestNil` |
| The request URL has no host | `nil` | `nil` | `ErrUrlHostEmpty` |
| `config` has an offer wlgows refuses — see [Compression](#compression-permessage-deflate) | `nil` | `nil` | `ErrHandshakeRequestDeflateUnsupported` or `ErrHandshakeRequestDeflateWindowBitsInvalid` |
| The request could not be sent | `nil` | `nil` | why |
| The answer could not be read | `nil` | `nil` | why |
| The server refused, or answered wrong | `nil` | its answer | why |
| Upgraded, but the writer could not be flushed | `nil` | the 101 | the write error |

`conn` is `nil` whenever `err` is not. A refusal comes back as
`ErrHandshakeResponseStatusCodeInvalid`, and `resp.StatusCode` and `resp.Body`
say why. The other ways an answer can be wrong, each a sentinel for
`errors.Is`. `ClientHandShake` closes the connection for every error in this
table:

| Error | The answer |
|---|---|
| `ErrHandshakeResponseProtoInvalid` | is a 101 that is not HTTP/1.1 |
| `ErrHandshakeResponseConnectionHeaderInvalid` | has no `Upgrade` token in `Connection` |
| `ErrHandshakeResponseUpgradeHeaderInvalid` | has no `websocket` token in `Upgrade` |
| `ErrHandshakeResponseAcceptHeaderMissing` | has no `Sec-WebSocket-Accept` |
| `ErrHandshakeResponseAcceptHeaderMismatch` | has a `Sec-WebSocket-Accept` that does not match the key sent |
| `ErrHandshakeResponseSubprotocolRepeated` | has the `Sec-WebSocket-Protocol` header more than once — see [Subprotocols](#subprotocols) |
| `ErrHandshakeResponseSubprotocolMultiple` | has more than one `Sec-WebSocket-Protocol` value on one line — see [Subprotocols](#subprotocols) |
| `ErrHandshakeResponseSubprotocolNotOffered` | has a `Sec-WebSocket-Protocol` you did not offer — see [Subprotocols](#subprotocols) |
| `ErrHandshakeResponseExtensionNotOffered` | accepts permessage-deflate twice, or in a way no offer allows — see [Compression](#compression-permessage-deflate) |
| `ErrHandshakeResponseDeflateParamsInvalid` | has a permessage-deflate param RFC 7692 7 forbids — see [Compression](#compression-permessage-deflate) |

**Who closes the connection.** For some broken answers, RFC 6455 4.1 and
RFC 7692 5 say to "Fail the WebSocket Connection", which means close it
(RFC 6455 7.1.7). For those, every error in the table above,
`ClientHandShake` closes `netConn` before it returns, and reports the failure
to you through the returned error. Every other error leaves `netConn` open, and you must close it.
Examples are a failed send or read, and a status other than 101. RFC 6455 4.1
says to handle a status other than 101 as plain HTTP, such as a redirect or a
401.

**We recommend closing it on every handshake error.** After an error, the
connection can never become a WebSocket. For a status other than 101, read
`resp` first if you want, then close: following a redirect or retrying with
credentials takes a new connection. `defer netConn.Close()` right after dialing
covers every case. On a connection `ClientHandShake` already closed, the second
close only returns `net.ErrClosed`.

`ClientMustFailConnection(err)` tells the two apart: `true` for every error in
the table above. Use it to know whether the connection is already closed, or, if you
[run the handshake yourself](#running-the-handshake-yourself), to know when to
close it.

It knows only handshake errors. For errors from `Listen`, see
[`StandardClosePayloadFor`](./LISTENER.md#errors).

## Subprotocols

A subprotocol is the application protocol your messages follow, such as
`graphql-transport-ws`. The client offers a list. The server answers with one
of them, or none (RFC 6455 4.2.2).

### Offering

Put them in `config.Subprotocols`, most preferred first. They are sent in one
`Sec-WebSocket-Protocol` header:

```go
config := wlgows.ClientHandShakeConfig{Subprotocols: []string{"chat.v2", "chat.v1"}}
```

The order is only a hint. The server decides. A `Sec-WebSocket-Protocol` you
set on `request` yourself is sent too, and counts as offered. A value already
there is not added again. Setting it by hand is not recommended: use
`config.Subprotocols`.

### Checking the answer

RFC 6455 4.1 says the client must fail the connection if the server answers
with a subprotocol it never offered. The server may answer with one value only
(4.2.2). `ClientHandShake` checks both for you, and the error message says what
the server sent:

| The server answers with | Error |
|---|---|
| the header more than once (11.3.4) | `ErrHandshakeResponseSubprotocolRepeated` |
| more than one value on one line | `ErrHandshakeResponseSubprotocolMultiple` |
| a value you did not offer, or any value when you offered nothing | `ErrHandshakeResponseSubprotocolNotOffered` |

No `Sec-WebSocket-Protocol` header means the server picked none (4.2.2), and
passes. An empty header is different: the server sent something, just something
invalid, so it fails as `ErrHandshakeResponseSubprotocolNotOffered`.

All three are a 4.1 Fail, so `ClientHandShake` closes the connection before it
returns, and `conn` is `nil`.

```go
config := wlgows.ClientHandShakeConfig{Subprotocols: []string{"chat.v2", "chat.v1"}}

conn, resp, err := wlgows.ClientHandShake(netConn, reader, writer, request, config)
if err != nil {
	netConn.Close() // already closed if the subprotocol check failed; a second close is harmless
	return
}
subprotocol := resp.Header.Get("Sec-WebSocket-Protocol") // one you offered, or "" when the server picked none
```

To run the same check yourself, see
[Running the handshake yourself](#running-the-handshake-yourself).

### Requiring one

**When.** Require a subprotocol only if your client cannot talk to a server
without it. If it can also talk plain WebSocket, accept `""` and carry on.

**How.** An empty answer means the server picked none. The handshake
cannot know whether that is fine for you, so this check is yours. The connection
is already open here, so start the closing handshake with a close frame first,
then close (7.1.2):

```go
if resp.Header.Get("Sec-WebSocket-Protocol") == "" {
	conn.SendClose(&wlgows.ClosePayload{StatusCode: wlgows.ClosePolicyViolation, Reason: "subprotocol chat.v2 or chat.v1 required"})
	// The server picked none. A client SHOULD first wait briefly for the server to close (RFC 6455 5.5.1).
	netConn.Close()
	return
}
```

A server that requires a subprotocol you did not offer refuses the handshake
instead. That comes back as `ErrHandshakeResponseStatusCodeInvalid`, usually
with a 400 and a reason in `resp.Body`.

Both client examples have the offer and the check, commented out.

## Compression (permessage-deflate)

Each item in `config.PermessageDeflates` is one offer, most preferred first.
What each field means is in [PERMESSAGE_DEFLATE.md](./PERMESSAGE_DEFLATE.md#permessagedeflateconfig).
Sending deflated once it is agreed is in [SENDING.md](./SENDING.md#compression-permessage-deflate).

**We recommend sending without context, on both sides.** Then a connection
keeps almost no compression memory between messages. The numbers are in
[PERMESSAGE_DEFLATE.md](./PERMESSAGE_DEFLATE.md#per-connection).
Use the same config in the server's list:

```go
config := wlgows.ClientHandShakeConfig{
	PermessageDeflates: []wlgows.PermessageDeflateConfig{
		{
			ServerNoContextTakeover: true, // the server sends without context
			ClientNoContextTakeover: true, // the client sends without context
		},
	},
}
```

Setting `Sec-WebSocket-Extensions` by hand is not recommended. A header you
write is sent as it is, typos and all. The config is checked before the request
is sent, so a misuse comes back as an error instead of going on the wire.
`ClientHandShake` refuses two configs, and leaves `netConn` open:

| Config | Error |
|---|---|
| `ClientMaxWindowBitsEnabled` true | `ErrHandshakeRequestDeflateUnsupported` |
| `ServerMaxWindowBits` not 0 or 8 to 15 (RFC 7692 7.1.2.1) | `ErrHandshakeRequestDeflateWindowBitsInvalid` |

The first is a library choice. `client_max_window_bits` would let the server
set the client's window under 15 bits, which wlgows cannot send with (see
[Windows under 15 bits](./PERMESSAGE_DEFLATE.md#windows-under-15-bits)).
Without it, the client may always use 15 bits.

The server accepts one offer, or none. No permessage-deflate in the answer is
not an error: the connection opens without compression. An answer no offer
allows, or one with an [invalid entry](./PERMESSAGE_DEFLATE.md#invalid-entries),
fails the connection (RFC 7692 5):

| The server answers with | Error |
|---|---|
| permessage-deflate twice | `ErrHandshakeResponseExtensionNotOffered` |
| `client_max_window_bits=N`, which wlgows never offers | `ErrHandshakeResponseExtensionNotOffered` |
| params no single offer allows, such as a `server_max_window_bits` above the offer's, or no `server_no_context_takeover` or `server_max_window_bits` when the offer asked for it (RFC 7692 7.1.1.1, 7.1.2.1) | `ErrHandshakeResponseExtensionNotOffered` |
| an invalid entry | `ErrHandshakeResponseDeflateParamsInvalid` |

`ClientHandShake` does not return what was agreed. Get it with
`ValidateHandShakeResponseDeflate`:

```go
agreed, err := wlgows.ValidateHandShakeResponseDeflate(resp, request) // agreed is nil when the server declined
```

`agreed` is a `*PermessageDeflateConfig`: what both sides follow, not a copy
of the answer. If an offer that allows the answer has
`client_no_context_takeover`, `agreed` has it too, even when the answer
leaves it out. That offer was the client's promise
(RFC 7692 7.1.1.2), and the answer does not say which offer it took.

## Running the handshake yourself

**Not recommended. Only do this if you know what you are doing.**
`ClientHandShake` runs these steps in this order, and closes the connection
when RFC 6455 or RFC 7692 says to. When you run them yourself, a step you skip
or reorder breaks the protocol, and nothing warns you.

```go
reader := bufio.NewReader(netConn)
writer := bufio.NewWriter(netConn)

// 1. Make the request an opening handshake, plus what config asks for.
if err := wlgows.UpgradeRequest(request, config); err != nil {
	return err
}
// 2. Send it to netConn. writer is for frames.
if err := wlgows.SendHandShakeRequest(netConn, request); err != nil {
	return err
}
// 3. Read the answer from the reader you will read frames from.
resp, err := wlgows.ReadHandShakeResponse(reader, request)
if err != nil {
	return err
}
// 4. Check the answer: RFC 6455 4.1, the subprotocol, then permessage-deflate.
if err := wlgows.ValidateHandShakeResponse(resp, request.Header.Get("Sec-WebSocket-Key")); err != nil {
	if wlgows.ClientMustFailConnection(err) {
		netConn.Close() // RFC 6455 7.1.7
	}
	return err
}
if err := wlgows.ValidateHandShakeResponseSubprotocol(resp, request); err != nil {
	if wlgows.ClientMustFailConnection(err) {
		netConn.Close() // RFC 6455 7.1.7
	}
	return err
}
agreed, err := wlgows.ValidateHandShakeResponseDeflate(resp, request)
if err != nil {
	if wlgows.ClientMustFailConnection(err) {
		netConn.Close() // RFC 7692 5
	}
	return err
}
// 5. Build the Conn. The peer is a server, so this Conn masks every frame it
// sends (RFC 6455 5.1). agreed is nil when no deflate was agreed.
conn, err := wlgows.NewConn(netConn, reader, writer, wlgows.NewConnConfig{PeerIsClient: false, PermessageDeflate: agreed})
```

- **`NewConn` checks nothing.** A connection whose handshake broke a rule, or
  the wrong `PeerIsClient` or agreement, breaks the protocol from the first frame.
- **Its error** is a write error, from flushing a `writer` smaller than 14 bytes.
- **A struct literal panics.** A `Conn` carries unexported dependencies, so
  build one with `NewConn` only.

## Next: reading and sending

With a `Conn` in hand, the rest is reading and sending:

- **Reading:** [READING.md](./READING.md) — the bytes of a message, size
  limits, errors, and compressed messages.
- **The Listener:** [LISTENER.md](./LISTENER.md) — hooks, limits, what each
  error means, and [noticing a dead peer](./LISTENER.md#it-has-no-pingpong-liveness).
- **Sending:** [SENDING.md](./SENDING.md) — [which call to use](./SENDING.md#choosing-a-call),
  streaming a large message, [pinging on an interval](./SENDING.md#pinging-on-an-interval),
  the write buffer, and locks.
