package backup

import (
	"errors"
	"fmt"
	"io/fs"
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

// Back up now (R86a): the request file says queued until the service starts,
// its status says running until it's done, and one at a time.
func TestBackUpNow(t *testing.T) {
	now := time.Date(2026, 11, 14, 10, 0, 0, 0, time.UTC)
	dir := t.TempDir()
	status, request := filepath.Join(dir, "status.json"), filepath.Join(dir, "requests", "request.json")
	write := func(running int64) {
		t.Helper()
		data := fmt.Sprintf(`{"since":%d,"last_run_at":%d,"last_run_ok":true,"running_since":%d,"last_good_at":%d,"nights":3,"error":""}`,
			now.Add(-240*time.Hour).Unix(), now.Add(-6*time.Hour).Unix(), running, now.Add(-6*time.Hour).Unix())
		if err := os.WriteFile(status, []byte(data), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write(0)

	// Before the service has made its folder, there's no one to ask.
	if _, err := Request(status, request, now, now); !errors.Is(err, ErrNoService) {
		t.Fatalf("no request folder: %v", err)
	}
	if err := os.Mkdir(filepath.Dir(request), 0o755); err != nil {
		t.Fatal(err)
	}
	if got := Current(status, request, now, now); got.Run != RunIdle || got.RequestedAt != nil || got.RunningSince != nil {
		t.Fatalf("idle: %+v", got)
	}
	if got := Current(status, "", now, now); got.Run != RunOff {
		t.Errorf("no request file set: %s", got.Run)
	}

	got, err := Request(status, request, now, now)
	if err != nil || got.Run != RunQueued || got.RequestedAt == nil || got.State != StateOK {
		t.Fatalf("asked: %v, %+v", err, got)
	}
	if _, err := os.Stat(request); err != nil {
		t.Fatalf("no request file: %v", err)
	}
	if _, err := Request(status, request, now, now); !errors.Is(err, ErrQueued) {
		t.Errorf("asked again while queued: %v", err)
	}

	// The service has started it: running, with the request still there.
	write(now.Add(-time.Minute).Unix())
	got = Current(status, request, now, now)
	if got.Run != RunRunning || got.RunningSince == nil || !got.RunningSince.Equal(now.Add(-time.Minute)) {
		t.Errorf("running: %+v", got)
	}
	if _, err := Request(status, request, now, now); !errors.Is(err, ErrRunning) {
		t.Errorf("asked again while running: %v", err)
	}
	// It deletes the request first, so it's still running without one.
	if err := os.Remove(request); err != nil {
		t.Fatal(err)
	}
	if _, err := Request(status, request, now, now); !errors.Is(err, ErrRunning) {
		t.Errorf("asked while a nightly backup runs: %v", err)
	}
	if _, err := os.Stat(request); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("a refused request left a file: %v", err)
	}

	// Done: idle, and another can be asked for.
	write(0)
	if got := Current(status, request, now, now); got.Run != RunIdle {
		t.Errorf("done: %s", got.Run)
	}
	if _, err := Request(status, request, now, now); err != nil {
		t.Errorf("asked after it was done: %v", err)
	}
}
