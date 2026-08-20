package wlgows

import (
	"bufio"
	"errors"
	"net"
	"net/http"

	"github.com/gin-gonic/gin"
)

/*
HijackFromHttp takes over an already-accepted HTTP connection — nothing more.
It does not run the handshake or build a *Conn, so you're free to do both
yourself, using the request net/http already parsed for you:

	netConn, r, err := wlgows.HijackFromHttp(w)
	res, err := wlgows.ServerHandShake(netConn, req)
	conn := wlgows.NewConn(netConn, r, false)

r is the hijacked connection's own *bufio.Reader — net/http may have buffered
bytes past the request headers (the start of the first frame) before handing
the connection over, and a fresh reader here would strand them. It must be
the same reader you pass to NewConn.
*/
func HijackFromHttp(w http.ResponseWriter) (net.Conn, *bufio.Reader, error) {
	hijacker, ok := w.(http.Hijacker)
	if !ok {
		return nil, nil, errors.New("responsewriter does not suport the hijack connection")
	}
	conn, bufRW, err := hijacker.Hijack()
	if err != nil {
		return nil, nil, err
	}
	return conn, bufRW.Reader, nil
}

/*
gin.ResponseWriter embeds http.ResponseWriter and http.Hijacker, so the gin
case is just HijackFromHttp with the writer pulled off the context.
*/
func HijackFromGin(c *gin.Context) (net.Conn, *bufio.Reader, error) {
	return HijackFromHttp(c.Writer)
}
