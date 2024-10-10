package http_msg

import (
	"net/http"
	"strconv"
)

type Request struct {
	http.Request
	// to know this request whether has recieved the response from server
	Response *Response
}

// return like "GET / HTTP/1.1"
func (rq *Request) EncodeTop() string {
	return rq.Method + " " + rq.URL.String() + " " + rq.Protocol + "/" + rq.Version
}

/*
return like
GET / HTTP/1.1\r\n
Content-Type: application/json\r\n
\r\n
Body......
*/
func (rq *Request) Encode() string {
	if rq.Header.Get("Content-Length") == "" {
		rq.Header.Set("Content-Length", strconv.Itoa(len(rq.Body)))
	}
	return rq.EncodeTop() + "\r\n" + rq.Header.Encode() + "\r\n" + rq.Body
}
