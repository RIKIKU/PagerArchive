package acceptance

import (
	"bytes"
	"strings"
	"testing"
	"time"

	"github.com/RIKIKU/PagerArchive/internal/ingest"
	"github.com/RIKIKU/PagerArchive/internal/neardup"
	"github.com/RIKIKU/PagerArchive/internal/profile"
)

// R19b (2026-11-26): dedup-report's close pairs, as ingest stored them. A
// receiver's near repeat of a page is another transmission (R19a, ADR 0050
// decision 5): the pair is counted as one receiver heard both, and never
// among the pairs that'd merge, at any ratio.
func TestA131DedupReportClosePairs(t *testing.T) {
	e := setup(t)
	t0 := e.now.Add(-10 * time.Minute)
	page := "@@EX0007101 HSF1 ZORBLAX QUENTIFY @ 12 FLUMMOX ST, WIBBLETON GRID 123456 UNITS T1 T2 #STN1"
	repeat := strings.Replace(page, "QUENTIFY", "QU}NTIFY", 1)
	first := e.mustPost("a", "0100001", page, t0)
	second := e.mustPost("a", "0100001", repeat, t0.Add(3*time.Second)) // A's repeat, decoded differently
	e.mustPost("b", "0100001", repeat, t0.Add(3300*time.Millisecond))   // B's copy of the repeat
	if first.MessageID == second.MessageID {
		t.Fatal("a receiver's near repeat was merged into its first copy")
	}
	other := "@@EX0007102 HSF1 ZORBLAX QUENTIFY @ 12 FLUMMOX ST, WIBBLETON GRID 123456 UNITS T1 T2 #STN2"
	e.mustPost("a", "0100002", other, t0.Add(time.Minute))
	e.mustPost("b", "0100002", strings.Replace(other, "FLUMMOX", "FLUM}OX", 1), t0.Add(time.Minute+400*time.Millisecond)) // merged

	set, err := profile.LoadDir("../../profiles/example")
	if err != nil {
		t.Fatal(err)
	}
	rep, err := ingest.DedupReport(e.ctx, e.st, set.Networks["example-network"], set.Agencies, neardup.DefaultRatio, t0.Add(-time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if c := rep.Close; c.Pairs != 1 || c.OtherJobs != 0 || c.OneReceiver != 1 {
		t.Errorf("close pairs %+v", c)
	}
	for _, a := range rep.AtRatio {
		if a.CloseMerged != 0 || a.CloseOneHeard != 1 {
			t.Errorf("at %v: %+v", a.Ratio, a)
		}
	}
	var b bytes.Buffer
	rep.Write(&b)
	out := b.String()
	if !strings.Contains(out, "one receiver heard both") {
		t.Errorf("the report doesn't say one receiver heard both:\n%s", out)
	}
	for _, frag := range []string{"ZORBLAX", "QU}NTIFY", "FLUMMOX", "WIBBLETON", "EX000710", "0100001", "Client "} {
		if strings.Contains(out, frag) {
			t.Errorf("the report holds %q:\n%s", frag, out)
		}
	}
}

// R19b, ADR 0050's measuring voting: dedup-report's section on messages
// three receivers heard, read from the database as ingest stored them, with
// counts only (P1).
func TestA131DedupReportVoting(t *testing.T) {
	e := setup(t)
	key, err := e.st.AddClient(e.ctx, "c", "Client C")
	if err != nil {
		t.Fatal(err)
	}
	e.keys["c"] = key
	t0 := e.now.Add(-10 * time.Minute) // receive times in the past

	page := func(job string) string {
		return "@@" + job + " HSF1 ZORBLAX QUENTIFY @ 12 FLUMMOX ST, WIBBLETON GRID 123456 UNITS T1 T2 #STN1"
	}
	post := func(at time.Time, copies map[string]string) {
		t.Helper()
		for i, c := range []string{"a", "b", "c"} {
			if text, ok := copies[c]; ok {
				e.mustPost(c, "0100001", text, at.Add(time.Duration(i)*300*time.Millisecond))
			}
		}
	}
	same := page("EX0007001")
	post(t0, map[string]string{"a": same, "b": same, "c": same}) // every copy the same
	one := page("EX0007002")
	post(t0.Add(time.Minute), map[string]string{"a": one, "b": strings.Replace(one, "QUENTIFY", "QU}NTIFY", 1), "c": one}) // B's differs
	three := page("EX0007003")
	post(t0.Add(2*time.Minute), map[string]string{ // three texts: the vote gives C's, the whole one
		"a": strings.Replace(three, "ZORBLAX", "ZOR#LAX", 1), "b": strings.Replace(three, "FLUMMOX", "FLUM}OX", 1), "c": three})
	post(t0.Add(3*time.Minute), map[string]string{"a": page("EX0007004"), "b": page("EX0007004")})
	post(t0.Add(4*time.Minute), map[string]string{"a": page("EX0007005")})

	set, err := profile.LoadDir("../../profiles/example")
	if err != nil {
		t.Fatal(err)
	}
	rep, err := ingest.DedupReport(e.ctx, e.st, set.Networks["example-network"], set.Agencies, neardup.DefaultRatio, t0.Add(-time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	h := rep.Heard
	if h.ByClients != [4]int{1, 1, 3, 0} || h.Identical != 1 || h.OneOdd != 1 || h.ThreeOrMore != 1 || h.TwoSplit != 0 {
		t.Errorf("heard %+v", h)
	}
	if v := h.Vote; v.Same != 1 || v.Differs != 1 || v.AsWell != 1 || v.Worse != 0 || v.Unredacted != 0 {
		t.Errorf("vote %+v", v)
	}
	// Receivers by copies heard: A (5), B (4), C (3); B's was the odd one.
	if len(h.Receivers) != 3 || h.Receivers[0].Copies != 5 || h.Receivers[1].Copies != 4 || h.Receivers[2].Copies != 3 ||
		h.Receivers[1].OddOneOut != 1 || h.Receivers[0].OddOneOut+h.Receivers[2].OddOneOut != 0 {
		t.Errorf("receivers %+v", h.Receivers)
	}
	var b bytes.Buffer
	rep.Write(&b)
	out := b.String()
	for _, frag := range []string{"ZORBLAX", "ZOR#LAX", "QUENTIFY", "FLUMMOX", "WIBBLETON", "EX000700", "0100001", "Client "} {
		if strings.Contains(out, frag) {
			t.Errorf("the report holds %q:\n%s", frag, out)
		}
	}
	t.Log("\n" + out)
}
