package http_msg

import (
	"bytes"
	"io"
	"net/http"
)

type ResponseWriter struct {
	statusCode int
	header     Header
	body       []byte
}

func (w *ResponseWriter) Header() Header {
	return w.header
}

func (w *ResponseWriter) Write(body []byte) {
	w.body = body
}

func (w *ResponseWriter) WriteHeader(statusCode int) {
	w.statusCode = statusCode
}

func (w *ResponseWriter) GenerateResponse() *http.Response {
	response := &http.Response{
		StatusCode: w.statusCode,
		Header:     make(http.Header),
		Body:       io.NopCloser(bytes.NewReader(w.body)),
	}
	return response
}
