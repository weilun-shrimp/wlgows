package wlgows

import (
	"context"
	"crypto/tls"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"slices"
)

/*
Dial is DialContext with context.Background() and a default dialer. It has no
timeout, so DialContext is the recommended way to dial.
*/
func Dial(raw_url string, tls_config *tls.Config) (net.Conn, *http.Request, error) {
	return DialContext(context.Background(), raw_url, nil, tls_config)
}

/*
DialContext connects to raw_url and builds the opening request — nothing more.
It does not run the handshake or build a *Conn, so you're free to add headers
to req before sending it:

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	dialer := &net.Dialer{Timeout: 15 * time.Second}
	netConn, req, err := wlgows.DialContext(ctx, url, dialer, tlsConfig)
	r := bufio.NewReader(netConn)
	w := bufio.NewWriterSize(netConn, 4096)
	conn, res, err := wlgows.ClientHandShake(netConn, r, w, req)

r must be the same *bufio.Reader you pass to both ClientHandShake and
NewConn — see ClientHandShake's doc comment for why.

ctx bounds the TCP connect and, for wss://, the TLS handshake. Once it
returns, ctx no longer affects the conn; use SetDeadline for the WebSocket
handshake and the frames.

dialer sets options such as Timeout and KeepAlive. nil means a zero
net.Dialer. ctx must not be nil.
*/
func DialContext(ctx context.Context, raw_url string, dialer *net.Dialer, tls_config *tls.Config) (net.Conn, *http.Request, error) {
	return dialContext(ctx, raw_url, dialer, tls_config, dialContextDI{
		httpNewRequest:       http.NewRequest,
		validateWebsocketUrl: ValidateWebsocketUrl,
		defaultDialer:        &net.Dialer{},
		dialContext:          (*net.Dialer).DialContext,
		tlsDialContext:       (*tls.Dialer).DialContext,
	})
}

type dialContextDI struct {
	httpNewRequest       func(method string, url string, body io.Reader) (*http.Request, error)
	validateWebsocketUrl func(parsedUrl *url.URL) error
	defaultDialer        *net.Dialer
	dialContext          func(dialer *net.Dialer, ctx context.Context, network string, address string) (net.Conn, error)
	tlsDialContext       func(dialer *tls.Dialer, ctx context.Context, network string, address string) (net.Conn, error)
}

func dialContext(ctx context.Context, raw_url string, dialer *net.Dialer, tls_config *tls.Config, di dialContextDI) (net.Conn, *http.Request, error) {
	req, err := di.httpNewRequest("GET", raw_url, nil)
	if err != nil {
		return nil, nil, err
	}
	if err := di.validateWebsocketUrl(req.URL); err != nil {
		return nil, nil, err
	}
	if dialer == nil {
		dialer = di.defaultDialer
	}
	var conn net.Conn
	switch req.URL.Scheme {
	case "http", "ws":
		conn, err = di.dialContext(dialer, ctx, "tcp", req.URL.Host)
	case "https", "wss":
		tlsDialer := &tls.Dialer{NetDialer: dialer, Config: tls_config}
		conn, err = di.tlsDialContext(tlsDialer, ctx, "tcp", req.URL.Host)
	default:
		return nil, nil, fmt.Errorf("scheme %q: %w", req.URL.Scheme, ErrUrlSchemeInvalid)
	}
	if err != nil {
		return nil, nil, err
	}
	return conn, req, nil
}

func ValidateWebsocketUrl(parsedUrl *url.URL) error {
	if !slices.Contains([]string{"http", "https", "ws", "wss"}, parsedUrl.Scheme) {
		return fmt.Errorf("%q: %w", parsedUrl.String(), ErrUrlSchemeInvalid)
	}
	if parsedUrl.Host == "" {
		return fmt.Errorf("%q: %w", parsedUrl.String(), ErrUrlHostEmpty)
	}
	return nil
}
