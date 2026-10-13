// Package streets holds the street list from the address dataset and splits a
// description/address span at the longest known street (ADR 0001, 2.1).
package streets

import (
	"regexp"
	"strings"
)

// Entry is one street in one locality, written as the dataset writes it.
type Entry struct {
	Street   string `json:"street"`
	Locality string `json:"locality"`
}

// Index answers "is this street in this locality?" with street types
// normalised (e.g. STREET = ST).
type Index struct {
	types      map[string]string
	streets    map[string]map[string]bool // locality -> normalised street -> true
	localities [][]string                 // each locality as tokens
}

// NewIndex builds an index. streetTypes maps a spelling to its abbreviation.
func NewIndex(entries []Entry, streetTypes map[string]string) *Index {
	ix := &Index{types: map[string]string{}, streets: map[string]map[string]bool{}}
	for k, v := range streetTypes {
		ix.types[strings.ToUpper(k)] = strings.ToUpper(v)
	}
	for _, e := range entries {
		loc := NormaliseLocality(e.Locality)
		if ix.streets[loc] == nil {
			ix.streets[loc] = map[string]bool{}
			ix.localities = append(ix.localities, strings.Fields(loc))
		}
		ix.streets[loc][ix.normalise(strings.Fields(e.Street))] = true
	}
	return ix
}

// normalise upper-cases a street and maps its last word (the street type).
func (ix *Index) normalise(words []string) string {
	out := make([]string, len(words))
	for i, w := range words {
		out[i] = strings.ToUpper(w)
	}
	if n := len(out); n > 0 {
		if abbr, ok := ix.types[out[n-1]]; ok {
			out[n-1] = abbr
		}
	}
	return strings.Join(out, " ")
}

var houseNumber = regexp.MustCompile(`^[0-9]+[A-Z]?(?:[-/][0-9]+[A-Z]?)*$`)

type token struct {
	text       string
	start, end int
}

func tokenise(s string) []token {
	var toks []token
	start := -1
	for i, r := range s {
		if r == ' ' {
			if start >= 0 {
				toks = append(toks, token{s[start:i], start, i})
				start = -1
			}
		} else if start < 0 {
			start = i
		}
	}
	if start >= 0 {
		toks = append(toks, token{s[start:], start, len(s)})
	}
	return toks
}

// Split finds the longest suffix of span that reads
// [house number] <street> <locality>, where the street is listed in that
// locality. It returns the literal text before and from that suffix, both
// trimmed. ok is false if no suffix matches.
func (ix *Index) Split(span string) (description, address string, ok bool) {
	toks := tokenise(span)
	for i := range toks {
		if ix.isAddress(toks[i:]) {
			return strings.TrimSpace(span[:toks[i].start]), span[toks[i].start:toks[len(toks)-1].end], true
		}
	}
	return strings.TrimSpace(span), "", false
}

func (ix *Index) isAddress(toks []token) bool {
	for _, loc := range ix.localities {
		if _, ok := ix.matchAt(toks, loc); ok {
			return true
		}
	}
	return false
}

// matchAt checks whether toks read [house number] <street> <loc>, with the
// street listed in that locality.
func (ix *Index) matchAt(toks []token, loc []string) (Place, bool) {
	n := len(toks) - len(loc)
	if n < 1 || !wordsEqual(toks[n:], loc) {
		return Place{}, false
	}
	return ix.matchStreet(toks[:n], strings.Join(loc, " "))
}

// matchStreet checks whether toks read [house number] <street>, with the
// street listed in locality (normalised).
func (ix *Index) matchStreet(toks []token, locality string) (Place, bool) {
	p := Place{Locality: locality}
	if len(toks) > 0 && houseNumber.MatchString(strings.ToUpper(toks[0].text)) {
		p.Number = strings.ToUpper(toks[0].text)
		toks = toks[1:]
	}
	if len(toks) == 0 {
		return Place{}, false
	}
	words := make([]string, len(toks))
	for i, t := range toks {
		words[i] = t.text
	}
	p.Street = ix.normalise(words)
	if !ix.streets[locality][p.Street] {
		return Place{}, false
	}
	return p, true
}

// Place is an address as the geocoder looks it up: street and locality
// normalised as the index stores them, and the house number (upper case)
// or "" (R24).
type Place struct {
	Number, Street, Locality string
}

// ParseAddress reads an address field as [house number] <street>
// [<locality>]. When locality (a separate field) is given, the street must
// be listed there; otherwise the address must end with a known locality
// (R24).
func (ix *Index) ParseAddress(address, locality string) (Place, bool) {
	toks := tokenise(address)
	if loc := NormaliseLocality(locality); loc != "" {
		if p, ok := ix.matchAt(toks, strings.Fields(loc)); ok {
			return p, true
		}
		return ix.matchStreet(toks, loc)
	}
	for _, loc := range ix.localities {
		if p, ok := ix.matchAt(toks, loc); ok {
			return p, true
		}
	}
	return Place{}, false
}

// LocalityAtEnd finds the longest known locality that ends text, for
// placing a grid reference when the address couldn't be split (R25).
func (ix *Index) LocalityAtEnd(text string) (string, bool) {
	toks := tokenise(text)
	best := ""
	for _, loc := range ix.localities {
		if n := len(toks) - len(loc); n >= 0 && wordsEqual(toks[n:], loc) && len(loc) > len(strings.Fields(best)) {
			best = strings.Join(loc, " ")
		}
	}
	return best, best != ""
}

// NormaliseStreet is a street as the index stores it: upper case, single
// spaces, street type abbreviated.
func (ix *Index) NormaliseStreet(street string) string { return ix.normalise(strings.Fields(street)) }

// NormaliseLocality is a locality as the index stores it.
func NormaliseLocality(locality string) string {
	return strings.Join(strings.Fields(strings.ToUpper(locality)), " ")
}

// NumberCandidates are the house numbers to try for an address, most
// specific first: as written, then the street number of a unit (2/12 →
// 12), the first of a range (12-14 → 12), and without a letter suffix
// (12A → 12). The geocoder falls back to the street when none is found
// (ADR 0005).
func NumberCandidates(number string) []string {
	out := []string{}
	add := func(s string) {
		for _, o := range out {
			if o == s {
				return
			}
		}
		if s != "" {
			out = append(out, s)
		}
	}
	n := strings.ToUpper(number)
	add(n)
	if i := strings.LastIndex(n, "/"); i >= 0 {
		n = n[i+1:]
		add(n)
	}
	if i := strings.Index(n, "-"); i >= 0 {
		n = n[:i]
		add(n)
	}
	add(strings.TrimRight(n, "ABCDEFGHIJKLMNOPQRSTUVWXYZ"))
	return out
}

func wordsEqual(toks []token, words []string) bool {
	for i, w := range words {
		if strings.ToUpper(toks[i].text) != w {
			return false
		}
	}
	return true
}
