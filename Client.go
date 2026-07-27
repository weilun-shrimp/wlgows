package wlgows

import (
	"crypto/tls"
	"errors"
	"io"
	"net"
	"net/http"
	"net/url"
	"slices"
)

func Dial(raw_url string, tls_config *tls.Config) (*ClientConn, error) {
	return dial(raw_url, tls_config, dialDI{
		httpNewRequest:       http.NewRequest,
		validateWebsocketUrl: ValidateWebsocketUrl,
		netDial:              net.Dial,
		tlsDial:              tls.Dial,
		newClientConn:        NewClientConn,
	})
}

type dialDI struct {
	httpNewRequest       func(method string, url string, body io.Reader) (*http.Request, error)
	validateWebsocketUrl func(parsedUrl *url.URL) error
	netDial              func(network string, address string) (net.Conn, error)
	tlsDial              func(network string, addr string, config *tls.Config) (*tls.Conn, error)
	newClientConn        func(c net.Conn, req *http.Request) *ClientConn
}

func dial(raw_url string, tls_config *tls.Config, di dialDI) (*ClientConn, error) {
	req, err := di.httpNewRequest("GET", raw_url, nil)
	if err != nil {
		return nil, err
	}
	if err := di.validateWebsocketUrl(req.URL); err != nil {
		return nil, err
	}
	var conn net.Conn
	switch req.URL.Scheme {
	case "http", "ws":
		conn, err = di.netDial("tcp", req.URL.Host)
	case "https", "wss":
		conn, err = di.tlsDial("tcp", req.URL.Host, tls_config)
	}
	if err != nil {
		return nil, err
	}
	return di.newClientConn(conn, req), nil
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
