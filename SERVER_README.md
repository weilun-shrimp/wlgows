**English** · [繁體中文](./SERVER_README.zh-TW.md)

# Server

How a server goes from a TCP connection to a ready `*wlgows.Conn`: accepting,
the handshake, hijacking from `net/http` or Gin, TLS, and subprotocols.

## Contents

- [Accepting a connection](#accepting-a-connection) — raw TCP, with the timeout and size limit left to you
- [The handshake](#the-handshake) — [what it returns](#what-it-returns) · [refusals](#refusals)
- [HTTP hijacking](#http-hijacking) — `net/http` and Gin
- [TLS (wss://)](#tls-wss)
- [Subprotocols](#subprotocols) — [picking one](#picking-one) · [requiring one](#requiring-one) · [what was sent](#what-was-sent)
- [Checking Origin](#checking-origin) — for a server browsers reach
- [Building a Conn yourself](#building-a-conn-yourself) — not recommended
- [Next: reading and sending](#next-reading-and-sending)

Reading is in [LISTENER_README.md](./LISTENER_README.md), sending in
[SENDING_README.md](./SENDING_README.md), and the client side in
[CLIENT_README.md](./CLIENT_README.md).

## Accepting a connection

**We recommend [net/http or Gin](#http-hijacking) instead.** With them, a
timeout and a size limit on the request are one setting each. `Run` and
`Accept` leave both to you. Without them, a client can hold a connection
forever, or send headers until memory runs out. You can add them yourself, with
a read deadline and a capped reader, but it takes more care, so we do not
recommend it. But `Run` is fast. If you know what you are doing, such as with
internal connections from trusted sources, it is yours to use.

`Run` listens. `Accept` returns the next TCP connection, and nothing more. You
read the request and run the handshake yourself:

```go
server, err := wlgows.Run(":8001") // ":8001" or "[::]:8001" listens on IPv4 and IPv6
if err != nil {
	panic(err)
}
defer server.Close()

for {
	netConn, err := server.Accept()
	if err != nil {
		continue
	}
	go handle(netConn)
}
```

```go
func handle(netConn net.Conn) {
	defer netConn.Close()

	reader := bufio.NewReader(netConn)
	request, err := http.ReadRequest(reader)
	if err != nil {
		respWriter := wlgows.NewResponseWriter()
		respWriter.DeclineByError(wlgows.ErrHttpMsgFormationInvalid) // 400: a malformed request (RFC 6455 4.2.1)
		wlgows.SendHandShakeResponse(netConn, respWriter.GenerateResponse())
		return
	}
	conn, _, err := wlgows.ServerHandShake(netConn, reader, bufio.NewWriter(netConn), request, wlgows.ServerHandShakeConfig{})
	if err != nil {
		return
	}
	// read with a Listener, send with conn.Send*
}
```

`Server` keeps its `TCPAddr` and `TCPListener` as exported fields, readable and
settable.

## The handshake

`ServerHandShake(netConn, reader, writer, request, config)` checks the request's
method, HTTP version, `Host`, `Upgrade`, `Connection`, `Sec-WebSocket-Key` and
`Sec-WebSocket-Version` against RFC 6455 4.2.1, writes the answer, and builds
the `Conn`. `Origin` and the other fields are not checked.

| Parameter | What it must be |
|---|---|
| `netConn` | The connection. The answer is written straight to it. |
| `reader` | The same `*bufio.Reader` you read the request from. It may already hold the start of the first frame, so a new one would lose it. |
| `writer` | The `*bufio.Writer` every frame is sent through. Its size is yours: see [Memory: the write buffer](./SENDING_README.md#memory-the-write-buffer). |
| `request` | The request, already parsed. |
| `config` | `wlgows.ServerHandShakeConfig{}` for a plain handshake. See [Subprotocols](#subprotocols). |

The `Conn` comes back unmasked: a server never masks what it sends (5.1).

### What it returns

| Outcome | `conn` | `resp` | `err` |
|---|---|---|---|
| Upgraded | the `Conn` | the 101 that went out | `nil` |
| The request broke a rule | `nil` | the refusal that went out | why |
| The answer could not be written | `nil` | the answer | the write error |
| Upgraded, but the writer could not be flushed | `nil` | the 101 that went out | the write error |
| `request` is `nil` | `nil` | `nil` | `ErrHandshakeRequestNil` |

`conn` is `nil` whenever `err` is not. `resp` is what reached the wire, so you
can log it. Its body has already been read out.

`ServerHandShake` never closes the connection. **Unless you have a better reason
not to, we recommend closing it on every handshake error**: the connection can
never become a WebSocket. `defer netConn.Close()` right after accepting or hijacking
covers every case.

### Refusals

A request that breaks one of those rules is answered for you, with a status code
that names it:

| Error | Status |
|---|---|
| `ErrHttpMethodNotAllowed` | 405 Method Not Allowed |
| `ErrHttpProtocolOrVersionNotAllowed` | 505 HTTP Version Not Supported |
| `ErrHttpHostHeaderInvalid` | 400 Bad Request (RFC 6455 4.2.1, RFC 7230 5.4) |
| `ErrHttpSecWebSocketKeyHeaderNotSet`, `ErrHttpSecWebSocketKeyInvalid` | 400 Bad Request |
| `ErrHttpSecWebSocketKeyDuplicated`, `ErrHttpSecWebSocketVersionDuplicated` | 400 Bad Request (RFC 6455 11.3.1, 11.3.5) |
| `ErrHttpConnectionHeaderNotUpgrade`, `ErrHttpUpgradeHeaderNotWebsocket` | 400 Bad Request |
| `ErrHttpSecWebSocketVersionNotSupported` | 426 Upgrade Required, with `Sec-WebSocket-Version: 13` (4.2.2, 4.4) |

Every error wraps its sentinel, so compare with `errors.Is`:

```go
if _, _, err := wlgows.ServerHandShake(netConn, reader, bufio.NewWriter(netConn), request, wlgows.ServerHandShakeConfig{}); errors.Is(err, wlgows.ErrHttpMethodNotAllowed) {
	// ...
}
```

## HTTP hijacking

Behind `net/http`, the request is already parsed. Take the connection with
`http.Hijacker`:

```go
func handler(w http.ResponseWriter, r *http.Request) {
	hijacker, ok := w.(http.Hijacker) // under Gin, c.Writer is one
	if !ok {
		http.Error(w, "could not hijack connection", http.StatusInternalServerError)
		return
	}
	netConn, bufRW, err := hijacker.Hijack()
	if err != nil {
		http.Error(w, "could not hijack connection", http.StatusInternalServerError)
		return
	}
	defer netConn.Close()

	conn, _, err := wlgows.ServerHandShake(netConn, bufRW.Reader, bufRW.Writer, r, wlgows.ServerHandShakeConfig{})
	if err != nil {
		return
	}
	// read and send
}
```

- **Pass `bufRW.Reader`.** net/http may have read the start of the first frame
  into it.
- **`bufRW.Writer` reuses net/http's writer.** `bufio.NewWriterSize(netConn, size)`
  works too. To free memory after a hijack, see
  [The lowest memory setup](./SENDING_README.md#the-lowest-memory-setup).
- **Refuse before `Hijack`**, while net/http can still answer for you. After it,
  the connection is yours, and so is every byte you write to it.

net/http sets no timeout by default, so set `ReadHeaderTimeout` and
`IdleTimeout`. `ReadHeaderTimeout` only starts once a request's first bytes
arrive. Without `IdleTimeout`, a client can send one ordinary request, then keep
the connection open and silent forever. Neither one touches a hijacked
connection. `MaxHeaderBytes` is 1 MB by default. Gin's `Run` uses a default server too, so
serve a Gin engine with your own `http.Server`:

```go
server := &http.Server{
	Addr:              ":8001",
	Handler:           mux, // or a Gin engine
	ReadHeaderTimeout: 10 * time.Second, // once a request starts, the wait for its headers
	IdleTimeout:       60 * time.Second, // on a kept-alive connection, the wait for the next request
	MaxHeaderBytes:    16 * 1024,
}
log.Fatal(server.ListenAndServe())
```

Under Gin it is the same, with `c.Writer.Hijack()` and `c.Request`. The
[`hijack_http`](./example/hijack_http/main.go) and
[`hijack_gin`](./example/hijack_gin/main.go) examples run both.

## TLS (wss://)

There are two ways:

- **Behind net/http.** Serve with `http.ListenAndServeTLS` and hijack as above.
  The connection you get is already TLS. The hijack examples do this.
- **On raw TCP.** `Run` is plain TCP, so wrap each accepted connection, and use
  the wrapped one everywhere after:

```go
for {
	netConn, err := server.Accept()
	if err != nil {
		continue
	}
	tlsConn := tls.Server(netConn, tlsConfig) // the TLS handshake runs on the first read
	go handle(tlsConn)
}
```

## Subprotocols

A subprotocol is the application protocol your messages follow, such as
`graphql-transport-ws`. The client offers a list in `Sec-WebSocket-Protocol`.
The server answers with one of them, or none (RFC 6455 4.2.2).

`RequestSubprotocols(request)` reads the offer, in the client's order. It reads
every header line and splits each on commas, so `"a, b"` on one line and `"a"`
and `"b"` on two give the same list.

### Picking one

Set the one you picked in the config:

```go
conn, resp, err := wlgows.ServerHandShake(netConn, reader, writer, request, wlgows.ServerHandShakeConfig{
	Subprotocol: "chat.v2",
})
```

It is a single field because the server may answer with one subprotocol only.
It is sent only if the client offered it. Otherwise no header is sent, and the
connection opens with no subprotocol. So the line above already means "chat.v2
if the client speaks it, nothing otherwise".

To pick by your own order from several:

```go
subprotocol := ""
for _, supported := range []string{"chat.v2", "chat.v1"} { // most preferred first
	if slices.Contains(wlgows.RequestSubprotocols(request), supported) {
		subprotocol = supported
		break
	}
}
config := wlgows.ServerHandShakeConfig{Subprotocol: subprotocol}
```

### Requiring one

**When.** Refuse only if your server cannot talk to a client without that
subprotocol, for example because every message it sends and expects is in
chat.v2's format. If it can also serve a plain client, do not refuse: pick, and
let the connection open with no subprotocol.

**How.** Check before `ServerHandShake`, and answer with an ordinary HTTP
refusal. RFC 6455 names no status for this. 400 Bad Request with a short reason
is the usual one. Do not refuse by closing after the handshake: by then the
client thinks it is connected, and gets no reason.

Behind net/http, refuse before `Hijack`:

```go
if !slices.Contains(wlgows.RequestSubprotocols(r), "chat.v2") {
	http.Error(w, "subprotocol chat.v2 required", http.StatusBadRequest)
	return
}
```

Under Gin, `c.String(http.StatusBadRequest, "subprotocol chat.v2 required")`.

On raw TCP nothing answers for you, so write the refusal with the package's own
`ResponseWriter`:

```go
if !slices.Contains(wlgows.RequestSubprotocols(request), "chat.v2") {
	respWriter := wlgows.NewResponseWriter()
	respWriter.WriteHeader(http.StatusBadRequest)
	respWriter.Write([]byte("subprotocol chat.v2 required"))
	wlgows.SendHandShakeResponse(netConn, respWriter.GenerateResponse())
	netConn.Close()
	return
}
```

After the check, set `Subprotocol: "chat.v2"` as above. The client will see
`400` and the reason. Every server example has this code, commented out.

### What was sent

`resp.Header.Get("Sec-WebSocket-Protocol")` is what went out: the subprotocol,
or `""` when none was agreed.

## Checking Origin

A browser always sends `Origin`. If your server should only serve certain sites,
check it before `ServerHandShake` and refuse with 403 Forbidden (RFC 6455 10.2,
4.2.2), the same way as a required subprotocol. Clients that are not browsers
may send any `Origin`, or none.

## Building a Conn yourself

**Not recommended. Only do this if you know what you are doing.**
`ServerHandShake` builds the `Conn` for you, and checks the request against
RFC 6455 on the way. `NewConn` checks nothing. A connection whose handshake broke
a rule, or the wrong masking flag, breaks the protocol from the first frame. To
build one around a connection whose handshake you ran yourself:

```go
conn, err := wlgows.NewConn(netConn, reader, writer, false) // false: a server does not mask (5.1)
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
