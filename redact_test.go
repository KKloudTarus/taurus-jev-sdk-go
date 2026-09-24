package jev

import "testing"

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

func TestRedactorIgnoresShortSecrets(t *testing.T) {
	// A short value collides with ordinary words more often than it protects.
	r := newRedactor("abc")
	if got := r.string("abc appears in alphabet"); got != "abc appears in alphabet" {
		t.Errorf("short secret was redacted: %q", got)
	}
}

func TestNilRedactorIsSafe(t *testing.T) {
	var r *redactor
	if got := r.string("unchanged"); got != "unchanged" {
		t.Errorf("got %q", got)
	}
}
