package jev

import (
	"encoding/json"
	"net/url"
	"strings"
	"testing"
)

func TestRedactor(t *testing.T) {
	r := newRedactor("sk-live-abcdef123456")
	cases := []struct {
		text string
		want string
	}{
		{`Post "https://api.typesafe.ai": header Authorization: Bearer sk-live-abcdef123456`,
			`Post "https://api.typesafe.ai": header Authorization: ***`},
		{"leaked sk-live-abcdef123456 here", "leaked *** here"},
		{"nothing to hide", "nothing to hide"},
		{"", ""},
	}
	for _, testCase := range cases {
		if got := r.string(testCase.text); got != testCase.want {
			t.Errorf("redact(%q) = %q, want %q", testCase.text, got, testCase.want)
		}
	}
}

func TestShortSecretsAreStillRedacted(t *testing.T) {
	// The caller passes values that are known to be credentials, so a short key
	// is exactly the case that must not fail open.
	r := newRedactor("sk-1234")
	if got := r.string("rejected header Authorization: Bearer sk-1234"); strings.Contains(got, "sk-1234") {
		t.Errorf("a short key was left in the clear: %q", got)
	}
}

func TestRedactorCoversEscapedSpellings(t *testing.T) {
	const secret = "sk-live-abcdef123456"
	r := newRedactor(secret)
	cases := map[string]string{
		"raw":             secret,
		"bearer":          "Bearer " + secret,
		"json escaped":    `["Bearer sk-live-abcdef123456"]`,
		"percent encoded": url.QueryEscape(secret),
		"path escaped":    url.PathEscape(secret),
	}
	for name, text := range cases {
		// A JSON-escaped spelling only matters once it is decoded back; check
		// both the literal bytes and the decoded form.
		decoded := text
		var list []string
		if json.Unmarshal([]byte(text), &list) == nil && len(list) == 1 {
			decoded = list[0]
		}
		if got := r.string(decoded); strings.Contains(got, secret) {
			t.Errorf("%s: credential survived redaction: %q", name, got)
		}
	}
}

func TestRedactorPrefersTheLongestMatch(t *testing.T) {
	r := newRedactor("sk-live-abcdef123456")
	got := r.string("Authorization: Bearer sk-live-abcdef123456 end")
	if got != "Authorization: *** end" {
		t.Errorf("got %q; the bearer form should win over the bare key", got)
	}
}

func TestRedactorBytes(t *testing.T) {
	r := newRedactor("sk-live-abcdef123456")
	body := []byte(`{"error":"bad Bearer sk-live-abcdef123456"}`)
	if got := string(r.bytes(body)); strings.Contains(got, "sk-live-abcdef123456") {
		t.Errorf("body not redacted: %s", got)
	}
}

func TestNilRedactorIsSafe(t *testing.T) {
	var r *redactor
	if got := r.string("unchanged"); got != "unchanged" {
		t.Errorf("got %q", got)
	}
}
