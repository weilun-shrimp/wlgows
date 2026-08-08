package wlgows

import "errors"

/*
Sentinel errors. Every error this package returns wraps one of these with
fmt.Errorf and %w, so the caller gets a message with context and can still ask
which failure it was:

	_, err := sc.HandShake()
	if errors.Is(err, wlgows.ErrHttpMethodNotAllowed) {
		// ...
	}

errors.Is unwraps to any depth, so wrapping again on the way up costs nothing.
Compare with errors.Is rather than ==: a returned error is the wrapper, never
the sentinel itself.
*/

// frame
var (
	// ErrFrameByteLengthExceeded is returned by GetFrameFromTCPConn before it
	// allocates, when the frame header declares a payload larger than the max
	// the caller allowed. RFC 6455 close code 1009 is the matching response.
	ErrFrameByteLengthExceeded = errors.New("frame payload exceeds the max byte length")
)

// connection state
var (
	ErrClientRequestHasSet = errors.New("client request has already been set")
)

// handshake
var (
	ErrHttpMsgFormationInvalid         = errors.New("invalid http message formation")
	ErrHttpMethodNotAllowed            = errors.New("http method not allowed")
	ErrHttpProtocolOrVersionNotAllowed = errors.New("http protocol or version not allowed")
	ErrHttpSecWebSocketKeyHeaderNotSet = errors.New("Sec-WebSocket-Key header is not set")
	ErrHttpConnectionHeaderNotUpgrade  = errors.New("Connection header is not Upgrade")
	ErrHttpUpgradeHeaderNotWebsocket   = errors.New("Upgrade header is not websocket")
	ErrHttpRequestHasResponse          = errors.New("http request already has a response")
)
