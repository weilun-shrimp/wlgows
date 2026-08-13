package wlgows

import (
	"net/http"
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
