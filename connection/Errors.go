package connection

type Error struct {
	Type string
	Msg  string
}

func (e *Error) Error() string {
	return e.Msg
}

// type list

// Base errors
const (
	ConnectionReadFail    = "ConnectionReadFail"
	ConnectionReadEmpty   = "ConnectionReadEmpty"
	EmptyClientRequest    = "EmptyClientRequest"
	ExistingClientRequest = "ExistingClientRequest"
	FailSendHand          = "FailSendHand"
)

// http
const (
	InvalidHttpMsgFormation = "InvalidHttpMsgFormation"
	// attribute errors
	HttpMethodNotAllowed            = "MethodNotAllowed"
	HttpProtocolOrVersionNotAllowed = "HttpProtocolOrVersionNotAllowed"
	// header item errors
	HttpSecWebSocketKeyHeaderNotSet = "HttpSecWebSocketKeyHeaderNotSet"
	HttpConnectionHeaderNotUpgrade  = "HttpSecConnectionNotUpgrade"
	HttpUpgradeHeaderNotWebsocket   = "HttpSecUpgradeNotWebsocket"
	// request response
	HttpRequestHasResponse = "HttpRequestHasResponse"
)

// Server errors
const (
// FailSendHand = "Fail Send Hand"
)
