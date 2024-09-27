package connection

import (
	"bufio"
	"bytes"
	"crypto/sha1"
	"encoding/base64"
	"io"
	"net/http"
	"strconv"
)

type ResponseWriter struct {
	http.ResponseWriter
	statusCode  int
	header      http.Header
	body_buff   *bytes.Buffer
	body_writer bufio.Writer
}

func NewResponseWriter() *ResponseWriter {
	buf := bytes.NewBufferString("")
	return &ResponseWriter{
		header:      http.Header{},
		body_buff:   buf,
		body_writer: *bufio.NewWriter(buf),
	}
}

func (w *ResponseWriter) Header() http.Header {
	return w.header
}

func (w *ResponseWriter) Write(data_append []byte) (int, error) {
	return w.body_writer.Write(data_append)
}

func (w *ResponseWriter) WriteHeader(statusCode int) {
	w.statusCode = statusCode
}

/*
Will set the Content-Length automatically if header inexists

Will set Content-Type to text/plain automatically if body is not empty and Content-Type is not set
*/
func (w *ResponseWriter) GenerateResponse() *http.Response {
	if w.header.Get("Content-Length") == "" {
		w.header.Set("Content-Length", strconv.Itoa(w.body_buff.Len()))
	}
	if w.body_buff.Len() > 0 && w.header.Get("Content-Type") == "" {
		w.header.Set("Content-Type", "text/plain")
	}
	response := &http.Response{
		Proto:      "HTTP/1.1",
		ProtoMajor: 1,
		ProtoMinor: 1,
		StatusCode: w.statusCode,
		Header:     w.header,
		Body:       io.NopCloser(bytes.NewReader(w.body_buff.Bytes())),
	}
	return response
}

/*
Set the ResponseWriter appropriate status code and body by error_type

empty error type means valid request, will put 101 for websocket
*/
func (w *ResponseWriter) DeclineByErrorType(error_type string) {
	switch error_type {
	case HttpMsgFormationInvalid:
		w.WriteHeader(http.StatusBadRequest)
	case HttpMethodNotAllowed:
		w.WriteHeader(http.StatusMethodNotAllowed)
	case HttpProtocolOrVersionNotAllowed:
		w.WriteHeader(http.StatusHTTPVersionNotSupported)
	case HttpSecWebSocketKeyHeaderNotSet, HttpConnectionHeaderNotUpgrade, HttpUpgradeHeaderNotWebsocket:
		w.WriteHeader(http.StatusBadRequest)
		w.Write([]byte(string(error_type)))
	default: // other undefined type value
		w.WriteHeader(http.StatusInternalServerError)
	}
}

func (w *ResponseWriter) UpgradeForWebsocket(sec_websocket_key string) {
	w.WriteHeader(http.StatusSwitchingProtocols)
	for k, v := range map[string]string{
		"Upgrade":               "websocket",
		"Connection":            "Upgrade",
		"Sec-WebSocket-Version": "13",
		"Sec-WebSocket-Accept":  generateSecWebsocketAccept(sec_websocket_key),
	} {
		w.Header().Add(k, v)
	}
	// if val, ok := c.ClientInfo.Headers["Sec-WebSocket-Protocol"]; ok && val != "" { // Protocol 可以自己定義，自己做溝通用
	// 	c.ServerInfo.Header.Add("Sec-WebSocket-Protocol", val)
	// }
}

func generateSecWebsocketAccept(sec_websocket_key string) string {
	hasher := sha1.New()
	io.WriteString(hasher, sec_websocket_key+"258EAFA5-E914-47DA-95CA-C5AB0DC85B11")
	return base64.StdEncoding.EncodeToString((hasher.Sum(nil)))
}
