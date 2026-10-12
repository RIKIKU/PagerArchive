package accounts

import (
	"net/mail"
	"strings"
)

// Email is an address as entered and its comparison key.
type Email struct {
	Address string // as entered (trimmed), used for sending
	Key     string // lower case, without a +tag: one account per key (owner, 2026-10-13)
	Domain  string // lower case
}

// ParseEmail checks a bare address (no display name) and normalises it.
func ParseEmail(raw string) (Email, bool) {
	raw = strings.TrimSpace(raw)
	a, err := mail.ParseAddress(raw)
	if err != nil || a.Name != "" || a.Address != raw || len(raw) > 254 {
		return Email{}, false
	}
	local, domain, ok := strings.Cut(strings.ToLower(raw), "@")
	if !ok || local == "" || domain == "" || strings.Contains(domain, "@") || !strings.Contains(domain, ".") {
		return Email{}, false
	}
	if base, _, tagged := strings.Cut(local, "+"); tagged && base != "" {
		local = base
	}
	return Email{Address: raw, Key: local + "@" + domain, Domain: domain}, true
}

// ParseDomains reads ALLOWED_EMAIL_DOMAINS: comma- or space-separated
// domains, matched exactly and case-insensitively (R41).
func ParseDomains(s string) []string {
	var out []string
	for _, d := range strings.FieldsFunc(s, func(r rune) bool { return r == ',' || r == ' ' }) {
		out = append(out, strings.ToLower(strings.TrimPrefix(d, "@")))
	}
	return out
}
