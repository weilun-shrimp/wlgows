package wlgows

import (
	"bufio"
	"bytes"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"slices"
	"strings"
)

/*
ClientHandShake runs the RFC 6455 4.1 client handshake over c: it adds the
upgrade headers to req, sends it, and checks the response. On success it
returns a *Conn that masks what it sends (RFC 6455 5.1). conn is nil whenever
err is non-nil, but the response is still returned.

r must be the reader you go on to read frames from, since it may already hold
the start of the first frame. w is the Conn's writer, see NewConn.

config adds subprotocols and the permessage-deflate offer to req, see
ClientHandShakeConfig.

A response RFC 6455 4.1 says to fail closes c before the error returns (7.1.7).
Any other error, such as a status other than 101, leaves c open for you to
close. ClientMustFailConnection(err) tells the two apart.
*/
func ClientHandShake(c net.Conn, r *bufio.Reader, w *bufio.Writer, req *http.Request, config ClientHandShakeConfig) (*Conn, *http.Response, error) {
	return clientHandShake(c, r, w, req, config, clientHandShakeDI{
		upgradeRequest:                       UpgradeRequest,
		sendHandShakeRequest:                 SendHandShakeRequest,
		readHandShakeResponse:                ReadHandShakeResponse,
		validateHandShakeResponse:            ValidateHandShakeResponse,
		validateHandShakeResponseSubprotocol: ValidateHandShakeResponseSubprotocol,
		validateHandShakeResponseDeflate:     ValidateHandShakeResponseDeflate,
		newConn:                              NewConn,
	})
}

/*
ClientHandShakeConfig is what ClientHandShake adds to the request. The zero
value adds nothing.

Each item is appended after the request's own lines, which are sent as they
are. Setting a header by hand and here at the same time is not recommended
unless you know what you are doing.
*/
type ClientHandShakeConfig struct {
	// Subprotocols are offered in Sec-WebSocket-Protocol, in order of
	// preference (RFC 6455 4.1). One already in the request is not added again.
	Subprotocols []string

	// PermessageDeflates are permessage-deflate offers, in order of preference
	// (RFC 7692 5). Each one is sent as one offer.
	//
	// ClientMaxWindowBitsEnabled returns ErrHandshakeRequestDeflateUnsupported
	// (library choice). It would let the server set the client's window under
	// 15 bits, and Go's compress/flate cannot send with a window that small.
	// Without it the client may always use 15 bits.
	PermessageDeflates []PermessageDeflateConfig
}

type clientHandShakeDI struct {
	upgradeRequest                       func(req *http.Request, config ClientHandShakeConfig) error
	sendHandShakeRequest                 func(w io.Writer, req *http.Request) error
	readHandShakeResponse                func(r *bufio.Reader, req *http.Request) (*http.Response, error)
	validateHandShakeResponse            func(res *http.Response, sec_websocket_key string) error
	validateHandShakeResponseSubprotocol func(res *http.Response, req *http.Request) error
	validateHandShakeResponseDeflate     func(res *http.Response, req *http.Request) (agreed_config *PermessageDeflateConfig, err error)
	newConn                              func(c net.Conn, r *bufio.Reader, w *bufio.Writer, maskSendFrame bool) (*Conn, error)
}

func clientHandShake(c net.Conn, r *bufio.Reader, w *bufio.Writer, req *http.Request, config ClientHandShakeConfig, di clientHandShakeDI) (*Conn, *http.Response, error) {
	if err := di.upgradeRequest(req, config); err != nil {
		return nil, nil, err
	}
	if err := di.sendHandShakeRequest(c, req); err != nil {
		return nil, nil, err
	}
	res, err := di.readHandShakeResponse(r, req)
	if err != nil {
		return nil, res, err
	}
	if err := di.validateHandShakeResponse(res, req.Header.Get("Sec-WebSocket-Key")); err != nil {
		if ClientMustFailConnection(err) {
			c.Close() // the error is the report 7.1.7 allows; a failed close adds nothing to it
		}
		return nil, res, err
	}
	if err := di.validateHandShakeResponseSubprotocol(res, req); err != nil {
		if ClientMustFailConnection(err) {
			c.Close() // the error is the report 7.1.7 allows; a failed close adds nothing to it
		}
		return nil, res, err
	}
	if _, err := di.validateHandShakeResponseDeflate(res, req); err != nil {
		if ClientMustFailConnection(err) {
			c.Close() // the error is the report 7.1.7 allows; a failed close adds nothing to it
		}
		return nil, res, err
	}
	conn, err := di.newConn(c, r, w, true)
	if err != nil {
		return nil, res, err
	}
	return conn, res, nil
}

/*
UpgradeRequest makes req a valid opening handshake (RFC 6455 4.1): GET, HTTP/1.1,
no body and the four upgrade headers, whatever req had before. Then it appends
what config asks for (see ClientHandShakeConfig). Anything else you set is sent
as it is. A url with no host has no Host to send, so it returns
ErrUrlHostEmpty.
*/
func UpgradeRequest(req *http.Request, config ClientHandShakeConfig) error {
	return upgradeRequest(req, config, upgradeRequestDI{
		generateWebSocketKey: GenerateWebSocketKey,
		addSubprotocols:      addSubprotocols,
		addReqDeflateOffers:  addReqDeflateOffers,
	})
}

type upgradeRequestDI struct {
	generateWebSocketKey func() string
	addSubprotocols      func(req *http.Request, subprotocols []string)
	addReqDeflateOffers  func(req *http.Request, premsg_configs []PermessageDeflateConfig) error
}

func upgradeRequest(req *http.Request, config ClientHandShakeConfig, di upgradeRequestDI) error {
	if req == nil {
		return fmt.Errorf("UpgradeRequest: %w", ErrHandshakeRequestNil)
	}
	if req.URL == nil || req.URL.Host == "" {
		return fmt.Errorf("UpgradeRequest: %w", ErrUrlHostEmpty)
	}
	req.Method = "GET"
	req.Proto, req.ProtoMajor, req.ProtoMinor = "HTTP/1.1", 1, 1
	req.Body = nil // bytes after the headers would be read as the first frame
	for k, v := range map[string]string{
		"Upgrade":               "websocket",
		"Connection":            "Upgrade",
		"Sec-WebSocket-Key":     di.generateWebSocketKey(),
		"Sec-WebSocket-Version": "13",
	} {
		req.Header.Set(k, v)
	}

	di.addSubprotocols(req, config.Subprotocols)
	return di.addReqDeflateOffers(req, config.PermessageDeflates)
}

// addSubprotocols appends the subprotocols that req does not have yet, on a
// line of their own after req's lines (RFC 6455 4.1).
func addSubprotocols(req *http.Request, subprotocols []string) {
	var added []string
	for _, subprotocol := range subprotocols {
		if !slices.Contains(RequestSubprotocols(req), subprotocol) && !slices.Contains(added, subprotocol) {
			added = append(added, subprotocol)
		}
	}
	if len(added) > 0 {
		req.Header.Add("Sec-WebSocket-Protocol", strings.Join(added, ", "))
	}
}

// addReqDeflateOffers appends the offers in order, on a line of their own after
// req's lines (RFC 7692 5). If any offer is refused below, it adds nothing and
// returns the error.
func addReqDeflateOffers(req *http.Request, premsg_configs []PermessageDeflateConfig) error {
	var offers []string
	for _, premsg_config := range premsg_configs {
		// Library choice: client_max_window_bits, with or without a value, lets
		// the server answer 8 to 14 (RFC 7692 7.1.2.2). Go's compress/flate
		// cannot keep a window under 15 bits, so the client would have to fail
		// the connection. Without the param the client may always use 15.
		if premsg_config.ClientMaxWindowBitsEnabled {
			return fmt.Errorf("UpgradeRequest: ClientMaxWindowBitsEnabled: %w", ErrHandshakeRequestDeflateUnsupported)
		}
		// RFC 7692 7.1.2.1: window bits are 8 to 15. 0 is not sent.
		if bits := premsg_config.ServerMaxWindowBits; bits != 0 && (bits < 8 || bits > 15) {
			return fmt.Errorf("UpgradeRequest: window bits %d: %w", bits, ErrHandshakeRequestDeflateWindowBitsInvalid)
		}
		offers = append(offers, premsg_config.Format())
	}
	if len(offers) > 0 {
		req.Header.Add("Sec-WebSocket-Extensions", strings.Join(offers, ", "))
	}
	return nil
}

// SendHandShakeRequest writes req to w as a plain HTTP message.
func SendHandShakeRequest(w io.Writer, req *http.Request) error {
	return sendHandShakeRequest(w, req, sendHandShakeRequestDI{
		requestToPlainHTTPMsg: RequestToPlainHTTPMsg,
	})
}

type sendHandShakeRequestDI struct {
	requestToPlainHTTPMsg func(req *http.Request) (string, error)
}

func sendHandShakeRequest(w io.Writer, req *http.Request, di sendHandShakeRequestDI) error {
	if req == nil {
		return fmt.Errorf("SendHandShakeRequest: %w", ErrHandshakeRequestNil)
	}
	plainHttpRequestMsg, err := di.requestToPlainHTTPMsg(req)
	if err != nil {
		return err
	}
	_, err = w.Write([]byte(plainHttpRequestMsg))
	return err
}

// ReadHandShakeResponse reads and parses the server's handshake response from
// r. r must be the same *bufio.Reader the caller goes on to read frames from
// — see the note on ClientHandShake.
func ReadHandShakeResponse(r *bufio.Reader, req *http.Request) (*http.Response, error) {
	return readHandShakeResponse(r, req, readHandShakeResponseDI{
		httpReadResponse: http.ReadResponse,
	})
}

type readHandShakeResponseDI struct {
	httpReadResponse func(r *bufio.Reader, req *http.Request) (*http.Response, error)
}

func readHandShakeResponse(r *bufio.Reader, req *http.Request, di readHandShakeResponseDI) (*http.Response, error) {
	if req == nil {
		return nil, fmt.Errorf("ReadHandShakeResponse: %w", ErrHandshakeRequestNil)
	}
	return di.httpReadResponse(r, req)
}

func ValidateHandShakeResponse(res *http.Response, sec_websocket_key string) error {
	if res.StatusCode != 101 {
		return fmt.Errorf("invalid handshake response status code %d: %w", res.StatusCode, ErrHandshakeResponseStatusCodeInvalid)
	}
	if res.Proto != "HTTP/1.1" {
		return fmt.Errorf("invalid handshake response proto %s: %w", res.Proto, ErrHandshakeResponseProtoInvalid)
	}
	// Read as a token list, not compared whole — a server answering
	// "Connection: keep-alive, Upgrade" is conforming (RFC 7230 6.1). See
	// headerHasToken.
	if !headerHasToken(res.Header, "Connection", "upgrade") {
		return fmt.Errorf(
			"invalid handshake response header Connection %s: %w",
			strings.Join(res.Header.Values("Connection"), ", "),
			ErrHandshakeResponseConnectionHeaderInvalid,
		)
	}
	// Compared whole, not as a token list: RFC 6455 4.1 fails any value other
	// than "websocket", so "websocket, h2c" or a second line is refused.
	if upgrade := res.Header.Values("Upgrade"); len(upgrade) != 1 || !strings.EqualFold(strings.TrimSpace(upgrade[0]), "websocket") {
		return fmt.Errorf(
			"invalid handshake response header Upgrade %s: %w",
			strings.Join(res.Header.Values("Upgrade"), ", "),
			ErrHandshakeResponseUpgradeHeaderInvalid,
		)
	}
	if res.Header.Get("Sec-WebSocket-Accept") == "" {
		return fmt.Errorf("invalid handshake response header Sec-WebSocket-Accept: %w", ErrHandshakeResponseAcceptHeaderMissing)
	}
	sec_ws_accept := GenerateSecWebsocketAccept(sec_websocket_key)
	if res.Header.Get("Sec-WebSocket-Accept") != sec_ws_accept {
		return fmt.Errorf("invalid handshake response header Sec-WebSocket-Accept %s, want %s: %w",
			res.Header.Get("Sec-WebSocket-Accept"), sec_ws_accept, ErrHandshakeResponseAcceptHeaderMismatch)
	}

	// optional - RFC 6455 doesn't put Sec-WebSocket-Version in a successful 101 response at all
	// if res.Header.Get("Sec-WebSocket-Version") != "13" {
	// 	return errors.New("invalid handshake response header Sec-WebSocket-Version. The Sec-WebSocket-Version header is must be 13")
	// }
	return nil
}

/*
ClientMustFailConnection reports whether err is a response check RFC 6455
4.1 answers with "Fail the WebSocket Connection", which means closing it
(7.1.7). The list follows 4.1's order. A version other than HTTP/1.1 is one:
the check runs only on a 101, and a 101 that is not a valid HTTP/1.1 upgrade
cannot be read as one.

ClientHandShake has already closed the connection for these. If you run the
checks yourself, close it. Only for errors from the client handshake: for
errors from Listen, see StandardClosePayloadFor.
*/
func ClientMustFailConnection(err error) bool {
	return errors.Is(err, ErrHandshakeResponseProtoInvalid) ||
		errors.Is(err, ErrHandshakeResponseUpgradeHeaderInvalid) ||
		errors.Is(err, ErrHandshakeResponseConnectionHeaderInvalid) ||
		errors.Is(err, ErrHandshakeResponseAcceptHeaderMissing) ||
		errors.Is(err, ErrHandshakeResponseAcceptHeaderMismatch) ||
		errors.Is(err, ErrHandshakeResponseSubprotocolRepeated) ||
		errors.Is(err, ErrHandshakeResponseSubprotocolMultiple) ||
		errors.Is(err, ErrHandshakeResponseSubprotocolNotOffered) ||
		errors.Is(err, ErrHandshakeResponseExtensionNotOffered) ||
		errors.Is(err, ErrHandshakeResponseDeflateParamsInvalid) ||
		errors.Is(err, ErrHandshakeResponseDeflateUnsupported)
}

/*
ValidateHandShakeResponseSubprotocol checks the server's Sec-WebSocket-Protocol
against what req offered (RFC 6455 4.1). No header passes: the server picked
none. One value the client offered passes. The header on more than one line
returns ErrHandshakeResponseSubprotocolRepeated. More than one value on one
line returns ErrHandshakeResponseSubprotocolMultiple. One value never
offered, including any value when nothing was offered, returns
ErrHandshakeResponseSubprotocolNotOffered.

Whether no subprotocol is acceptable is the caller's choice, so it is not
checked here.
*/
func ValidateHandShakeResponseSubprotocol(res *http.Response, req *http.Request) error {
	lines := res.Header.Values("Sec-WebSocket-Protocol")
	// No header means the server picked none (RFC 6455 4.2.2). An empty header
	// is still one line: the server sent something, just something invalid.
	if len(lines) == 0 {
		return nil
	}
	// RFC 6455 11.3.4: the header appears once in a response.
	if len(lines) > 1 {
		return fmt.Errorf("invalid handshake response header Sec-WebSocket-Protocol %q: %w",
			lines, ErrHandshakeResponseSubprotocolRepeated)
	}
	// RFC 6455 4.3: the response is one token, not a list.
	values := strings.Split(lines[0], ",")
	if len(values) > 1 {
		return fmt.Errorf("invalid handshake response header Sec-WebSocket-Protocol %q: %w",
			lines[0], ErrHandshakeResponseSubprotocolMultiple)
	}
	chosen := strings.TrimSpace(values[0])
	if offered := RequestSubprotocols(req); !slices.Contains(offered, chosen) {
		return fmt.Errorf("invalid handshake response header Sec-WebSocket-Protocol %q, offered %q: %w",
			chosen, offered, ErrHandshakeResponseSubprotocolNotOffered)
	}
	return nil
}

/*
ValidateHandShakeResponseDeflate checks the server's permessage-deflate answer
in Sec-WebSocket-Extensions, and that one offer in req allows it (RFC 7692 5,
7.1). It returns what both sides follow: the answer, plus the client's own
promises from the offers. It returns nil when the server declined deflate.
*/
func ValidateHandShakeResponseDeflate(res *http.Response, req *http.Request) (agreed_config *PermessageDeflateConfig, err error) {
	return validateHandShakeResponseDeflate(res, req, validateHandShakeResponseDeflateDI{
		validateRespDeflateExt:        validateRespDeflateExt,
		validateRespDeflateExtWithReq: validateRespDeflateExtWithReq,
	})
}

type validateHandShakeResponseDeflateDI struct {
	validateRespDeflateExt        func(res *http.Response) (server_config *PermessageDeflateConfig, err error)
	validateRespDeflateExtWithReq func(server_config PermessageDeflateConfig, req *http.Request) (agreed_config PermessageDeflateConfig, err error)
}

// validateHandShakeResponseDeflate returns nil when the server declined
// deflate.
func validateHandShakeResponseDeflate(res *http.Response, req *http.Request, di validateHandShakeResponseDeflateDI) (agreed_config *PermessageDeflateConfig, err error) {
	server_config, err := di.validateRespDeflateExt(res)
	if err != nil || server_config == nil {
		return nil, err
	}
	agreed, err := di.validateRespDeflateExtWithReq(*server_config, req)
	if err != nil {
		return nil, err
	}
	return &agreed, nil
}

// validateRespDeflateExt reads the permessage-deflate entry of res. It returns
// nil and no error when the server did not accept deflate.
func validateRespDeflateExt(res *http.Response) (server_config *PermessageDeflateConfig, err error) {
	var deflate *extension
	for _, extension := range parseExtensions(res.Header) {
		if extension.name != permessageDeflateName {
			continue
		}
		// RFC 7692 5: the server accepts one offer at most.
		if deflate != nil {
			return nil, fmt.Errorf("permessage-deflate accepted twice: %w", ErrHandshakeResponseExtensionNotOffered)
		}
		deflate = &extension
	}
	if deflate == nil { // Server don't offer the deflate. It is ok.
		return nil, nil
	}

	params, err := parseDeflateParams(deflate.params)
	if err != nil {
		return nil, fmt.Errorf("%v: %w", err, ErrHandshakeResponseDeflateParamsInvalid)
	}
	// RFC 7692 7.1.2.2: a response must give client_max_window_bits a value.
	if params.ClientMaxWindowBitsEnabled && params.ClientMaxWindowBits == 0 {
		return nil, fmt.Errorf("client_max_window_bits has no value: %w", ErrHandshakeResponseDeflateParamsInvalid)
	}
	return &params, nil
}

/*
validateRespDeflateExtWithReq checks that one deflate offer in req allows the
response params. The server accepts a single offer (RFC 7692 5), so the params
are matched against each offer alone, not against all of them together.

An offer allows the response when:

  - client_max_window_bits is in the response only if the offer had it, and
    is not over the offer's value when it had one (RFC 7692 7.1.2.2).
  - server_no_context_takeover is in the response if the offer had it
    (RFC 7692 7.1.1.1).
  - server_max_window_bits is in the response if the offer had it, and is not
    over the offer's value (RFC 7692 7.1.2.1).

server_no_context_takeover, client_no_context_takeover and
server_max_window_bits may be added without an offer (RFC 7692 7.1.1.1,
7.1.1.2, 7.1.2.1).

It returns server_config with the client's own promises added: a
client_no_context_takeover from any offer that allows the response binds the
client, even when the response leaves it out (RFC 7692 7.1.1.2).
*/
func validateRespDeflateExtWithReq(server_config PermessageDeflateConfig, req *http.Request) (agreed_config PermessageDeflateConfig, err error) {
	agreed_config = server_config
	allowed := false
	for _, extension := range parseExtensions(req.Header) {
		if extension.name != permessageDeflateName {
			continue
		}
		// An offer that does not parse must be declined (RFC 7692 7), so it
		// does not count as offered.
		offer, err := parseDeflateParams(extension.params)
		if err != nil {
			continue
		}
		if server_config.ClientMaxWindowBitsEnabled && !offer.ClientMaxWindowBitsEnabled {
			continue
		}
		if offer.ClientMaxWindowBits != 0 && server_config.ClientMaxWindowBits > offer.ClientMaxWindowBits {
			continue
		}
		// RFC 7692 7.1.1.1: a server that accepts this offer must send it back.
		if offer.ServerNoContextTakeover && !server_config.ServerNoContextTakeover {
			continue
		}
		// RFC 7692 7.1.2.1: a server that accepts this offer must send it back,
		// with the offer's value or less.
		if offer.ServerMaxWindowBits != 0 && (server_config.ServerMaxWindowBits == 0 || server_config.ServerMaxWindowBits > offer.ServerMaxWindowBits) {
			continue
		}
		allowed = true
		// The response does not say which offer it accepted, so the client
		// downgrades for safety: if any offer that allows it had
		// client_no_context_takeover, the client drops context. Dropping
		// context is always safe for a sender. Keeping it could break a server
		// that relied on the hint. Nothing later can change the result.
		if offer.ClientNoContextTakeover {
			agreed_config.ClientNoContextTakeover = true
			return agreed_config, nil
		}
	}
	if !allowed {
		return PermessageDeflateConfig{}, fmt.Errorf("permessage-deflate response matches no offer: %w", ErrHandshakeResponseExtensionNotOffered)
	}
	return agreed_config, nil
}

func GenerateWebSocketKey() string {
	return generateWebSocketKey(generateWebSocketKeyDI{
		// Go 1.24+: crypto/rand.Read never returns an error; it crashes the
		// program instead.
		randRead: rand.Read,
	})
}

type generateWebSocketKeyDI struct {
	randRead func(b []byte) (n int, err error)
}

func generateWebSocketKey(di generateWebSocketKeyDI) string {
	key := make([]byte, 16) // RFC 6455 4.1: a random 16 byte nonce
	_, _ = di.randRead(key)
	return base64.StdEncoding.EncodeToString(key)
}

// Function to convert http.Request to a plain HTTP message
func RequestToPlainHTTPMsg(req *http.Request) (string, error) {
	return requestToPlainHTTPMsg(req, requestToPlainHTTPMsgDI{
		ioReadAll: io.ReadAll,
	})
}

type requestToPlainHTTPMsgDI struct {
	ioReadAll func(r io.Reader) ([]byte, error)
}

func requestToPlainHTTPMsg(req *http.Request, di requestToPlainHTTPMsgDI) (string, error) {
	// Create a buffer to hold the entire HTTP message
	var buf bytes.Buffer

	// Write the top line (e.g., "GET / HTTP/1.1")
	// RequestURI() gives the origin form (path + query, "/" when the path is
	// empty). Writing req.URL instead would emit the absolute form
	// "GET ws://host:port HTTP/1.1", which servers read as a proxy request and
	// routers answer with a 301 redirect.
	fmt.Fprintf(&buf, "%s %s %s\r\n", req.Method, req.URL.RequestURI(), req.Proto)

	// Write the headers
	if req.Header.Get("Host") == "" { // put Host header if not exists
		req.Header.Set("Host", req.URL.Host)
	}
	for key, values := range req.Header {
		for _, value := range values {
			fmt.Fprintf(&buf, "%s: %s\r\n", key, value)
		}
	}

	// End the headers section
	buf.WriteString("\r\n")

	// Write the body if it's not nil
	if req.Body != nil {
		bodyBytes, err := di.ioReadAll(req.Body)
		if err != nil {
			return "", err
		}
		// Write the body
		buf.Write(bodyBytes)
		// Close the body
		req.Body.Close() // Important to close the body after reading
	}

	return buf.String(), nil
}
