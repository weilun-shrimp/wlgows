# WLGOWS

A lightweight, low-level WebSocket implementation library for Go. Provides both server-side and client-side WebSocket support with manual connection handshake and message frame handling, fully compliant with RFC 6455.

## Features

- **WebSocket Server** - Raw TCP-based WebSocket server with HTTP handshake
- **WebSocket Client** - Dial remote WebSocket servers (ws:// and wss://)
- **TLS/SSL Support** - Secure WebSocket connections for both server and client
- **HTTP Hijacking** - Integrate with standard `http.Server` or Gin framework
- **Frame-Level Control** - Low-level frame manipulation and multi-frame message handling

## Installation

```bash
go get github.com/weilun-shrimp/wlgows
```

## Quick Start

### Server Side

```go
package main

import (
	"fmt"
	"github.com/weilun-shrimp/wlgows/server"
)

func main() {
	// Start WebSocket server on port 8001
	s, err := server.Run(":8001")
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

				// Echo back the message
				conn.SendText([]byte(msg.GetStr()))
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
	"github.com/weilun-shrimp/wlgows/client"
)

func main() {
	// Connect to WebSocket server
	conn, err := client.Dial("ws://localhost:8001", nil)
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
	"github.com/weilun-shrimp/wlgows/client"
)

// Load CA certificate
caCert, _ := os.ReadFile("ca.crt")
caCertPool := x509.NewCertPool()
caCertPool.AppendCertsFromPEM(caCert)

tlsConfig := &tls.Config{
	RootCAs: caCertPool,
}

conn, err := client.Dial("wss://localhost:8001", tlsConfig)
```

### HTTP Hijacking (with http.Server)

```go
import (
	"net/http"
	"github.com/weilun-shrimp/wlgows/connection"
)

func handler(w http.ResponseWriter, r *http.Request) {
	conn, err := connection.HijackFromHttp(w, r)
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
	"github.com/weilun-shrimp/wlgows/connection"
)

func handler(c *gin.Context) {
	conn, err := connection.HijackFromGin(c)
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

## License

MIT
