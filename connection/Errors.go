package connection

import "net/http"

type Error struct {
	Type string
	Msg  string
}

func (e *Error) Error() string {
	return e.Msg
}

// type list

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

/*
Set the ResponseWriter appropriate status code and body by error_type

empty error type means valid request, will put 101 for websocket
*/
func SetResponseWriterByErrprType(w http.ResponseWriter, error_type string) {
	switch error_type {
	case "": // valid type, set to 101 for websocket
		w.WriteHeader(http.StatusSwitchingProtocols)
	case HttpMsgFormationInvalid:
		w.WriteHeader(http.StatusBadRequest)
	case HttpMethodNotAllowed:
		w.WriteHeader(http.StatusMethodNotAllowed)
	case HttpProtocolOrVersionNotAllowed:
		w.WriteHeader(http.StatusHTTPVersionNotSupported)
	case HttpSecWebSocketKeyHeaderNotSet, HttpConnectionHeaderNotUpgrade, HttpUpgradeHeaderNotWebsocket:
		w.WriteHeader(http.StatusBadRequest)
		w.Write([]byte(string(error_type)))
	default: // other ondefined type value
		w.WriteHeader(http.StatusInternalServerError)
	}
}
