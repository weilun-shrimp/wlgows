package wlgows

import (
	"net/http"
	"reflect"
	"testing"
)

func TestHeaderHasToken(t *testing.T) {
	tests := []struct {
		name   string
		values []string // one entry per line the field arrived on
		token  string
		want   bool
	}{
		{
			name:   "the token alone",
			values: []string{"Upgrade"},
			token:  "upgrade",
			want:   true,
		},
		{
			name:   "case insensitive both ways",
			values: []string{"UPGRADE"},
			token:  "upgrade",
			want:   true,
		},
		// What a client behind a proxy sends, and the case this helper exists for.
		{
			name:   "last of a list",
			values: []string{"keep-alive, Upgrade"},
			token:  "upgrade",
			want:   true,
		},
		{
			name:   "first of a list",
			values: []string{"Upgrade, keep-alive"},
			token:  "upgrade",
			want:   true,
		},
		{
			name:   "no space after the comma",
			values: []string{"Keep-Alive,upgrade"},
			token:  "upgrade",
			want:   true,
		},
		{
			name:   "padded with spaces and tabs",
			values: []string{"keep-alive, \t Upgrade \t "},
			token:  "upgrade",
			want:   true,
		},
		// Get would return the first line and miss this one.
		{
			name:   "on the second of two lines",
			values: []string{"keep-alive", "Upgrade"},
			token:  "upgrade",
			want:   true,
		},
		{
			name:   "absent field",
			values: nil,
			token:  "upgrade",
			want:   false,
		},
		{
			name:   "empty field",
			values: []string{""},
			token:  "upgrade",
			want:   false,
		},
		{
			name:   "a different token",
			values: []string{"close"},
			token:  "upgrade",
			want:   false,
		},
		// The substring trap: both contain "upgrade" and neither one is it.
		{
			name:   "token is a prefix of the value",
			values: []string{"upgraded"},
			token:  "upgrade",
			want:   false,
		},
		{
			name:   "token is a suffix of the value",
			values: []string{"no-upgrade"},
			token:  "upgrade",
			want:   false,
		},
		{
			name:   "the trap inside a list",
			values: []string{"keep-alive, no-upgrade"},
			token:  "upgrade",
			want:   false,
		},
	}

	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			header := http.Header{}
			for _, value := range testCase.values {
				header.Add("Connection", value)
			}
			if got := headerHasToken(header, "Connection", testCase.token); got != testCase.want {
				t.Errorf("headerHasToken(%q, %q) = %v, want %v",
					testCase.values, testCase.token, got, testCase.want)
			}
		})
	}

	// Header.Values canonicalises the key the same way Get does, so a caller
	// does not have to spell it the way it arrived.
	t.Run("key is canonicalised", func(t *testing.T) {
		header := http.Header{}
		header.Set("Connection", "Upgrade")
		if !headerHasToken(header, "connection", "upgrade") {
			t.Error("headerHasToken with a lowercased key = false, want true")
		}
	})
}

func TestHeaderValues(t *testing.T) {
	cases := []struct {
		name  string
		lines []string // one entry per line the field arrived on
		want  []string
	}{
		{name: "absent field"},
		{
			name:  "one value",
			lines: []string{"chat"},
			want:  []string{"chat"},
		},
		{
			name:  "a list on one line",
			lines: []string{"chat, superchat"},
			want:  []string{"chat", "superchat"},
		},
		{
			name:  "a list across two lines keeps the order",
			lines: []string{"chat, superchat", "mine"},
			want:  []string{"chat", "superchat", "mine"},
		},
		{
			name:  "padded with spaces and tabs",
			lines: []string{" \tchat \t,superchat "},
			want:  []string{"chat", "superchat"},
		},
		{
			name:  "empty values are skipped",
			lines: []string{", chat, , superchat,", ""},
			want:  []string{"chat", "superchat"},
		},
		{
			name:  "a comma inside quotes does not split",
			lines: []string{`x-ext; note="a,b", chat`},
			want:  []string{`x-ext; note="a,b"`, "chat"},
		},
	}
	for _, testCase := range cases {
		header := http.Header{}
		for _, line := range testCase.lines {
			header.Add("Sec-WebSocket-Protocol", line)
		}
		if got := headerValues(header, "Sec-WebSocket-Protocol"); !reflect.DeepEqual(got, testCase.want) {
			t.Errorf("%s: headerValues = %q, want %q", testCase.name, got, testCase.want)
		}
	}
}

func TestSplitOutsideQuotes(t *testing.T) {
	cases := []struct {
		name      string
		input     string
		separator byte
		want      []string
	}{
		{
			name:      "splits at each separator",
			input:     "a,b,c",
			separator: ',',
			want:      []string{"a", "b", "c"},
		},
		{
			name:      "splits at another separator",
			input:     "a;b",
			separator: ';',
			want:      []string{"a", "b"},
		},
		{
			name:      "no separator",
			input:     "abc",
			separator: ',',
			want:      []string{"abc"},
		},
		{
			name:      "an empty string is one empty part",
			input:     "",
			separator: ',',
			want:      []string{""},
		},
		{
			name:      "separators at the ends keep empty parts",
			input:     ",a,",
			separator: ',',
			want:      []string{"", "a", ""},
		},
		{
			// RFC 7230 3.2.6.
			name:      "a separator inside quotes does not split",
			input:     `a="x,y",b`,
			separator: ',',
			want:      []string{`a="x,y"`, "b"},
		},
		{
			// RFC 7230 3.2.6: a quoted-pair escapes the next byte.
			name:      "an escaped quote does not close the quotes",
			input:     `a="x\",y",b`,
			separator: ',',
			want:      []string{`a="x\",y"`, "b"},
		},
		{
			name:      "a backslash outside quotes is not an escape",
			input:     `a\,b`,
			separator: ',',
			want:      []string{`a\`, "b"},
		},
		{
			name:      "an unclosed quote keeps the rest as one part",
			input:     `a="x,y`,
			separator: ',',
			want:      []string{`a="x,y`},
		},
		{
			name:      "a backslash at the end inside quotes",
			input:     `a="x\`,
			separator: ',',
			want:      []string{`a="x\`},
		},
	}
	for _, testCase := range cases {
		if got := splitOutsideQuotes(testCase.input, testCase.separator); !reflect.DeepEqual(got, testCase.want) {
			t.Errorf("%s: splitOutsideQuotes = %q, want %q", testCase.name, got, testCase.want)
		}
	}
}
