package wlgows

import (
	"bufio"
	"bytes"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
)

type ServerHandShakeRespWriter interface {
	DeclineByError(error)
	UpgradeForWebsocket(string)
	GenerateResponse() *http.Response
}

/*
ServerHandShake runs the RFC 6455 4.1 server opening handshake over c: it
validates req and writes back either a declining response or an upgrading
one. req must already be parsed — read it yourself first (e.g.
http.ReadRequest for a raw accept, or from whatever already parsed it for a
hijacked connection). On success it wraps c in a ready-to-use *Conn —
unmasked, since 5.1 forbids a server masking what it sends. conn is nil
whenever err is non-nil; the response is still returned so a caller can
inspect what actually went out.

r must be the same *bufio.Reader you read req from (or, for an
already-parsed req such as a hijacked connection's, the same reader you go on
to read frames from) — the returned *Conn reuses it rather than wrapping c
again.
*/
func ServerHandShake(c net.Conn, r *bufio.Reader, req *http.Request) (*Conn, *http.Response, error) {
	res, err := serverHandShake(c, req, serverHandShakeDI{
		validateHandShakeRequest: ValidateHandShakeRequest,
		newResponseWriter:        func() ServerHandShakeRespWriter { return NewResponseWriter() },
		sendHandShakeResponse:    SendHandShakeResponse,
	})
	if err != nil {
		return nil, res, err
	}
	return NewConn(c, r, false), res, nil
}

type serverHandShakeDI struct {
	validateHandShakeRequest func(req *http.Request) error
	newResponseWriter        func() ServerHandShakeRespWriter
	sendHandShakeResponse    func(w io.Writer, res *http.Response) error
}

func serverHandShake(w io.Writer, req *http.Request, di serverHandShakeDI) (*http.Response, error) {
	if req == nil {
		return nil, fmt.Errorf("ServerHandShake: %w", ErrHandshakeRequestNil)
	}

	invalid := di.validateHandShakeRequest(req)

	writer := di.newResponseWriter()
	if invalid != nil {
		writer.DeclineByError(invalid)
	} else {
		writer.UpgradeForWebsocket(req.Header.Get("Sec-Websocket-Key"))
	}

	res := writer.GenerateResponse()
	err := di.sendHandShakeResponse(w, res)
	if err == nil && invalid != nil {
		err = invalid
	}
	req.Response = res
	res.Request = req
	return res, err
}

/*
ValidateHandShakeRequest checks a client's opening request against RFC 6455 4.1,
returning the first rule it breaks wrapped around the sentinel for it, which is
what DeclineByError routes on to pick a status code.

Connection and Upgrade are read as token lists rather than compared whole — see
headerHasToken. The rest are single values the RFC fixes exactly.

The failing value is reported as it arrived, joined across lines if it came on
more than one, so a rejected handshake says what the peer actually sent.
*/
func ValidateHandShakeRequest(client_request *http.Request) error {
	// top validation
	if client_request.Method != "GET" {
		return fmt.Errorf("validate handshake: method %q: %w", client_request.Method, ErrHttpMethodNotAllowed)
	}
	if client_request.Proto != "HTTP/1.1" {
		return fmt.Errorf("validate handshake: protocol %q: %w", client_request.Proto, ErrHttpProtocolOrVersionNotAllowed)
	}
	// header validation
	if val := client_request.Header.Get("Sec-WebSocket-Key"); val == "" {
		return fmt.Errorf("validate handshake: %w", ErrHttpSecWebSocketKeyHeaderNotSet)
	}
	if !headerHasToken(client_request.Header, "Connection", "upgrade") {
		return fmt.Errorf("validate handshake: Connection is %q: %w",
			strings.Join(client_request.Header.Values("Connection"), ", "),
			ErrHttpConnectionHeaderNotUpgrade)
	}
	if !headerHasToken(client_request.Header, "Upgrade", "websocket") {
		return fmt.Errorf("validate handshake: Upgrade is %q: %w",
			strings.Join(client_request.Header.Values("Upgrade"), ", "),
			ErrHttpUpgradeHeaderNotWebsocket)
	}
	// 4.2.1: the header is required and 13 is the only version RFC 6455 defines.
	// Missing and wrong are the same answer — a client that sends neither is
	// speaking a draft this package does not implement.
	if val := client_request.Header.Get("Sec-WebSocket-Version"); val != "13" {
		return fmt.Errorf("validate handshake: Sec-WebSocket-Version is %q: %w", val, ErrHttpSecWebSocketVersionNotSupported)
	}
	return nil
}

// SendHandShakeResponse writes res to w as a plain HTTP message.
func SendHandShakeResponse(w io.Writer, res *http.Response) error {
	return sendHandShakeResponse(w, res, sendHandShakeResponseDI{
		responseToPlainHTTPMsg: responseToPlainHTTPMsg,
	})
}

type sendHandShakeResponseDI struct {
	responseToPlainHTTPMsg func(res *http.Response) (string, error)
}

func sendHandShakeResponse(w io.Writer, res *http.Response, di sendHandShakeResponseDI) error {
	if res == nil {
		return fmt.Errorf("SendHandShakeResponse: %w", ErrHandshakeResponseNil)
	}
	plainHttpResponseMsg, err := di.responseToPlainHTTPMsg(res)
	if err != nil {
		return err
	}
	_, err = w.Write([]byte(plainHttpResponseMsg))
	return err
}

// Function to convert http.Response to a plain HTTP message
func responseToPlainHTTPMsg(resp *http.Response) (string, error) {
	return responseToPlainHTTPMsgInner(resp, responseToPlainHTTPMsgDI{
		ioReadAll:      io.ReadAll,
		httpStatusText: http.StatusText,
	})
}

type responseToPlainHTTPMsgDI struct {
	ioReadAll      func(r io.Reader) ([]byte, error)
	httpStatusText func(code int) string
}

func responseToPlainHTTPMsgInner(resp *http.Response, di responseToPlainHTTPMsgDI) (string, error) {
	// Create a buffer to hold the entire HTTP message
	var buf bytes.Buffer

	// Write the status line
	fmt.Fprintf(&buf, "%s %d %s\r\n", resp.Proto, resp.StatusCode, di.httpStatusText(resp.StatusCode))

	// Write the headers
	for key, values := range resp.Header {
		for _, value := range values {
			fmt.Fprintf(&buf, "%s: %s\r\n", key, value)
		}
	}

	// End the headers section
	buf.WriteString("\r\n")

	// Write the body if it's not nil
	if resp.Body != nil {
		bodyBytes, err := di.ioReadAll(resp.Body)
		if err != nil {
			return "", err
		}
		// Write the body
		buf.Write(bodyBytes)
		// Close the body
		resp.Body.Close() // Important to close the body after reading
	}

	return buf.String(), nil
}
