# WLGOWS

A lightweight, low-level WebSocket implementation library for Go. Provides both server-side and client-side WebSocket support with manual connection handshake and message frame handling, fully compliant with RFC 6455.

## Features

- **WebSocket Server** - Raw TCP-based WebSocket server with HTTP handshake
- **WebSocket Client** - Dial remote WebSocket servers (ws:// and wss://)
- **TLS/SSL Support** - Secure WebSocket connections for both server and client
- **HTTP Hijacking** - Integrate with standard `http.Server` or Gin framework
- **Frame-Level Control** - Low-level frame manipulation and multi-frame message handling
- **Concurrent Sending** - Writes are mutex-guarded, so many goroutines can send on one connection

## Installation

```bash
go get github.com/weilun-shrimp/wlgows/v2
```

The import path carries the `/v2` suffix that Go requires for major version 2
and above, but the package name is still `wlgows` — call sites read
`wlgows.Dial(...)` as usual.

## Design

**Single package.** Everything lives in the root `wlgows` package:

```go
import "github.com/weilun-shrimp/wlgows/v2"

conn, _ := wlgows.Dial(url, nil)
s, _    := wlgows.Run(":8001")
sc, _   := wlgows.HijackFromHttp(w, r)
```

**Connections must be built by their constructors.** `Conn`, `ClientConn`, `ServerConn`, and `Server` carry unexported dependency fields, so a hand-written struct literal will panic on first use. `Dial`, `Run`, `Accept`, and `HijackFrom*` already do the right thing; only direct construction needs care:

```go
cc := wlgows.NewClientConn(netConn, req)
sc := wlgows.NewServerConn(netConn, req)
c  := wlgows.NewConn(netConn, req, res)
```

Exported fields (`ClientRequest`, `ServerResponse`, `TCPAddr`, `TCPListener`) are readable and settable.

`wlgows.UpgradeRequest(req *http.Request)` is package-level rather than a method on `ClientConn`, since it only decorates the request headers and never touches the socket.

## Concurrency

`Conn` carries two `sync.Locker` values in its `di` field, both defaulting to a `*sync.Mutex`. Every method that touches the socket takes one:

| Locker | Methods |
|--------|---------|
| `writeLocker` | `SendMsg`, `SendText`, `SendByte`, `SendHand` |
| `readLocker` | `GetNextMsg`, `GetNextFrame`, `ReadRequest`, `ReadResponse` |

**Sending from many goroutines is safe.** `writeLocker` spans the whole frame loop, so frames from different messages never interleave.

**Use one reader goroutine.** `readLocker` stops readers stealing each other's bytes, but two readers would still each get an arbitrary subset of messages.

**`conn.Write` / `conn.Read` bypass the locks** — they are promoted from the embedded `net.Conn`. Send through `SendText` / `SendByte` / `SendMsg`.

`Close` takes neither lock on purpose: closing the fd is what unblocks a read or write parked on a dead peer.

## Reading a message

`GetNextMsg` returns a `Msg`, which is just a slice of the frames the peer sent.
Five ways to get at it:

| Call | Returns | Use it for |
|------|---------|------------|
| `msg.GetStr()` | `string` | text (opcode 1) payloads |
| `msg.GetBytes()` | `[]byte` | binary (opcode 2) payloads, and anything you will hand back to `SendText`/`SendByte` — both take `[]byte` whatever opcode they send |
| `msg.Frames` | `[]*Frame` | the opcode, the FIN/RSV bits, per-frame detail |
| `msg.IsIncludedMaskedFrame()` | `bool` | asserting a client masked its payload, as RFC 6455 requires |
| `msg.IsIncludedUnMaskedFrame()` | `bool` | the mirror check |

Both assemblers join every frame, so a message fragmented across frames comes
back whole — including a multi-byte rune split down the middle by a frame
boundary. Prefer `GetBytes()` over `[]byte(GetStr())`: the conversion copies the
whole payload a second time (see [Testing](#testing) for the numbers).

The opcode lives on the first frame — continuation frames carry 0:

```go
switch msg.Frames[0].Opcode {
case 0x1: // text
case 0x2: // binary
case 0x8: // close — stop reading, the peer is done
case 0x9: // ping
case 0xA: // pong
}
```

`GetBytes()` hands back a copy you own, so writing to it never reaches the
frames. If you need zero allocations and the message is one frame, read
`msg.Frames[0].PayloadData` directly instead — but that slice belongs to the
`Msg`, so mutating it mutates the message.

## Quick Start

### Server Side

```go
package main

import (
	"fmt"
	"github.com/weilun-shrimp/wlgows/v2"
)

func main() {
	// Start WebSocket server on port 8001
	s, err := wlgows.Run(":8001")
	if err != nil {
		panic(err)
	}
	defer s.Close()

	for {
		// Accept incoming connection
		conn, err := s.Accept()
		if err != nil {
			continue
		}

		go func() {
			defer conn.Close()

			// Perform WebSocket handshake
			_, err := conn.HandShake()
			if err != nil {
				return
			}

			// Message loop
			for {
				msg, err := conn.GetNextMsg()
				if err != nil {
					break
				}

				// Check for close frame
				// Or if msg.Frames[0].Opcode == 8 also works
				if msg.Frames[0].Opcode == 0x8 {
					break
				}

				// Echo back the message. GetBytes assembles the frame
				// payloads once; []byte(msg.GetStr()) would copy them twice.
				conn.SendText(msg.GetBytes())
			}
		}()
	}
}
```

### Client Side

```go
package main

import (
	"fmt"
	"github.com/weilun-shrimp/wlgows/v2"
)

func main() {
	// Connect to WebSocket server
	conn, err := wlgows.Dial("ws://localhost:8001", nil)
	if err != nil {
		panic(err)
	}
	defer conn.Close()

	// Perform WebSocket handshake
	err = conn.HandShake()
	if err != nil {
		panic(err)
	}

	// Send a message
	conn.SendText([]byte("Hello, WebSocket!"))

	// Receive response
	msg, err := conn.GetNextMsg()
	if err != nil {
		panic(err)
	}

	fmt.Println("Received:", msg.GetStr())
}
```

### With TLS (wss://)

```go
import (
	"crypto/tls"
	"crypto/x509"
	"os"
	"github.com/weilun-shrimp/wlgows/v2"
)

// Load CA certificate
caCert, _ := os.ReadFile("ca.crt")
caCertPool := x509.NewCertPool()
caCertPool.AppendCertsFromPEM(caCert)

tlsConfig := &tls.Config{
	RootCAs: caCertPool,
}

conn, err := wlgows.Dial("wss://localhost:8001", tlsConfig)
```

### HTTP Hijacking (with http.Server)

```go
import (
	"net/http"
	"github.com/weilun-shrimp/wlgows/v2"
)

func handler(w http.ResponseWriter, r *http.Request) {
	conn, err := wlgows.HijackFromHttp(w, r)
	if err != nil {
		return
	}
	defer conn.Close()

	conn.HandShake()
	// ... handle WebSocket messages
}

func main() {
	http.HandleFunc("/", handler)
	http.ListenAndServe(":8001", nil)
}
```

### HTTP Hijacking (with Gin)

```go
import (
	"github.com/gin-gonic/gin"
	"github.com/weilun-shrimp/wlgows/v2"
)

func handler(c *gin.Context) {
	conn, err := wlgows.HijackFromGin(c)
	if err != nil {
		return
	}
	defer conn.Close()

	conn.HandShake()
	// ... handle WebSocket messages
}

func main() {
	r := gin.Default()
	r.GET("/ws", handler)
	r.Run(":8001")
}
```

## Examples

For more comprehensive examples, check out the `./example` folder:

| Example | Description |
|---------|-------------|
| [`echo`](./example/echo) | Simple WebSocket echo server using raw TCP |
| [`client`](./example/client) | Interactive WebSocket client with TLS support |
| [`hijack_http`](./example/hijack_http) | WebSocket server integrated with `http.Server` |
| [`hijack_gin`](./example/hijack_gin) | WebSocket server integrated with Gin framework |

Run an example:

```bash
# Start the echo server
go run ./example/echo

# In another terminal, run the client
go run ./example/client
```

## Testing

```bash
go test ./...           # full suite
go test -race ./...     # the integration tests spawn goroutines
go test -cover .        # 97.9% of statements
go test -bench . .      # benchmarks, which plain `go test` skips
```

One `_test.go` per source file, all in package `wlgows` so the `di` seams are
reachable. Two files have no source counterpart:

| File | Contents |
|------|----------|
| [`Fakes_test.go`](./Fakes_test.go) | Shared doubles — `fakeConn` (in-memory `net.Conn`), `fixedRandRead`, `scriptedReadTCPConn` |
| [`Integration_test.go`](./Integration_test.go) | Five tests over real sockets with **no** `di` substitution: raw TCP echo, a 70000 byte payload forcing the 64 bit length path, a non-websocket request rejected with 400, and both hijack paths against live servers |

`BenchmarkMsgAssembly` in [`Msg_test.go`](./Msg_test.go) is the one benchmark,
and it exists to justify `GetBytes` over `[]byte(GetStr())` on a 70000 byte
message:

```
[]byte(GetStr())   10447 ns/op   147457 B/op   2 allocs/op
GetBytes()          5786 ns/op    73728 B/op   1 allocs/op
```

Half the time, half the memory — the `string` header only ever existed to be
converted away. `TestMsgGetBytes/allocates_once_at_the_exact_size` is the
matching assertion, so a regression fails the suite rather than quietly showing
up here.

The integration tests are what catch wiring mistakes the unit tests structurally
cannot — a constructor that forgets a `di` field, or a default pointing at the
wrong function.

### Substituting a dependency

Every function that performs I/O or uses randomness is a thin exported wrapper
over an implementation taking a `di` struct of function values; types hold their
dependencies in a `di` field populated by the constructor. All of it is
unexported, so only in-package tests can reach it. Deterministic functions
(`Frame.Seal`, `Msg.GetStr`, `ValidateHandShakeRequest`, …) have no seam and are
tested directly.

```go
// package-level func: pass a di struct
cc, err := dial("ws://localhost:8001", nil, dialDI{
    httpNewRequest: http.NewRequest,
    validateWebsocketUrl: ValidateWebsocketUrl,
    netDial: func(string, string) (net.Conn, error) { return fakeConn, nil },
    tlsDial: tls.Dial,
    newClientConn: NewClientConn,
})

// method: overwrite the field the constructor set
sc := NewServerConn(conn, req)
sc.di.newMsg = func(data []byte, opcode uint8, need_mask bool) (*Msg, error) { ... }
```

Fixing the random source is what makes masked output assertable — a masked
frame's bytes cannot be pinned otherwise:

```go
m, _ := newMsg([]byte("hi"), 1, true, newMsgDI{
    generateMaskingKey: func() ([]byte, error) { return []byte{1, 2, 3, 4}, nil },
})
// m.Frames[0].Seal() == []byte{0x81, 0x82, 1, 2, 3, 4, 'h'^1, 'i'^2}
```

Two tests deliberately assert current behaviour rather than correct behaviour,
and say so in their names and comments:

- `TestResponseWriterWrittenBodyIsLostWithoutFlush` — `Write` fills a
  `bufio.Writer` that is never flushed, while `GenerateResponse` reads the
  underlying buffer, so a short body never reaches the response and
  `Content-Length` stays 0. Bodies over 4096 bytes bypass the buffer and do
  survive. When this is fixed, the test fails and tells you to update it.
- `TestDial/an_unknown_scheme_yields_a_connection_with_no_socket` — the scheme
  switch in `dial` has no default branch. Unreachable in production because
  `ValidateWebsocketUrl` gates it; only a permissive fake exposes it.

`newMsg` is the one function below 100% coverage: its
`dataLength > 18446744073709551612` branch would need an 18 exabyte slice to
enter.

## License

MIT
