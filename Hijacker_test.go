package wlgows

import (
	"bufio"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
)

// fakeHijackWriter is an http.ResponseWriter that also implements http.Hijacker.
type fakeHijackWriter struct {
	http.ResponseWriter
	conn net.Conn
	err  error
}

func (writer *fakeHijackWriter) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	return writer.conn, nil, writer.err
}

// fakeGinWriter satisfies gin.ResponseWriter by embedding it; only Hijack is real.
type fakeGinWriter struct {
	gin.ResponseWriter
	conn net.Conn
	err  error
}

func (writer *fakeGinWriter) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	return writer.conn, nil, writer.err
}

func TestHijackFromHttp(t *testing.T) {
	t.Run("wraps the hijacked conn and the request", func(t *testing.T) {
		netConn := newFakeConn(nil)
		request := validClientHandShakeRequest(t)
		var gotConn net.Conn
		var gotReq *http.Request

		sc, err := hijackFromHttp(&fakeHijackWriter{conn: netConn}, request, hijackFromHttpDI{
			newServerConn: func(ginCtx net.Conn, r *http.Request) *ServerConn {
				gotConn, gotReq = ginCtx, r
				return NewServerConn(ginCtx, r)
			},
		})
		if err != nil {
			t.Fatalf("hijackFromHttp: %v", err)
		}
		if gotConn != net.Conn(netConn) || gotReq != request {
			t.Error("the hijacked conn and the request should reach the constructor")
		}
		if sc.ClientRequest != request {
			t.Error("ClientRequest was not set")
		}
		if sc.di.readRequest == nil {
			t.Error("the ServerConn must have its di populated")
		}
	})

	t.Run("rejects a ResponseWriter that cannot hijack", func(t *testing.T) {
		// httptest.ResponseRecorder deliberately does not implement http.Hijacker.
		_, err := hijackFromHttp(httptest.NewRecorder(), validClientHandShakeRequest(t), hijackFromHttpDI{
			newServerConn: func(net.Conn, *http.Request) *ServerConn {
				t.Fatal("no connection should be built")
				return nil
			},
		})
		if err == nil {
			t.Fatal("expected an error")
		}
		if !strings.Contains(err.Error(), "does not suport the hijack connection") {
			t.Errorf("err = %q", err.Error())
		}
	})

	t.Run("propagates a Hijack error", func(t *testing.T) {
		want := errors.New("cannot hijack")
		_, err := hijackFromHttp(&fakeHijackWriter{err: want}, validClientHandShakeRequest(t), hijackFromHttpDI{
			newServerConn: func(net.Conn, *http.Request) *ServerConn {
				t.Fatal("no connection should be built")
				return nil
			},
		})
		if !errors.Is(err, want) {
			t.Errorf("err = %v, want %v", err, want)
		}
	})
}

/*
HijackFromGin is a pure delegator to HijackFromHttp, so it has no seam of its
own. These tests go through the real exported function to prove the two values
it pulls off the gin.Context reach the http path intact; the error branches are
already covered by TestHijackFromHttp.
*/
func TestHijackFromGin(t *testing.T) {
	t.Run("forwards gin's writer and request to the http path", func(t *testing.T) {
		netConn := newFakeConn(nil)
		request := validClientHandShakeRequest(t)
		ginCtx := &gin.Context{Request: request}
		ginCtx.Writer = &fakeGinWriter{conn: netConn}

		sc, err := HijackFromGin(ginCtx)
		if err != nil {
			t.Fatalf("HijackFromGin: %v", err)
		}
		if sc.Conn.Conn != net.Conn(netConn) {
			t.Error("the hijacked conn should be embedded in the ServerConn")
		}
		if sc.ClientRequest != request {
			t.Error("gin's request should become ClientRequest")
		}
		if sc.di.readRequest == nil {
			t.Error("the ServerConn must have its di populated")
		}
	})

	t.Run("propagates a Hijack error", func(t *testing.T) {
		want := errors.New("cannot hijack")
		ginCtx := &gin.Context{Request: validClientHandShakeRequest(t)}
		ginCtx.Writer = &fakeGinWriter{err: want}
		if _, err := HijackFromGin(ginCtx); !errors.Is(err, want) {
			t.Errorf("err = %v, want %v", err, want)
		}
	})

	// Only reachable with a nil Writer: gin.ResponseWriter always embeds
	// http.Hijacker, so a real gin writer can never fail the assertion.
	t.Run("rejects a nil writer", func(t *testing.T) {
		ginCtx := &gin.Context{Request: validClientHandShakeRequest(t)}
		_, err := HijackFromGin(ginCtx)
		if err == nil {
			t.Fatal("expected an error")
		}
		if !strings.Contains(err.Error(), "does not suport the hijack connection") {
			t.Errorf("err = %q", err.Error())
		}
	})
}
