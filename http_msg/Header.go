package http_msg

import "net/http"

type Header struct {
	http.Header
}

func (h Header) Encode() string {
	result := ""
	for k, arr := range h.Header {
		for _, v := range arr {
			result += k + ": " + v + "\r\n"
		}
	}
	return result
}
