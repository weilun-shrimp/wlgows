**English** · [繁體中文](./CLIENT_README.zh-TW.md)

# Client

How a client goes from a URL to a ready `*wlgows.Conn`: dialing, TLS, the
handshake, and subprotocols.

## Contents

- [Dialing](#dialing)
- [TLS (wss://)](#tls-wss)
- [The handshake](#the-handshake) — [adding headers](#adding-headers) · [what it returns](#what-it-returns)
- [Subprotocols](#subprotocols) — [offering](#offering) · [checking the answer](#checking-the-answer) · [requiring one](#requiring-one)
- [Building a Conn yourself](#building-a-conn-yourself) — not recommended
- [Next: reading and sending](#next-reading-and-sending)

Reading is in [LISTENER_README.md](./LISTENER_README.md), sending in
[SENDING_README.md](./SENDING_README.md), and the server side in
[SERVER_README.md](./SERVER_README.md).

## Dialing

`DialContext` connects and builds the opening request, and nothing more. It
does not run the handshake, so you can still add headers to the request first.

```go
// Either bounds the TCP connect and the TLS handshake; the earlier one wins.
ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
defer cancel()
dialer := &net.Dialer{Timeout: 15 * time.Second}
netConn, request, err := wlgows.DialContext(ctx, "ws://localhost:8001", dialer, nil)
if err != nil {
	panic(err)
}
defer netConn.Close()
```

- **The URL** is `ws://` or `wss://`. `http://` and `https://` work the same.
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

`ClientHandShake(netConn, reader, writer, request)` adds the upgrade headers,
sends the request, reads the answer, checks it against RFC 6455 4.1, and builds
the `Conn`.

```go
reader := bufio.NewReader(netConn)
conn, resp, err := wlgows.ClientHandShake(netConn, reader, bufio.NewWriter(netConn), request)
if err != nil {
	netConn.Close() // recommended on every handshake error
	return
}
```

- **`reader`** must be the one you go on to read frames from. Reading the answer
  may pull the start of the first frame into it.
- **`writer`** is what every frame is sent through. A client masks a copy of
  each payload in it, so its size decides how many socket writes a large
  message takes. See [Memory: the write buffer](./SENDING_README.md#memory-the-write-buffer).

The `Conn` comes back masking: a client masks every frame it sends (5.1).

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
one.

### What it returns

| Outcome | `conn` | `resp` | `err` |
|---|---|---|---|
| Upgraded | the `Conn` | the 101 | `nil` |
| The request URL has no host | `nil` | `nil` | `ErrUrlHostEmpty` |
| The request could not be sent | `nil` | `nil` | why |
| The answer could not be read | `nil` | `nil` | why |
| The server refused, or answered wrong | `nil` | its answer | why |
| Upgraded, but the writer could not be flushed | `nil` | the 101 | the write error |

`conn` is `nil` whenever `err` is not. A refusal comes back as
`ErrHandshakeResponseStatusCodeInvalid`, and `resp.StatusCode` and `resp.Body`
say why. The other ways an answer can be wrong, each a sentinel for
`errors.Is`:

| Error | The answer | Closes the connection |
|---|---|---|
| `ErrHandshakeResponseProtoInvalid` | is a 101 that is not HTTP/1.1 | yes |
| `ErrHandshakeResponseConnectionHeaderInvalid` | has no `Upgrade` token in `Connection` | yes |
| `ErrHandshakeResponseUpgradeHeaderInvalid` | has no `websocket` token in `Upgrade` | yes |
| `ErrHandshakeResponseAcceptHeaderMissing` | has no `Sec-WebSocket-Accept` | yes |
| `ErrHandshakeResponseAcceptHeaderMismatch` | has a `Sec-WebSocket-Accept` that does not match the key sent | yes |
| `ErrHandshakeResponseSubprotocolMultiple` | has more than one `Sec-WebSocket-Protocol` value — see [Subprotocols](#subprotocols) | yes |
| `ErrHandshakeResponseSubprotocolNotOffered` | has a `Sec-WebSocket-Protocol` you did not offer — see [Subprotocols](#subprotocols) | yes |

**Who closes the connection.** RFC 6455 4.1 answers some broken responses with
"Fail the WebSocket Connection", which means close it (7.1.7). For those, the
"yes" rows above, `ClientHandShake` closes `netConn` before it returns the
error. The error is the report 7.1.7 allows. Every other error leaves `netConn`
open, and closing it is yours: a failed send or read, and a status other than 101, which 4.1 says to handle as plain HTTP, such as a
redirect or a 401.

**Unless you have a better reason not to, we recommend closing the connection on every handshake error.** After any of
them, it can never become a WebSocket. For a status other than 101, read `resp`
first if you want, then close: following a redirect or retrying with
credentials takes a new connection. `defer netConn.Close()` right after dialing
covers every case. On a connection `ClientHandShake` already closed, the second
close only returns `net.ErrClosed`.

`ClientMustFailConnection(err)` tells the two apart: `true` for the "yes"
rows. Use it to know whether the connection is already closed, or, if you run
the checks yourself with `ValidateHandShakeResponse` and
`ValidateHandShakeResponseSubprotocol`, to know when to close it:

```go
if err := wlgows.ValidateHandShakeResponseSubprotocol(resp, request); err != nil {
	if wlgows.ClientMustFailConnection(err) {
		netConn.Close() // RFC 6455 7.1.7
	}
	return err
}
```

It knows only handshake errors. For errors from `Listen`, see
[`StandardClosePayloadFor`](./LISTENER_README.md#errors).

## Subprotocols

A subprotocol is the application protocol your messages follow, such as
`graphql-transport-ws`. The client offers a list. The server answers with one
of them, or none (RFC 6455 4.2.2).

### Offering

Set the header before the handshake, most preferred first. A list goes in one
header:

```go
request.Header.Set("Sec-WebSocket-Protocol", "chat.v2, chat.v1")
```

The order is only a hint. The server decides.

### Checking the answer

RFC 6455 4.1 says the client must fail the connection if the server answers
with a subprotocol it never offered. The server may answer with one value only
(4.2.2). `ClientHandShake` checks both for you, and the error message says what
the server sent:

| The server answers with | Error |
|---|---|
| more than one value, on one line or several | `ErrHandshakeResponseSubprotocolMultiple` |
| a value you did not offer, or any value when you offered nothing | `ErrHandshakeResponseSubprotocolNotOffered` |

No `Sec-WebSocket-Protocol` header means the server picked none (4.2.2), and
passes. An empty header is different: the server sent something, just something
invalid, so it fails as `ErrHandshakeResponseSubprotocolNotOffered`.

Both are a 4.1 Fail, so `ClientHandShake` closes the connection before it
returns, and `conn` is `nil`. The WebSocket connection was never established, so
7.1.7 asks for no close frame.

```go
request.Header.Set("Sec-WebSocket-Protocol", "chat.v2, chat.v1")

conn, resp, err := wlgows.ClientHandShake(netConn, reader, bufio.NewWriter(netConn), request)
if err != nil {
	netConn.Close() // already closed if the subprotocol check failed; a second close is harmless
	return
}
subprotocol := resp.Header.Get("Sec-WebSocket-Protocol") // one you offered, or "" when the server picked none
```

To run the same check yourself, for example around `NewConn`, call
`ValidateHandShakeResponseSubprotocol(resp, request)`.

### Requiring one

**When.** Require a subprotocol only if your client cannot talk to a server
without it. If it can also talk plain WebSocket, accept `""` and carry on.

**How.** An empty answer means the server picked none. The handshake
cannot know whether that is fine for you, so this check is yours. The connection
is already open here, so start the closing handshake with a close frame first,
then close (7.1.2):

```go
if resp.Header.Get("Sec-WebSocket-Protocol") == "" {
	conn.SendClose(&wlgows.ClosePayload{StatusCode: wlgows.ClosePolicyViolation, Reason: "subprotocol chat.v2 required"})
	netConn.Close() // the server does not speak chat.v2 or chat.v1; a client SHOULD first wait briefly for the server to close (5.5.1)
	return
}
```

A server that requires a subprotocol you did not offer refuses the handshake
instead. That comes back as `ErrHandshakeResponseStatusCodeInvalid`, usually
with a 400 and a reason in `resp.Body`.

Both client examples have the offer and the check, commented out.

## Building a Conn yourself

**Not recommended. Only do this if you know what you are doing.**
`ClientHandShake` builds the `Conn` for you, and checks the server's answer
against RFC 6455 on the way. `NewConn` checks nothing. A connection whose
handshake broke a rule, or the wrong masking flag, breaks the protocol from the
first frame. To build one around a connection whose handshake you ran yourself:

```go
conn, err := wlgows.NewConn(netConn, reader, writer, true) // true: a client masks (5.1)
```

A `Conn` carries unexported dependencies, so a struct literal panics on first
use. The error comes only from a writer under 14 bytes: it is flushed before it
is replaced, and that flush can fail.

## Next: reading and sending

With a `Conn` in hand, the rest is reading and sending:

```go
listener := conn.NewStandardListener() // ping, pong, close and reserved opcodes answered for you

config := listener.GetConfig()
config.Text = func(frames wlgows.DataFrames) {
	conn.SendText(frames.Bytes(), 0) // echo
}
listener.SetConfig(config)

go conn.StartPingLoop(30*time.Second, nil) // heartbeat; ends itself

if err := listener.Listen(); err != nil {
	if payload := wlgows.StandardClosePayloadFor(err); payload != nil {
		conn.SendClose(payload)
	}
}
```

- **Reading:** [LISTENER_README.md](./LISTENER_README.md) — hooks, limits, what
  each error means, [noticing a dead peer](./LISTENER_README.md#it-has-no-pingpong-liveness),
  and reading one frame at a time.
- **Sending:** [SENDING_README.md](./SENDING_README.md) — [which call to use](./SENDING_README.md#choosing-a-call),
  streaming a large message, [pinging on an interval](./SENDING_README.md#pinging-on-an-interval),
  the write buffer, and locks.
