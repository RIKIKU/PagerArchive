package neardup

import (
	"bytes"
	"strings"
	"testing"
)

// R19b: the report counts how near duplicates differ, and prints counts
// only: no text from a page (P1).
func TestReport(t *testing.T) {
	parsed := Quality{Matched: true, Split: true, Fields: 7}
	failed := Quality{Fields: 1}
	r := NewReport(DefaultRatio)
	r.AddKept(page, garbled2, parsed, parsed)       // substitution, both parse
	r.AddKept(page, page[:50], parsed, failed)      // truncation: the longer parses
	r.AddKept(page, page+" \u00ff", parsed, failed) // junk tail
	r.AddClose(page, correction, false, false)
	r.AddClose(page, strings.Replace(page, "0123", "0124", 1), true, false) // another job: never merged
	var b bytes.Buffer
	r.Write(&b)
	out := b.String()
	if r.Kept.Pairs != 3 || r.Kept.Shapes[Substitution] != 1 || r.Kept.Shapes[Truncation] != 1 || r.Kept.Shapes[JunkTail] != 1 {
		t.Errorf("kept %+v", r.Kept)
	}
	if r.Kept.LongerParses != 1 || r.Kept.MessageBetter != 2 || r.Kept.AsWell != 1 || r.Close.Pairs != 2 || r.Close.OtherJobs != 1 {
		t.Errorf("parsing %+v, close %+v", r.Kept, r.Close)
	}
	// Two short pages a character apart would merge at 0.1 (1 edit in 13),
	// not at 0.05.
	r.AddClose("QDTest page 1", "QDTest page 2", false, false)
	for _, a := range r.AtRatio {
		if (a.Ratio == 0.05 && a.CloseMerged != 0) || (a.Ratio == 0.1 && a.CloseMerged != 1) || a.KeptApart != 0 {
			t.Errorf("at %v: %+v", a.Ratio, a)
		}
	}
	// One receiver's near repeat is another transmission (ADR 0050 decision
	// 5): counted, in the histograms, never among the pairs that'd merge. An
	// exact repeat is R19's, so it isn't one.
	r.AddClose(page, garbled2, false, true)
	r.AddClose(page, page, false, true)
	if r.Close.OneReceiver != 1 || r.Close.Pairs != 5 || r.Close.PerChar.counts[0] != 1 {
		t.Errorf("close %+v", r.Close)
	}
	for _, a := range r.AtRatio {
		if (a.Ratio == 0.1 && (a.CloseMerged != 1 || a.CloseOneHeard != 1)) || (a.Ratio == 0.05 && a.CloseOneHeard != 1) {
			t.Errorf("at %v: %+v", a.Ratio, a)
		}
	}
	for _, frag := range []string{"EX0000123", "HOUSE", "EXAMPLE ST", "MADEUPTON", "STN", "F}RQ"} {
		if strings.Contains(out, frag) {
			t.Errorf("the report holds page text %q:\n%s", frag, out)
		}
	}
	t.Log("\n" + out)
}
