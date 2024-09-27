package connection

type Error struct {
	Type string
	Msg  string
}

func (e *Error) Error() string {
	return e.Msg
}

// type list

// base errors
const (
	ClientRequestHasSet = "ClientRequestHasSet"
)

// http
const (
	HttpMsgFormationInvalid = "HttpMsgFormationInvalid"
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
