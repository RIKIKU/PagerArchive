// Package backup reads the state of the nightly backups (R86a, ADR 0020). The
// stack's backup service does them and writes a small status file, on a volume
// the API mounts read-only: the API never sees the dumps themselves. It's a file
// rather than a table so that a failure caused by the database being down is
// still recorded, and still shows when the database is.
package backup

import (
	"encoding/json"
	"errors"
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

// file is what deploy/backup/backup.sh writes. Times are seconds since 1970.
type file struct {
	Since         int64  `json:"since"`
	LastRunAt     int64  `json:"last_run_at"`
	LastRunOK     bool   `json:"last_run_ok"`
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
		Nights: f.Nights, Error: f.Error,
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
