// The same echo server, reached through Gin. c.Writer is an http.Hijacker, which
// takes the connection off Gin after routing, so the WebSocket lives on a
// handler's own goroutine and ordinary routes keep working beside it — GET /ping
// still answers JSON.
//
//	cd example/hijack_gin && go run .   # in one terminal
//	cd example/client && go run .       # in another
//
// Answer y to the TLS prompt to serve wss:// instead. Empty paths use the
// bundled ../server_fullchain.crt and ../server.key.
package main

import (
	"compress/flate"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"
	"unicode/utf8"

	"github.com/gin-gonic/gin"
	"github.com/weilun-shrimp/wlgows/v7"
	"github.com/weilun-shrimp/wlgows/v7/example/example_helpers"
)

const (
	service = ":8001"

	// A peer can claim a 10 GB payload in a 10 byte header. Without this that
	// claim becomes a 10 GB allocation before a byte of payload arrives.
	maxDataFramesSize = 10 * 1024 * 1024 // 10 MB

	// Armed before each frame read, and only fires when no bytes arrive at all.
	frameReadTimeout = 60 * time.Second

	// A ping every interval, answered by the peer's pong (5.5.2). It only asks;
	// deciding a silent peer is gone would be a deadline of our own.
	pingInterval = 30 * time.Second

	// net/http sets no timeout by default, so a client that never finishes its
	// request would hold a goroutine forever.
	readHeaderTimeout = 10 * time.Second

	// readHeaderTimeout only starts once a request's first bytes arrive. This
	// bounds the wait for the next request on a kept-alive connection. It
	// does not touch a hijacked connection.
	idleTimeout = 60 * time.Second

	// The request line and headers. net/http allows 1 MB by default, and a
	// handshake is a few hundred bytes.
	maxHeaderBytes = 16 * 1024
)

// readMessage returns the bytes of one message, up to maxDataFramesSize. It
// decompresses a compressed message and checks text as UTF-8. For a larger
// message, it returns ErrSizeLimitExceeded. See READING.md.
func readMessage(conn *wlgows.Conn, frames wlgows.DataFrames) ([]byte, error) {
	reader, err := conn.DataFramesReader(frames)
	if err != nil {
		return nil, err
	}
	defer reader.Close() // always close it
	return io.ReadAll(wlgows.NewSizeLimitReader(reader, maxDataFramesSize))
}

// Echoes are deflated when the handshake agreed permessage-deflate, and sent as
// is otherwise. DeflateLevel must be set: its zero is flate.NoCompression.
var sendDataConfig = wlgows.SendDataConfig{
	DeflateIfAgreed: true,
	DeflateLevel:    flate.DefaultCompression,
}

func main() {
	serverConfig, err := example_helpers.ReadUserInputServerConfig()
	if err != nil {
		fmt.Println("error: server config:", err)
		return
	}

	router := gin.Default()
	router.GET("/ping", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{"message": "pong"})
	})
	router.GET("/", func(c *gin.Context) {
		handler(c, serverConfig.SupportedSubprotocols)
	})

	// Not router.Run: it serves with a default http.Server, which has no timeout.
	server := &http.Server{
		Addr:              service,
		Handler:           router,
		ReadHeaderTimeout: readHeaderTimeout,
		IdleTimeout:       idleTimeout,
		MaxHeaderBytes:    maxHeaderBytes,
	}

	fmt.Println("hijack_gin server listening on 0.0.0.0" + service + " (on windows, localhost" + service + ")")
	if serverConfig.ServerCertPath != "" && serverConfig.ServerKeyPath != "" {
		fmt.Println("tls mode — connect with wss://")
		err = server.ListenAndServeTLS(serverConfig.ServerCertPath, serverConfig.ServerKeyPath)
	} else {
		fmt.Println("plain mode — connect with ws://")
		err = server.ListenAndServe()
	}
	if err != nil {
		fmt.Println("error: starting server:", err)
	}
}

func handler(c *gin.Context, supportedSubprotocols []string) {
	handShakeConfig := wlgows.ServerHandShakeConfig{
		// permessage-deflate (RFC 7692). One answer with every field zero
		// accepts any offer this package can serve. Matching adds the offer's
		// server_no_context_takeover (RFC 7692 7.1.1.1) and
		// client_no_context_takeover (7.1.1.2), so the client picks the mode.
		// An offer of server_max_window_bits under 15 is skipped (library
		// choice). A client that offers nothing gets no compression.
		PermessageDeflates: []wlgows.PermessageDeflateConfig{{}},

		// Subprotocols (RFC 6455 4.2.2). When no offer is one the server
		// speaks, this is "" and the connection opens without one.
		Subprotocol: example_helpers.PickSubprotocol(wlgows.RequestSubprotocols(c.Request), supportedSubprotocols),
	}

	// gin.ResponseWriter is an http.Hijacker.
	netConn, bufRW, err := c.Writer.Hijack()
	if err != nil {
		// Still an ordinary response: the hijack failed, so nothing was taken.
		c.AbortWithError(http.StatusInternalServerError, errors.New("could not hijack connection"))
		fmt.Println("error: hijack:", err)
		return
	}

	// The only close in this handler. The hooks pause the loop and let this
	// function return rather than closing underneath it.
	defer netConn.Close()

	// c.Request is already parsed by net/http, so no read step is needed here.
	// Pass bufRW.Reader, not a new reader: net/http may have buffered the start
	// of the first frame in it.
	// bufRW.Writer reuses net/http's writer; bufio.NewWriterSize(netConn, size) works too.
	conn, resp, err := wlgows.ServerHandShake(netConn, bufRW.Reader, bufRW.Writer, c.Request, handShakeConfig)
	if err != nil {
		fmt.Println("error: handshake:", err)
		return
	}

	fmt.Println("connected:", conn.RemoteAddr())
	example_helpers.PrintAgreedDeflate(conn)
	example_helpers.PrintSubprotocol(resp.Header.Get("Sec-WebSocket-Protocol"))

	// Ping, pong, close and reserved opcodes are answered for you, and masking
	// (5.1) is settled from which side this connection is.
	listener := conn.NewStandardListener()

	// SetConfig replaces all of it, so start from what the standard hooks left
	// rather than from an empty ListenerConfig.
	config := listener.GetConfig()
	config.MaxDataFramesSize = maxDataFramesSize
	config.MaxFrameSize = maxDataFramesSize            // one frame may carry the whole message
	config.MaxDataFrameCount = maxDataFramesSize / 256 // fragments of 256 bytes or more
	config.FrameReadTimeout = frameReadTimeout

	// A whole message, assembled across every fragment. The payload is already
	// checked as valid UTF-8 (5.6, 8.1).
	config.Text = func(frames wlgows.DataFrames) {
		message, err := readMessage(conn, frames)
		if err != nil {
			listener.PauseListen(err) // Listen returns err
			return
		}
		fmt.Printf("text: frames=%d bytes=%d runes=%d: %s\n",
			len(frames), len(message), utf8.RuneCount(message), message)

		if err := conn.SendText(message, sendDataConfig); err != nil {
			fmt.Println("error: echo text:", err)
		}
	}

	// Arbitrary bytes — 5.6 gives binary no encoding at all.
	config.Binary = func(frames wlgows.DataFrames) {
		message, err := readMessage(conn, frames)
		if err != nil {
			listener.PauseListen(err) // Listen returns err
			return
		}
		fmt.Printf("binary: frames=%d bytes=%d\n", len(frames), len(message))

		if err := conn.SendBinary(message, sendDataConfig); err != nil {
			fmt.Println("error: echo binary:", err)
		}
	}

	// The standard hooks already answer and pause, so these only report what
	// arrived and hand over. Reading them back out of the config is what makes
	// that possible.
	standardClose, standardUnknown := config.Close, config.Unknown
	config.Close = func(f *wlgows.Frame) {
		payload, _ := f.GetClosePayload() // already validated by the Listener
		fmt.Printf("closed by peer: %+v\n", payload)

		standardClose(f)
	}
	config.Unknown = func(f *wlgows.Frame) {
		fmt.Printf("unknown opcode %#x\n", f.Opcode)

		standardUnknown(f)
	}

	listener.SetConfig(config)

	// Answers the peer's pings by itself; this asks the peer in turn. It ends
	// when the connection does, so nothing here has to stop it.
	go conn.StartPingLoop(pingInterval, nil)

	// Blocks until a read fails, a frame breaks a rule, or a hook pauses it.
	// nil means the close hook paused it: the peer closed, the ordinary
	// shutdown. The unknown hook pauses with ErrReservedOpcode, already
	// answered with 1002, so the SendClose below gets ErrCloseAlreadySent.
	if err := listener.Listen(); err != nil {
		// No payload when no close frame can answer err: a dead socket, or
		// something this package cannot attribute to the peer.
		if payload := wlgows.StandardClosePayloadFor(err); payload != nil {
			conn.SendClose(payload)
		}
		fmt.Println("error: listen:", err)
	}
}
