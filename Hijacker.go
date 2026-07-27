package wlgows

import (
	"errors"
	"net"
	"net/http"

	"github.com/gin-gonic/gin"
)

func HijackFromHttp(w http.ResponseWriter, r *http.Request) (*ServerConn, error) {
	return hijackFromHttp(w, r, hijackFromHttpDI{
		newServerConn: NewServerConn,
	})
}

type hijackFromHttpDI struct {
	newServerConn func(c net.Conn, req *http.Request) *ServerConn
}

func hijackFromHttp(w http.ResponseWriter, r *http.Request, di hijackFromHttpDI) (*ServerConn, error) {
	hijacker, ok := w.(http.Hijacker)
	if !ok {
		return nil, errors.New("responsewriter does not suport the hijack connection")
	}
	conn, _, err := hijacker.Hijack()
	if err != nil {
		// http.Error(w, "could not hijack connection", http.StatusInternalServerError)
		return nil, err
	}
	return di.newServerConn(conn, r), nil
}

/*
gin.ResponseWriter embeds http.ResponseWriter and http.Hijacker, and
gin.Context.Request is already an *http.Request, so the gin case is just
HijackFromHttp with the two values pulled off the context.
*/
func HijackFromGin(c *gin.Context) (*ServerConn, error) {
	return HijackFromHttp(c.Writer, c.Request)
}
