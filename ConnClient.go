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
	"strconv"
	"strings"
)

type ClientConn struct {
	Conn
	di clientConnDI
}

type clientConnDI struct {
	upgradeRequest            func(req *http.Request) error
	sendHand                  func() error
	readResponse              func() error
	validateHandShakeResponse func(res *http.Response, sec_websocket_key string) error
	requestToPlainHTTPMsg     func(req *http.Request) (string, error)
	bufioNewReader            func(rd io.Reader) *bufio.Reader
	httpReadResponse          func(r *bufio.Reader, req *http.Request) (*http.Response, error)
}

func NewClientConn(c net.Conn, req *http.Request) *ClientConn {
	cc := &ClientConn{Conn: *NewConn(c, req, nil, true)}
	cc.di = clientConnDI{
		upgradeRequest:            UpgradeRequest,
		sendHand:                  cc.SendHand,
		readResponse:              cc.ReadResponse,
		validateHandShakeResponse: ValidateHandShakeResponse,
		requestToPlainHTTPMsg:     RequestToPlainHTTPMsg,
		bufioNewReader:            bufio.NewReader,
		httpReadResponse:          http.ReadResponse,
	}
	return cc
}

func (cc *ClientConn) HandShake() error {
	if err := cc.di.upgradeRequest(cc.ClientRequest); err != nil {
		return err
	}
	if err := cc.di.sendHand(); err != nil {
		return err
	}
	if err := cc.di.readResponse(); err != nil {
		return err
	}
	if err := cc.di.validateHandShakeResponse(cc.ServerResponse, cc.ClientRequest.Header.Get("Sec-WebSocket-Key")); err != nil {
		return err
	}
	return nil
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
		return errors.New(" ClientConn detect the ClientRequest is nil on UpgradeRequest process")
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

// send plain http msg to server
func (cc *ClientConn) SendHand() error {
	cc.Conn.di.writeLocker.Lock()
	defer cc.Conn.di.writeLocker.Unlock()
	if cc.ClientRequest == nil {
		return errors.New(" ClientConn detect the ClientRequest is nil on handshake process")
	}
	plainHttpRequestMsg, err := cc.di.requestToPlainHTTPMsg(cc.ClientRequest)
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
	cc.Conn.di.readLocker.Lock()
	defer cc.Conn.di.readLocker.Unlock()
	if cc.ServerResponse != nil {
		return errors.New(" ClientConn detect the ServerResponse has been set before ReadResponse()")
	}
	if cc.ClientRequest == nil {
		return errors.New(" ClientConn detect the ClientRequest is nil on handshake process ReadResponse()")
	}
	res, err := cc.di.httpReadResponse(cc.di.bufioNewReader(cc), cc.ClientRequest)
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
	// Read as a token list, not compared whole — a server answering
	// "Connection: keep-alive, Upgrade" is conforming (RFC 7230 3.2.2). See
	// headerHasToken.
	if !headerHasToken(res.Header, "Connection", "upgrade") {
		return fmt.Errorf(
			"invalid handshake response header Connection %s. The valid value should contain the Upgrade token",
			strings.Join(res.Header.Values("Connection"), ", "),
		)
	}
	if !headerHasToken(res.Header, "Upgrade", "websocket") {
		return fmt.Errorf(
			"invalid handshake response header Upgrade %s. The valid value should contain the websocket token",
			strings.Join(res.Header.Values("Upgrade"), ", "),
		)
	}
	if res.Header.Get("Sec-WebSocket-Accept") == "" {
		return errors.New("invalid handshake response header Sec-WebSocket-Accept. The Sec-WebSocket-Accept header is required")
	}
	sec_ws_accept := GenerateSecWebsocketAccept(sec_websocket_key)
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

// The send path is deliberately absent in v3 for now. Client frames must be
// masked (RFC 6455 5.3), so whatever replaces it has to keep need_mask true.
