package wlgows

import (
	"bufio"
	"bytes"
	"encoding/base64"
	"fmt"
	"io"
	"net"
	"net/http"
	"slices"
	"strings"
)

type ServerHandShakeRespWriter interface {
	DeclineByError(error)
	UpgradeForWebsocket(string)
	GenerateResponse() *http.Response
}

// ServerHandShakeConfig is what ServerHandShake reads besides the request. The
// zero value runs a plain handshake.
type ServerHandShakeConfig struct {
	// Sent back in Sec-WebSocket-Protocol on an upgrade, if the client offered
	// it (see RequestSubprotocols). Otherwise, or when "", no header is sent and
	// the connection opens without a subprotocol. One value, not a list,
	// because RFC 6455 4.2.2 lets the server answer with a single subprotocol.
	Subprotocol string

	// PermessageDeflates are the permessage-deflate answers the server wants,
	// in its order of preference. On an upgrade, the first one an offer allows
	// is answered, see AcceptDeflateOffer. The server's order first is a
	// library choice: RFC 7692 5 lets the server accept any one offer. When none fits, or the
	// list is empty, the connection opens without compression. The server reads
	// each field this way:
	//
	//   - ServerNoContextTakeover true: the server resets its window after each
	//     message. It is also set when the offer asks for it (RFC 7692 7.1.1.1).
	//   - ClientNoContextTakeover true: the server asks the client to reset its
	//     window after each message. It is also set when the offer has it
	//     (RFC 7692 7.1.1.2).
	//   - ServerMaxWindowBits: 0 or 15. 8 to 14 returns
	//     ErrHandshakeServerDeflateConfigUnsupported (library choice), because
	//     Go's compress/flate cannot send with a window under 15 bits.
	//   - ClientMaxWindowBitsEnabled false: the server accepts any client
	//     window, up to 15 bits. ClientMaxWindowBits is ignored.
	//   - ClientMaxWindowBitsEnabled true: the client's window is at most
	//     ClientMaxWindowBits, which must be 8 to 15. An offer without
	//     client_max_window_bits is accepted only when it is 15
	//     (RFC 7692 7.1.2.2).
	//
	// Other values return ErrHandshakeServerDeflateConfigInvalid.
	PermessageDeflates []PermessageDeflateConfig
}

/*
ServerHandShake runs the RFC 6455 4.2 server opening handshake over c: it
validates req and writes back either a declining response or an upgrading
one. req must already be parsed — read it yourself first (e.g.
http.ReadRequest for a raw accept, or from whatever already parsed it for a
hijacked connection). On success it wraps c in a ready-to-use *Conn —
unmasked, since 5.1 forbids a server masking what it sends. conn is nil
whenever err is non-nil; the response is still returned so a caller can
inspect what actually went out.

r must be the same *bufio.Reader you read req from (or, for an
already-parsed req such as a hijacked connection's, the same reader you go on
to read frames from) — the returned *Conn reuses it rather than wrapping c
again. From net/http, that is the hijacked connection's own reader:

	netConn, bufRW, err := responseWriter.(http.Hijacker).Hijack()
	conn, res, err := wlgows.ServerHandShake(netConn, bufRW.Reader, bufRW.Writer, req, wlgows.ServerHandShakeConfig{})

w is the returned *Conn's writer — see NewConn. Pass net/http's, as above, or
your own, such as bufio.NewWriterSize(c, 16*1024).
*/
func ServerHandShake(c net.Conn, r *bufio.Reader, w *bufio.Writer, req *http.Request, config ServerHandShakeConfig) (*Conn, *http.Response, error) {
	return serverHandShake(c, r, w, req, config, serverHandShakeDI{
		validateServerDeflateConfig: ValidateServerDeflateConfig,
		validateHandShakeRequest:    ValidateHandShakeRequest,
		newResponseWriter:           func() ServerHandShakeRespWriter { return NewResponseWriter() },
		acceptDeflateOffer:          AcceptDeflateOffer,
		sendHandShakeResponse:       SendHandShakeResponse,
		newConn:                     NewConn,
	})
}

type serverHandShakeDI struct {
	validateServerDeflateConfig func(server_config PermessageDeflateConfig) error
	validateHandShakeRequest    func(req *http.Request) error
	newResponseWriter           func() ServerHandShakeRespWriter
	acceptDeflateOffer          func(server_config PermessageDeflateConfig, req *http.Request) *PermessageDeflateConfig
	sendHandShakeResponse       func(w io.Writer, res *http.Response) error
	newConn                     func(c net.Conn, r *bufio.Reader, w *bufio.Writer, maskSendFrame bool) (*Conn, error)
}

func serverHandShake(c net.Conn, r *bufio.Reader, w *bufio.Writer, req *http.Request, config ServerHandShakeConfig, di serverHandShakeDI) (*Conn, *http.Response, error) {
	if req == nil {
		return nil, nil, fmt.Errorf("ServerHandShake: %w", ErrHandshakeRequestNil)
	}
	for index, server_config := range config.PermessageDeflates {
		if err := di.validateServerDeflateConfig(server_config); err != nil {
			return nil, nil, fmt.Errorf("ServerHandShake: PermessageDeflates[%d]: %w", index, err)
		}
	}

	invalid := di.validateHandShakeRequest(req)

	writer := di.newResponseWriter()
	if invalid != nil {
		writer.DeclineByError(invalid)
	} else {
		writer.UpgradeForWebsocket(req.Header.Get("Sec-Websocket-Key"))
	}

	res := writer.GenerateResponse()
	if invalid == nil && config.Subprotocol != "" && slices.Contains(RequestSubprotocols(req), config.Subprotocol) {
		res.Header.Set("Sec-WebSocket-Protocol", config.Subprotocol)
	}
	if invalid == nil {
		// Library choice: the server's order first. Within one config, the
		// client's order picks the offer (RFC 7692 5).
		for _, server_config := range config.PermessageDeflates {
			if answer_deflate_config := di.acceptDeflateOffer(server_config, req); answer_deflate_config != nil {
				res.Header.Set("Sec-WebSocket-Extensions", answer_deflate_config.Format())
				break
			}
		}
	}
	err := di.sendHandShakeResponse(c, res)
	req.Response = res
	res.Request = req
	if err != nil {
		return nil, res, err
	}
	if invalid != nil {
		return nil, res, invalid
	}
	conn, err := di.newConn(c, r, w, false)
	if err != nil {
		return nil, res, err
	}
	return conn, res, nil
}

/*
RequestSubprotocols returns the subprotocols the client offered, in its order.
Pick one for ServerHandShakeConfig.Subprotocol.

The offer is read with headerValues, so "a, b" on one line and "a" and "b" on
two give the same result.
*/
func RequestSubprotocols(req *http.Request) []string {
	return headerValues(req.Header, "Sec-WebSocket-Protocol")
}

/*
ValidateServerDeflateConfig checks the server's permessage-deflate config
before it is used to answer an offer. ServerHandShake already runs it on each
of ServerHandShakeConfig.PermessageDeflates. Call it yourself to know a config
is right without a handshake, for example in a test or before a handshake. It
returns ErrHandshakeServerDeflateConfigInvalid or
ErrHandshakeServerDeflateConfigUnsupported when one of these fails:

  - ServerMaxWindowBits is 0 or 8 to 15 (RFC 7692 7.1.2.1).
  - ClientMaxWindowBits is 8 to 15 when ClientMaxWindowBitsEnabled is true,
    since a response must give it a value (RFC 7692 7.1.2.2).
  - ServerMaxWindowBits is not 8 to 14, since Go's compress/flate cannot send
    with a window under 15 bits (library choice).
*/
func ValidateServerDeflateConfig(server_config PermessageDeflateConfig) error {
	if server_config.ServerMaxWindowBits != 0 && (server_config.ServerMaxWindowBits < 8 || server_config.ServerMaxWindowBits > 15) {
		return fmt.Errorf("ServerMaxWindowBits %d: %w", server_config.ServerMaxWindowBits, ErrHandshakeServerDeflateConfigInvalid)
	}
	if server_config.ClientMaxWindowBitsEnabled && (server_config.ClientMaxWindowBits < 8 || server_config.ClientMaxWindowBits > 15) {
		return fmt.Errorf("ClientMaxWindowBits %d: %w", server_config.ClientMaxWindowBits, ErrHandshakeServerDeflateConfigInvalid)
	}
	// Library choice: 8 to 14 is valid (RFC 7692 7.1.2.1), but the server would
	// promise to send within that window, and Go's compress/flate cannot.
	if server_config.ServerMaxWindowBits != 0 && server_config.ServerMaxWindowBits < 15 {
		return fmt.Errorf("ServerMaxWindowBits %d: %w", server_config.ServerMaxWindowBits, ErrHandshakeServerDeflateConfigUnsupported)
	}
	return nil
}

/*
AcceptDeflateOffer returns the answer to the first offer in req that allows
server_config, or nil when none does. Offers are tried in the client's order
(RFC 7692 5). ServerHandShake calls it for each of
ServerHandShakeConfig.PermessageDeflates. Call it yourself to pick a config by
your own rule. server_config must pass ValidateServerDeflateConfig.

The answer is server_config plus what the offer requires:

  - server_no_context_takeover is added if offered (RFC 7692 7.1.1.1).
  - client_no_context_takeover is sent back if offered (RFC 7692 7.1.1.2).
  - server_max_window_bits is lowered to the offer's value (RFC 7692 7.1.2.1).
  - client_max_window_bits is lowered to the offer's value (RFC 7692 7.1.2.2).

An offer without client_max_window_bits is skipped when server_config limits
the client under 15 bits (RFC 7692 7.1.2.2). An offer is also skipped when the
server's window would be under 15 bits, since Go's compress/flate cannot send
with a window that small (library choice).

There is no validator for the request's offers. An invalid offer is declined,
not an error that fails the handshake (RFC 7692 7), so it is skipped here like
any offer that does not allow server_config.
*/
func AcceptDeflateOffer(server_config PermessageDeflateConfig, req *http.Request) *PermessageDeflateConfig {
	for _, extension := range parseExtensions(req.Header) {
		if extension.name != permessageDeflateName {
			continue
		}
		// An offer that does not parse must be declined (RFC 7692 7).
		offer, err := parseDeflateParams(extension.params)
		if err != nil {
			continue
		}
		// A copy of server_config, so changing answer_deflate_config never
		// touches server_config. It is made again for each offer, so a skipped
		// offer leaves nothing behind. Only what the offer requires is changed
		// below.
		answer_deflate_config := server_config
		if offer.ServerNoContextTakeover {
			answer_deflate_config.ServerNoContextTakeover = true
		}
		// The offer's client_no_context_takeover is only a hint. Sending it back
		// makes it agreed, so the server knows the client drops its context
		// (RFC 7692 7.1.1.2).
		if offer.ClientNoContextTakeover {
			answer_deflate_config.ClientNoContextTakeover = true
		}
		if offer.ServerMaxWindowBits != 0 && (answer_deflate_config.ServerMaxWindowBits == 0 || offer.ServerMaxWindowBits < answer_deflate_config.ServerMaxWindowBits) {
			answer_deflate_config.ServerMaxWindowBits = offer.ServerMaxWindowBits
		}
		// Library choice: 8 to 14 is valid (RFC 7692 7.1.2.1), but the server would
		// promise to send within that window, and Go's compress/flate cannot.
		if answer_deflate_config.ServerMaxWindowBits != 0 && answer_deflate_config.ServerMaxWindowBits < 15 {
			continue
		}
		// Without client_max_window_bits in the offer, the server must not send it
		// and the client may use 15 bits (RFC 7692 7.1.2.2). Only a server limit
		// of 15 allows that.
		if answer_deflate_config.ClientMaxWindowBitsEnabled && !offer.ClientMaxWindowBitsEnabled {
			if answer_deflate_config.ClientMaxWindowBits != 15 {
				continue
			}
			// The client's 15 bits are accepted, so no client_max_window_bits is
			// sent. The server takes the 0 as 15 bits (RFC 7692 7.1.2.2).
			answer_deflate_config.ClientMaxWindowBitsEnabled = false
			answer_deflate_config.ClientMaxWindowBits = 0
		}
		if answer_deflate_config.ClientMaxWindowBitsEnabled && offer.ClientMaxWindowBits != 0 && offer.ClientMaxWindowBits < answer_deflate_config.ClientMaxWindowBits {
			answer_deflate_config.ClientMaxWindowBits = offer.ClientMaxWindowBits
		}
		return &answer_deflate_config
	}
	return nil
}

/*
ValidateHandShakeRequest checks a client's opening request against RFC 6455
4.2.1 — the method, the HTTP version, Host, Upgrade, Connection,
Sec-WebSocket-Key and Sec-WebSocket-Version — returning the first rule it breaks
wrapped around the sentinel for it, which is what DeclineByError routes on to
pick a status code. The other fields are not checked.

Host is read from req.Host, since http.ReadRequest moves it out of Header.

Connection and Upgrade are read as token lists rather than compared whole — see
headerHasToken. Sec-WebSocket-Key must be base64 of 16 bytes. The rest are
single values the RFC fixes exactly. Sec-WebSocket-Key and Sec-WebSocket-Version
must each come on one line (11.3.1, 11.3.5).

The failing value is reported as it arrived, joined across lines if it came on
more than one, so a rejected handshake says what the peer actually sent.
*/
func ValidateHandShakeRequest(client_request *http.Request) error {
	// top validation
	if client_request.Method != "GET" {
		return fmt.Errorf("validate handshake: method %q: %w", client_request.Method, ErrHttpMethodNotAllowed)
	}
	if client_request.Proto != "HTTP/1.1" {
		return fmt.Errorf("validate handshake: protocol %q: %w", client_request.Proto, ErrHttpProtocolOrVersionNotAllowed)
	}
	if !validHost(client_request.Host) {
		return fmt.Errorf("validate handshake: Host %q: %w", client_request.Host, ErrHttpHostHeaderInvalid)
	}
	// header validation
	if vals := client_request.Header.Values("Sec-WebSocket-Key"); len(vals) > 1 {
		return fmt.Errorf("validate handshake: Sec-WebSocket-Key is %q: %w", strings.Join(vals, ", "), ErrHttpSecWebSocketKeyDuplicated)
	}
	if val := client_request.Header.Get("Sec-WebSocket-Key"); val == "" {
		return fmt.Errorf("validate handshake: %w", ErrHttpSecWebSocketKeyHeaderNotSet)
	} else if key, err := base64.StdEncoding.DecodeString(val); err != nil || len(key) != 16 {
		return fmt.Errorf("validate handshake: Sec-WebSocket-Key %q: %w", val, ErrHttpSecWebSocketKeyInvalid)
	}
	if !headerHasToken(client_request.Header, "Connection", "upgrade") {
		return fmt.Errorf("validate handshake: Connection is %q: %w",
			strings.Join(client_request.Header.Values("Connection"), ", "),
			ErrHttpConnectionHeaderNotUpgrade)
	}
	if !headerHasToken(client_request.Header, "Upgrade", "websocket") {
		return fmt.Errorf("validate handshake: Upgrade is %q: %w",
			strings.Join(client_request.Header.Values("Upgrade"), ", "),
			ErrHttpUpgradeHeaderNotWebsocket)
	}
	if vals := client_request.Header.Values("Sec-WebSocket-Version"); len(vals) > 1 {
		return fmt.Errorf("validate handshake: Sec-WebSocket-Version is %q: %w", strings.Join(vals, ", "), ErrHttpSecWebSocketVersionDuplicated)
	}
	// 4.2.1: the header is required and 13 is the only version RFC 6455 defines.
	// Missing and wrong are the same answer — a client that sends neither is
	// speaking a draft this package does not implement.
	if val := client_request.Header.Get("Sec-WebSocket-Version"); val != "13" {
		return fmt.Errorf("validate handshake: Sec-WebSocket-Version is %q: %w", val, ErrHttpSecWebSocketVersionNotSupported)
	}
	return nil
}

// validHost uses httpguts.ValidHostHeader's byte set but also refuses empty,
// since a ws URI always has a host (RFC 6455 3, 4.2.1).
func validHost(host string) bool {
	if host == "" {
		return false
	}
	for i := 0; i < len(host); i++ {
		c := host[i]
		if 'a' <= c && c <= 'z' || 'A' <= c && c <= 'Z' || '0' <= c && c <= '9' {
			continue
		}
		if !strings.ContainsRune("!$%&'()*+,-.:;=[]_~", rune(c)) {
			return false
		}
	}
	return true
}

// SendHandShakeResponse writes res to w as a plain HTTP message.
func SendHandShakeResponse(w io.Writer, res *http.Response) error {
	return sendHandShakeResponse(w, res, sendHandShakeResponseDI{
		responseToPlainHTTPMsg: responseToPlainHTTPMsg,
	})
}

type sendHandShakeResponseDI struct {
	responseToPlainHTTPMsg func(res *http.Response) (string, error)
}

func sendHandShakeResponse(w io.Writer, res *http.Response, di sendHandShakeResponseDI) error {
	if res == nil {
		return fmt.Errorf("SendHandShakeResponse: %w", ErrHandshakeResponseNil)
	}
	plainHttpResponseMsg, err := di.responseToPlainHTTPMsg(res)
	if err != nil {
		return err
	}
	_, err = w.Write([]byte(plainHttpResponseMsg))
	return err
}

// Function to convert http.Response to a plain HTTP message
func responseToPlainHTTPMsg(resp *http.Response) (string, error) {
	return responseToPlainHTTPMsgInner(resp, responseToPlainHTTPMsgDI{
		ioReadAll:      io.ReadAll,
		httpStatusText: http.StatusText,
	})
}

type responseToPlainHTTPMsgDI struct {
	ioReadAll      func(r io.Reader) ([]byte, error)
	httpStatusText func(code int) string
}

func responseToPlainHTTPMsgInner(resp *http.Response, di responseToPlainHTTPMsgDI) (string, error) {
	// Create a buffer to hold the entire HTTP message
	var buf bytes.Buffer

	// Write the status line
	fmt.Fprintf(&buf, "%s %d %s\r\n", resp.Proto, resp.StatusCode, di.httpStatusText(resp.StatusCode))

	// Write the headers
	for key, values := range resp.Header {
		for _, value := range values {
			fmt.Fprintf(&buf, "%s: %s\r\n", key, value)
		}
	}

	// End the headers section
	buf.WriteString("\r\n")

	// Write the body if it's not nil
	if resp.Body != nil {
		bodyBytes, err := di.ioReadAll(resp.Body)
		if err != nil {
			return "", err
		}
		// Write the body
		buf.Write(bodyBytes)
		// Close the body
		resp.Body.Close() // Important to close the body after reading
	}

	return buf.String(), nil
}
