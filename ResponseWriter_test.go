package wlgows

import (
	"errors"
	"fmt"
	"hash"
	"io"
	"net/http"
	"strings"
	"testing"
)

func TestNewResponseWriter(t *testing.T) {
	writer := NewResponseWriter()
	if writer.header == nil {
		t.Error("header must be initialised")
	}
	if writer.body_buff == nil {
		t.Error("body_buff must be initialised")
	}
	if writer.di.generateSecWebsocketAccept == nil {
		t.Error("NewResponseWriter must populate di")
	}
	if writer.statusCode != 0 {
		t.Errorf("statusCode = %d, want 0 before WriteHeader", writer.statusCode)
	}
}

func TestResponseWriterHeaderAndWriteHeader(t *testing.T) {
	writer := NewResponseWriter()
	writer.Header().Set("X-Test", "value")
	if got := writer.Header().Get("X-Test"); got != "value" {
		t.Errorf("Header().Get = %q, want %q", got, "value")
	}
	// Header() returns the live map, not a copy.
	if writer.header.Get("X-Test") != "value" {
		t.Error("Header() should expose the internal header map")
	}

	writer.WriteHeader(http.StatusSwitchingProtocols)
	if writer.statusCode != 101 {
		t.Errorf("statusCode = %d, want 101", writer.statusCode)
	}
}

func TestResponseWriterGenerateResponse(t *testing.T) {
	writer := NewResponseWriter()
	writer.WriteHeader(http.StatusSwitchingProtocols)
	writer.Header().Set("Upgrade", "websocket")

	response := writer.GenerateResponse()

	if response.Proto != "HTTP/1.1" || response.ProtoMajor != 1 || response.ProtoMinor != 1 {
		t.Errorf("proto = %s %d.%d", response.Proto, response.ProtoMajor, response.ProtoMinor)
	}
	if response.StatusCode != 101 {
		t.Errorf("StatusCode = %d, want 101", response.StatusCode)
	}
	if response.Header.Get("Upgrade") != "websocket" {
		t.Errorf("Upgrade header = %q", response.Header.Get("Upgrade"))
	}
	if response.Header.Get("Content-Length") != "0" {
		t.Errorf("Content-Length = %q, want %q", response.Header.Get("Content-Length"), "0")
	}
	// No body was written, so Content-Type is left unset.
	if response.Header.Get("Content-Type") != "" {
		t.Errorf("Content-Type = %q, want empty", response.Header.Get("Content-Type"))
	}
}

func TestResponseWriterGenerateResponseKeepsExplicitContentLength(t *testing.T) {
	writer := NewResponseWriter()
	writer.Header().Set("Content-Length", "42")
	response := writer.GenerateResponse()
	if response.Header.Get("Content-Length") != "42" {
		t.Errorf("Content-Length = %q, want the caller's 42", response.Header.Get("Content-Length"))
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
	writer := NewResponseWriter()
	writer.WriteHeader(http.StatusBadRequest)
	n, err := writer.Write([]byte("some error detail"))
	if err != nil {
		t.Fatalf("Write: %v", err)
	}
	if n != len("some error detail") {
		t.Errorf("Write returned n = %d, want %d", n, len("some error detail"))
	}

	response := writer.GenerateResponse()
	body, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatalf("ReadAll: %v", err)
	}
	if len(body) != 0 {
		t.Errorf("body = %q; the unflushed-buffer bug appears to be fixed, update this test", body)
	}
	if response.Header.Get("Content-Length") != "0" {
		t.Errorf("Content-Length = %q, want 0 while the bug stands", response.Header.Get("Content-Length"))
	}
}

/*
The flush bug is size-dependent, which is the nastiest part of it. bufio.Writer
passes a write larger than its 4096 byte buffer straight through to the
underlying buffer, so a large body DOES survive while a small one is swallowed.
That asymmetry is why the Content-Type branch is reachable at all.
*/
func TestResponseWriterLargeBodyBypassesTheBufferAndSurvives(t *testing.T) {
	writer := NewResponseWriter()
	writer.WriteHeader(http.StatusOK)
	big := strings.Repeat("x", 5000)
	if _, err := writer.Write([]byte(big)); err != nil {
		t.Fatalf("Write: %v", err)
	}

	response := writer.GenerateResponse()
	body, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatalf("ReadAll: %v", err)
	}
	if len(body) != 5000 {
		t.Errorf("body is %d bytes, want 5000", len(body))
	}
	if response.Header.Get("Content-Length") != "5000" {
		t.Errorf("Content-Length = %q, want 5000", response.Header.Get("Content-Length"))
	}
	// Only reached when the body actually made it into body_buff.
	if response.Header.Get("Content-Type") != "text/plain" {
		t.Errorf("Content-Type = %q, want text/plain", response.Header.Get("Content-Type"))
	}
}

func TestResponseWriterDeclineByError(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want int
	}{
		{"msg formation invalid", ErrHttpMsgFormationInvalid, http.StatusBadRequest},
		{"method not allowed", ErrHttpMethodNotAllowed, http.StatusMethodNotAllowed},
		{"protocol not allowed", ErrHttpProtocolOrVersionNotAllowed, http.StatusHTTPVersionNotSupported},
		{"websocket key not set", ErrHttpSecWebSocketKeyHeaderNotSet, http.StatusBadRequest},
		{"connection not upgrade", ErrHttpConnectionHeaderNotUpgrade, http.StatusBadRequest},
		{"upgrade not websocket", ErrHttpUpgradeHeaderNotWebsocket, http.StatusBadRequest},
		{"websocket version not 13", ErrHttpSecWebSocketVersionNotSupported, http.StatusUpgradeRequired},
		{"an error from outside this package", errors.New("something else"), http.StatusInternalServerError},
		{"nil", nil, http.StatusInternalServerError},
	}
	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			writer := NewResponseWriter()
			writer.DeclineByError(testCase.err)
			if writer.statusCode != testCase.want {
				t.Errorf("statusCode = %d, want %d", writer.statusCode, testCase.want)
			}
		})
	}
}

/*
4.4 asks a 426 to name the versions the server does speak, so a client on an
older draft learns what to retry with instead of guessing. The status alone
would leave it no better off.
*/
func TestResponseWriterDeclineByErrorVersionNamesThirteen(t *testing.T) {
	writer := NewResponseWriter()

	writer.DeclineByError(ErrHttpSecWebSocketVersionNotSupported)

	if got := writer.Header().Get("Sec-WebSocket-Version"); got != "13" {
		t.Errorf("Sec-WebSocket-Version = %q, want 13", got)
	}
}

// Routing must survive wrapping, or DeclineByError would only work on a bare
// sentinel — which is never what the handshake path actually returns.
func TestResponseWriterDeclineByErrorUnwraps(t *testing.T) {
	wrapped := fmt.Errorf("handshake: %w",
		fmt.Errorf("validate: %w", ErrHttpMethodNotAllowed))

	writer := NewResponseWriter()
	writer.DeclineByError(wrapped)

	if writer.statusCode != http.StatusMethodNotAllowed {
		t.Errorf("statusCode = %d, want %d — errors.Is should have unwrapped two layers",
			writer.statusCode, http.StatusMethodNotAllowed)
	}
}

func TestResponseWriterUpgradeForWebsocket(t *testing.T) {
	writer := NewResponseWriter()
	writer.di.generateSecWebsocketAccept = func(key string) string { return "ACCEPT(" + key + ")" }

	writer.UpgradeForWebsocket("the-key")

	if writer.statusCode != http.StatusSwitchingProtocols {
		t.Errorf("statusCode = %d, want 101", writer.statusCode)
	}
	want := map[string]string{
		"Upgrade":               "websocket",
		"Connection":            "Upgrade",
		"Sec-Websocket-Version": "13",
		"Sec-Websocket-Accept":  "ACCEPT(the-key)",
	}
	for headerKey, wantValue := range want {
		if got := writer.Header().Get(headerKey); got != wantValue {
			t.Errorf("header %s = %q, want %q", headerKey, got, wantValue)
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
