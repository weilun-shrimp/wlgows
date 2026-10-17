package wlgows

import (
	"net/http"
	"reflect"
	"strconv"
	"testing"
)

func TestFormat(t *testing.T) {
	cases := []struct {
		name          string
		premsg_config PermessageDeflateConfig
		want          string
	}{
		{name: "the zero value", premsg_config: PermessageDeflateConfig{}, want: "permessage-deflate"},
		{
			name:          "ServerNoContextTakeover",
			premsg_config: PermessageDeflateConfig{ServerNoContextTakeover: true},
			want:          "permessage-deflate; server_no_context_takeover",
		},
		{
			name:          "ClientNoContextTakeover",
			premsg_config: PermessageDeflateConfig{ClientNoContextTakeover: true},
			want:          "permessage-deflate; client_no_context_takeover",
		},
		{
			name:          "ServerMaxWindowBits",
			premsg_config: PermessageDeflateConfig{ServerMaxWindowBits: 12},
			want:          "permessage-deflate; server_max_window_bits=12",
		},
		{
			name:          "ClientMaxWindowBitsEnabled with ClientMaxWindowBits",
			premsg_config: PermessageDeflateConfig{ClientMaxWindowBitsEnabled: true, ClientMaxWindowBits: 12},
			want:          "permessage-deflate; client_max_window_bits=12",
		},
		{
			// RFC 7692 7.1.2.2.
			name:          "ClientMaxWindowBitsEnabled with ClientMaxWindowBits 0 has no value",
			premsg_config: PermessageDeflateConfig{ClientMaxWindowBitsEnabled: true},
			want:          "permessage-deflate; client_max_window_bits",
		},
		{
			name:          "ClientMaxWindowBits without ClientMaxWindowBitsEnabled is left out",
			premsg_config: PermessageDeflateConfig{ClientMaxWindowBits: 12},
			want:          "permessage-deflate",
		},
		{
			name: "every param",
			premsg_config: PermessageDeflateConfig{
				ClientNoContextTakeover:    true,
				ServerNoContextTakeover:    true,
				ClientMaxWindowBitsEnabled: true,
				ClientMaxWindowBits:        9,
				ServerMaxWindowBits:        11,
			},
			want: "permessage-deflate; server_no_context_takeover; client_no_context_takeover; server_max_window_bits=11; client_max_window_bits=9",
		},
	}
	for _, testCase := range cases {
		if got := testCase.premsg_config.Format(); got != testCase.want {
			t.Errorf("%s: Format = %q, want %q", testCase.name, got, testCase.want)
		}
	}
}

func TestParseExtensions(t *testing.T) {
	cases := []struct {
		name  string
		lines []string // one Sec-WebSocket-Extensions line each
		want  []extension
	}{
		{name: "no header"},
		{
			name:  "a name with no params",
			lines: []string{"permessage-deflate"},
			want:  []extension{{name: "permessage-deflate"}},
		},
		{
			name:  "a param with a value and one without",
			lines: []string{"permessage-deflate; server_max_window_bits=10; client_max_window_bits"},
			want: []extension{{name: "permessage-deflate", params: []extensionParam{
				{name: "server_max_window_bits", value: "10", hasValue: true},
				{name: "client_max_window_bits", value: "", hasValue: false},
			}}},
		},
		{
			name:  "spaces around names and values are trimmed",
			lines: []string{" permessage-deflate ;  server_max_window_bits = 10 "},
			want: []extension{{name: "permessage-deflate", params: []extensionParam{
				{name: "server_max_window_bits", value: "10", hasValue: true},
			}}},
		},
		{
			// RFC 6455 9.1: a param value may be a quoted-string.
			name:  "a quoted value is unquoted and its ; does not split params",
			lines: []string{`permessage-deflate; x_note = "a;b"`},
			want: []extension{{name: "permessage-deflate", params: []extensionParam{
				{name: "x_note", value: "a;b", hasValue: true},
			}}},
		},
		{
			name:  "an empty value after = still has a value",
			lines: []string{"permessage-deflate; server_max_window_bits="},
			want: []extension{{name: "permessage-deflate", params: []extensionParam{
				{name: "server_max_window_bits", value: "", hasValue: true},
			}}},
		},
		{
			name:  "only the first = splits a param",
			lines: []string{"x-ext; a=b=c"},
			want: []extension{{name: "x-ext", params: []extensionParam{
				{name: "a", value: "b=c", hasValue: true},
			}}},
		},
		{
			name:  "several entries on one line, in order",
			lines: []string{"x-ext, permessage-deflate; client_no_context_takeover"},
			want: []extension{
				{name: "x-ext"},
				{name: "permessage-deflate", params: []extensionParam{
					{name: "client_no_context_takeover", value: "", hasValue: false},
				}},
			},
		},
		{
			// RFC 7230 3.2.2: a list field may be split across lines.
			name:  "entries on several lines, in order",
			lines: []string{"x-ext", "permessage-deflate; client_no_context_takeover"},
			want: []extension{
				{name: "x-ext"},
				{name: "permessage-deflate", params: []extensionParam{
					{name: "client_no_context_takeover", value: "", hasValue: false},
				}},
			},
		},
		{
			name:  "an entry with params but no name keeps the name empty",
			lines: []string{"; server_max_window_bits=10"},
			want: []extension{{name: "", params: []extensionParam{
				{name: "server_max_window_bits", value: "10", hasValue: true},
			}}},
		},
		{
			name:  "a trailing ; adds a param with no name",
			lines: []string{"permessage-deflate;"},
			want: []extension{{name: "permessage-deflate", params: []extensionParam{
				{name: "", value: "", hasValue: false},
			}}},
		},
	}
	for _, testCase := range cases {
		header := http.Header{}
		for _, line := range testCase.lines {
			header.Add("Sec-WebSocket-Extensions", line)
		}
		if got := parseExtensions(header); !reflect.DeepEqual(got, testCase.want) {
			t.Errorf("%s: parseExtensions = %+v, want %+v", testCase.name, got, testCase.want)
		}
	}
}

func TestUnquote(t *testing.T) {
	cases := []struct {
		name  string
		input string
		want  string
	}{
		{name: "a token is returned as it is", input: "10", want: "10"},
		{name: "an empty string is returned as it is", input: "", want: ""},
		{name: "a lone quote is returned as it is", input: `"`, want: `"`},
		{name: "no closing quote is returned as it is", input: `"10`, want: `"10`},
		{name: "no opening quote is returned as it is", input: `10"`, want: `10"`},
		{
			// RFC 7230 3.2.6.
			name:  "the quotes are removed",
			input: `"10"`,
			want:  "10",
		},
		{name: "an empty quoted-string", input: `""`, want: ""},
		{
			// RFC 7230 3.2.6: a quoted-pair is the byte after the backslash.
			name:  "an escaped quote",
			input: `"a\"b"`,
			want:  `a"b`,
		},
		{
			// RFC 7230 3.2.6.
			name:  "an escaped backslash",
			input: `"a\\b"`,
			want:  `a\b`,
		},
		{
			name:  "a backslash just before the closing quote is kept",
			input: `"a\"`,
			want:  `a\`,
		},
	}
	for _, testCase := range cases {
		if got := unquote(testCase.input); got != testCase.want {
			t.Errorf("%s: unquote(%q) = %q, want %q", testCase.name, testCase.input, got, testCase.want)
		}
	}
}

func TestParseDeflateParams(t *testing.T) {
	passes := []struct {
		name   string
		params []extensionParam
		want   PermessageDeflateConfig
	}{
		{name: "no params", params: nil, want: PermessageDeflateConfig{}},
		{
			name:   "client_no_context_takeover",
			params: []extensionParam{{name: "client_no_context_takeover"}},
			want:   PermessageDeflateConfig{ClientNoContextTakeover: true},
		},
		{
			name:   "server_no_context_takeover",
			params: []extensionParam{{name: "server_no_context_takeover"}},
			want:   PermessageDeflateConfig{ServerNoContextTakeover: true},
		},
		{
			// RFC 7692 7.1.2.2: it may come without a value.
			name:   "client_max_window_bits with no value",
			params: []extensionParam{{name: "client_max_window_bits"}},
			want:   PermessageDeflateConfig{ClientMaxWindowBitsEnabled: true},
		},
		{
			name:   "client_max_window_bits with a value",
			params: []extensionParam{{name: "client_max_window_bits", value: "10", hasValue: true}},
			want:   PermessageDeflateConfig{ClientMaxWindowBitsEnabled: true, ClientMaxWindowBits: 10},
		},
		{
			name:   "server_max_window_bits",
			params: []extensionParam{{name: "server_max_window_bits", value: "12", hasValue: true}},
			want:   PermessageDeflateConfig{ServerMaxWindowBits: 12},
		},
		{
			name: "a param after client_max_window_bits with no value is still read",
			params: []extensionParam{
				{name: "client_max_window_bits"},
				{name: "server_max_window_bits", value: "10", hasValue: true},
			},
			want: PermessageDeflateConfig{ClientMaxWindowBitsEnabled: true, ServerMaxWindowBits: 10},
		},
		{
			name: "every param",
			params: []extensionParam{
				{name: "client_no_context_takeover"},
				{name: "server_no_context_takeover"},
				{name: "client_max_window_bits", value: "9", hasValue: true},
				{name: "server_max_window_bits", value: "11", hasValue: true},
			},
			want: PermessageDeflateConfig{
				ClientNoContextTakeover:    true,
				ServerNoContextTakeover:    true,
				ClientMaxWindowBitsEnabled: true,
				ClientMaxWindowBits:        9,
				ServerMaxWindowBits:        11,
			},
		},
	}
	for _, testCase := range passes {
		got, err := parseDeflateParams(testCase.params)
		if err != nil {
			t.Errorf("%s: err = %v, want nil", testCase.name, err)
			continue
		}
		if got != testCase.want {
			t.Errorf("%s: parseDeflateParams = %+v, want %+v", testCase.name, got, testCase.want)
		}
	}

	// RFC 7692 7: each of these makes the entry invalid.
	fails := []struct {
		name   string
		params []extensionParam
	}{
		{
			name:   "a duplicate param",
			params: []extensionParam{{name: "server_no_context_takeover"}, {name: "server_no_context_takeover"}},
		},
		{
			// RFC 7692 7.1.1.2: it has no value.
			name:   "a value on client_no_context_takeover",
			params: []extensionParam{{name: "client_no_context_takeover", value: "1", hasValue: true}},
		},
		{
			// RFC 7692 7.1.1.1: it has no value.
			name:   "a value on server_no_context_takeover",
			params: []extensionParam{{name: "server_no_context_takeover", value: "1", hasValue: true}},
		},
		{
			// RFC 7692 7.1.2.2: a value must be 8 to 15.
			name:   "an invalid client_max_window_bits",
			params: []extensionParam{{name: "client_max_window_bits", value: "16", hasValue: true}},
		},
		{
			// RFC 7692 7.1.2.1: it always has a value.
			name:   "server_max_window_bits with no value",
			params: []extensionParam{{name: "server_max_window_bits"}},
		},
		{
			// RFC 7692 7.1.2.1: a value must be 8 to 15.
			name:   "an invalid server_max_window_bits",
			params: []extensionParam{{name: "server_max_window_bits", value: "7", hasValue: true}},
		},
		{
			name:   "an unknown param",
			params: []extensionParam{{name: "x_unknown"}},
		},
		{
			name:   "a param with no name",
			params: []extensionParam{{name: ""}},
		},
	}
	for _, testCase := range fails {
		if _, err := parseDeflateParams(testCase.params); err == nil {
			t.Errorf("%s: err = nil, want an error", testCase.name)
		}
	}
}

func TestParseWindowBits(t *testing.T) {
	// RFC 7692 7.1.2.1: a decimal 8 to 15 with no leading zero.
	for bits := uint(8); bits <= 15; bits++ {
		value := strconv.FormatUint(uint64(bits), 10)
		got, ok := parseWindowBits(extensionParam{name: "server_max_window_bits", value: value, hasValue: true})
		if !ok || got != bits {
			t.Errorf("%s: parseWindowBits = (%d, %t), want (%d, true)", value, got, ok, bits)
		}
	}

	fails := []struct {
		name  string
		param extensionParam
	}{
		{
			name:  "no value",
			param: extensionParam{name: "server_max_window_bits", value: "10", hasValue: false},
		},
		{
			name:  "an empty value",
			param: extensionParam{name: "server_max_window_bits", value: "", hasValue: true},
		},
		{
			name:  "7",
			param: extensionParam{name: "server_max_window_bits", value: "7", hasValue: true},
		},
		{
			name:  "16",
			param: extensionParam{name: "server_max_window_bits", value: "16", hasValue: true},
		},
		{
			name:  "a leading zero",
			param: extensionParam{name: "server_max_window_bits", value: "010", hasValue: true},
		},
	}
	for _, testCase := range fails {
		if got, ok := parseWindowBits(testCase.param); ok || got != 0 {
			t.Errorf("%s: parseWindowBits = (%d, %t), want (0, false)", testCase.name, got, ok)
		}
	}
}
