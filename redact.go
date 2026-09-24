package jev

import "strings"

// redactor masks configured secrets in text that leaves the SDK: error
// messages and log records. It is the single redaction point, so no call site
// can leak a credential on its own.
type redactor struct {
	// variants holds every spelling a secret may take, longest first, so a
	// longer match is replaced before a shorter substring of it.
	variants []string
}

func newRedactor(secrets ...string) *redactor {
	seen := map[string]bool{}
	variants := make([]string, 0, len(secrets)*2)
	for _, secret := range secrets {
		secret = strings.TrimSpace(secret)
		// Below 8 characters a secret is more likely to collide with ordinary
		// text than to be a real credential.
		if len(secret) < 8 {
			continue
		}
		for _, variant := range []string{secret, "Bearer " + secret} {
			if !seen[variant] {
				seen[variant] = true
				variants = append(variants, variant)
			}
		}
	}
	// Longest first: "Bearer sk-123" must win over "sk-123".
	for i := 1; i < len(variants); i++ {
		for j := i; j > 0 && len(variants[j]) > len(variants[j-1]); j-- {
			variants[j], variants[j-1] = variants[j-1], variants[j]
		}
	}
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
