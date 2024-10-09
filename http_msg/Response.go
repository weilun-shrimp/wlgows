package http_msg

type Response struct {
	Protocal    string `json:"protocal"`
	Version     string `json:"version"`
	StatusCode  string `json:"status_code"`
	Description string `json:"description"`
	Header      Header `json:"header"`
	Body        string `json:"body"`
}

// return like "HTTP/1.1 101 Switching Protocols"
func (rs *Response) EncodeTop() string {
	return rs.Protocal + "/" + rs.Version + " " + rs.StatusCode + " " + rs.Description
}

/*
return like
HTTP/1.1 101 Switching Protocols\r\n
Content-Type: application/json\r\n
\r\n
Body......
*/
func (rs *Response) Encode() string {
	return rs.EncodeTop() + "\r\n" + rs.Header.Encode() + "\r\n" + rs.Body
}
