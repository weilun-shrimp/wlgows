package connection

import (
	"errors"
	"net/http"
)

func HijackFromHttp(w http.ResponseWriter, r *http.Request) (*ServerConn, error) {
	hijacker, ok := w.(http.Hijacker)
	if !ok {
		return nil, errors.New("responsewriter does not suport the hijack connection")
	}
	conn, _, err := hijacker.Hijack()
	if err != nil {
		// http.Error(w, "could not hijack connection", http.StatusInternalServerError)
		return nil, err
	}
	return &ServerConn{
		Conn: Conn{
			Conn:          conn,
			ClientRequest: r,
		},
	}, nil
}
