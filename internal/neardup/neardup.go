// Package neardup decides whether two copies of a page to one capcode,
// received within R19's 10 seconds, are the same transmission though their
// text differs a little (a near duplicate, R19a), and which copy a message
// keeps (ADR 0050). It is pure: no I/O, no logging. Callers must never log
// the texts it compares (P1).
//
// Two receivers hear the same POCSAG transmission through different radio
// paths, and they get it wrong in three ways:
//
//   - substitution: a codeword with more bit errors than BCH(31,21) can put
//     right decodes to the wrong characters. A codeword carries 20 bits, so
//     under three 7-bit characters: one bad codeword garbles a run of up to 3
//     characters, and the page keeps its length (a character garbled into a
//     control character is removed with the control tokens, R17, so one may
//     also go missing);
//   - truncation: the receiver loses the signal and the page ends early;
//   - a junk tail: the decoder reads past the end and adds a few characters,
//     often control or non-ASCII ones.
//
// So a copy is compared against the start of the other (edit distance with
// the longer copy's end free): substitutions and the odd missing character
// cost an edit each, while a truncated copy matches the start of the whole
// one at no cost. Trailing junk (whitespace, control and non-ASCII
// characters) is stripped first.
package neardup

import (
	"math"
	"unicode"
)

// DefaultRatio is how many edits a copy may differ by, per character of the
// shorter copy: 1 in 10, so a page of 30 characters may have one bad
// codeword (3 characters), and one of 100 characters three or so. A page of
// fewer than 10 characters may differ by none: it must match the other's
// start exactly (a truncated copy, or one with a junk tail).
const DefaultRatio = 0.1

// MaxRatio is the most a deployment may set (DEDUP_NEAR_RATIO): beyond it,
// different pages to one capcode would be merged.
const MaxRatio = 0.5

// JunkTailRunes is the most the longer copy may run past the shorter one's
// end and still be the same length for the rules here: under one
// codeword's worth of characters, which is what a decoder adds when it reads
// one codeword past the end. Longer than that, the shorter copy is a
// truncated one.
const JunkTailRunes = 3

// Shape is how a near duplicate differs from the copy it matched (the
// dedup-report's classes, R19b).
type Shape string

const (
	// Substitution: about the same length, with characters changed.
	Substitution Shape = "substitution"
	// Truncation: the shorter copy is the longer one's start, exactly.
	Truncation Shape = "truncation"
	// JunkTail: the copies differ only after the shorter one's end, by no
	// more than JunkTailRunes characters, or only in trailing junk.
	JunkTail Shape = "junk_tail"
	// Other: truncated and garbled.
	Other Shape = "other"
)

// Shapes are every shape, in the order a report lists them.
var Shapes = []Shape{Substitution, Truncation, JunkTail, Other}

// Match is how two copies compare.
type Match struct {
	// Near: the same transmission (R19a). The copies' texts differ, or they
	// would be an exact duplicate (R19).
	Near bool
	// Distance is the edits between the shorter copy (cleaned) and the start
	// of the longer one, up to Allowed+1: more than Allowed is "not near".
	Distance int
	// Allowed is the most edits the shorter copy's length allows.
	Allowed int
	// Shorter is the shorter copy's length in characters, after cleaning.
	Shorter int
	// Extra is how many characters the longer copy runs past the shorter.
	Extra int
	// Shape is how they differ; meaningful when Near.
	Shape Shape
	// Truncated: the shorter copy looks like a truncated copy of the longer
	// (it matches the longer one's start, and the longer runs on by more than
	// a junk tail), so the longer one is kept when neither parses better
	// (ADR 0050).
	Truncated bool
	// ALonger: a is the longer copy after cleaning.
	ALonger bool
}

// Clean is the text compared: the page without its trailing junk
// (whitespace, control characters and anything outside printable ASCII).
// The stored text keeps them (R22); only the comparison ignores them.
func Clean(s string) []rune {
	r := []rune(s)
	end := len(r)
	for end > 0 && junk(r[end-1]) {
		end--
	}
	return r[:end]
}

func junk(r rune) bool {
	return r < 0x21 || r > 0x7e || unicode.IsSpace(r) || unicode.IsControl(r)
}

// Allowed is the most edits a copy of n characters (cleaned) may differ by
// at ratio: floor(ratio × n).
func Allowed(n int, ratio float64) int {
	if ratio <= 0 || n <= 0 {
		return 0
	}
	return int(math.Floor(ratio*float64(n) + 1e-9))
}

// Compare compares two copies' texts (as stored: control tokens removed and
// redacted, P1) at ratio. Equal texts are an exact duplicate, which is R19's
// to merge: Compare says they're near, with distance 0.
func Compare(a, b string, ratio float64) Match {
	ca, cb := Clean(a), Clean(b)
	short, long := ca, cb
	m := Match{}
	if len(ca) > len(cb) {
		short, long = cb, ca
		m.ALonger = true
	}
	m.Shorter = len(short)
	m.Extra = len(long) - len(short)
	m.Allowed = Allowed(len(short), ratio)
	if ratio < 0 {
		// Near duplicates are off: only an exact copy matches.
		m.Distance = 1
		if a == b {
			m.Distance = 0
		}
		m.Near = a == b
		return m
	}
	m.Distance = PrefixDistance(short, long, m.Allowed)
	m.Near = m.Distance <= m.Allowed
	switch {
	case string(ca) == string(cb), m.Distance == 0 && m.Extra <= JunkTailRunes:
		m.Shape = JunkTail
	case m.Distance == 0:
		m.Shape = Truncation
	case m.Extra <= JunkTailRunes:
		m.Shape = Substitution
	default:
		m.Shape = Other
	}
	m.Truncated = m.Near && m.Extra > JunkTailRunes
	return m
}

// PrefixDistance is the edit distance (Levenshtein: a substitution, an
// insertion or a deletion each cost 1) between short and the start of long
// that matches it best, so long's end is free. It counts up to limit+1:
// any distance over limit is limit+1. limit < 0 is taken as 0. It takes
// time in proportion to len(short) × limit (a band around the diagonal).
func PrefixDistance(short, long []rune, limit int) int {
	if limit < 0 {
		limit = 0
	}
	over := limit + 1
	n := len(short)
	// The best prefix of long is no longer than n+limit: any longer one has
	// more than limit insertions.
	m := min(len(long), n+limit)
	long = long[:m]
	// prev and cur are rows of the table, D[i][j] = distance between short[:i]
	// and long[:j], kept only inside the band |i-j| <= limit, capped at over.
	prev := make([]int, m+1)
	cur := make([]int, m+1)
	for j := 0; j <= m; j++ {
		prev[j] = min(j, over)
	}
	lo, hi := 0, m
	for i := 1; i <= n; i++ {
		lo, hi = max(0, i-limit), min(m, i+limit)
		// The next row reads this one from lo-1 to hi+1: just outside the
		// band, it's over.
		if lo > 0 {
			cur[lo-1] = over
		}
		if hi < m {
			cur[hi+1] = over
		}
		rowMin := over
		for j := lo; j <= hi; j++ {
			if j == 0 {
				cur[j] = min(i, over)
			} else {
				cost := 1
				if short[i-1] == long[j-1] {
					cost = 0
				}
				cur[j] = min(prev[j-1]+cost, prev[j]+1, cur[j-1]+1, over)
			}
			rowMin = min(rowMin, cur[j])
		}
		if rowMin >= over {
			return over
		}
		prev, cur = cur, prev
	}
	if n == 0 {
		return 0 // an empty copy is the start of anything
	}
	best := over
	for j := lo; j <= hi; j++ {
		best = min(best, prev[j])
	}
	return best
}

// Quality is how well a copy parses, best first by field: a format matched
// (not parse_failed, R4), its address was split (not address_unsplit), and
// the number of fields it found.
type Quality struct {
	Matched bool
	Split   bool
	Fields  int
}

// Better reports whether q parses better than o.
func (q Quality) Better(o Quality) bool {
	if q.Matched != o.Matched {
		return q.Matched
	}
	if q.Split != o.Split {
		return q.Split
	}
	return q.Fields > o.Fields
}

// Copy is one copy of a page: its text, as stored, and how it parses.
type Copy struct {
	Text    string
	Quality Quality
}

// Kept is why a near duplicate's message keeps the copy it does (the
// metric's label, R19a).
type Kept string

const (
	// KeptParsed: one copy parses better (R19a's first rule).
	KeptParsed Kept = "parsed"
	// KeptLonger: they parse as well as each other, and the shorter is a
	// truncated copy of the longer: the longer one (the second rule).
	KeptLonger Kept = "longer"
	// KeptFirst: otherwise, the copy stored first (the third rule).
	KeptFirst Kept = "first"
)

// Kepts are every reason, in the order the metrics list them.
var Kepts = []Kept{KeptFirst, KeptParsed, KeptLonger}

// Choose picks the copy a message keeps, of the one stored and one that has
// just come in, which m says is near it (Compare(stored.Text,
// incoming.Text)). incomingWins says the new copy's text replaces the
// message's (ADR 0050).
func Choose(stored, incoming Copy, m Match) (incomingWins bool, why Kept) {
	switch {
	case incoming.Quality.Better(stored.Quality):
		return true, KeptParsed
	case stored.Quality.Better(incoming.Quality):
		return false, KeptParsed
	case m.Truncated:
		// Compare(stored, incoming): ALonger means the stored copy is longer.
		return !m.ALonger, KeptLonger
	}
	return false, KeptFirst
}
