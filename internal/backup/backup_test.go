package backup

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// R86a: failed or more than 36 hours old is what an admin is told, by the clock
// A80's stack test can't wait for.
func TestStates(t *testing.T) {
	now := time.Date(2026, 11, 3, 12, 0, 0, 0, time.UTC)
	ago := func(d time.Duration) int64 { return now.Add(-d).Unix() }
	dir := t.TempDir()
	path := filepath.Join(dir, "status.json")
	for _, c := range []struct {
		name                 string
		since, lastRun, good int64
		ok                   bool
		want                 string
		failing              bool
	}{
		{"a new install", ago(time.Hour), 0, 0, false, StateWaiting, false},
		{"a new install whose first backup never came", ago(37 * time.Hour), 0, 0, false, StateOverdue, true},
		{"a good backup last night", ago(240 * time.Hour), ago(8 * time.Hour), ago(8 * time.Hour), true, StateOK, false},
		{"just inside 36 hours", ago(240 * time.Hour), ago(36*time.Hour - time.Minute), ago(36*time.Hour - time.Minute), true, StateOK, false},
		{"just past 36 hours", ago(240 * time.Hour), ago(36*time.Hour + time.Minute), ago(36*time.Hour + time.Minute), true, StateOverdue, true},
		{"a failed backup, the last good one recent", ago(240 * time.Hour), ago(time.Hour), ago(25 * time.Hour), false, StateFailed, true},
		{"a first backup that failed", ago(2 * time.Hour), ago(time.Hour), 0, false, StateFailed, true},
	} {
		data := fmt.Sprintf(`{"since":%d,"last_run_at":%d,"last_run_ok":%t,"last_good_at":%d,"nights":3,"error":""}`, c.since, c.lastRun, c.ok, c.good)
		if err := os.WriteFile(path, []byte(data), 0o644); err != nil {
			t.Fatal(err)
		}
		got := Read(path, now, now)
		if got.State != c.want || got.Failing() != c.failing {
			t.Errorf("%s: %s (failing %t), want %s (failing %t)", c.name, got.State, got.Failing(), c.want, c.failing)
		}
	}
}

// With no status file the service hasn't started yet, or never does.
func TestNoStatusFile(t *testing.T) {
	now := time.Date(2026, 11, 3, 12, 0, 0, 0, time.UTC)
	path := filepath.Join(t.TempDir(), "status.json")
	if got := Read(path, now, now.Add(-time.Hour)); got.State != StateWaiting {
		t.Errorf("soon after the API's start: %s", got.State)
	}
	if got := Read(path, now, now.Add(-37*time.Hour)); got.State != StateOverdue {
		t.Errorf("37 hours after the API's start: %s", got.State)
	}
}

func TestUnreadableStatusIsAFailure(t *testing.T) {
	path := filepath.Join(t.TempDir(), "status.json")
	if err := os.WriteFile(path, []byte("{half"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := Read(path, time.Now(), time.Now()); got.State != StateFailed || got.Error == "" {
		t.Errorf("%+v", got)
	}
}

// R86d: the last good backup's size and how long its dumps and check took,
// for the metrics, as backup.sh writes them.
func TestLastGoodNumbers(t *testing.T) {
	now := time.Date(2026, 11, 3, 12, 0, 0, 0, time.UTC)
	path := filepath.Join(t.TempDir(), "status.json")
	data := fmt.Sprintf(`{
  "since": %d,
  "last_night": "2026-11-03",
  "last_run_at": %d,
  "last_run_ok": true,
  "last_good_at": %d,
  "last_good_night": "2026-11-03",
  "last_good_bytes": 52428800,
  "last_good_dump_seconds": 41,
  "last_good_check_seconds": 187,
  "nights": 14,
  "error": ""
}`, now.Add(-240*time.Hour).Unix(), now.Add(-8*time.Hour).Unix(), now.Add(-8*time.Hour).Unix())
	if err := os.WriteFile(path, []byte(data), 0o644); err != nil {
		t.Fatal(err)
	}
	got := Read(path, now, now)
	if got.LastGoodBytes != 52428800 || got.LastGoodDumpSeconds != 41 || got.LastGoodCheckSeconds != 187 {
		t.Errorf("size %d, dumps %d s, check %d s; want 52428800, 41 s, 187 s", got.LastGoodBytes, got.LastGoodDumpSeconds, got.LastGoodCheckSeconds)
	}
}
