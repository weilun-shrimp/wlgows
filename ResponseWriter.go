package wlgows

import (
	"bytes"
	"crypto/sha1"
	"encoding/base64"
	"errors"
	"hash"
	"io"
	"net/http"
	"strconv"
)

type ResponseWriter struct {
	statusCode int
	header     http.Header
	body       []byte
	di         responseWriterDI
}

type responseWriterDI struct {
	generateSecWebsocketAccept func(sec_websocket_key string) string
}

func NewResponseWriter() *ResponseWriter {
	return &ResponseWriter{
		header: http.Header{},
		body:   make([]byte, 0),
		di: responseWriterDI{
			generateSecWebsocketAccept: GenerateSecWebsocketAccept,
		},
	}
}

func (w *ResponseWriter) Header() http.Header {
	return w.header
}

func (w *ResponseWriter) Write(data_append []byte) (int, error) {
	w.body = append(w.body, data_append...)
	return len(data_append), nil
}

func (w *ResponseWriter) WriteHeader(statusCode int) {
	w.statusCode = statusCode
}

/*
Sets Content-Length when it is not set, except on 1xx and 204, which RFC 7230
3.3.2 forbids, and 304.

Sets Content-Type to text/plain when the body is not empty and Content-Type is not set.

Those statuses carry no body. Don't write a body or set Content-Length on them.
*/
func (w *ResponseWriter) GenerateResponse() *http.Response {
	noBody := w.statusCode >= 100 && w.statusCode < 200 ||
		w.statusCode == http.StatusNoContent || w.statusCode == http.StatusNotModified
	if !noBody && w.header.Get("Content-Length") == "" {
		w.header.Set("Content-Length", strconv.Itoa(len(w.body)))
	}
	if len(w.body) > 0 && w.header.Get("Content-Type") == "" {
		w.header.Set("Content-Type", "text/plain")
	}
	response := &http.Response{
		Proto:      "HTTP/1.1",
		ProtoMajor: 1,
		ProtoMinor: 1,
		StatusCode: w.statusCode,
		Header:     w.header,
		Body:       io.NopCloser(bytes.NewReader(w.body)),
	}
	return response
}

// DeclineByError picks the status a failed handshake should answer with.
// errors.Is unwraps, so an error carrying context still routes on the sentinel
// it wraps; anything unrecognised is a 500.
func (w *ResponseWriter) DeclineByError(err error) {
	switch {
	case errors.Is(err, ErrHttpMsgFormationInvalid):
		w.WriteHeader(http.StatusBadRequest)

	// RFC 9110 15.5.6: a 405 must list the methods the resource allows.
	case errors.Is(err, ErrHttpMethodNotAllowed):
		w.WriteHeader(http.StatusMethodNotAllowed)
		w.Header().Set("Allow", "GET")

	case errors.Is(err, ErrHttpProtocolOrVersionNotAllowed):
		w.WriteHeader(http.StatusHTTPVersionNotSupported)

	// 4.4: the answer names the version this package speaks, so a client on an
	// older draft can retry rather than guess. RFC 9110 15.5.22 requires a 426
	// to name the protocol in Upgrade, and 7.8 to list upgrade in Connection.
	case errors.Is(err, ErrHttpSecWebSocketVersionNotSupported):
		w.WriteHeader(http.StatusUpgradeRequired)
		w.Header().Set("Sec-WebSocket-Version", "13")
		w.Header().Set("Upgrade", "websocket")
		w.Header().Set("Connection", "Upgrade")
		w.Write([]byte(err.Error()))

	case errors.Is(err, ErrHttpHostHeaderInvalid),
		errors.Is(err, ErrHttpSecWebSocketKeyHeaderNotSet),
		errors.Is(err, ErrHttpSecWebSocketKeyInvalid),
		errors.Is(err, ErrHttpSecWebSocketKeyDuplicated),
		errors.Is(err, ErrHttpSecWebSocketVersionDuplicated),
		errors.Is(err, ErrHttpConnectionHeaderNotUpgrade),
		errors.Is(err, ErrHttpUpgradeHeaderNotWebsocket):
		w.WriteHeader(http.StatusBadRequest)
		w.Write([]byte(err.Error()))

	default: // nil, or an error this package did not produce
		w.WriteHeader(http.StatusInternalServerError)
	}
}

func (w *ResponseWriter) UpgradeForWebsocket(sec_websocket_key string) {
	w.WriteHeader(http.StatusSwitchingProtocols)
	for k, v := range map[string]string{
		"Upgrade":              "websocket",
		"Connection":           "Upgrade",
		"Sec-WebSocket-Accept": w.di.generateSecWebsocketAccept(sec_websocket_key),
	} {
		w.Header().Add(k, v)
	}
}

func GenerateSecWebsocketAccept(sec_websocket_key string) string {
	return generateSecWebsocketAccept(sec_websocket_key, generateSecWebsocketAcceptDI{
		sha1New:       sha1.New,
		ioWriteString: io.WriteString,
	})
}

type generateSecWebsocketAcceptDI struct {
	sha1New       func() hash.Hash
	ioWriteString func(w io.Writer, s string) (n int, err error)
}

func generateSecWebsocketAccept(sec_websocket_key string, di generateSecWebsocketAcceptDI) string {
	hasher := di.sha1New()
	di.ioWriteString(hasher, sec_websocket_key+"258EAFA5-E914-47DA-95CA-C5AB0DC85B11")
	return base64.StdEncoding.EncodeToString((hasher.Sum(nil)))
}
