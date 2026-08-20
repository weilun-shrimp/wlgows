package wlgows

import (
	"bufio"
	"bytes"
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
)

/*
ClientHandShake runs the RFC 6455 4.1 client opening handshake over c/r: it
decorates req with the upgrade headers, sends it, reads back the server's
response, and validates that response against req's Sec-WebSocket-Key. On
success it wraps c in a ready-to-use *Conn — masked, since 5.1 requires every
frame a client sends to be. conn is nil whenever err is non-nil; the response
is still returned so a caller can inspect what the server actually said.

r must be the same *bufio.Reader you go on to read frames from. http.ReadResponse
can only consume complete lines, so it may pull bytes past the header block —
the start of the first frame — into r's internal buffer; handing back a fresh
reader here would strand them. That's also why the returned *Conn reuses r
rather than wrapping c again.
*/
func ClientHandShake(c net.Conn, r *bufio.Reader, req *http.Request) (*Conn, *http.Response, error) {
	res, err := clientHandShake(c, r, req, clientHandShakeDI{
		upgradeRequest:            UpgradeRequest,
		sendHandShakeRequest:      SendHandShakeRequest,
		readHandShakeResponse:     ReadHandShakeResponse,
		validateHandShakeResponse: ValidateHandShakeResponse,
	})
	if err != nil {
		return nil, res, err
	}
	return NewConn(c, r, true), res, nil
}

type clientHandShakeDI struct {
	upgradeRequest            func(req *http.Request) error
	sendHandShakeRequest      func(w io.Writer, req *http.Request) error
	readHandShakeResponse     func(r *bufio.Reader, req *http.Request) (*http.Response, error)
	validateHandShakeResponse func(res *http.Response, sec_websocket_key string) error
}

func clientHandShake(w io.Writer, r *bufio.Reader, req *http.Request, di clientHandShakeDI) (*http.Response, error) {
	if err := di.upgradeRequest(req); err != nil {
		return nil, err
	}
	if err := di.sendHandShakeRequest(w, req); err != nil {
		return nil, err
	}
	res, err := di.readHandShakeResponse(r, req)
	if err != nil {
		return res, err
	}
	if err := di.validateHandShakeResponse(res, req.Header.Get("Sec-WebSocket-Key")); err != nil {
		return res, err
	}
	return res, nil
}

// upgrade request for websocket
func UpgradeRequest(req *http.Request) error {
	return upgradeRequest(req, upgradeRequestDI{
		generateWebSocketKey: GenerateWebSocketKey,
	})
}

type upgradeRequestDI struct {
	generateWebSocketKey func() string
}

func upgradeRequest(req *http.Request, di upgradeRequestDI) error {
	if req == nil {
		return fmt.Errorf("UpgradeRequest: %w", ErrHandshakeRequestNil)
	}
	for k, v := range map[string]string{
		"Upgrade":               "websocket",
		"Connection":            "Upgrade",
		"Sec-WebSocket-Key":     di.generateWebSocketKey(),
		"Sec-WebSocket-Version": "13",
	} {
		req.Header.Set(k, v)
	}
	return nil
}

// SendHandShakeRequest writes req to w as a plain HTTP message.
func SendHandShakeRequest(w io.Writer, req *http.Request) error {
	return sendHandShakeRequest(w, req, sendHandShakeRequestDI{
		requestToPlainHTTPMsg: RequestToPlainHTTPMsg,
	})
}

type sendHandShakeRequestDI struct {
	requestToPlainHTTPMsg func(req *http.Request) (string, error)
}

func sendHandShakeRequest(w io.Writer, req *http.Request, di sendHandShakeRequestDI) error {
	if req == nil {
		return fmt.Errorf("SendHandShakeRequest: %w", ErrHandshakeRequestNil)
	}
	plainHttpRequestMsg, err := di.requestToPlainHTTPMsg(req)
	if err != nil {
		return err
	}
	_, err = w.Write([]byte(plainHttpRequestMsg))
	return err
}

// ReadHandShakeResponse reads and parses the server's handshake response from
// r. r must be the same *bufio.Reader the caller goes on to read frames from
// — see the note on ClientHandShake.
func ReadHandShakeResponse(r *bufio.Reader, req *http.Request) (*http.Response, error) {
	return readHandShakeResponse(r, req, readHandShakeResponseDI{
		httpReadResponse: http.ReadResponse,
	})
}

type readHandShakeResponseDI struct {
	httpReadResponse func(r *bufio.Reader, req *http.Request) (*http.Response, error)
}

func readHandShakeResponse(r *bufio.Reader, req *http.Request, di readHandShakeResponseDI) (*http.Response, error) {
	if req == nil {
		return nil, fmt.Errorf("ReadHandShakeResponse: %w", ErrHandshakeRequestNil)
	}
	return di.httpReadResponse(r, req)
}

func ValidateHandShakeResponse(res *http.Response, sec_websocket_key string) error {
	if res.StatusCode != 101 {
		return fmt.Errorf("invalid handshake response status code %d: %w", res.StatusCode, ErrHandshakeResponseStatusCodeInvalid)
	}
	if res.Proto != "HTTP/1.1" {
		return fmt.Errorf("invalid handshake response proto %s: %w", res.Proto, ErrHandshakeResponseProtoInvalid)
	}
	// Read as a token list, not compared whole — a server answering
	// "Connection: keep-alive, Upgrade" is conforming (RFC 7230 3.2.2). See
	// headerHasToken.
	if !headerHasToken(res.Header, "Connection", "upgrade") {
		return fmt.Errorf(
			"invalid handshake response header Connection %s: %w",
			strings.Join(res.Header.Values("Connection"), ", "),
			ErrHandshakeResponseConnectionHeaderInvalid,
		)
	}
	if !headerHasToken(res.Header, "Upgrade", "websocket") {
		return fmt.Errorf(
			"invalid handshake response header Upgrade %s: %w",
			strings.Join(res.Header.Values("Upgrade"), ", "),
			ErrHandshakeResponseUpgradeHeaderInvalid,
		)
	}
	if res.Header.Get("Sec-WebSocket-Accept") == "" {
		return fmt.Errorf("invalid handshake response header Sec-WebSocket-Accept: %w", ErrHandshakeResponseAcceptHeaderMissing)
	}
	sec_ws_accept := GenerateSecWebsocketAccept(sec_websocket_key)
	if res.Header.Get("Sec-WebSocket-Accept") != sec_ws_accept {
		return fmt.Errorf("invalid handshake response header Sec-WebSocket-Accept %s, want %s: %w",
			res.Header.Get("Sec-WebSocket-Accept"), sec_ws_accept, ErrHandshakeResponseAcceptHeaderMismatch)
	}

	// optional
	// if res.Header.Get("Sec-WebSocket-Version") != "13" {
	// 	return errors.New("invalid handshake response header Sec-WebSocket-Version. The Sec-WebSocket-Version header is must be 13")
	// }
	return nil
}

func GenerateWebSocketKey() string {
	return generateWebSocketKey(generateWebSocketKeyDI{
		randRead: rand.Read,
	})
}

type generateWebSocketKeyDI struct {
	randRead func(b []byte) (n int, err error)
}

func generateWebSocketKey(di generateWebSocketKeyDI) string {
	key := make([]byte, 16)
	di.randRead(key)
	return base64.StdEncoding.EncodeToString(key)
}

// Function to convert http.Request to a plain HTTP message
func RequestToPlainHTTPMsg(req *http.Request) (string, error) {
	return requestToPlainHTTPMsg(req, requestToPlainHTTPMsgDI{
		ioReadAll: io.ReadAll,
	})
}

type requestToPlainHTTPMsgDI struct {
	ioReadAll func(r io.Reader) ([]byte, error)
}

func requestToPlainHTTPMsg(req *http.Request, di requestToPlainHTTPMsgDI) (string, error) {
	// Create a buffer to hold the entire HTTP message
	var buf bytes.Buffer

	// Write the top line (e.g., "GET / HTTP/1.1")
	// RequestURI() gives the origin form (path + query, "/" when the path is
	// empty). Writing req.URL instead would emit the absolute form
	// "GET ws://host:port HTTP/1.1", which servers read as a proxy request and
	// routers answer with a 301 redirect.
	fmt.Fprintf(&buf, "%s %s %s\r\n", req.Method, req.URL.RequestURI(), req.Proto)

	// Write the headers
	if req.Header.Get("Host") == "" { // put Host header if not exists
		req.Header.Set("Host", req.URL.Host)
	}
	for key, values := range req.Header {
		for _, value := range values {
			fmt.Fprintf(&buf, "%s: %s\r\n", key, value)
		}
	}

	// End the headers section
	buf.WriteString("\r\n")

	// Write the body if it's not nil
	if req.Body != nil {
		bodyBytes, err := di.ioReadAll(req.Body)
		if err != nil {
			return "", err
		}
		// Write the body
		buf.Write(bodyBytes)
		// Close the body
		req.Body.Close() // Important to close the body after reading
	}

	return buf.String(), nil
}
