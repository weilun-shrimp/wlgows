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

The response's Upgrade is stricter: 4.1 fails any value other than "websocket",
so ValidateHandShakeResponse compares it whole and does not use this.

Values rather than Get, because Get returns the first line only and a field
split across two would have its second half ignored.

Token equality rather than substring, because "no-upgrade" and "upgraded" both
contain "upgrade" and neither one is it.
*/
func headerHasToken(header http.Header, key string, token string) bool {
	for _, value := range headerValues(header, key) {
		if strings.EqualFold(value, token) {
			return true
		}
	}
	return false
}

/*
headerValues returns every value of a comma separated header, across all its
lines, in order (RFC 7230 3.2.2). Each value is trimmed, and empty ones are
skipped (RFC 7230 7). A comma inside a quoted string does not split
(RFC 7230 3.2.6).

Before:

	Sec-WebSocket-Protocol: chat, , superchat
	Sec-WebSocket-Protocol: mine

After:

	[]string{"chat", "superchat", "mine"}
*/
func headerValues(header http.Header, key string) []string {
	var values []string
	for _, line := range header.Values(key) {
		for _, value := range splitOutsideQuotes(line, ',') {
			if value = strings.TrimSpace(value); value != "" {
				values = append(values, value)
			}
		}
	}
	return values
}

/*
splitOutsideQuotes splits s at every separator that is not inside a quoted
string. A backslash inside quotes escapes the next byte.

Before, with separator ',':

	permessage-deflate; server_max_window_bits="10", x-ext; note="a,b"

After:

	[]string{
		`permessage-deflate; server_max_window_bits="10"`,
		` x-ext; note="a,b"`,
	}
*/
func splitOutsideQuotes(s string, separator byte) []string {
	var parts []string
	start, quoted := 0, false
	for i := 0; i < len(s); i++ {
		switch {
		case quoted && s[i] == '\\':
			i++
		case s[i] == '"':
			quoted = !quoted
		case !quoted && s[i] == separator:
			parts = append(parts, s[start:i])
			start = i + 1
		}
	}
	return append(parts, s[start:])
}
