package jev

import (
	"encoding/json"
	"net/url"
	"sort"
	"strconv"
	"strings"
)

// redactor masks configured secrets in text that leaves the SDK: error
// messages, log records and error bodies. It is the single redaction point, so
// no call site can leak a credential on its own.
type redactor struct {
	// variants holds every spelling a secret may take, longest first, so a
	// longer match is replaced before a shorter substring of it.
	variants []string
}

// newRedactor masks every supplied secret. There is no length floor: the caller
// passes values that are known to be credentials, so a short key is exactly the
// case that must not fail open.
func newRedactor(secrets ...string) *redactor {
	seen := map[string]bool{}
	var variants []string
	add := func(variant string) {
		if variant != "" && !seen[variant] {
			seen[variant] = true
			variants = append(variants, variant)
		}
	}
	for _, secret := range secrets {
		secret = strings.TrimSpace(secret)
		if secret == "" {
			continue
		}
		// An error may carry the credential raw, behind the scheme, Go-quoted,
		// JSON-escaped, or percent-encoded from a URL that held it as userinfo.
		add(secret)
		add("Bearer " + secret)
		add(strings.Trim(strconv.Quote(secret), `"`))
		if encoded, err := json.Marshal(secret); err == nil {
			add(strings.Trim(string(encoded), `"`))
		}
		add(url.QueryEscape(secret))
		add(url.PathEscape(secret))
	}
	// Longest first: "Bearer sk-123" must win over "sk-123".
	sort.SliceStable(variants, func(i, j int) bool { return len(variants[i]) > len(variants[j]) })
	return &redactor{variants: variants}
}

// string returns text with every known secret replaced by "***".
func (r *redactor) string(text string) string {
	if r == nil || len(r.variants) == 0 || text == "" {
		return text
	}
	for _, variant := range r.variants {
		text = strings.ReplaceAll(text, variant, "***")
	}
	return text
}

// bytes returns body with every known secret replaced by "***".
func (r *redactor) bytes(body []byte) []byte {
	if r == nil || len(r.variants) == 0 || len(body) == 0 {
		return body
	}
	redacted := r.string(string(body))
	return []byte(redacted)
}
