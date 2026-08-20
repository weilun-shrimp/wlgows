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
	// ErrFrameByteLengthExceeded is returned by GetFrameFromReader before it
	// allocates, when the frame header declares a payload larger than the max
	// the caller allowed. RFC 6455 close code 1009 is the matching response.
	ErrFrameByteLengthExceeded = errors.New("frame payload exceeds the max byte length")

	// ErrControlFramePayloadTooLong is returned before anything reaches the
	// socket. RFC 6455 5.5 caps a control frame payload at 125 bytes so it
	// always fits in the 7 bit inline length.
	ErrControlFramePayloadTooLong = errors.New("control frame payload exceeds 125 bytes")

	// ErrNotControlFrameOpcode guards the control frame send path against a
	// data opcode, which would go out with control frame rules applied to it.
	ErrNotControlFrameOpcode = errors.New("opcode is not a control frame opcode")

	// ErrNotDataFrameOpcode guards NewDataFrame against a control opcode, which
	// would go out without the 125 byte cap and forced FIN that RFC 6455 5.5
	// requires of one.
	ErrNotDataFrameOpcode = errors.New("opcode is not a data frame opcode")

	// ErrNotCloseFrameOpcode is returned by Frame.GetClosePayload for a frame
	// that is not a close frame.
	ErrNotCloseFrameOpcode = errors.New("opcode is not a close frame opcode")

	// ErrClosePayloadTooShort is a close frame carrying exactly one byte. RFC
	// 6455 5.5.1 allows no body or a body starting with a 2 byte status code,
	// so a single byte is half a status code and a protocol error — answer it
	// with close code 1002.
	ErrClosePayloadTooShort = errors.New("close frame payload is 1 byte, which cannot hold a status code")
)

// listener
var (
	// ErrListenerIsListening is returned by Listen when a Listen is already
	// running on the same Listener. Two read loops on one connection would each
	// take an arbitrary subset of the frames.
	ErrListenerIsListening = errors.New("listener is already listening")

	// ErrListenerConnIsNil is a Listen on a Listener built by hand rather than by
	// NewListener, so it has no connection. Reading from it would panic on the
	// first frame instead of saying what is wrong.
	ErrListenerConnIsNil = errors.New("listener has no conn")

	// ErrContinuationFrameWithoutMsg is a continuation frame with no fragmented
	// message open. RFC 6455 5.4 only allows opcode 0 after a data frame with
	// FIN clear.
	//
	// Both directions: from Listen the peer sent it, and close code 1002 is the
	// answer. From StartLongDataTransmission you opened a message with it, and
	// there is nothing for it to continue.
	ErrContinuationFrameWithoutMsg = errors.New("continuation frame with no message being assembled")

	// ErrDataFrameDuringMsg is a data frame arriving while a fragmented message
	// is still open. RFC 6455 5.4 forbids interleaving messages, so every frame
	// after the first must carry opcode 0 — answer with close code 1002.
	ErrDataFrameDuringMsg = errors.New("data frame arrived while a message was still being assembled")

	// ErrInvalidUTF8 is a text message payload that is not valid UTF-8. RFC
	// 6455 5.6 makes a text message UTF-8 as a whole, and 8.1 requires failing
	// the connection when it is not.
	//
	// Both directions: from Listen it means the peer sent it, and answering with
	// close code 1007 is the response. From SendText it means you did, and the
	// message was refused before it reached the socket.
	//
	// A close frame reason is UTF-8 too (5.5.1) and is not checked yet.
	ErrInvalidUTF8 = errors.New("text message payload is not valid UTF-8")

	// ErrInvalidCloseStatusCode is a close frame carrying a status code RFC
	// 6455 7.4 does not allow on the wire — answer with close code 1002.
	ErrInvalidCloseStatusCode = errors.New("close frame status code is not one the RFC allows on the wire")

	// ErrMsgFrameCountExceeded is a message arriving in more frames than
	// MaxMsgFrameCount allows — answer with close code 1009.
	ErrMsgFrameCountExceeded = errors.New("message arrived in more frames than allowed")

	// ErrReservedBitsSet is a frame with RSV1, RSV2 or RSV3 set. RFC 6455 5.2
	// requires them clear unless an extension defining them was negotiated in
	// the handshake, and none is — answer with close code 1002.
	ErrReservedBitsSet = errors.New("frame has a reserved bit set with no extension negotiated")

	// ErrControlFrameFragmented is a control frame with FIN clear. RFC 6455 5.5
	// forbids fragmenting one — answer with close code 1002.
	ErrControlFrameFragmented = errors.New("control frame must not be fragmented")

	// ErrFrameNotMasked is an unmasked frame where the peer is a client. RFC
	// 6455 5.1 requires a client to mask every frame it sends, and a server to
	// fail the connection on one that is not — answer with close code 1002.
	ErrFrameNotMasked = errors.New("frame from the peer is not masked")

	// ErrFrameMasked is a masked frame where the peer is a server. RFC 6455 5.1
	// forbids a server masking, and requires a client to fail the connection on
	// one that is — answer with close code 1002.
	ErrFrameMasked = errors.New("frame from the peer is masked")
)

// connection state
var (
	// ErrLongDataTransmissionNotStarted is TransmitData or
	// EndLongDataTransmission called with no transmission open. Both rely on
	// StartLongDataTransmission having taken dataFramesWriteLocker, so acting
	// anyway would write outside the lock and unlock what was never locked.
	ErrLongDataTransmissionNotStarted = errors.New("no long data transmission is open")

	// ErrCloseAlreadySent is a send on a connection that has already put a close
	// frame on the wire. RFC 6455 5.5.1 ends the conversation there: the closing
	// handshake is one close each way, and no data frame follows one.
	//
	// From SendClose it is a race resolved rather than a failure to handle — two
	// goroutines both answering the peer's close both call it, and this tells the
	// loser its frame was not needed. From SendText, SendBinary or
	// StartLongDataTransmission it means the message came too late to send.
	//
	// Nothing reached the socket either way.
	ErrCloseAlreadySent = errors.New("a close frame has already been sent")
)

// handshake
var (
	ErrHttpMsgFormationInvalid         = errors.New("invalid http message formation")
	ErrHttpMethodNotAllowed            = errors.New("http method not allowed")
	ErrHttpProtocolOrVersionNotAllowed = errors.New("http protocol or version not allowed")
	ErrHttpSecWebSocketKeyHeaderNotSet = errors.New("Sec-WebSocket-Key header is not set")
	ErrHttpConnectionHeaderNotUpgrade  = errors.New("Connection header is not Upgrade")
	ErrHttpUpgradeHeaderNotWebsocket   = errors.New("Upgrade header is not websocket")

	// ErrHttpSecWebSocketVersionNotSupported is a handshake request whose
	// Sec-WebSocket-Version is missing or is not 13. RFC 6455 4.2.1 requires the
	// header and fixes the value at 13; 4.4 has the server answer 426 Upgrade
	// Required, naming the versions it does support, so a client speaking an
	// older draft learns what to retry with rather than guessing.
	ErrHttpSecWebSocketVersionNotSupported = errors.New("Sec-WebSocket-Version is not 13")
	ErrHttpRequestHasResponse              = errors.New("http request already has a response")
)

// client handshake
var (
	// ErrHandshakeRequestNil is UpgradeRequest, SendHandShakeRequest or
	// ReadHandShakeResponse called with a nil *http.Request.
	ErrHandshakeRequestNil = errors.New("handshake request is nil")

	// The following are ValidateHandShakeResponse rejecting the server's
	// opening handshake response against RFC 6455 4.1.
	ErrHandshakeResponseStatusCodeInvalid       = errors.New("handshake response status code is not 101")
	ErrHandshakeResponseProtoInvalid            = errors.New("handshake response protocol is not HTTP/1.1")
	ErrHandshakeResponseConnectionHeaderInvalid = errors.New("handshake response Connection header does not contain the Upgrade token")
	ErrHandshakeResponseUpgradeHeaderInvalid    = errors.New("handshake response Upgrade header does not contain the websocket token")
	ErrHandshakeResponseAcceptHeaderMissing     = errors.New("handshake response Sec-WebSocket-Accept header is not set")
	ErrHandshakeResponseAcceptHeaderMismatch    = errors.New("handshake response Sec-WebSocket-Accept header does not match the request's key")
)

// server handshake
var (
	// ErrHandshakeResponseNil is SendHandShakeResponse called with a nil
	// *http.Response.
	ErrHandshakeResponseNil = errors.New("handshake response is nil")
)

/*
StandardClosePayloadFor returns the close frame payload that answers err, or
nil when a close frame is not the answer.

RFC 6455 7.1.7 fails the connection on a protocol violation and 7.4.1 fixes
which status code names each one. This is that table:

	ErrInvalidUTF8                                -> 1007 CloseInvalidFramePayloadData
	ErrFrameByteLengthExceeded,
	ErrMsgFrameCountExceeded                      -> 1009 CloseMessageTooBig
	every other violation a frame can commit      -> 1002 CloseProtocolError

Reason is left empty. What to tell the peer about your own internals is a policy
this package has no opinion on — set it before sending if you want one.

Everything else gets nil. That is the absence of an attribution, not a claim
that the connection is healthy, and it never means "close the connection":

  - The connection already failed. io.EOF, os.ErrDeadlineExceeded and
    net.ErrClosed arrive here through GetNextFrame, and the peer broke no rule.
    7.4.1 calls this 1006 abnormal closure and forbids putting 1006 on the wire,
    because it is what an endpoint records about itself.

  - ErrListenerConnIsNil or ErrListenerIsListening, which Listen returns before
    it reads anything. ErrListenerIsListening means another goroutine holds a
    running Listen, so closing the connection would end a healthy session.

  - An error of your own, handed to PauseListen and returned by Listen. This
    package cannot answer for a rule it does not know, so map yours before
    calling if it deserves a close code.

  - An error this package has never seen, which a custom net.Conn or a wrapping
    layer can return through GetNextFrame. Guessing 1002 would blame the peer
    for something nothing here can attribute to them. 7.1.1 permits closing with
    no close frame at all, so sending nothing is always legal, while a wrong
    status code is not something the peer can recover from. Add your own arm
    before this call if you know what such an error means.

Decide the connection's fate from its state, not from this result. The Errors
section of LISTENER_README.md works through the whole shape.
*/
func StandardClosePayloadFor(err error) *ClosePayload {
	switch {
	case errors.Is(err, ErrInvalidUTF8):
		return &ClosePayload{StatusCode: CloseInvalidFramePayloadData}

	case errors.Is(err, ErrFrameByteLengthExceeded),
		errors.Is(err, ErrMsgFrameCountExceeded):
		return &ClosePayload{StatusCode: CloseMessageTooBig}

	case errors.Is(err, ErrReservedBitsSet),
		errors.Is(err, ErrFrameNotMasked),
		errors.Is(err, ErrFrameMasked),
		errors.Is(err, ErrControlFrameFragmented),
		errors.Is(err, ErrControlFramePayloadTooLong),
		errors.Is(err, ErrContinuationFrameWithoutMsg),
		errors.Is(err, ErrDataFrameDuringMsg),
		errors.Is(err, ErrInvalidCloseStatusCode),
		errors.Is(err, ErrClosePayloadTooShort):
		return &ClosePayload{StatusCode: CloseProtocolError}
	}
	return nil
}
