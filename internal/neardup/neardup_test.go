package neardup

import (
	"strings"
	"testing"
)

// Made-up pages in the example profile's emergency format (SPEC.md section 10).
const (
	page = "@@EX0000123 HSF1 HOUSE FIRE @ 12 EXAMPLE ST, MADEUPTON GRID 123456 UNITS P1 T1 #STN1"
	// One codeword garbled: characters wrong in a run of three.
	garbled = "@@EX0000123 HSF1 HOUSE F}RQ @ 12 EXAMPLE ST, MADEUPTON GRID 123456 UNITS P1 T1 #STN1"
	// Two codewords garbled, and a character that became a control character
	// and was removed (R17).
	garbled2 = "@@EX0000123 HSF1 HOUSE F}RQ @ 12 EXAMPLE ST, MADEUPTN GRID 1#3456 UNITS P1 T1 #STN1"
	// A correction: the same job, but another address and locality.
	correction = "@@EX0000123 HSF1 HOUSE FIRE @ 48 SAMPLE RD, OTHERVILLE GRID 654321 UNITS P2 #STN2"
)

func TestCompare(t *testing.T) {
	tests := []struct {
		name      string
		a, b      string
		near      bool
		shape     Shape
		truncated bool
	}{
		{"one codeword garbled", page, garbled, true, Substitution, false},
		{"two codewords and a lost character", page, garbled2, true, Substitution, false},
		{"truncated", page, page[:50], true, Truncation, true},
		{"truncated, the other way round", page[:50], page, true, Truncation, true},
		{"truncated and garbled", garbled[:60], page, true, Other, true},
		{"junk tail of control and non-ASCII characters", page, page + " \x7fÿ", true, JunkTail, false},
		{"a printable junk tail of one codeword", page, page + "x}~", true, JunkTail, false},
		{"a correction", page, correction, false, "", false},
		{"a short page and another", "QDTest 1", "QDTest 2", false, "", false},
		{"a short page cut by a junk tail's length", "QDTest 1", "QDTest", true, JunkTail, false},
		{"a short page truncated more", "QDTest page 1", "QDTe", true, Truncation, true},
		{"another page to the capcode", page, "HbRE EX0000123 CROWS ARRIVED ON SCENE #STN1", false, "", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := Compare(tt.a, tt.b, DefaultRatio)
			if m.Near != tt.near {
				t.Fatalf("near %v, want %v (distance %d, allowed %d)", m.Near, tt.near, m.Distance, m.Allowed)
			}
			if !tt.near {
				return
			}
			if m.Shape != tt.shape || m.Truncated != tt.truncated {
				t.Errorf("shape %s truncated %v, want %s %v (distance %d, extra %d)", m.Shape, m.Truncated, tt.shape, tt.truncated, m.Distance, m.Extra)
			}
		})
	}
}

// The threshold is floor(ratio × the shorter copy's length): a page under
// 10 characters may differ by nothing but its tail; one of 30, by a codeword.
func TestAllowed(t *testing.T) {
	for _, tt := range []struct {
		n     int
		ratio float64
		want  int
	}{{9, 0.1, 0}, {10, 0.1, 1}, {30, 0.1, 3}, {88, 0.1, 8}, {100, 0.1, 10}, {100, 0.05, 5}, {100, 0, 0}} {
		if got := Allowed(tt.n, tt.ratio); got != tt.want {
			t.Errorf("Allowed(%d, %v) = %d, want %d", tt.n, tt.ratio, got, tt.want)
		}
	}
	// A ratio below 0 turns near duplicates off.
	if Compare(page, garbled, -1).Near {
		t.Error("near with near duplicates off")
	}
}

func TestPrefixDistance(t *testing.T) {
	r := func(s string) []rune { return []rune(s) }
	for _, tt := range []struct {
		short, long string
		limit, want int
	}{
		{"", "anything", 0, 0},
		{"abc", "abc", 0, 0},
		{"abc", "abcdef", 0, 0},  // the start of the longer
		{"abd", "abcdef", 2, 1},  // a substitution
		{"acd", "abcdef", 2, 1},  // a deletion: c d matches after b is dropped
		{"abxcd", "abcd", 2, 1},  // an insertion
		{"xyz", "abcdef", 2, 3},  // over the limit: limit+1
		{"xyzw", "abcdef", 1, 2}, // over the limit: limit+1
		{"kitten", "sitting", 5, 2},
	} {
		if got := PrefixDistance(r(tt.short), r(tt.long), tt.limit); got != tt.want {
			t.Errorf("PrefixDistance(%q, %q, %d) = %d, want %d", tt.short, tt.long, tt.limit, got, tt.want)
		}
	}
	// A long page costs a band, not a square.
	long := strings.Repeat("EXAMPLE ", 500)
	if got := PrefixDistance(r(long), r(long+"X"), 10); got != 0 {
		t.Errorf("long page: %d", got)
	}
}

func TestClean(t *testing.T) {
	if got := string(Clean("@@PAGE #STN1 \t\x01é ")); got != "@@PAGE #STN1" {
		t.Errorf("Clean = %q", got)
	}
}

func TestChoose(t *testing.T) {
	parsed := Quality{Matched: true, Split: true, Fields: 7}
	fewer := Quality{Matched: true, Split: true, Fields: 6}
	unsplit := Quality{Matched: true, Split: false, Fields: 7}
	failed := Quality{Fields: 1}
	tests := []struct {
		name             string
		stored, incoming Copy
		wins             bool
		why              Kept
	}{
		{"both parse, substitution: the first", Copy{page, parsed}, Copy{garbled, parsed}, false, KeptFirst},
		{"neither parses, substitution: the first", Copy{garbled, failed}, Copy{page, failed}, false, KeptFirst},
		{"the new copy parses", Copy{garbled, failed}, Copy{page, parsed}, true, KeptParsed},
		{"the stored copy parses", Copy{page, parsed}, Copy{garbled, failed}, false, KeptParsed},
		{"the new copy found a field the stored one lost", Copy{garbled, fewer}, Copy{page, parsed}, true, KeptParsed},
		{"the new copy's address splits", Copy{garbled, unsplit}, Copy{page, parsed}, true, KeptParsed},
		{"a late, longer copy, neither parsing: the longer", Copy{page[:50], failed}, Copy{page[:70], failed}, true, KeptLonger},
		{"a late, truncated copy, neither parsing: the longer, already stored", Copy{page[:70], failed}, Copy{page[:50], failed}, false, KeptLonger},
		{"a late, whole copy that parses, after a truncated one", Copy{page[:50], failed}, Copy{page, parsed}, true, KeptParsed},
		{"both parse, one with a junk tail: the first", Copy{page, parsed}, Copy{page + "x}~", parsed}, false, KeptFirst},
		{"both parse, the new one with a junk tail first stored", Copy{page + "x}~", parsed}, Copy{page, parsed}, false, KeptFirst},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := Compare(tt.stored.Text, tt.incoming.Text, DefaultRatio)
			if !m.Near {
				t.Fatalf("not near: distance %d, allowed %d", m.Distance, m.Allowed)
			}
			wins, why := Choose(tt.stored, tt.incoming, m)
			if wins != tt.wins || why != tt.why {
				t.Errorf("Choose = %v %s, want %v %s", wins, why, tt.wins, tt.why)
			}
		})
	}
}
