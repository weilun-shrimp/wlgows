package wlgows

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
)

/*
End to end tests over real sockets. Everything below runs the genuine
handshake — no di substitution — so it catches wiring mistakes the unit tests
cannot: a constructor that forgets a field, a di default pointing at the wrong
function, a frame that is masked in the wrong direction.
*/

// echoOnce handshakes, reads one message, echoes it back, and closes.
func echoOnce(t *testing.T, sc *ServerConn, done chan<- string, fail chan<- error) {
	t.Helper()
	defer sc.Close()
	if _, err := sc.HandShake(); err != nil {
		fail <- err
		return
	}
	m, err := sc.GetNextMsg()
	if err != nil {
		fail <- err
		return
	}
	done <- m.GetStr()
	if err := sc.SendText([]byte(m.GetStr())); err != nil {
		fail <- err
	}
}

func TestIntegrationRawTCPEchoRoundTrip(t *testing.T) {
	s, err := Run("127.0.0.1:0")
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	defer s.Close()

	done := make(chan string, 1)
	fail := make(chan error, 1)
	go func() {
		sc, err := s.Accept()
		if err != nil {
			fail <- err
			return
		}
		echoOnce(t, sc, done, fail)
	}()

	cc, err := Dial("ws://"+s.TCPListener.Addr().String(), nil)
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}
	defer cc.Close()

	if err := cc.HandShake(); err != nil {
		t.Fatalf("client HandShake: %v", err)
	}
	if cc.ServerResponse.StatusCode != 101 {
		t.Fatalf("StatusCode = %d, want 101", cc.ServerResponse.StatusCode)
	}
	if cc.ServerResponse.Header.Get("Sec-WebSocket-Accept") == "" {
		t.Error("server did not return Sec-WebSocket-Accept")
	}

	const payload = "hello wlgows 中文"
	if err := cc.SendText([]byte(payload)); err != nil {
		t.Fatalf("SendText: %v", err)
	}

	select {
	case got := <-done:
		if got != payload {
			t.Fatalf("server received %q, want %q", got, payload)
		}
	case err := <-fail:
		t.Fatalf("server side: %v", err)
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for the server")
	}

	echo, err := cc.GetNextMsg()
	if err != nil {
		t.Fatalf("client GetNextMsg: %v", err)
	}
	if echo.GetStr() != payload {
		t.Errorf("echo = %q, want %q", echo.GetStr(), payload)
	}
	// RFC 6455: the server must not mask.
	if echo.IsIncludedMaskedFrame() {
		t.Error("server to client frames must be unmasked")
	}
}

func TestIntegrationLargePayloadRoundTrip(t *testing.T) {
	s, err := Run("127.0.0.1:0")
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	defer s.Close()

	done := make(chan string, 1)
	fail := make(chan error, 1)
	go func() {
		sc, err := s.Accept()
		if err != nil {
			fail <- err
			return
		}
		echoOnce(t, sc, done, fail)
	}()

	cc, err := Dial("ws://"+s.TCPListener.Addr().String(), nil)
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}
	defer cc.Close()
	if err := cc.HandShake(); err != nil {
		t.Fatalf("HandShake: %v", err)
	}

	// 70000 bytes forces the 64 bit extended length path.
	payload := strings.Repeat("abcdefghij", 7000)
	if err := cc.SendText([]byte(payload)); err != nil {
		t.Fatalf("SendText: %v", err)
	}

	select {
	case got := <-done:
		if got != payload {
			t.Fatalf("server received %d bytes, want %d", len(got), len(payload))
		}
	case err := <-fail:
		t.Fatalf("server side: %v", err)
	case <-time.After(10 * time.Second):
		t.Fatal("timed out waiting for the server")
	}
}

func TestIntegrationServerRejectsANonWebsocketRequest(t *testing.T) {
	s, err := Run("127.0.0.1:0")
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	defer s.Close()

	result := make(chan *Error, 1)
	go func() {
		sc, err := s.Accept()
		if err != nil {
			result <- &Error{Msg: err.Error()}
			return
		}
		defer sc.Close()
		_, handshakeErr := sc.HandShake()
		if e, ok := handshakeErr.(*Error); ok {
			result <- e
			return
		}
		result <- nil
	}()

	// A plain HTTP GET with none of the websocket headers.
	res, err := http.Get("http://" + s.TCPListener.Addr().String() + "/")
	if err != nil {
		t.Fatalf("http.Get: %v", err)
	}
	defer res.Body.Close()

	if res.StatusCode != http.StatusBadRequest {
		t.Errorf("StatusCode = %d, want 400", res.StatusCode)
	}
	select {
	case got := <-result:
		if got == nil {
			t.Fatal("the server should have reported a handshake error")
		}
		if got.Type != HttpSecWebSocketKeyHeaderNotSet {
			t.Errorf("Type = %q, want %q", got.Type, HttpSecWebSocketKeyHeaderNotSet)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for the server")
	}
}

func TestIntegrationHijackFromHttp(t *testing.T) {
	done := make(chan string, 1)
	fail := make(chan error, 1)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, ginEngine *http.Request) {
		sc, err := HijackFromHttp(w, ginEngine)
		if err != nil {
			fail <- err
			return
		}
		echoOnce(t, sc, done, fail)
	}))
	defer server.Close()

	cc, err := Dial(server.URL, nil) // httptest serves http://, which Dial accepts
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}
	defer cc.Close()
	if err := cc.HandShake(); err != nil {
		t.Fatalf("HandShake: %v", err)
	}
	if err := cc.SendText([]byte("via http hijack")); err != nil {
		t.Fatalf("SendText: %v", err)
	}

	select {
	case got := <-done:
		if got != "via http hijack" {
			t.Errorf("server received %q", got)
		}
	case err := <-fail:
		t.Fatalf("server side: %v", err)
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for the server")
	}

	echo, err := cc.GetNextMsg()
	if err != nil {
		t.Fatalf("GetNextMsg: %v", err)
	}
	if echo.GetStr() != "via http hijack" {
		t.Errorf("echo = %q", echo.GetStr())
	}
}

func TestIntegrationHijackFromGin(t *testing.T) {
	gin.SetMode(gin.TestMode)
	done := make(chan string, 1)
	fail := make(chan error, 1)

	ginEngine := gin.New()
	ginEngine.GET("/", func(c *gin.Context) {
		sc, err := HijackFromGin(c)
		if err != nil {
			fail <- err
			return
		}
		echoOnce(t, sc, done, fail)
	})

	server := httptest.NewServer(ginEngine)
	defer server.Close()

	// No trailing slash: the request line must still go out as "GET / ..."
	// or gin answers with a 301 instead of upgrading.
	cc, err := Dial(server.URL, nil)
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}
	defer cc.Close()
	if err := cc.HandShake(); err != nil {
		t.Fatalf("HandShake: %v", err)
	}
	if err := cc.SendText([]byte("via gin hijack")); err != nil {
		t.Fatalf("SendText: %v", err)
	}

	select {
	case got := <-done:
		if got != "via gin hijack" {
			t.Errorf("server received %q", got)
		}
	case err := <-fail:
		t.Fatalf("server side: %v", err)
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for the server")
	}

	echo, err := cc.GetNextMsg()
	if err != nil {
		t.Fatalf("GetNextMsg: %v", err)
	}
	if echo.GetStr() != "via gin hijack" {
		t.Errorf("echo = %q", echo.GetStr())
	}
}
