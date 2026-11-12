package pagerarchive

import (
	"os"
	"regexp"
	"slices"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// unstampedVersion is what the repository's own VERSION says (ADR 0024).
const unstampedVersion = "0.0.0-dev"

// VERSION is the repository's own, 0.0.0-dev, or a build's: a release's
// MAJOR.MINOR.PATCH, or <MAJOR.MINOR.PATCH>-dev+<commit> (scripts/ci/versions-lib.sh
// checks the same).
func TestVersionIsSemantic(t *testing.T) {
	if !regexp.MustCompile(`^(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)(-dev\+[0-9a-f]{7,40})?$`).MatchString(Version) && Version != unstampedVersion {
		t.Fatalf("VERSION is %q, want %s, MAJOR.MINOR.PATCH or MAJOR.MINOR.PATCH-dev+<commit>", Version, unstampedVersion)
	}
}

// ADR 0024: every CI job that checks out the code and builds, tests or
// publishes from it writes the build's version into the checkout first, so
// no image or file is published as 0.0.0-dev and what's tested is what's
// published. The exceptions check the repository's own files, or don't use
// the version.
func TestEveryCIJobStampsTheVersion(t *testing.T) {
	unstamped := []string{
		"stamp",      // works the version out
		"changes",    // decides which checks run, from the files alone
		"versions",   // checks that the repository's own files are unstamped
		"powershell", // the module has its own version (R63)
		"publish-powershell",
	}
	type step struct {
		Uses string `yaml:"uses"`
		Run  string `yaml:"run"`
	}
	var ci struct {
		Jobs map[string]struct {
			Needs needs  `yaml:"needs"`
			Steps []step `yaml:"steps"`
		} `yaml:"jobs"`
	}
	data, err := os.ReadFile(".github/workflows/ci.yml")
	if err != nil {
		t.Fatal(err)
	}
	if err := yaml.Unmarshal(data, &ci); err != nil {
		t.Fatal(err)
	}
	for name, job := range ci.Jobs {
		checkout := slices.IndexFunc(job.Steps, func(s step) bool { return strings.HasPrefix(s.Uses, "actions/checkout@") })
		if checkout < 0 || slices.Contains(unstamped, name) {
			continue
		}
		if checkout+1 >= len(job.Steps) || !strings.Contains(job.Steps[checkout+1].Run, "scripts/ci/stamp-version.sh") {
			t.Errorf("ci.yml: %s checks out the code but doesn't run scripts/ci/stamp-version.sh straight after", name)
		}
		if !slices.Contains(job.Needs, "stamp") && !slices.Contains(job.Needs, "release") {
			t.Errorf("ci.yml: %s needs neither stamp nor release, so it has no version to stamp", name)
		}
	}
}
