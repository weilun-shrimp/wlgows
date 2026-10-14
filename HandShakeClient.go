package wlgows

import (
	"bufio"
	"bytes"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"slices"
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

w is the returned *Conn's writer, such as bufio.NewWriter(c) — see NewConn.

A response that breaks a rule RFC 6455 4.1 answers with "Fail the WebSocket
Connection" closes c before the error returns (7.1.7): a 101 that is not
HTTP/1.1, or a wrong Upgrade, Connection, Sec-WebSocket-Accept or
Sec-WebSocket-Protocol. Every other error leaves c open, and closing it is
yours. A status other than 101 is one of them, since 4.1 has it handled as
plain HTTP, such as a redirect or a 401.
ClientMustFailConnection(err) tells the two apart.
*/
func ClientHandShake(c net.Conn, r *bufio.Reader, w *bufio.Writer, req *http.Request) (*Conn, *http.Response, error) {
	return clientHandShake(c, r, w, req, clientHandShakeDI{
		upgradeRequest:            UpgradeRequest,
		sendHandShakeRequest:      SendHandShakeRequest,
		readHandShakeResponse:     ReadHandShakeResponse,
		validateHandShakeResponse: ValidateHandShakeResponse,
		validateSubprotocol:       ValidateHandShakeResponseSubprotocol,
		newConn:                   NewConn,
	})
}

type clientHandShakeDI struct {
	upgradeRequest            func(req *http.Request) error
	sendHandShakeRequest      func(w io.Writer, req *http.Request) error
	readHandShakeResponse     func(r *bufio.Reader, req *http.Request) (*http.Response, error)
	validateHandShakeResponse func(res *http.Response, sec_websocket_key string) error
	validateSubprotocol       func(res *http.Response, req *http.Request) error
	newConn                   func(c net.Conn, r *bufio.Reader, w *bufio.Writer, maskSendFrame bool) (*Conn, error)
}

func clientHandShake(c net.Conn, r *bufio.Reader, w *bufio.Writer, req *http.Request, di clientHandShakeDI) (*Conn, *http.Response, error) {
	if err := di.upgradeRequest(req); err != nil {
		return nil, nil, err
	}
	if err := di.sendHandShakeRequest(c, req); err != nil {
		return nil, nil, err
	}
	res, err := di.readHandShakeResponse(r, req)
	if err != nil {
		return nil, res, err
	}
	if err := di.validateHandShakeResponse(res, req.Header.Get("Sec-WebSocket-Key")); err != nil {
		if ClientMustFailConnection(err) {
			c.Close() // the error is the report 7.1.7 allows; a failed close adds nothing to it
		}
		return nil, res, err
	}
	if err := di.validateSubprotocol(res, req); err != nil {
		if ClientMustFailConnection(err) {
			c.Close() // the error is the report 7.1.7 allows; a failed close adds nothing to it
		}
		return nil, res, err
	}
	conn, err := di.newConn(c, r, w, true)
	if err != nil {
		return nil, res, err
	}
	return conn, res, nil
}

/*
UpgradeRequest makes req a valid opening handshake (RFC 6455 4.1): GET, HTTP/1.1,
no body and the four upgrade headers, whatever req had before. Anything else you set is
sent as it is. A url with no host has no Host to send, so it returns
ErrUrlHostEmpty.
*/
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
	if req.URL == nil || req.URL.Host == "" {
		return fmt.Errorf("UpgradeRequest: %w", ErrUrlHostEmpty)
	}
	req.Method = "GET"
	req.Proto, req.ProtoMajor, req.ProtoMinor = "HTTP/1.1", 1, 1
	req.Body = nil // bytes after the headers would be read as the first frame
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
	// "Connection: keep-alive, Upgrade" is conforming (RFC 7230 6.1). See
	// headerHasToken.
	if !headerHasToken(res.Header, "Connection", "upgrade") {
		return fmt.Errorf(
			"invalid handshake response header Connection %s: %w",
			strings.Join(res.Header.Values("Connection"), ", "),
			ErrHandshakeResponseConnectionHeaderInvalid,
		)
	}
	// Compared whole, not as a token list: RFC 6455 4.1 fails any value other
	// than "websocket", so "websocket, h2c" or a second line is refused.
	if upgrade := res.Header.Values("Upgrade"); len(upgrade) != 1 || !strings.EqualFold(strings.TrimSpace(upgrade[0]), "websocket") {
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

	// optional - RFC 6455 doesn't put Sec-WebSocket-Version in a successful 101 response at all
	// if res.Header.Get("Sec-WebSocket-Version") != "13" {
	// 	return errors.New("invalid handshake response header Sec-WebSocket-Version. The Sec-WebSocket-Version header is must be 13")
	// }
	return nil
}

/*
ClientMustFailConnection reports whether err is a response check RFC 6455
4.1 answers with "Fail the WebSocket Connection", which means closing it
(7.1.7). The list follows 4.1's order. A version other than HTTP/1.1 is one:
the check runs only on a 101, and a 101 that is not a valid HTTP/1.1 upgrade
cannot be read as one.

ClientHandShake has already closed the connection for these. If you run the
checks yourself, close it. Only for errors from the client handshake: for
errors from Listen, see StandardClosePayloadFor.
*/
func ClientMustFailConnection(err error) bool {
	return errors.Is(err, ErrHandshakeResponseProtoInvalid) ||
		errors.Is(err, ErrHandshakeResponseUpgradeHeaderInvalid) ||
		errors.Is(err, ErrHandshakeResponseConnectionHeaderInvalid) ||
		errors.Is(err, ErrHandshakeResponseAcceptHeaderMissing) ||
		errors.Is(err, ErrHandshakeResponseAcceptHeaderMismatch) ||
		errors.Is(err, ErrHandshakeResponseSubprotocolMultiple) ||
		errors.Is(err, ErrHandshakeResponseSubprotocolNotOffered)
}

/*
ValidateHandShakeResponseSubprotocol checks the server's Sec-WebSocket-Protocol
against what req offered (RFC 6455 4.1). No header passes: the server picked
none. One value the client offered passes. More than one value, on one line or
several, returns ErrHandshakeResponseSubprotocolMultiple. One value never
offered, including any value when nothing was offered, returns
ErrHandshakeResponseSubprotocolNotOffered.

Whether no subprotocol is acceptable is the caller's choice, so it is not
checked here.
*/
func ValidateHandShakeResponseSubprotocol(res *http.Response, req *http.Request) error {
	lines := res.Header.Values("Sec-WebSocket-Protocol")
	// No header means the server picked none (RFC 6455 4.2.2). An empty header
	// is still one line: the server sent something, just something invalid.
	if len(lines) == 0 {
		return nil
	}
	var chosen []string
	for _, line := range lines {
		for _, value := range strings.Split(line, ",") {
			chosen = append(chosen, strings.TrimSpace(value))
		}
	}
	if len(chosen) > 1 {
		return fmt.Errorf("invalid handshake response header Sec-WebSocket-Protocol %q: %w",
			strings.Join(lines, ", "), ErrHandshakeResponseSubprotocolMultiple)
	}
	if offered := RequestSubprotocols(req); !slices.Contains(offered, chosen[0]) {
		return fmt.Errorf("invalid handshake response header Sec-WebSocket-Protocol %q, offered %q: %w",
			chosen[0], offered, ErrHandshakeResponseSubprotocolNotOffered)
	}
	return nil
}

func GenerateWebSocketKey() string {
	return generateWebSocketKey(generateWebSocketKeyDI{
		// Go 1.24+: crypto/rand.Read never returns an error; it crashes the
		// program instead.
		randRead: rand.Read,
	})
}

type generateWebSocketKeyDI struct {
	randRead func(b []byte) (n int, err error)
}

func generateWebSocketKey(di generateWebSocketKeyDI) string {
	key := make([]byte, 16) // RFC 6455 4.1: a random 16 byte nonce
	_, _ = di.randRead(key)
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
