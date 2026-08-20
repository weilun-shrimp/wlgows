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
	if writer.err != nil {
		return nil, nil, writer.err
	}
	return writer.conn, bufio.NewReadWriter(bufio.NewReader(writer.conn), bufio.NewWriter(writer.conn)), nil
}

// fakeGinWriter satisfies gin.ResponseWriter by embedding it; only Hijack is real.
type fakeGinWriter struct {
	gin.ResponseWriter
	conn net.Conn
	err  error
}

func (writer *fakeGinWriter) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	if writer.err != nil {
		return nil, nil, writer.err
	}
	return writer.conn, bufio.NewReadWriter(bufio.NewReader(writer.conn), bufio.NewWriter(writer.conn)), nil
}

func TestHijackFromHttp(t *testing.T) {
	t.Run("returns the hijacked conn and its reader", func(t *testing.T) {
		netConn := newFakeConn(nil)

		conn, r, err := HijackFromHttp(&fakeHijackWriter{conn: netConn})
		if err != nil {
			t.Fatalf("HijackFromHttp: %v", err)
		}
		if conn != net.Conn(netConn) {
			t.Error("the hijacked conn should be returned")
		}
		if r == nil {
			t.Error("the hijacked bufio.Reader should be returned")
		}
	})

	t.Run("rejects a ResponseWriter that cannot hijack", func(t *testing.T) {
		// httptest.ResponseRecorder deliberately does not implement http.Hijacker.
		_, _, err := HijackFromHttp(httptest.NewRecorder())
		if err == nil {
			t.Fatal("expected an error")
		}
		if !strings.Contains(err.Error(), "does not suport the hijack connection") {
			t.Errorf("err = %q", err.Error())
		}
	})

	t.Run("propagates a Hijack error", func(t *testing.T) {
		want := errors.New("cannot hijack")
		_, _, err := HijackFromHttp(&fakeHijackWriter{err: want})
		if !errors.Is(err, want) {
			t.Errorf("err = %v, want %v", err, want)
		}
	})
}

/*
HijackFromGin is a pure delegator to HijackFromHttp, so it has no seam of its
own. These tests go through the real exported function to prove the writer it
pulls off the gin.Context reaches the http path intact; the error branches are
already covered by TestHijackFromHttp.
*/
func TestHijackFromGin(t *testing.T) {
	t.Run("forwards gin's writer to the http path", func(t *testing.T) {
		netConn := newFakeConn(nil)
		ginCtx := &gin.Context{}
		ginCtx.Writer = &fakeGinWriter{conn: netConn}

		conn, r, err := HijackFromGin(ginCtx)
		if err != nil {
			t.Fatalf("HijackFromGin: %v", err)
		}
		if conn != net.Conn(netConn) {
			t.Error("the hijacked conn should be returned")
		}
		if r == nil {
			t.Error("the hijacked bufio.Reader should be returned")
		}
	})

	t.Run("propagates a Hijack error", func(t *testing.T) {
		want := errors.New("cannot hijack")
		ginCtx := &gin.Context{}
		ginCtx.Writer = &fakeGinWriter{err: want}
		if _, _, err := HijackFromGin(ginCtx); !errors.Is(err, want) {
			t.Errorf("err = %v, want %v", err, want)
		}
	})

	// Only reachable with a nil Writer: gin.ResponseWriter always embeds
	// http.Hijacker, so a real gin writer can never fail the assertion.
	t.Run("rejects a nil writer", func(t *testing.T) {
		ginCtx := &gin.Context{}
		_, _, err := HijackFromGin(ginCtx)
		if err == nil {
			t.Fatal("expected an error")
		}
		if !strings.Contains(err.Error(), "does not suport the hijack connection") {
			t.Errorf("err = %q", err.Error())
		}
	})
}
