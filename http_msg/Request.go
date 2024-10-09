package http_msg

import "net/url"

type Request struct {
	Method   string  `json:"method"`
	Url      url.URL `json:"url"`
	Protocol string  `json:"protocal"`
	Version  string  `json:"version"`
	Header   Header  `json:"header"`
	Body     string  `json:"body"`
}

// return like "GET / HTTP/1.1"
func (rq *Request) EncodeTop() string {
	return rq.Method + " " + rq.Url.String() + " " + rq.Protocol + "/" + rq.Version
}

/*
return like
GET / HTTP/1.1\r\n
Content-Type: application/json\r\n
\r\n
Body......
*/
func (rq *Request) Encode() string {
	// if rq.Header
	return rq.EncodeTop() + "\r\n" + rq.Header.Encode() + "\r\n" + rq.Body
}
