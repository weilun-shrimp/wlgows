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

/*
Dial connects to raw_url and builds the opening request — nothing more. It
does not run the handshake or build a *Conn, so you're free to add headers to
req before sending it:

	netConn, req, err := wlgows.Dial(url, tlsConfig)
	r := bufio.NewReader(netConn)
	res, err := wlgows.ClientHandShake(netConn, r, req)
	conn := wlgows.NewConn(netConn, r, true)

r must be the same *bufio.Reader you pass to both ClientHandShake and
NewConn — see ClientHandShake's doc comment for why.
*/
func Dial(raw_url string, tls_config *tls.Config) (net.Conn, *http.Request, error) {
	return dial(raw_url, tls_config, dialDI{
		httpNewRequest:       http.NewRequest,
		validateWebsocketUrl: ValidateWebsocketUrl,
		netDial:              net.Dial,
		tlsDial:              tls.Dial,
	})
}

type dialDI struct {
	httpNewRequest       func(method string, url string, body io.Reader) (*http.Request, error)
	validateWebsocketUrl func(parsedUrl *url.URL) error
	netDial              func(network string, address string) (net.Conn, error)
	tlsDial              func(network string, addr string, config *tls.Config) (*tls.Conn, error)
}

func dial(raw_url string, tls_config *tls.Config, di dialDI) (net.Conn, *http.Request, error) {
	req, err := di.httpNewRequest("GET", raw_url, nil)
	if err != nil {
		return nil, nil, err
	}
	if err := di.validateWebsocketUrl(req.URL); err != nil {
		return nil, nil, err
	}
	var conn net.Conn
	switch req.URL.Scheme {
	case "http", "ws":
		conn, err = di.netDial("tcp", req.URL.Host)
	case "https", "wss":
		conn, err = di.tlsDial("tcp", req.URL.Host, tls_config)
	}
	if err != nil {
		return nil, nil, err
	}
	return conn, req, nil
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
