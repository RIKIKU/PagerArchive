package neardup

import (
	"bytes"
	"strings"
	"testing"
)

// sub is s with the character at i replaced by r (a garbled character).
func sub(s string, i int, r rune) string {
	rs := []rune(s)
	rs[i] = r
	return string(rs)
}

// The simulated vote (ADR 0050, measuring voting), on made-up pages.
func TestVote(t *testing.T) {
	redacted := "@@EX0000123 HSF1 HOUSE FIRE @ 12 EXAMPLE ST, MADEUPTON CALLER REDACTED UNITS P1 T1 #STN1"
	tests := []struct {
		name       string
		copies     []string
		prefer     int
		want       string
		ties       int
		unredacted bool
	}{
		{"a substitution in one copy", []string{page, page, sub(page, 20, '}')}, 0, page, 0, false},
		{"the kept copy is the odd one", []string{sub(page, 20, '}'), page, page}, 0, page, 0, false},
		{"different substitutions in two copies", []string{sub(page, 20, '}'), sub(page, 60, '#'), page}, 0, page, 0, false},
		{"different substitutions in all three", []string{sub(page, 20, '}'), sub(page, 40, '#'), sub(page, 60, 'Q')}, 0, page, 0, false},
		// Past its end, the truncated copy doesn't vote: the other two
		// disagree at 60, a tie, and the medoid (the kept one) holds.
		{"a truncated copy", []string{page, page[:40], sub(page, 60, '#')}, 0, page, 1, false},
		{"a truncated copy outvoted where it covers", []string{page[:50], sub(page, 30, '#'), page}, 0, page, 0, false},
		{"a junk tail", []string{page + " \x7f\u00ff", page, sub(page, 20, '}')}, 0, page, 0, false},
		{"a three-way tie", []string{sub(page, 20, 'X'), sub(page, 20, 'Y'), sub(page, 20, 'Z')}, 1, sub(page, 20, 'Y'), 1, false},
		{"a lost character", []string{strings.Replace(page, "HOUSE", "HOSE", 1), page, page}, 0, page, 0, false},
		{"an added character", []string{strings.Replace(page, "HOUSE", "HOUSSE", 1), page, page}, 0, page, 0, false},
		{"a redaction in one copy only", []string{redacted, strings.Replace(redacted, "REDACTED", "R3DACT3D", 1), strings.Replace(redacted, "REDACTED", "R3DACT3D", 1)}, 0,
			strings.Replace(redacted, "REDACTED", "R3DACT3D", 1), 0, true},
		{"a redaction in every copy", []string{redacted, sub(redacted, 20, '}'), redacted}, 0, redacted, 0, false},
		{"garbled next to a redaction", []string{redacted, sub(redacted, 63, 'Q'), sub(redacted, 63, 'Q')}, 0, sub(redacted, 63, 'Q'), 0, true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			v := Vote(tc.copies, tc.prefer, "REDACTED")
			if v.Text != tc.want || v.Ties != tc.ties || v.Unredacted != tc.unredacted {
				t.Errorf("got %q, %d ties, unredacted %v (medoid %d)\nwant %q, %d ties, unredacted %v",
					v.Text, v.Ties, v.Unredacted, v.Medoid, tc.want, tc.ties, tc.unredacted)
			}
		})
	}
}

// A truncated copy is never the frame while whole copies are there.
func TestVoteMedoidIsWhole(t *testing.T) {
	if v := Vote([]string{page[:30], page, sub(page, 70, '#')}, 0, "REDACTED"); v.Medoid == 0 {
		t.Errorf("the truncated copy is the medoid: %+v", v)
	}
}

func TestAlignTruncated(t *testing.T) {
	a := align([]rune("ABCD"), []rune("ABCDEFGH"))
	if a.covered != 4 || a.at[3] != 3 || len(a.ins[8]) != 0 {
		t.Errorf("truncated: %+v", a)
	}
	a = align([]rune("ABCDEFGHIJ"), []rune("ABCDEFGH"))
	if a.covered != 8 || len(a.ins[8]) != 2 {
		t.Errorf("longer: %+v", a)
	}
	a = align([]rune("ABDEFGH"), []rune("ABCDEFGH"))
	if a.covered != 8 || a.at[2] != gap || a.at[3] != 2 {
		t.Errorf("lost character: %+v", a)
	}
}

// The voting section counts copies, and prints counts only: no page text
// (P1). The pages carry distinctive made-up words that must not appear.
func TestReportVoting(t *testing.T) {
	const w = "@@EX0000777 HSF1 ZORBLAX QUENTIFY @ 9 FLUMMOX RD, WIBBLETON GRID 765432 UNITS P9 #STN9"
	q := func(text string) Quality {
		// Made-up: a copy parses when it holds the job and the station whole.
		return Quality{Matched: strings.Contains(text, "EX0000777") && strings.HasSuffix(text, "#STN9"), Fields: 5}
	}
	r := NewReport(DefaultRatio)
	r.RedactionMarker = "REDACTED"
	r.AddHeardBy(1, 10)
	r.AddHeardBy(2, 5)
	r.AddHeardBy(3, 4)
	r.AddHeardBy(5, 1)
	r.AddReceiver(19)
	r.AddReceiver(18)
	r.AddReceiver(4)
	r.AddCopies(w, []string{w, w, w}, []int{0, 1, 2}, q)                                   // all the same
	r.AddCopies(w, []string{w, w, sub(w, 15, 'Q')}, []int{0, 1, 2}, q)                     // receiver 3 differs; the vote keeps w
	broken := sub(w, 6, 'X')                                                               // the job garbled: it fails to parse
	r.AddCopies(broken, []string{broken, sub(w, 30, '#'), w}, []int{1, 0, 2}, q)           // 3 texts: the vote rescues it
	r.AddCopies(w, []string{w, w, sub(w, 15, 'Q'), sub(w, 15, 'Q')}, []int{0, 1, 2, 2}, q) // 2-2
	h := r.Heard
	if h.ByClients != [4]int{10, 5, 4, 1} || h.Identical != 1 || h.OneOdd != 1 || h.ThreeOrMore != 1 || h.TwoSplit != 1 {
		t.Errorf("heard %+v", h)
	}
	if h.Receivers[2].OddOneOut != 1 || h.Receivers[0].OddOneOut != 0 {
		t.Errorf("receivers %+v", h.Receivers)
	}
	if v := h.Vote; v.Same != 2 || v.Differs != 1 || v.AsWell != 1 || v.Worse != 0 || v.Rescued != 1 || v.Unredacted != 0 {
		t.Errorf("vote %+v", v)
	}
	var b bytes.Buffer
	r.Write(&b)
	out := b.String()
	for _, frag := range []string{"ZORBLAX", "QUENTIFY", "FLUMMOX", "WIBBLETON", "EX0000777", "765432", "STN9"} {
		if strings.Contains(out, frag) {
			t.Errorf("the report holds page text %q:\n%s", frag, out)
		}
	}
	t.Log("\n" + out)
}
