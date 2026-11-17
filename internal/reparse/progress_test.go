package reparse

import (
	"os"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"
)

// R23b: reasons are logged as codes only, each once; one the code doesn't
// queue with is "other".
func TestReasonCodes(t *testing.T) {
	for in, want := range map[string]string{
		"profiles changed":                          "profiles_changed",
		"alias added|alias added|retry":             "alias_added,retry",
		"template profile changed|SHED FIRE 3 ST.":  "other,template_profile_changed",
		"someone@example.com|alias capcode changed": "alias_capcode_changed,other",
		"SHED FIRE 3 FICTION ST|Profiles Changed":   "other",
	} {
		if got := reasonCodes(strings.Split(in, "|")); got != want {
			t.Errorf("reasonCodes(%q) = %q, want %q", in, got, want)
		}
	}
}

// Every reason the code queues a re-parse with is a known one, so it's
// logged as itself, not "other".
func TestKnownReasons(t *testing.T) {
	re := regexp.MustCompile(`Reason: "([^"]+)"`)
	re2 := regexp.MustCompile(`(?:rp\.add\([^,]+, |reparse\(ctx, q, [^,]+, )"([^"]+)"`)
	n := 0
	for _, f := range []string{"reparse.go", "../catalog/catalog.go", "../catalog/import.go", "../catalog/templates.go"} {
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		for _, r := range []*regexp.Regexp{re, re2} {
			for _, m := range r.FindAllStringSubmatch(string(b), -1) {
				n++
				if !slices.Contains(knownReasons, m[1]) {
					t.Errorf("%s queues a re-parse with %q, not in knownReasons", f, m[1])
				}
			}
		}
	}
	if n < 10 {
		t.Errorf("found %d reasons in the code, want at least 10", n)
	}
}

// R23b: the rate and time left are from the run's start; done can pass the
// total (pages stored while it runs), and the finished run's total is then
// what it did.
func TestProgress(t *testing.T) {
	var p Progress
	if s := p.Snapshot(); s.Running || s.Total != 0 || !s.Started.IsZero() {
		t.Fatalf("zero value: %+v", s)
	}
	at := time.Date(2026, 11, 18, 0, 0, 0, 0, time.UTC)
	p.start(1000, at)
	for range 250 {
		p.add()
	}
	s := p.Snapshot()
	perMin, eta := s.Rate(at.Add(time.Minute))
	if !s.Running || s.Done != 250 || s.Percent() != 25 || perMin != 250 || eta != 180 {
		t.Errorf("a quarter done in a minute: %+v, %v a minute, %d s left, %d%%", s, perMin, eta, s.Percent())
	}
	if _, eta := (ProgressSnapshot{Total: 10, Started: at}).Rate(at.Add(time.Minute)); eta != -1 {
		t.Errorf("nothing done: %d s left, want -1 (unknown)", eta)
	}
	for range 760 {
		p.add()
	}
	if s := p.Snapshot(); s.Percent() != 100 {
		t.Errorf("past the total: %d%%", s.Percent())
	}
	p.finish()
	if s := p.Snapshot(); s.Running || s.Total != 1010 || s.Done != 1010 || s.Reparsed != 1010 {
		t.Errorf("finished: %+v", s)
	}
	p.start(5, at)
	p.add()
	if s := p.Snapshot(); s.Done != 1 || s.Total != 5 || s.Reparsed != 1011 {
		t.Errorf("a second run: %+v", s)
	}
}
