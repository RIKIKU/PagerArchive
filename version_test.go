package pagerarchive

import (
	"regexp"
	"testing"
)

// VERSION is a release's version: MAJOR.MINOR.PATCH (scripts/ci/check-versions.sh
// checks the same on every branch).
func TestVersionIsSemantic(t *testing.T) {
	if !regexp.MustCompile(`^(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)$`).MatchString(Version) {
		t.Fatalf("VERSION is %q, want MAJOR.MINOR.PATCH", Version)
	}
}
