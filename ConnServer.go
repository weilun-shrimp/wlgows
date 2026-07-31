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
	readRequest              func() (*http.Request, *Error)
	validateHandShakeRequest func(client_request *http.Request) *Error
	newResponseWriter        func() *ResponseWriter
	sendHand                 func(w *ResponseWriter) (*http.Response, error)
	responseToPlainHTTPMsg   func(resp *http.Response) (string, error)
	bufioNewReader           func(rd io.Reader) *bufio.Reader
	httpReadRequest          func(b *bufio.Reader) (*http.Request, error)
	newMsg                   func(data []byte, opcode uint8, need_mask bool) (*Msg, error)
	fmtPrintln               func(a ...any) (n int, err error)
}

func NewServerConn(c net.Conn, req *http.Request) *ServerConn {
	sc := &ServerConn{Conn: *NewConn(c, req, nil)}
	sc.di = serverConnDI{
		readRequest:              sc.ReadRequest,
		validateHandShakeRequest: ValidateHandShakeRequest,
		newResponseWriter:        NewResponseWriter,
		sendHand:                 sc.SendHand,
		responseToPlainHTTPMsg:   responseToPlainHTTPMsg,
		bufioNewReader:           bufio.NewReader,
		httpReadRequest:          http.ReadRequest,
		newMsg:                   NewMsg,
		fmtPrintln:               fmt.Println,
	}
	return sc
}

func (sc *ServerConn) HandShake() (*http.Response, error) {
	if sc.Conn.ServerResponse != nil {
		return nil, errors.New(" Server connection detect the erorr in handshake process. Server Response has been set in connection")
	}
	// find invalid error
	var invalid *Error
	if sc.Conn.ClientRequest == nil { // fetch client request if needed.
		_, invalid = sc.di.readRequest()
	}
	if invalid == nil {
		invalid = sc.di.validateHandShakeRequest(sc.Conn.ClientRequest)
	}

	writer := sc.di.newResponseWriter()
	if invalid != nil {
		writer.DeclineByErrorType(invalid.Type)
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
func (sc *ServerConn) ReadRequest() (*http.Request, *Error) {
	sc.Conn.di.readLocker.Lock()
	defer sc.Conn.di.readLocker.Unlock()
	if sc.Conn.ClientRequest != nil { // fetch client request if needed.
		return sc.Conn.ClientRequest, &Error{
			Type: ClientRequestHasSet,
			Msg:  "Server Action ReadRequest detect the client request has set before read.",
		}
	}

	req, err := sc.di.httpReadRequest(sc.di.bufioNewReader(sc))
	if err != nil {
		sc.di.fmtPrintln("Error reading request:", err)
		return nil, &Error{
			Type: HttpMsgFormationInvalid,
			Msg:  "Server Action ReadRequest detect the client passed the InvalidHttpMsgFormation. raw error msg => " + err.Error(),
		}
	}
	sc.Conn.ClientRequest = req
	return req, nil
}

func ValidateHandShakeRequest(client_request *http.Request) *Error {
	// top validation
	if client_request.Method != "GET" {
		return &Error{
			Type: HttpMethodNotAllowed,
			Msg: "Server Action validate Client handshake fail." +
				"Method not equal to 'GET'. " +
				"Raw client handshake method => " + client_request.Method,
		}
	}
	if client_request.Proto != "HTTP/1.1" {
		return &Error{
			Type: HttpProtocolOrVersionNotAllowed,
			Msg: "Server Action validate Client handshake fail. " +
				"Protocol not equal to 'HTTP/1.1'. " +
				"Raw client handshake protocol => " + client_request.Proto,
		}
	}
	// header validation
	if val := client_request.Header.Get("Sec-WebSocket-Key"); val == "" {
		return &Error{
			Type: HttpSecWebSocketKeyHeaderNotSet,
			Msg: "Server Action validate Client handshake fail. " +
				"Header Sec-WebSocket-Key is not set correctly for websocket.",
		}
	}
	if val := strings.ToLower(client_request.Header.Get("Connection")); val != "upgrade" {
		return &Error{
			Type: HttpConnectionHeaderNotUpgrade,
			Msg: "Server Action validate Client handshake fail. " +
				"Header Connection is not set 'Upgrade' for websocket.",
		}
	}
	if val := strings.ToLower(client_request.Header.Get("Upgrade")); val != "websocket" {
		return &Error{
			Type: HttpUpgradeHeaderNotWebsocket,
			Msg: "Server Action validate Client handshake fail. " +
				"Header Upgrade is not set 'websocket' for websocket.",
		}
	}
	// 13 is option for your app, but in general, we set it in 13
	// if client_header["Sec-WebSocket-Version"] != "13" {
	// 	return client_header, errors.New("Sec-WebSocket-Version is not set \"13\" in client handshake header for websocket")
	// }
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

// server side be allowed not to mask the payload
func (sc *ServerConn) SendText(text []byte) error {
	send_msg, err := sc.di.newMsg(text, 1, false)
	if err != nil {
		return err
	}
	return sc.SendMsg(send_msg)
}

// server side is allowed not to mask the payload
func (sc *ServerConn) SendByte(byte_data []byte) error {
	send_msg, err := sc.di.newMsg(byte_data, 2, false)
	if err != nil {
		return err
	}
	return sc.SendMsg(send_msg)
}
