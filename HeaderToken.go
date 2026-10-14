package wlgows

import (
	"net/http"
	"strings"
)

/*
headerHasToken reports whether a header field carries token among its values.

RFC 7230 6.1 and 6.7 make Connection and Upgrade comma separated lists, and
3.2.2 lets one repeat across several lines. RFC 6455 4.2.1 asks only that the
request's Connection and Upgrade carry the token, and 4.1 the same of the
response's Connection. "keep-alive, Upgrade" is a conforming client and a proxy
is entitled to add to the list on the way through, so comparing the whole field
value refuses a request the RFC allows.

The response's Upgrade is stricter: 4.1 fails any value other than "websocket".
The client still reads it as a token list.

Values rather than Get, because Get returns the first line only and a field
split across two would have its second half ignored.

Token equality rather than substring, because "no-upgrade" and "upgraded" both
contain "upgrade" and neither one is it.
*/
func headerHasToken(header http.Header, key string, token string) bool {
	for _, value := range header.Values(key) {
		for _, candidate := range strings.Split(value, ",") {
			if strings.EqualFold(strings.TrimSpace(candidate), token) {
				return true
			}
		}
	}
	return false
}
