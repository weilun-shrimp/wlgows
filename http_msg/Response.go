package http_msg

import (
	"net/http"
	"strconv"
)

type Response struct {
	Protocal    string `json:"protocal"`
	Version     string `json:"version"`
	StatusCode  int    `json:"status_code"`
	Description string `json:"description"`
	Header      Header `json:"header"`
	Body        string `json:"body"`
}

// return like "HTTP/1.1 101 Switching Protocols"
func (rs *Response) EncodeTop() string {
	return rs.Protocal + "/" + rs.Version + " " + strconv.Itoa(rs.StatusCode) + " " + rs.Description
}

/*
return like
HTTP/1.1 101 Switching Protocols\r\n
Content-Type: application/json\r\n
\r\n
Body......
*/
func (rs *Response) Encode() string {
	if rs.Header.Get("Content-Length") == "" {
		rs.Header.Set("Content-Length", strconv.Itoa(len(rs.Body)))
	}
	return rs.EncodeTop() + "\r\n" + rs.Header.Encode() + "\r\n" + rs.Body
}

func (rs *Response) SetStatus(status_code int) {
	rs.StatusCode = status_code
	rs.Description = http.StatusText(status_code)
}
