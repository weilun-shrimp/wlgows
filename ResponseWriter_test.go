package wlgows

import (
	"hash"
	"io"
	"net/http"
	"strings"
	"testing"
)

func TestNewResponseWriter(t *testing.T) {
	w := NewResponseWriter()
	if w.header == nil {
		t.Error("header must be initialised")
	}
	if w.body_buff == nil {
		t.Error("body_buff must be initialised")
	}
	if w.di.generateSecWebsocketAccept == nil {
		t.Error("NewResponseWriter must populate di")
	}
	if w.statusCode != 0 {
		t.Errorf("statusCode = %d, want 0 before WriteHeader", w.statusCode)
	}
}

func TestResponseWriterHeaderAndWriteHeader(t *testing.T) {
	w := NewResponseWriter()
	w.Header().Set("X-Test", "value")
	if got := w.Header().Get("X-Test"); got != "value" {
		t.Errorf("Header().Get = %q, want %q", got, "value")
	}
	// Header() returns the live map, not a copy.
	if w.header.Get("X-Test") != "value" {
		t.Error("Header() should expose the internal header map")
	}

	w.WriteHeader(http.StatusSwitchingProtocols)
	if w.statusCode != 101 {
		t.Errorf("statusCode = %d, want 101", w.statusCode)
	}
}

func TestResponseWriterGenerateResponse(t *testing.T) {
	w := NewResponseWriter()
	w.WriteHeader(http.StatusSwitchingProtocols)
	w.Header().Set("Upgrade", "websocket")

	res := w.GenerateResponse()

	if res.Proto != "HTTP/1.1" || res.ProtoMajor != 1 || res.ProtoMinor != 1 {
		t.Errorf("proto = %s %d.%d", res.Proto, res.ProtoMajor, res.ProtoMinor)
	}
	if res.StatusCode != 101 {
		t.Errorf("StatusCode = %d, want 101", res.StatusCode)
	}
	if res.Header.Get("Upgrade") != "websocket" {
		t.Errorf("Upgrade header = %q", res.Header.Get("Upgrade"))
	}
	if res.Header.Get("Content-Length") != "0" {
		t.Errorf("Content-Length = %q, want %q", res.Header.Get("Content-Length"), "0")
	}
	// No body was written, so Content-Type is left unset.
	if res.Header.Get("Content-Type") != "" {
		t.Errorf("Content-Type = %q, want empty", res.Header.Get("Content-Type"))
	}
}

func TestResponseWriterGenerateResponseKeepsExplicitContentLength(t *testing.T) {
	w := NewResponseWriter()
	w.Header().Set("Content-Length", "42")
	res := w.GenerateResponse()
	if res.Header.Get("Content-Length") != "42" {
		t.Errorf("Content-Length = %q, want the caller's 42", res.Header.Get("Content-Length"))
	}
}

/*
Documents a known defect carried over from v1, listed as out of scope for the
v2 restructure: Write goes into body_writer (a bufio.Writer) but
GenerateResponse reads body_buff, and nothing ever calls Flush. So a written
body never reaches the response, Content-Length stays 0, and Content-Type is
never auto-set.

When the flush bug is fixed, this test should start failing — that is the
signal to update it to assert the corrected behaviour.
*/
func TestResponseWriterWrittenBodyIsLostWithoutFlush(t *testing.T) {
	w := NewResponseWriter()
	w.WriteHeader(http.StatusBadRequest)
	n, err := w.Write([]byte("some error detail"))
	if err != nil {
		t.Fatalf("Write: %v", err)
	}
	if n != len("some error detail") {
		t.Errorf("Write returned n = %d, want %d", n, len("some error detail"))
	}

	res := w.GenerateResponse()
	body, err := io.ReadAll(res.Body)
	if err != nil {
		t.Fatalf("ReadAll: %v", err)
	}
	if len(body) != 0 {
		t.Errorf("body = %q; the unflushed-buffer bug appears to be fixed, update this test", body)
	}
	if res.Header.Get("Content-Length") != "0" {
		t.Errorf("Content-Length = %q, want 0 while the bug stands", res.Header.Get("Content-Length"))
	}
}

/*
The flush bug is size-dependent, which is the nastiest part of it. bufio.Writer
passes a write larger than its 4096 byte buffer straight through to the
underlying buffer, so a large body DOES survive while a small one is swallowed.
That asymmetry is why the Content-Type branch is reachable at all.
*/
func TestResponseWriterLargeBodyBypassesTheBufferAndSurvives(t *testing.T) {
	w := NewResponseWriter()
	w.WriteHeader(http.StatusOK)
	big := strings.Repeat("x", 5000)
	if _, err := w.Write([]byte(big)); err != nil {
		t.Fatalf("Write: %v", err)
	}

	res := w.GenerateResponse()
	body, err := io.ReadAll(res.Body)
	if err != nil {
		t.Fatalf("ReadAll: %v", err)
	}
	if len(body) != 5000 {
		t.Errorf("body is %d bytes, want 5000", len(body))
	}
	if res.Header.Get("Content-Length") != "5000" {
		t.Errorf("Content-Length = %q, want 5000", res.Header.Get("Content-Length"))
	}
	// Only reached when the body actually made it into body_buff.
	if res.Header.Get("Content-Type") != "text/plain" {
		t.Errorf("Content-Type = %q, want text/plain", res.Header.Get("Content-Type"))
	}
}

func TestResponseWriterDeclineByErrorType(t *testing.T) {
	tests := []struct {
		errorType string
		want      int
	}{
		{HttpMsgFormationInvalid, http.StatusBadRequest},
		{HttpMethodNotAllowed, http.StatusMethodNotAllowed},
		{HttpProtocolOrVersionNotAllowed, http.StatusHTTPVersionNotSupported},
		{HttpSecWebSocketKeyHeaderNotSet, http.StatusBadRequest},
		{HttpConnectionHeaderNotUpgrade, http.StatusBadRequest},
		{HttpUpgradeHeaderNotWebsocket, http.StatusBadRequest},
		{"something we never defined", http.StatusInternalServerError},
		{"", http.StatusInternalServerError},
	}
	for _, tt := range tests {
		t.Run(tt.errorType, func(t *testing.T) {
			w := NewResponseWriter()
			w.DeclineByErrorType(tt.errorType)
			if w.statusCode != tt.want {
				t.Errorf("statusCode = %d, want %d", w.statusCode, tt.want)
			}
		})
	}
}

func TestResponseWriterUpgradeForWebsocket(t *testing.T) {
	w := NewResponseWriter()
	w.di.generateSecWebsocketAccept = func(key string) string { return "ACCEPT(" + key + ")" }

	w.UpgradeForWebsocket("the-key")

	if w.statusCode != http.StatusSwitchingProtocols {
		t.Errorf("statusCode = %d, want 101", w.statusCode)
	}
	want := map[string]string{
		"Upgrade":               "websocket",
		"Connection":            "Upgrade",
		"Sec-Websocket-Version": "13",
		"Sec-Websocket-Accept":  "ACCEPT(the-key)",
	}
	for k, v := range want {
		if got := w.Header().Get(k); got != v {
			t.Errorf("header %s = %q, want %q", k, got, v)
		}
	}
}

func TestGenerateSecWebsocketAccept(t *testing.T) {
	// The worked example from RFC 6455 section 1.3.
	t.Run("rfc 6455 test vector", func(t *testing.T) {
		got := GenerateSecWebsocketAccept("dGhlIHNhbXBsZSBub25jZQ==")
		want := "s3pPLMBiTxaQ9kYGzzhZRbK+xOo="
		if got != want {
			t.Errorf("GenerateSecWebsocketAccept() = %q, want %q", got, want)
		}
	})

	t.Run("is deterministic", func(t *testing.T) {
		first := GenerateSecWebsocketAccept("abc")
		second := GenerateSecWebsocketAccept("abc")
		if first != second {
			t.Errorf("same key produced %q then %q", first, second)
		}
		if other := GenerateSecWebsocketAccept("abd"); first == other {
			t.Errorf("keys %q and %q both produced %q", "abc", "abd", other)
		}
	})

	t.Run("appends the RFC magic GUID before hashing", func(t *testing.T) {
		var hashed string
		got := generateSecWebsocketAccept("KEY", generateSecWebsocketAcceptDI{
			sha1New: func() hash.Hash { return stubHash{} },
			ioWriteString: func(_ io.Writer, s string) (int, error) {
				hashed = s
				return len(s), nil
			},
		})
		if !strings.HasPrefix(hashed, "KEY") {
			t.Errorf("hashed input = %q, should start with the key", hashed)
		}
		if !strings.HasSuffix(hashed, "258EAFA5-E914-47DA-95CA-C5AB0DC85B11") {
			t.Errorf("hashed input = %q, should end with the RFC 6455 GUID", hashed)
		}
		// stubHash sums to a fixed value, so the output is fully determined.
		if got != "AAECAwQ=" {
			t.Errorf("result = %q, want the base64 of the stub sum", got)
		}
	})
}

// stubHash lets the accept test assert on the base64 step alone.
type stubHash struct{ hash.Hash }

func (stubHash) Write(p []byte) (int, error) { return len(p), nil }
func (stubHash) Sum(b []byte) []byte         { return append(b, 0, 1, 2, 3, 4) }
