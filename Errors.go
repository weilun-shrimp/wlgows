package wlgows

import "errors"

/*
Sentinel errors. Every error this package returns wraps one of these with
fmt.Errorf and %w, so the caller gets a message with context and can still ask
which failure it was:

	_, _, err := wlgows.ServerHandShake(netConn, reader, writer, request, config)
	if errors.Is(err, wlgows.ErrHttpMethodNotAllowed) {
		// ...
	}

errors.Is unwraps to any depth, so wrapping again on the way up costs nothing.
Compare with errors.Is rather than ==: a returned error is the wrapper, never
the sentinel itself.
*/

// frame
var (
	// ErrFrameByteLengthExceeded is returned by Listen, judged from the frame
	// header before the payload is allocated, when a data or reserved-opcode
	// frame goes over MaxFrameSize or a message's data frames go over
	// MaxDataFramesSize, headers included. RFC 6455 close code 1009 is the matching response.
	ErrFrameByteLengthExceeded = errors.New("frame payload exceeds the max byte length")

	// ErrPayloadLengthMSBSet is returned by GetFrameHeaderFromReader when a 64
	// bit payload length has its most significant bit set. RFC 6455 5.2
	// requires that bit to be 0, so this is a protocol error. Answer it with
	// close code 1002.
	ErrPayloadLengthMSBSet = errors.New("64 bit payload length has its most significant bit set")

	// ErrPayloadLengthNotMinimal is returned by GetFrameHeaderFromReader when a
	// payload length uses more bytes than it needs. RFC 6455 5.2 requires the
	// shortest form. Answer it with close code 1002.
	ErrPayloadLengthNotMinimal = errors.New("payload length is not in its shortest encoding")

	// ErrControlFramePayloadTooLong is a control frame payload over 125 bytes,
	// which RFC 6455 5.5 forbids so it always fits in the 7 bit inline length.
	// A send refuses it before anything reaches the socket, and
	// GetFrameHeaderFromReader refuses one from the peer — answer with close
	// code 1002.
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

	// ErrInvalidUTF8 is a text payload that is not valid UTF-8. RFC
	// 6455 5.6 makes a text message UTF-8 as a whole, and 8.1 requires failing
	// the connection when it is not.
	//
	// Both directions: from Listen it means the peer sent it, and answering with
	// close code 1007 is the response. From SendText it means you did, and the
	// message was refused before it reached the socket.
	//
	// Listen also returns it for a close frame whose reason is not UTF-8 (5.5.1,
	// 8.1), and SendClose or SendFrame for such a reason, with nothing sent.
	ErrInvalidUTF8 = errors.New("payload is not valid UTF-8")

	// ErrInvalidCloseStatusCode is a close frame whose status code is local only
	// (1005, 1006, 1015: RFC 6455 7.4.1 forbids sending them), unused (0-999),
	// reserved with no meaning (1004), not registered in 1000-2999 (7.4.2,
	// IANA), or above 4999 — answer with close code 1002.
	//
	// SendClose or SendFrame also returns it for such a code, with nothing sent.
	ErrInvalidCloseStatusCode = errors.New("close frame status code is not one the RFC allows on the wire")

	// ErrDataFrameCountExceeded is a message arriving in more frames than
	// MaxDataFrameCount allows — answer with close code 1009.
	ErrDataFrameCountExceeded = errors.New("message arrived in more frames than allowed")

	// ErrReservedBitsSet is a frame with RSV1, RSV2 or RSV3 set where no
	// negotiated extension allows it. RFC 6455 5.2 requires them clear unless
	// an extension defining them was negotiated in the handshake. The only one
	// is permessage-deflate, which allows RSV1 on the first frame of a text or
	// binary message (RFC 7692 6, 6.1) — answer with close code 1002.
	ErrReservedBitsSet = errors.New("frame has a reserved bit set that no negotiated extension allows")

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

	// ErrReservedOpcode is a frame whose opcode RFC 6455 5.2 reserves, and no
	// negotiated extension defines. 5.2 has the receiver fail the connection —
	// answer with close code 1002. RFC6455UnknownHook stops a Listener with it.
	ErrReservedOpcode = errors.New("frame has a reserved opcode")
)

// connection state
var (
	// ErrLongDataTransmissionNotStarted is TransmitData or
	// EndLongDataTransmission called with no transmission open. Both rely on
	// StartLongDataTransmission having taken dataFramesWriteLocker, so acting
	// anyway would write outside the lock.
	ErrLongDataTransmissionNotStarted = errors.New("no long data transmission is open")

	// ErrCloseAlreadySent is a send on a connection that has already put a close
	// frame on the wire. The closing handshake is one close each way (RFC 6455
	// 1.4, 7.1.5), and no data frame follows one (5.5.1).
	//
	// From SendClose it is a race resolved rather than a failure to handle — two
	// goroutines both answering the peer's close both call it, and this tells the
	// loser its frame was not needed. From a data send it means the frame came
	// too late: nothing of it reached the socket, though earlier chunks of the
	// same message may have, leaving it unterminated. A ping or pong is never
	// refused: 5.5.2 still allows both.
	ErrCloseAlreadySent = errors.New("a close frame has already been sent")

	// ErrDeflateLevelInvalid is a DeflateLevel compress/flate has no level
	// for: below flate.HuffmanOnly (-2) or above flate.BestCompression (9).
	// Nothing is sent.
	ErrDeflateLevelInvalid = errors.New("deflate level is not a compress/flate level")

	// ErrInflateDataInvalid is a compressed message that does not inflate:
	// the peer sent a corrupt or truncated deflate stream (RFC 7692 7.2.2).
	// It wraps compress/flate's error — answer with close code 1007.
	ErrInflateDataInvalid = errors.New("compressed message does not inflate")

	// ErrInflatedSizeExceeded is a message past the size you allow. Nothing in
	// this package returns it: pass it to StandardClosePayloadFor for close
	// code 1009.
	ErrInflatedSizeExceeded = errors.New("inflated data exceeds the size allowed")

	// ErrInflateFramesTokenStale is a token from StartInflateDataFrames whose
	// message ended, or another message's token.
	ErrInflateFramesTokenStale = errors.New("token is not the open inflated message's")

	// ErrDataFramesNotEnded is data frames given to DataFramesReader that are
	// empty or whose last frame has FIN clear, so they are not one whole
	// message (RFC 6455 5.4).
	ErrDataFramesNotEnded = errors.New("data frames do not end with a FIN frame")
)

// dial
var (
	// ErrUrlSchemeInvalid is a url whose scheme is not ws, wss, http or https.
	ErrUrlSchemeInvalid = errors.New("url scheme is not ws, wss, http or https")

	// ErrUrlHostEmpty is a url with no host to dial, or to send as Host.
	ErrUrlHostEmpty = errors.New("url host is empty")

	// ErrUrlFragmentSet is a url with a #fragment, which RFC 6455 3 forbids. A
	// literal # belongs escaped as %23.
	ErrUrlFragmentSet = errors.New("url has a fragment")
)

// handshake
var (
	ErrHttpMsgFormationInvalid         = errors.New("invalid http message formation")
	ErrHttpMethodNotAllowed            = errors.New("http method not allowed")
	ErrHttpProtocolOrVersionNotAllowed = errors.New("http protocol or version not allowed")
	ErrHttpSecWebSocketKeyHeaderNotSet = errors.New("Sec-WebSocket-Key header is not set")

	// ErrHttpHostHeaderInvalid is a handshake request with no Host, or one with
	// bytes no host can hold. RFC 6455 4.2.1 requires Host, and RFC 7230 5.4
	// has the server answer 400.
	ErrHttpHostHeaderInvalid = errors.New("Host header is missing or invalid")

	// ErrHttpSecWebSocketKeyInvalid is a Sec-WebSocket-Key that is not base64 of
	// 16 bytes, as RFC 6455 4.2.1 requires.
	ErrHttpSecWebSocketKeyInvalid = errors.New("Sec-WebSocket-Key is not base64 of 16 bytes")

	// ErrHttpSecWebSocketKeyDuplicated is a Sec-WebSocket-Key on more than one
	// line. RFC 6455 11.3.1 allows it once.
	ErrHttpSecWebSocketKeyDuplicated = errors.New("Sec-WebSocket-Key appears more than once")

	// ErrHttpSecWebSocketVersionDuplicated is a Sec-WebSocket-Version on more
	// than one line. RFC 6455 11.3.5 allows it once.
	ErrHttpSecWebSocketVersionDuplicated = errors.New("Sec-WebSocket-Version appears more than once")

	ErrHttpConnectionHeaderNotUpgrade = errors.New("Connection header is not Upgrade")
	ErrHttpUpgradeHeaderNotWebsocket  = errors.New("Upgrade header is not websocket")

	// ErrHttpSecWebSocketVersionNotSupported is a handshake request whose
	// Sec-WebSocket-Version is missing or is not 13. RFC 6455 4.2.1 requires the
	// header and fixes the value at 13; 4.2.2 has the server answer with an
	// error such as 426 Upgrade Required, naming the versions it does support
	// (4.4), so a client speaking an older draft learns what to retry with
	// rather than guessing.
	ErrHttpSecWebSocketVersionNotSupported = errors.New("Sec-WebSocket-Version is not 13")
	ErrHttpRequestHasResponse              = errors.New("http request already has a response")
)

// client handshake
var (
	// ErrHandshakeRequestNil is UpgradeRequest, SendHandShakeRequest or
	// ReadHandShakeResponse called with a nil *http.Request.
	ErrHandshakeRequestNil = errors.New("handshake request is nil")

	// ErrHandshakeRequestDeflateWindowBitsInvalid is a PermessageDeflateConfig
	// whose ServerMaxWindowBits is not 0 or 8 to 15 (RFC 7692 7.1.2.1). A
	// server must decline such an offer (RFC 7692 7).
	ErrHandshakeRequestDeflateWindowBitsInvalid = errors.New("permessage-deflate window bits are not 0 or 8 to 15")

	// ErrHandshakeRequestDeflateUnsupported is a PermessageDeflateConfig with
	// ClientMaxWindowBitsEnabled (library choice). It would let the server set
	// the client's window under 15 bits, and Go's compress/flate cannot send
	// with a window that small.
	ErrHandshakeRequestDeflateUnsupported = errors.New("permessage-deflate offer with client_max_window_bits is not supported")

	// The following are ValidateHandShakeResponse rejecting the server's
	// opening handshake response against RFC 6455 4.1.
	ErrHandshakeResponseStatusCodeInvalid       = errors.New("handshake response status code is not 101")
	ErrHandshakeResponseProtoInvalid            = errors.New("handshake response protocol is not HTTP/1.1")
	ErrHandshakeResponseConnectionHeaderInvalid = errors.New("handshake response Connection header does not contain the Upgrade token")
	ErrHandshakeResponseUpgradeHeaderInvalid    = errors.New("handshake response Upgrade header is not websocket")
	ErrHandshakeResponseAcceptHeaderMissing     = errors.New("handshake response Sec-WebSocket-Accept header is not set")
	ErrHandshakeResponseAcceptHeaderMismatch    = errors.New("handshake response Sec-WebSocket-Accept header does not match the request's key")

	// The following are ValidateHandShakeResponseSubprotocol rejecting the
	// server's Sec-WebSocket-Protocol. RFC 6455 4.1 has the client fail the
	// connection on each. Repeated is the header sent more than once (11.3.4).
	// Multiple is one header holding a list, where the response allows one
	// token (4.3).
	ErrHandshakeResponseSubprotocolRepeated   = errors.New("handshake response Sec-WebSocket-Protocol header must not appear more than once")
	ErrHandshakeResponseSubprotocolMultiple   = errors.New("handshake response Sec-WebSocket-Protocol has more than one value")
	ErrHandshakeResponseSubprotocolNotOffered = errors.New("handshake response Sec-WebSocket-Protocol is not a value the client offered")

	// The following are ValidateHandShakeResponseDeflate rejecting the server's
	// permessage-deflate answer. RFC 7692 5 has the client fail the connection
	// on each. ExtensionNotOffered is an answer no single offer allows, or
	// permessage-deflate accepted twice. DeflateParamsInvalid is a param RFC
	// 7692 7 forbids. DeflateUnsupported is a valid answer wlgows cannot follow
	// (library choice).
	ErrHandshakeResponseExtensionNotOffered  = errors.New("handshake response Sec-WebSocket-Extensions is not what the client offered")
	ErrHandshakeResponseDeflateParamsInvalid = errors.New("handshake response permessage-deflate params are invalid")
	ErrHandshakeResponseDeflateUnsupported   = errors.New("handshake response permessage-deflate params are not supported")
)

// server handshake
var (
	// ErrHandshakeResponseNil is SendHandShakeResponse called with a nil
	// *http.Response.
	ErrHandshakeResponseNil = errors.New("handshake response is nil")

	// The following are ValidateServerDeflateConfig rejecting
	// ServerHandShakeConfig.PermessageDeflates. DeflateConfigInvalid would put a
	// param RFC 7692 7 forbids in the response. DeflateConfigUnsupported is a
	// window under 15 bits for the server, which Go's compress/flate cannot
	// send with (library choice).
	ErrHandshakeServerDeflateConfigInvalid     = errors.New("server permessage-deflate config is invalid")
	ErrHandshakeServerDeflateConfigUnsupported = errors.New("server permessage-deflate config is not supported")
)

/*
StandardClosePayloadFor returns the close frame payload that answers err, or
nil when a close frame is not the answer.

RFC 6455 7.1.7 fails the connection on a protocol violation and 7.4.1 defines
the codes an endpoint may use to name each one. This is that table:

	ErrInvalidUTF8,
	ErrInflateDataInvalid                         -> 1007 CloseInvalidFramePayloadData
	ErrFrameByteLengthExceeded,
	ErrDataFrameCountExceeded,
	ErrInflatedSizeExceeded                       -> 1009 CloseMessageTooBig
	every other violation a frame can commit      -> 1002 CloseProtocolError

Reason is left empty. What to tell the peer about your own internals is a policy
this package has no opinion on — set it before sending if you want one.

Everything else gets nil. That is the absence of an attribution, not a claim
that the connection is healthy, and it never means "close the connection":

  - The connection already failed. io.EOF, io.ErrUnexpectedEOF,
    os.ErrDeadlineExceeded and net.ErrClosed arrive here through a frame read,
    and the peer broke no rule. 7.4.1 calls this 1006 abnormal closure and
    forbids putting 1006 on the wire, because it is what an endpoint records
    about itself.

  - ErrListenerConnIsNil or ErrListenerIsListening, which Listen returns before
    it reads anything. ErrListenerIsListening means another goroutine holds a
    running Listen, so closing the connection would end a healthy session.

  - An error of your own, handed to PauseListen and returned by Listen. This
    package cannot answer for a rule it does not know, so map yours before
    calling if it deserves a close code.

  - An error this package has never seen, which a custom net.Conn or a wrapping
    layer can return through a frame read. Guessing 1002 would blame the peer
    for something nothing here can attribute to them. 7.1.7 only says a close
    frame SHOULD be sent, so sending none is not a violation, while a wrong
    status code is not something the peer can recover from. If you want to send
    one, 1011 says the fault is on this side. Add your own arm before this call
    if you know what such an error means.

Decide the connection's fate from its state, not from this result. The Errors
section of LISTENER_README.md works through the whole shape.
*/
func StandardClosePayloadFor(err error) *ClosePayload {
	switch {
	case errors.Is(err, ErrInvalidUTF8),
		errors.Is(err, ErrInflateDataInvalid):
		return &ClosePayload{StatusCode: CloseInvalidFramePayloadData}

	case errors.Is(err, ErrFrameByteLengthExceeded),
		errors.Is(err, ErrDataFrameCountExceeded),
		errors.Is(err, ErrInflatedSizeExceeded):
		return &ClosePayload{StatusCode: CloseMessageTooBig}

	case errors.Is(err, ErrReservedBitsSet),
		errors.Is(err, ErrFrameNotMasked),
		errors.Is(err, ErrFrameMasked),
		errors.Is(err, ErrReservedOpcode),
		errors.Is(err, ErrControlFrameFragmented),
		errors.Is(err, ErrControlFramePayloadTooLong),
		errors.Is(err, ErrContinuationFrameWithoutMsg),
		errors.Is(err, ErrDataFrameDuringMsg),
		errors.Is(err, ErrInvalidCloseStatusCode),
		errors.Is(err, ErrClosePayloadTooShort),
		errors.Is(err, ErrPayloadLengthMSBSet),
		errors.Is(err, ErrPayloadLengthNotMinimal):
		return &ClosePayload{StatusCode: CloseProtocolError}
	}
	return nil
}
