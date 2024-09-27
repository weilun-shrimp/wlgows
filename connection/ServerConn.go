package connection

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"io"
	"net/http"

	"github.com/weilun-shrimp/wlgows/msg"
)

type ServerConn struct {
	Conn
}

func (sc *ServerConn) HandShake() (*http.Response, error) {
	if sc.Conn.ServerResponse != nil {
		return nil, errors.New(" Server connection detect the erorr in handshake process. Server Response has been set in connection")
	}
	// find invalid error
	var invalid *Error
	if sc.Conn.ClientRequest == nil { // fetch client request if needed.
		_, invalid = sc.ReadRequest()
	}
	if invalid == nil {
		invalid = ValidateHandShakeRequest(sc.Conn.ClientRequest)
	}

	writer := NewResponseWriter()
	if invalid != nil {
		writer.DeclineByErrorType(invalid.Type)
	} else {
		writer.UpgradeForWebsocket(sc.Conn.ClientRequest.Header.Get("Sec-Websocket-Key"))
	}

	res, err := sc.SendHand(writer)
	return res, err
}

// Generate the http.Response and send back to client and put into sc.Conn.ServerResponse if error not occured
func (sc *ServerConn) SendHand(w *ResponseWriter) (*http.Response, error) {
	res := w.GenerateResponse()
	plain_http_msg, err := responseToPlainHTTPMsg(res)
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
	if sc.Conn.ClientRequest != nil { // fetch client request if needed.
		return sc.Conn.ClientRequest, &Error{
			Type: ClientRequestHasSet,
			Msg:  "Server Action ReadRequest detect the client request has set before read.",
		}
	}

	req, err := http.ReadRequest(bufio.NewReader(sc))
	if err != nil {
		fmt.Println("Error reading request:", err)
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
	if val := client_request.Header.Get("Connection"); val != "Upgrade" {
		return &Error{
			Type: HttpConnectionHeaderNotUpgrade,
			Msg: "Server Action validate Client handshake fail. " +
				"Header Connection is not set 'Upgrade' for websocket.",
		}
	}
	if val := client_request.Header.Get("Upgrade"); val != "websocket" {
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
	// Create a buffer to hold the entire HTTP message
	var buf bytes.Buffer

	// Write the status line
	fmt.Fprintf(&buf, "%s %d %s\r\n", resp.Proto, resp.StatusCode, http.StatusText(resp.StatusCode))

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
		bodyBytes, err := io.ReadAll(resp.Body)
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
	send_msg, err := msg.NewMsg(text, 1, false)
	if err != nil {
		return err
	}
	return sc.SendMsg(send_msg)
}

// server side is allowed not to mask the payload
func (sc *ServerConn) SendByte(byte_data []byte) error {
	send_msg, err := msg.NewMsg(byte_data, 2, false)
	if err != nil {
		return err
	}
	return sc.SendMsg(send_msg)
}
