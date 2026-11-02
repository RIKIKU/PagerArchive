package search

import "strings"

// Terms is a text search split into terms (R33a, owner 2026-11-02). A page
// matches if it holds every term in All, or any one term in Any (the or:
// terms, each an alternative to All together). Each term is matched as a
// case-insensitive substring of the text or an extra field (section 14).
type Terms struct {
	All []string
	Any []string
}

// MaxTerms caps how many terms one search may have.
const MaxTerms = 50

// ParseText splits a text search into terms. Text in double quotes is one
// term, as typed; a quote left open runs to the end. Each other word is a
// term of its own, colons and all (ADDR:, 14:05). A term written or: (a
// word, or:fire, or a phrase, or:"grass fire") goes in Any; or: with nothing
// after it is no term. "or:" in quotes is the text itself.
func ParseText(s string) Terms {
	var t Terms
	for i := 0; i < len(s); {
		if isSpace(s[i]) {
			i++
			continue
		}
		or := false
		if len(s)-i >= 3 && strings.EqualFold(s[i:i+3], "or:") {
			or = true
			i += 3
		}
		var term string
		if i < len(s) && s[i] == '"' {
			end := strings.IndexByte(s[i+1:], '"')
			if end < 0 {
				term, i = s[i+1:], len(s)
			} else {
				term, i = s[i+1:i+1+end], i+end+2
			}
		} else {
			j := i
			for j < len(s) && !isSpace(s[j]) {
				j++
			}
			term, i = s[i:j], j
		}
		if term = strings.TrimSpace(term); term == "" {
			continue
		}
		if or {
			t.Any = append(t.Any, term)
		} else {
			t.All = append(t.All, term)
		}
	}
	return t
}

func isSpace(c byte) bool {
	return c == ' ' || c == '\t' || c == '\n' || c == '\r'
}
