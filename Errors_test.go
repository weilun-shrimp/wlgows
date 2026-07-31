package wlgows

import "testing"

func TestErrorError(t *testing.T) {
	wsError := &Error{Type: HttpMethodNotAllowed, Msg: "method not allowed"}
	if got := wsError.Error(); got != "method not allowed" {
		t.Errorf("Error() = %q, want %q", got, "method not allowed")
	}
	// Error() reports Msg only; Type is a separate machine-readable tag.
	if wsError.Type != "MethodNotAllowed" {
		t.Errorf("Type = %q", wsError.Type)
	}
}

func TestErrorSatisfiesErrorInterface(t *testing.T) {
	var err error = &Error{Msg: "boom"}
	if err.Error() != "boom" {
		t.Errorf("Error() = %q", err.Error())
	}
}

/*
Pins the wire values of the error type constants. Four of them deliberately
differ from their Go identifier, so a careless rename would silently change
what DeclineByErrorType matches on.
*/
func TestErrorTypeConstants(t *testing.T) {
	tests := []struct {
		constant string
		want     string
	}{
		{ClientRequestHasSet, "ClientRequestHasSet"},
		{HttpMsgFormationInvalid, "HttpMsgFormationInvalid"},
		{HttpMethodNotAllowed, "MethodNotAllowed"},
		{HttpProtocolOrVersionNotAllowed, "HttpProtocolOrVersionNotAllowed"},
		{HttpSecWebSocketKeyHeaderNotSet, "HttpSecWebSocketKeyHeaderNotSet"},
		{HttpConnectionHeaderNotUpgrade, "HttpSecConnectionNotUpgrade"},
		{HttpUpgradeHeaderNotWebsocket, "HttpSecUpgradeNotWebsocket"},
		{HttpRequestHasResponse, "HttpRequestHasResponse"},
	}
	for _, testCase := range tests {
		if testCase.constant != testCase.want {
			t.Errorf("constant = %q, want %q", testCase.constant, testCase.want)
		}
	}
}
