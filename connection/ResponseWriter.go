package connection

import (
	"bytes"
	"io"
	"net/http"
)

type ResponseWriter struct {
	statusCode int
	header     *http.Header
	body       []byte
}

func (w *ResponseWriter) Header() *http.Header {
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
		Header:     *w.header,
		Body:       io.NopCloser(bytes.NewReader(w.body)),
	}
	return response
}
