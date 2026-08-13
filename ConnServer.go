package wlgows

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
)

type ServerConn struct {
	Conn
	di serverConnDI
}

type serverConnDI struct {
	readRequest              func() (*http.Request, error)
	validateHandShakeRequest func(client_request *http.Request) error
	newResponseWriter        func() *ResponseWriter
	sendHand                 func(w *ResponseWriter) (*http.Response, error)
	responseToPlainHTTPMsg   func(resp *http.Response) (string, error)
	bufioNewReader           func(rd io.Reader) *bufio.Reader
	httpReadRequest          func(b *bufio.Reader) (*http.Request, error)
	fmtPrintln               func(a ...any) (n int, err error)
}

func NewServerConn(c net.Conn, req *http.Request) *ServerConn {
	sc := &ServerConn{Conn: *NewConn(c, req, nil, false)}
	sc.di = serverConnDI{
		readRequest:              sc.ReadRequest,
		validateHandShakeRequest: ValidateHandShakeRequest,
		newResponseWriter:        NewResponseWriter,
		sendHand:                 sc.SendHand,
		responseToPlainHTTPMsg:   responseToPlainHTTPMsg,
		bufioNewReader:           bufio.NewReader,
		httpReadRequest:          http.ReadRequest,
		fmtPrintln:               fmt.Println,
	}
	return sc
}

func (sc *ServerConn) HandShake() (*http.Response, error) {
	if sc.Conn.ServerResponse != nil {
		return nil, errors.New(" Server connection detect the erorr in handshake process. Server Response has been set in connection")
	}
	// find invalid error
	var invalid error
	if sc.Conn.ClientRequest == nil { // fetch client request if needed.
		_, invalid = sc.di.readRequest()
	}
	if invalid == nil {
		invalid = sc.di.validateHandShakeRequest(sc.Conn.ClientRequest)
	}

	writer := sc.di.newResponseWriter()
	if invalid != nil {
		writer.DeclineByError(invalid)
	} else {
		writer.UpgradeForWebsocket(sc.Conn.ClientRequest.Header.Get("Sec-Websocket-Key"))
	}

	res, err := sc.di.sendHand(writer)
	if err == nil && invalid != nil {
		err = invalid
	}
	return res, err
}

// Generate the http.Response and send back to client and put into sc.Conn.ServerResponse if error not occured
func (sc *ServerConn) SendHand(w *ResponseWriter) (*http.Response, error) {
	sc.Conn.di.writeLocker.Lock()
	defer sc.Conn.di.writeLocker.Unlock()
	res := w.GenerateResponse()
	plain_http_msg, err := sc.di.responseToPlainHTTPMsg(res)
	if err != nil {
		return res, err
	}
	_, err = sc.Write([]byte(plain_http_msg))
	if err == nil {
		sc.Conn.ServerResponse = res
		if sc.Conn.ClientRequest != nil {
			sc.Conn.ClientRequest.Response = res
			res.Request = sc.Conn.ClientRequest
		}
	}
	return res, err
}

// Read and decode the Client http request msg and set to server connection's client request
func (sc *ServerConn) ReadRequest() (*http.Request, error) {
	sc.Conn.di.readLocker.Lock()
	defer sc.Conn.di.readLocker.Unlock()
	if sc.Conn.ClientRequest != nil { // fetch client request if needed.
		return sc.Conn.ClientRequest, fmt.Errorf("ReadRequest: %w", ErrClientRequestHasSet)
	}

	req, err := sc.di.httpReadRequest(sc.di.bufioNewReader(sc))
	if err != nil {
		sc.di.fmtPrintln("Error reading request:", err)
		return nil, fmt.Errorf("ReadRequest: %w: %w", ErrHttpMsgFormationInvalid, err)
	}
	sc.Conn.ClientRequest = req
	return req, nil
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

// The send path is deliberately absent in v3 for now. Server frames are allowed
// to go unmasked (RFC 6455 5.1), so whatever replaces it may pass need_mask
// false.
