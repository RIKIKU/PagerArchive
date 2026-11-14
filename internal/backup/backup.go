// Package backup reads the state of the nightly backups (R86a, ADR 0020), and
// asks for one now. The
// stack's backup service does them and writes a small status file, on a volume
// the API mounts read-only: the API never sees the dumps themselves. It's a file
// rather than a table so that a failure caused by the database being down is
// still recorded, and still shows when the database is.
//
// Back up now: the API writes a request file on a volume of its own, and the
// backup service, which checks for it every minute, runs the backup and check
// and deletes it. So the API can ask, but can neither write the status nor reach
// the dumps or Docker.
package backup

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"time"
)

// Overdue is how long after the last good backup one is overdue (R86a).
const Overdue = 36 * time.Hour

// What an admin is told.
const (
	StateOK      = "ok"      // the last backup was good, and recent
	StateWaiting = "waiting" // none yet, and not overdue: a new install
	StateFailed  = "failed"  // the last backup failed
	StateOverdue = "overdue" // no good backup for 36 hours
)

// What the backup service is doing now (Back up now).
const (
	RunOff     = "off"     // backups can't be asked for on this server
	RunIdle    = "idle"    // nothing: a backup can be asked for
	RunQueued  = "queued"  // asked for: the service starts it within a minute
	RunRunning = "running" // a backup, nightly or asked for, is running
)

// Why a request is refused: one backup at a time.
var (
	ErrQueued  = errors.New("a backup has been asked for already")
	ErrRunning = errors.New("a backup is running")
	// ErrNoService: the request folder isn't there or can't be written, so the
	// backup service hasn't started since it was installed.
	ErrNoService = errors.New("the backup service hasn't started")
)

// file is what deploy/backup/backup.sh writes. Times are seconds since 1970.
type file struct {
	Since         int64  `json:"since"`
	LastRunAt     int64  `json:"last_run_at"`
	LastRunOK     bool   `json:"last_run_ok"`
	RunningSince  int64  `json:"running_since"`
	LastGoodAt    int64  `json:"last_good_at"`
	LastGoodNight string `json:"last_good_night"`
	LastGoodBytes int64  `json:"last_good_bytes"`
	DumpSeconds   int64  `json:"last_good_dump_seconds"`
	CheckSeconds  int64  `json:"last_good_check_seconds"`
	Nights        int    `json:"nights"`
	Error         string `json:"error"`
}

// Status is the backups' state, for Administration and the metrics. Its error
// is fixed text from the script: it never holds page text (P1).
type Status struct {
	State         string     `json:"state"`
	LastRunAt     *time.Time `json:"last_run_at"`
	LastGoodAt    *time.Time `json:"last_good_at"`
	LastGoodNight string     `json:"last_good_night,omitempty"`
	LastGoodBytes int64      `json:"last_good_bytes"`
	// R86d: how long the last good backup's dumps and check took.
	LastGoodDumpSeconds  int64  `json:"last_good_dump_seconds"`
	LastGoodCheckSeconds int64  `json:"last_good_check_seconds"`
	Nights               int    `json:"nights"`
	Error                string `json:"error,omitempty"`
	// Back up now: what the service is doing, when a backup was asked for
	// (while it waits) and when the one running started.
	Run          string     `json:"run"`
	RequestedAt  *time.Time `json:"requested_at"`
	RunningSince *time.Time `json:"running_since"`
}

// Failing is whether an admin should be told, and an alert fire.
func (s Status) Failing() bool {
	return s.State == StateFailed || s.State == StateOverdue
}

func at(sec int64) *time.Time {
	if sec == 0 {
		return nil
	}
	t := time.Unix(sec, 0).UTC()
	return &t
}

// Read is the state at now. The status file not being there yet counts from
// started, the API's start: the backup service writes it when it first starts,
// and if it never does, that's overdue in time.
func Read(path string, now, started time.Time) Status {
	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		if now.Sub(started) > Overdue {
			return Status{State: StateOverdue}
		}
		return Status{State: StateWaiting}
	}
	var f file
	if err == nil {
		err = json.Unmarshal(data, &f)
	}
	if err != nil {
		return Status{State: StateFailed, Error: "the backup status can't be read"}
	}
	s := Status{
		LastRunAt: at(f.LastRunAt), LastGoodAt: at(f.LastGoodAt), LastGoodNight: f.LastGoodNight,
		LastGoodBytes: f.LastGoodBytes, LastGoodDumpSeconds: f.DumpSeconds, LastGoodCheckSeconds: f.CheckSeconds,
		Nights: f.Nights, Error: f.Error, RunningSince: at(f.RunningSince),
	}
	from := f.LastGoodAt
	if from == 0 {
		from = f.Since
	}
	switch {
	case f.LastRunAt != 0 && !f.LastRunOK:
		s.State = StateFailed
	case now.Sub(time.Unix(from, 0)) > Overdue:
		s.State = StateOverdue
	case f.LastGoodAt == 0:
		s.State = StateWaiting
	default:
		s.State = StateOK
	}
	return s
}

// Current is Read's state with what the service is doing now: running, as its
// status says, or queued, while a request is there. requestPath "" is a server
// where backups can't be asked for.
func Current(statusPath, requestPath string, now, started time.Time) Status {
	s := Read(statusPath, now, started)
	s.Run = RunIdle
	if requestPath == "" {
		s.Run = RunOff
	} else if fi, err := os.Lstat(requestPath); err == nil {
		t := fi.ModTime().UTC().Truncate(time.Second)
		s.RequestedAt = &t
		s.Run = RunQueued
	}
	if s.RunningSince != nil {
		s.Run = RunRunning
	}
	return s
}

// Request asks the backup service for a backup now, and returns the state
// after. One at a time: while one is asked for or running, it's refused with
// ErrQueued or ErrRunning and the state that says since when. The file is
// created only if there's none, so two admins at once can't both ask.
func Request(statusPath, requestPath string, now, started time.Time) (Status, error) {
	s := Current(statusPath, requestPath, now, started)
	switch s.Run {
	case RunOff:
		return s, ErrNoService
	case RunRunning:
		return s, ErrRunning
	case RunQueued:
		return s, ErrQueued
	}
	f, err := os.OpenFile(requestPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if errors.Is(err, fs.ErrExist) {
		return Current(statusPath, requestPath, now, started), ErrQueued
	}
	if err != nil {
		return s, fmt.Errorf("%w: %w", ErrNoService, err)
	}
	// For a person looking: the service never reads it.
	_, err = fmt.Fprintf(f, "{\"requested_at\": %q}\n", now.UTC().Format(time.RFC3339))
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		_ = os.Remove(requestPath)
		return s, fmt.Errorf("%w: %w", ErrNoService, err)
	}
	return Current(statusPath, requestPath, now, started), nil
}
