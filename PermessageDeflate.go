package wlgows

/*
permessage-deflate (RFC 7692) compresses each data message with DEFLATE
(RFC 1951). DEFLATE replaces repeated bytes with a back-reference: copy this
many bytes from that far back. The window is how far back a reference may go.

Context takeover decides what a reference may point into (RFC 7692 7.2.1):

  - Context: the current message and earlier ones, as one stream. This is the
    default. Small repeated messages compress well, but each side keeps its
    window between messages. The header has no flag:

	Sec-WebSocket-Extensions: permessage-deflate

  - No context: the current message only. Each message starts with an empty
    window, so nothing is kept between messages. The header has a flag for
    each side that drops it:

	Sec-WebSocket-Extensions: permessage-deflate; server_no_context_takeover; client_no_context_takeover

A sender is told to drop context with server_no_context_takeover or
client_no_context_takeover (RFC 7692 7.1.1). No parameter turns it on.

The window is 2^N bytes, N from 8 to 15, and 15 (32 KB) when not given. It
limits references in both modes. server_max_window_bits and
client_max_window_bits lower it for each sender (RFC 7692 7.1.2).

wlgows sends with a 32 KB window only (library choice): Go's compress/flate
cannot send with a smaller one, so a smaller one is declined.
*/

import (
	"fmt"
	"net/http"
	"strconv"
	"strings"
)

/*
PermessageDeflateConfig is one set of RFC 7692 compression params, each field
named after its param. The zero value keeps context on both sides, which is
the RFC 7692 default.
*/
type PermessageDeflateConfig struct {
	// ClientNoContextTakeover is client_no_context_takeover: the client resets
	// its window after each message it sends (RFC 7692 7.1.1.2).
	ClientNoContextTakeover bool

	// ServerNoContextTakeover is server_no_context_takeover: the server resets
	// its window after each message it sends (RFC 7692 7.1.1.1).
	ServerNoContextTakeover bool

	// ClientMaxWindowBitsEnabled sends client_max_window_bits, which lets the
	// server limit the client's window (RFC 7692 7.1.2.2).
	ClientMaxWindowBitsEnabled bool

	// ClientMaxWindowBits is the most window bits the client sends with, 2^N
	// bytes, used only when ClientMaxWindowBitsEnabled is true. 0 sends the
	// param with no value, which leaves the size to the server. Otherwise it
	// must be 8 to 15 (RFC 7692 7.1.2.2).
	ClientMaxWindowBits uint

	// ServerMaxWindowBits is the most window bits the server sends with, 2^N
	// bytes. 0 leaves the param out. Otherwise it must be 8 to 15
	// (RFC 7692 7.1.2.1).
	ServerMaxWindowBits uint
}

/*
Format writes premsg_config as one permessage-deflate entry, an offer or a
response, for Sec-WebSocket-Extensions. It does not check the config.

It is a low-level method, and putting its result on the wire yourself is not
recommended unless you know what you are doing. Printing it, such as in a log
or with fmt, is fine.

Before:

	PermessageDeflateConfig{
		ClientNoContextTakeover:    true,
		ServerMaxWindowBits:        10,
		ClientMaxWindowBitsEnabled: true,
	}

After:

	permessage-deflate; client_no_context_takeover; server_max_window_bits=10; client_max_window_bits
*/
func (premsg_config *PermessageDeflateConfig) Format() string {
	entry := permessageDeflateName
	if premsg_config.ServerNoContextTakeover {
		entry += "; server_no_context_takeover"
	}
	if premsg_config.ClientNoContextTakeover {
		entry += "; client_no_context_takeover"
	}
	if premsg_config.ServerMaxWindowBits != 0 {
		entry += "; server_max_window_bits=" + strconv.FormatUint(uint64(premsg_config.ServerMaxWindowBits), 10)
	}
	if premsg_config.ClientMaxWindowBitsEnabled && premsg_config.ClientMaxWindowBits != 0 {
		entry += "; client_max_window_bits=" + strconv.FormatUint(uint64(premsg_config.ClientMaxWindowBits), 10)
	} else if premsg_config.ClientMaxWindowBitsEnabled {
		// RFC 7692 7.1.2.2: only an offer may leave out the value.
		entry += "; client_max_window_bits"
	}
	return entry
}

const permessageDeflateName = "permessage-deflate"

// extension is one entry of Sec-WebSocket-Extensions.
type extension struct {
	name   string
	params []extensionParam
}

type extensionParam struct {
	name     string
	value    string
	hasValue bool
}

/*
parseExtensions reads Sec-WebSocket-Extensions with the RFC 6455 9.1 grammar:
a comma list across every line, each entry a name followed by ";"-separated
params, each param a name with an optional "=" value. A quoted value is
unquoted. Empty entries are skipped (RFC 7230 7). An entry with params but no
name is kept with the name "", so a response holding one fails.

Before:

	permessage-deflate; server_max_window_bits=10; client_max_window_bits, permessage-deflate

After:

	[]extension{
		{name: "permessage-deflate", params: []extensionParam{
			{name: "server_max_window_bits", value: "10", hasValue: true},
			{name: "client_max_window_bits", value: "", hasValue: false},
		}},
		{name: "permessage-deflate"},
	}
*/
func parseExtensions(header http.Header) []extension {
	var extensions []extension
	for _, entry := range headerValues(header, "Sec-WebSocket-Extensions") {
		parts := splitOutsideQuotes(entry, ';')
		ext := extension{name: strings.TrimSpace(parts[0])}
		for _, part := range parts[1:] {
			paramName, value, hasValue := strings.Cut(part, "=")
			ext.params = append(ext.params, extensionParam{
				name:     strings.TrimSpace(paramName),
				value:    unquote(strings.TrimSpace(value)),
				hasValue: hasValue,
			})
		}
		extensions = append(extensions, ext)
	}
	return extensions
}

/*
unquote removes the quotes and escapes of a quoted-string (RFC 7230 3.2.6).
Anything else is returned as it is.

Before -> after:

	"10"    -> 10
	"a\"b"  -> a"b
	10      -> 10
*/
func unquote(s string) string {
	if len(s) < 2 || s[0] != '"' || s[len(s)-1] != '"' {
		return s
	}
	var builder strings.Builder
	for i := 1; i < len(s)-1; i++ {
		if s[i] == '\\' && i+1 < len(s)-1 {
			i++
		}
		builder.WriteByte(s[i])
	}
	return builder.String()
}

/*
parseDeflateParams reads the params of one permessage-deflate entry, an offer
or a response. It refuses only what RFC 7692 7 makes invalid on both sides: an
unknown param, a duplicate, a value on a no_context_takeover flag, and window
bits that are not 8 to 15. What each side accepts is decided by the handshake
that calls it.

Before:

	[]extensionParam{
		{name: "client_no_context_takeover", value: "", hasValue: false},
		{name: "server_max_window_bits", value: "10", hasValue: true},
		{name: "client_max_window_bits", value: "", hasValue: false},
	}

After:

	PermessageDeflateConfig{
		ClientNoContextTakeover:    true,
		ServerMaxWindowBits:        10,
		ClientMaxWindowBitsEnabled: true,
	}
*/
func parseDeflateParams(params []extensionParam) (PermessageDeflateConfig, error) {
	var premsg_config PermessageDeflateConfig
	seen := map[string]bool{}
	for _, param := range params {
		if seen[param.name] {
			return premsg_config, fmt.Errorf("duplicate parameter %q", param.name)
		}
		seen[param.name] = true

		switch param.name {
		case "client_no_context_takeover":
			if param.hasValue {
				return premsg_config, fmt.Errorf("parameter %q takes no value", param.name)
			}
			premsg_config.ClientNoContextTakeover = true
		case "server_no_context_takeover":
			if param.hasValue {
				return premsg_config, fmt.Errorf("parameter %q takes no value", param.name)
			}
			premsg_config.ServerNoContextTakeover = true
		case "client_max_window_bits":
			// It may come without a value (RFC 7692 7.1.2.2).
			premsg_config.ClientMaxWindowBitsEnabled = true
			if !param.hasValue {
				continue
			}
			bits, ok := parseWindowBits(param)
			if !ok {
				return premsg_config, fmt.Errorf("parameter %q has an invalid value %q", param.name, param.value)
			}
			premsg_config.ClientMaxWindowBits = bits
		case "server_max_window_bits":
			bits, ok := parseWindowBits(param)
			if !ok {
				return premsg_config, fmt.Errorf("parameter %q has an invalid value %q", param.name, param.value)
			}
			premsg_config.ServerMaxWindowBits = bits
		default:
			return premsg_config, fmt.Errorf("unknown parameter %q", param.name)
		}
	}
	return premsg_config, nil
}

/*
parseWindowBits reads a window size: a decimal 8 to 15 with no leading zero
(RFC 7692 7.1.2.1). 15 is the most DEFLATE can reach back, 32 KB (RFC 1951
3.2.5). 8 is the least zlib allows, 256 bytes (RFC 1950 2.2).
*/
func parseWindowBits(param extensionParam) (uint, bool) {
	bits, err := strconv.Atoi(param.value)
	if !param.hasValue || err != nil || strconv.Itoa(bits) != param.value || bits < 8 || bits > 15 {
		return 0, false
	}
	return uint(bits), true
}
