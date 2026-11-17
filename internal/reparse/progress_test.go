package reparse

import (
	"os"
	"regexp"
	"slices"
	"strings"
	"testing"
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
	// "retry" is no longer queued (a failed batch is done again from the run, R23c),
	// but is kept as a code for requests an older version queued.
	if n < 9 {
		t.Errorf("found %d reasons in the code, want at least 9", n)
	}
}
