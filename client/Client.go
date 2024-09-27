package client

import (
	"crypto/tls"
	"errors"
	"net"
	"net/http"
	"net/url"
	"slices"

	"github.com/weilun-shrimp/wlgows/connection"
)

func Dial(raw_url string, tls_config *tls.Config) (*connection.ClientConn, error) {
	req, err := http.NewRequest("GET", raw_url, nil)
	if err != nil {
		return nil, err
	}
	if err := ValidateWebsocketUrl(req.URL); err != nil {
		return nil, err
	}
	var conn net.Conn
	switch req.URL.Scheme {
	case "http", "ws":
		conn, err = net.Dial("tcp", req.URL.Host)
	case "https", "wss":
		conn, err = tls.Dial("tcp", req.URL.Host, tls_config)
	}
	if err != nil {
		return nil, err
	}
	clientConn := &connection.ClientConn{
		Conn: connection.Conn{
			Conn:          conn,
			ClientRequest: req,
		},
	}
	return clientConn, nil
}

func ValidateWebsocketUrl(parsedUrl *url.URL) error {
	if !slices.Contains([]string{"http", "https", "ws", "wss"}, parsedUrl.Scheme) {
		return errors.New(`The url scheme is not valid to websocket. 
			Only validate in ('http', 'https', 'ws', 'wss').
			Raw url => ` + parsedUrl.String())
	}
	if parsedUrl.Host == "" {
		return errors.New(`The url host is empty. Raw url => ` + parsedUrl.String())
	}
	return nil
}
