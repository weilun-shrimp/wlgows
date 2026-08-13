package wlgows

import (
	"net/http"
	"strings"
)

/*
headerHasToken reports whether a header field carries token among its values.

RFC 7230 3.2.2 makes a field like Connection or Upgrade a comma separated list
that may also repeat across several lines, and RFC 6455 4.1 asks only that the
token be present. "keep-alive, Upgrade" is a conforming client and a proxy is
entitled to add to the list on the way through, so comparing the whole field
value refuses a request the RFC allows.

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
