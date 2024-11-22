package connection

import (
	"bufio"
	"bytes"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"

	"github.com/weilun-shrimp/wlgows/msg"
)

type ClientConn struct {
	Conn
}

func (cc *ClientConn) HandShake() error {
	if err := cc.UpgradeRequest(); err != nil {
		return err
	}
	if err := cc.SendHand(); err != nil {
		return err
	}
	if err := cc.ReadResponse(); err != nil {
		return err
	}
	if err := ValidateHandShakeResponse(cc.ServerResponse, cc.ClientRequest.Header.Get("Sec-WebSocket-Key")); err != nil {
		return err
	}
	return nil
}

// upgrade request for websocket
func (cc *ClientConn) UpgradeRequest() error {
	if cc.ClientRequest == nil {
		return errors.New(" ClientConn detect the ClientRequest is nil on UpgradeRequest process")
	}
	for k, v := range map[string]string{
		"Upgrade":               "websocket",
		"Connection":            "Upgrade",
		"Sec-WebSocket-Key":     generateWebSocketKey(),
		"Sec-WebSocket-Version": "13",
	} {
		cc.ClientRequest.Header.Set(k, v)
	}
	return nil
}

// send plain http msg to server
func (cc *ClientConn) SendHand() error {
	if cc.ClientRequest == nil {
		return errors.New(" ClientConn detect the ClientRequest is nil on handshake process")
	}
	plainHttpRequestMsg, err := requestToPlainHTTPMsg(cc.ClientRequest)
	if err != nil {
		return err
	}
	_, err = cc.Write([]byte(plainHttpRequestMsg))
	if err != nil {
		return err
	}
	return nil
}

// read response from server
func (cc *ClientConn) ReadResponse() error {
	if cc.ServerResponse != nil {
		return errors.New(" ClientConn detect the ServerResponse has been set before ReadResponse()")
	}
	if cc.ClientRequest == nil {
		return errors.New(" ClientConn detect the ClientRequest is nil on handshake process ReadResponse()")
	}
	res, err := http.ReadResponse(bufio.NewReader(cc), cc.ClientRequest)
	if err != nil {
		return err
	}
	cc.ServerResponse = res
	return nil
}

func ValidateHandShakeResponse(res *http.Response, sec_websocket_key string) error {
	if res.StatusCode != 101 {
		return errors.New("invalid handshake response status code " + strconv.Itoa(res.StatusCode) + ". The valid status code is 101")
	}
	if res.Proto != "HTTP/1.1" {
		return errors.New("invalid handshake response proto " + res.Proto + ". The valid proto is HTTP/1.1")
	}
	if strings.ToLower(res.Header.Get("Connection")) != "upgrade" {
		return fmt.Errorf(
			"invalid handshake response header Connection %s. The valid value should be Upgrade",
			res.Header.Get("Connection"),
		)
	}
	if strings.ToLower(res.Header.Get("Upgrade")) != "websocket" {
		return fmt.Errorf(
			"invalid handshake response header Upgrade %s. The valid value should be websocket",
			res.Header.Get("Upgrade"),
		)
	}
	if res.Header.Get("Sec-WebSocket-Accept") == "" {
		return errors.New("invalid handshake response header Sec-WebSocket-Accept. The Sec-WebSocket-Accept header is required")
	}
	sec_ws_accept := generateSecWebsocketAccept(sec_websocket_key)
	if res.Header.Get("Sec-WebSocket-Accept") != sec_ws_accept {
		return fmt.Errorf(`invalid handshake response header Sec-WebSocket-Accept %s.
			The valid value should be %s.
		`, res.Header.Get("Sec-WebSocket-Accept"), sec_ws_accept)
	}

	// optional
	// if res.Header.Get("Sec-WebSocket-Version") != "13" {
	// 	return errors.New("invalid handshake response header Sec-WebSocket-Version. The Sec-WebSocket-Version header is must be 13")
	// }
	return nil
}

func generateWebSocketKey() string {
	key := make([]byte, 16)
	rand.Read(key)
	return base64.StdEncoding.EncodeToString(key)
}

// Function to convert http.Request to a plain HTTP message
func requestToPlainHTTPMsg(req *http.Request) (string, error) {
	// Create a buffer to hold the entire HTTP message
	var buf bytes.Buffer

	// Write the top line (e.g., "GET / HTTP/1.1")
	fmt.Fprintf(&buf, "%s %s %s\r\n", req.Method, req.URL, req.Proto)

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
		bodyBytes, err := io.ReadAll(req.Body)
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

// client side is not allowed not to mask the payload
func (cc *ClientConn) SendText(text []byte) error {
	send_msg, err := msg.NewMsg(text, 1, true)
	if err != nil {
		return err
	}
	return cc.SendMsg(send_msg)
}

// client side is not allowed not to mask the payload
func (cc *ClientConn) SendByte(byte_data []byte) error {
	send_msg, err := msg.NewMsg(byte_data, 2, true)
	if err != nil {
		return err
	}
	return cc.SendMsg(send_msg)
}
