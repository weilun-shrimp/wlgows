package connection

import (
	"bufio"
	"bytes"
	"crypto/sha1"
	"encoding/base64"
	"fmt"
	"io"
	"net/http"

	"github.com/weilun-shrimp/wlgows/http_msg"
)

type ServerConn struct {
	Conn
}

func (c *Conn) HandShake() *Error {
	if c.ClientRequest == nil {
		return &Error{
			Type: EmptyClientRequest,
			Msg:  "Server handshake detect the client request empty.",
		}
	}
	if c.ServerInfo == nil {
		c.ServerInfo = &http_msg.Response{
			Protocal: "HTTP",
			Version:  "1.1",
			Header:   http_msg.Header{},
			Body:     "",
		}
		c.ServerInfo.SetStatus(http.StatusSwitchingProtocols)
	}
	for k, v := range map[string]string{
		"Upgrade":               "websocket",
		"Connection":            "Upgrade",
		"Sec-WebSocket-Version": "13",
		"Sec-WebSocket-Accept":  generateSecWebsocketAccept(c.ClientInfo.Headers["Sec-WebSocket-Key"]),
	} {
		c.ServerInfo.Header.Add(k, v)
	}
	// if val, ok := c.ClientInfo.Headers["Sec-WebSocket-Protocol"]; ok && val != "" { // Protocol 可以自己定義，自己做溝通用
	// 	c.ServerInfo.Header.Add("Sec-WebSocket-Protocol", val)
	// }

	_, err := c.Write([]byte(c.ServerInfo.Encode()))
	if err != nil {
		fmt.Println("Error sending WebSocket handshake response:", err)
		return &Error{
			Type: EmptyClient,
			Msg:  err.Error(),
		}
	}
	return nil
}

func (sc *ServerConn) AcceptClientHand() {
}

func (sc *ServerConn) RejectClientHand() {

}

func (sc *ServerConn) SendHand(w *http_msg.ResponseWriter, r *http.Request) (*http.Response, *Error) {
	if r.Response != nil {
		return nil, &Error{
			Type: HttpRequestHasResponse,
			Msg:  "Server Action SendHand detect the client request has response.",
		}
	}
}

// Read and decode the Client http request msg
func (sc *ServerConn) ReadClientHandShakeRequest() (*http.Request, *Error) {
	req, err := http.ReadRequest(bufio.NewReader(sc))
	if err != nil {
		fmt.Println("Error reading request:", err)
		return nil, &Error{
			Type: InvalidHttpMsgFormation,
			Msg:  "Server Action ReadClientHandShakeRequest detect the client passed the InvalidHttpMsgFormation. raw error msg => " + err.Error(),
		}
	}
	return req, nil
}

func ValidateClientHandShakeRequest(client_request *http.Request) *Error {
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

func generateSecWebsocketAccept(sec_websocket_key string) string {
	hasher := sha1.New()
	io.WriteString(hasher, sec_websocket_key+"258EAFA5-E914-47DA-95CA-C5AB0DC85B11")
	return base64.StdEncoding.EncodeToString((hasher.Sum(nil)))
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
