// Package zzdemo is throwaway (never merged): tests that fail on purpose, to
// show ADR 0025's retries in CI. TestFailsOnce fails the first time it runs
// in a job, so gotestsum's re-run passes it and reports it as flaky.
// TestFailsInAttempt1 fails in a run's first attempt, so the job fails and a
// re-run of the failed jobs passes, which "All checks passed" reports.
package zzdemo

import (
	"os"
	"path/filepath"
	"testing"
)

func TestFailsOnce(t *testing.T) {
	marker := filepath.Join(os.TempDir(), "zzdemo-"+os.Getenv("GITHUB_RUN_ID")+"-"+os.Getenv("GITHUB_RUN_ATTEMPT"))
	if _, err := os.Stat(marker); err != nil {
		if err := os.WriteFile(marker, nil, 0o600); err != nil {
			t.Fatal(err)
		}
		t.Fatal("the first try fails on purpose")
	}
}

func TestFailsInAttempt1(t *testing.T) {
	if os.Getenv("GITHUB_RUN_ATTEMPT") == "1" {
		t.Fatal("fails in the run's first attempt, on purpose")
	}
}
