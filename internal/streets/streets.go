// Package streets holds the street list from the address dataset and splits a
// description/address span at the longest known street (ADR 0001, 2.1).
package streets

import (
	"encoding/json"
	"fmt"
	"os"
	"regexp"
	"strings"

	"github.com/RIKIKU/PagerArchive/schemas"
)

// Entry is one street in one locality, written as the dataset writes it.
type Entry struct {
	Street   string `json:"street"`
	Locality string `json:"locality"`
}

// Dataset is an address dataset or a street fixture.
type Dataset struct {
	Attribution string  `json:"attribution"`
	Streets     []Entry `json:"streets"`
}

// LoadFile reads and validates a dataset or street fixture.
func LoadFile(path string) (*Dataset, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	if probs := schemas.Validate(schemas.AddressDataset, data); len(probs) > 0 {
		return nil, fmt.Errorf("%s: %s", path, probs[0])
	}
	var d Dataset
	if err := json.Unmarshal(data, &d); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return &d, nil
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
		loc := strings.Join(strings.Fields(strings.ToUpper(e.Locality)), " ")
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
		n := len(toks) - len(loc)
		if n < 1 || !wordsEqual(toks[n:], loc) {
			continue
		}
		street := toks[:n]
		if houseNumber.MatchString(strings.ToUpper(street[0].text)) {
			street = street[1:]
		}
		if len(street) == 0 {
			continue
		}
		words := make([]string, len(street))
		for i, t := range street {
			words[i] = t.text
		}
		if ix.streets[strings.Join(loc, " ")][ix.normalise(words)] {
			return true
		}
	}
	return false
}

func wordsEqual(toks []token, words []string) bool {
	for i, w := range words {
		if strings.ToUpper(toks[i].text) != w {
			return false
		}
	}
	return true
}
