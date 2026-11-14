// Package autoupgrade reads what automatic upgrades last did (SPEC.md R84h, ADR
// 0030). They run on the host, from deploy/auto-upgrade/auto-upgrade.sh, never
// in the API: the API has no Docker and can't upgrade anything. The script
// writes a small status file in a folder the API mounts read-only, as the
// backup service does for backups (R86a), and the API shows it in
// Administration and in its metrics (R52).
package autoupgrade

import (
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"regexp"
	"slices"
	"syscall"
	"time"
)

// Stale is how long after the last check the checks count as stopped: they
// run hourly, so three missed in a row.
const Stale = 3 * time.Hour

// What an admin is told.
const (
	StateOff     = "off"     // not switched on here
	StateOK      = "ok"      // checking hourly; the last upgrade, if any, went well
	StateFailed  = "failed"  // the last upgrade failed, and was rolled back (or not)
	StateStale   = "stale"   // switched on, but no check for 3 hours
	StateStopped = "stopped" // a run stopped part way through an upgrade
)

// file is what the script writes. Times are seconds since 1970.
type file struct {
	Enabled         bool     `json:"enabled"`
	EnabledAt       int64    `json:"enabled_at"`
	DisabledAt      int64    `json:"disabled_at"`
	State           string   `json:"state"`
	RunningSince    int64    `json:"running_since"`
	LastCheckAt     int64    `json:"last_check_at"`
	LastCheckResult string   `json:"last_check_result"`
	LastCheckNote   string   `json:"last_check_note"`
	RunningVersion  string   `json:"running_version"`
	LatestVersion   string   `json:"latest_version"`
	Failures        int64    `json:"failures"`
	Rollbacks       int64    `json:"rollbacks"`
	FailedVersions  []string `json:"failed_versions"`
	LastUpgrade     *struct {
		At   int64  `json:"at"`
		From string `json:"from"`
		To   string `json:"to"`
	} `json:"last_upgrade"`
	LastFailure *struct {
		At            int64  `json:"at"`
		From          string `json:"from"`
		To            string `json:"to"`
		Step          string `json:"step"`
		Reason        string `json:"reason"`
		RolledBack    bool   `json:"rolled_back"`
		Restored      bool   `json:"restored"`
		RollbackError string `json:"rollback_error"`
		ClearedAt     int64  `json:"cleared_at"`
	} `json:"last_failure"`
	History []struct {
		At     int64  `json:"at"`
		From   string `json:"from"`
		To     string `json:"to"`
		Result string `json:"result"`
	} `json:"history"`
}

// Upgrade is one upgrade, done or failed.
type Upgrade struct {
	At   time.Time `json:"at"`
	From string    `json:"from"`
	To   string    `json:"to"`
}

// Failure is the last failed upgrade. Reason is fixed text from the script,
// with versions and counts: never page text (P1).
type Failure struct {
	Upgrade
	Step          string `json:"step"`
	Reason        string `json:"reason"`
	RolledBack    bool   `json:"rolled_back"`
	Restored      bool   `json:"restored"`
	RollbackError string `json:"rollback_error,omitempty"`
	// Cleared: an admin ran `auto-upgrade.sh clear` after looking: no more notice.
	Cleared bool `json:"cleared"`
}

// Done is a line of the history: result is "upgraded", "rolled-back" or
// "rollback-failed".
type Done struct {
	Upgrade
	Result string `json:"result"`
}

// Status is the automatic upgrades' state, for Administration and the metrics.
type Status struct {
	State string `json:"state"`
	// What the script is doing now: "idle", "checking", "verifying",
	// "backing-up", "upgrading", "health-check" or "rolling-back".
	Run             string     `json:"run"`
	RunningSince    *time.Time `json:"running_since"`
	EnabledAt       *time.Time `json:"enabled_at"`
	DisabledAt      *time.Time `json:"disabled_at"`
	LastCheckAt     *time.Time `json:"last_check_at"`
	LastCheckResult string     `json:"last_check_result,omitempty"`
	LastCheckNote   string     `json:"last_check_note,omitempty"`
	RunningVersion  string     `json:"running_version,omitempty"`
	LatestVersion   string     `json:"latest_version,omitempty"`
	LastUpgrade     *Upgrade   `json:"last_upgrade"`
	LastFailure     *Failure   `json:"last_failure"`
	Failures        int64      `json:"failures"`
	Rollbacks       int64      `json:"rollbacks"`
	FailedVersions  []string   `json:"failed_versions"`
	History         []Done     `json:"history"`
}

// Failing is whether an admin should be told, and the alert fire: the last
// upgrade failed (rolled back or not), or one stopped part way. The newest
// event counts: a later successful upgrade clears it.
func (s Status) Failing() bool {
	return s.State == StateFailed || s.State == StateStopped
}

func at(sec int64) *time.Time {
	if sec == 0 {
		return nil
	}
	t := time.Unix(sec, 0).UTC()
	return &t
}

// Read is the state at now. No status file (or none mounted: the folder is
// /dev/null when it's off) is "off".
func Read(path string, now time.Time) Status {
	off := Status{State: StateOff, Run: "idle", FailedVersions: []string{}, History: []Done{}}
	if path == "" {
		return off
	}
	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) || errors.Is(err, syscall.ENOTDIR) {
		return off
	}
	var f file
	if err == nil {
		err = json.Unmarshal(data, &f)
	}
	if err != nil {
		return Status{State: StateFailed, Run: "idle", FailedVersions: []string{}, History: []Done{},
			LastFailure: &Failure{Reason: "the automatic upgrades' status can't be read"}}
	}
	s := Status{
		Run: f.State, RunningSince: at(f.RunningSince), EnabledAt: at(f.EnabledAt), DisabledAt: at(f.DisabledAt),
		LastCheckAt: at(f.LastCheckAt), LastCheckResult: f.LastCheckResult, LastCheckNote: f.LastCheckNote,
		RunningVersion: version(f.RunningVersion), LatestVersion: version(f.LatestVersion), Failures: f.Failures, Rollbacks: f.Rollbacks,
		History: []Done{},
	}
	if s.Run = oneOf(f.State, Runs); s.Run == "" {
		s.Run = "idle"
	}
	s.FailedVersions = []string{}
	for _, v := range f.FailedVersions {
		if v = version(v); v != "" {
			s.FailedVersions = append(s.FailedVersions, v)
		}
	}
	if u := f.LastUpgrade; u != nil && u.At != 0 {
		s.LastUpgrade = &Upgrade{At: time.Unix(u.At, 0).UTC(), From: version(u.From), To: version(u.To)}
	}
	if l := f.LastFailure; l != nil && l.At != 0 {
		s.LastFailure = &Failure{Upgrade: Upgrade{At: time.Unix(l.At, 0).UTC(), From: version(l.From), To: version(l.To)}, Step: oneOf(l.Step, Steps),
			Reason: l.Reason, RolledBack: l.RolledBack, Restored: l.Restored, RollbackError: l.RollbackError, Cleared: l.ClearedAt != 0}
	}
	for _, h := range f.History {
		s.History = append(s.History, Done{Upgrade: Upgrade{At: time.Unix(h.At, 0).UTC(), From: version(h.From), To: version(h.To)}, Result: oneOf(h.Result, []string{"upgraded", "rolled-back", "rollback-failed"})})
	}
	failedLast := s.LastFailure != nil && !s.LastFailure.Cleared && (s.LastUpgrade == nil || !s.LastUpgrade.At.After(s.LastFailure.At))
	last := f.LastCheckAt
	if last == 0 {
		last = f.EnabledAt
	}
	switch {
	case !f.Enabled:
		s.State = StateOff
	case f.LastCheckResult == "interrupted":
		s.State = StateStopped
	case failedLast:
		s.State = StateFailed
	case last == 0 || now.Sub(time.Unix(last, 0)) > Stale:
		s.State = StateStale
	default:
		s.State = StateOK
	}
	return s
}

// Runs are what a run can be doing, as the script writes it.
var Runs = []string{"idle", "checking", "verifying", "backing-up", "upgrading", "health-check", "rolling-back"}

// Steps are where an upgrade can fail.
var Steps = []string{"upgrade", "health"}

var versionRE = regexp.MustCompile(`^[0-9A-Za-z.+-]{1,64}$`)

// version keeps only what looks like a version, so a label or a page never
// shows anything else from the file.
func version(v string) string {
	if versionRE.MatchString(v) {
		return v
	}
	return ""
}

func oneOf(v string, allowed []string) string {
	if slices.Contains(allowed, v) {
		return v
	}
	return ""
}
