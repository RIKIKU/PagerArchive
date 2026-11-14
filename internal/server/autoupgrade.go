package server

import (
	"net/http"

	"github.com/RIKIKU/PagerArchive/internal/autoupgrade"
)

// autoUpgradeStatus is what the host's automatic upgrades last did (R84h).
// The API only reads it: it never runs Docker or upgrades anything.
func (s *Server) autoUpgradeStatus() autoupgrade.Status {
	return autoupgrade.Read(s.AutoUpgradeStatusFile, s.Now())
}

// getAutoUpgrade is GET /api/v1/admin/auto-upgrade, for Administration →
// Upgrades. With no status file the state is "off".
func (s *Server) getAutoUpgrade(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, s.autoUpgradeStatus())
}

// autoUpgradeMetrics are R84h's metrics (R52), for the example monitoring
// stack's dashboard and its "Upgrade failed" alert. None while it's off.
func (s *Server) autoUpgradeMetrics(m *exposition) {
	if s.AutoUpgradeStatusFile == "" {
		return
	}
	u := s.autoUpgradeStatus()
	m.family("pagerarchive_auto_upgrade_enabled", "gauge", "1 if automatic upgrades are switched on (R84d).")
	m.sample("pagerarchive_auto_upgrade_enabled", nil, b2f(u.State != autoupgrade.StateOff))
	if u.State == autoupgrade.StateOff {
		return
	}
	if u.LastCheckAt != nil {
		m.family("pagerarchive_auto_upgrade_last_check_timestamp_seconds", "gauge", "When automatic upgrades last checked for a release (R84h).")
		m.sample("pagerarchive_auto_upgrade_last_check_timestamp_seconds", nil, float64(u.LastCheckAt.Unix()))
	}
	if u.LastUpgrade != nil {
		m.family("pagerarchive_auto_upgrade_last_success_timestamp_seconds", "gauge", "When the last automatic upgrade succeeded, and to which version (R84h).")
		m.sample("pagerarchive_auto_upgrade_last_success_timestamp_seconds", labels{"from", u.LastUpgrade.From, "to", u.LastUpgrade.To}, float64(u.LastUpgrade.At.Unix()))
	}
	if u.LastFailure != nil && !u.LastFailure.At.IsZero() {
		m.family("pagerarchive_auto_upgrade_last_failure_timestamp_seconds", "gauge", "When the last automatic upgrade failed, and to which version (R84h).")
		m.sample("pagerarchive_auto_upgrade_last_failure_timestamp_seconds", labels{"to", u.LastFailure.To, "step", u.LastFailure.Step}, float64(u.LastFailure.At.Unix()))
	}
	m.family("pagerarchive_auto_upgrade_failures_total", "counter", "Automatic upgrades that failed after they started (R84g).")
	m.sample("pagerarchive_auto_upgrade_failures_total", nil, float64(u.Failures))
	m.family("pagerarchive_auto_upgrade_rollbacks_total", "counter", "Automatic upgrades rolled back to the previous release (R84g).")
	m.sample("pagerarchive_auto_upgrade_rollbacks_total", nil, float64(u.Rollbacks))
	m.family("pagerarchive_auto_upgrade_failing", "gauge", "1 if the last automatic upgrade failed or was rolled back, or one stopped part way (R84h).")
	m.sample("pagerarchive_auto_upgrade_failing", nil, b2f(u.Failing()))
	m.family("pagerarchive_auto_upgrade_state", "gauge", "Automatic upgrades' state for an admin (ok, failed, stale, stopped) and what a run is doing now, 1 for the current one (R84h).")
	for _, st := range []string{autoupgrade.StateOK, autoupgrade.StateFailed, autoupgrade.StateStale, autoupgrade.StateStopped} {
		m.sample("pagerarchive_auto_upgrade_state", labels{"state", st}, b2f(u.State == st))
	}
	m.family("pagerarchive_auto_upgrade_run", "gauge", "What an automatic upgrade run is doing now, 1 for the current step (R84h).")
	for _, run := range autoupgrade.Runs {
		m.sample("pagerarchive_auto_upgrade_run", labels{"step", run}, b2f(u.Run == run))
	}
}
