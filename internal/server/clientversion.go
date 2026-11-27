package server

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/RIKIKU/PagerArchive/internal/autoupgrade"
	"github.com/RIKIKU/PagerArchive/internal/store"
	"github.com/RIKIKU/PagerArchive/internal/store/db"
)

// Client versions (R14b, R14c) and client hosts' automatic upgrades (R84n-R84p,
// ADR 0054). A client says its version in its User-Agent
// (pagerarchive-client/<version>) on every post, and its host's automatic
// upgrades say it again in their report. The server keeps the last one and
// since when it's been an older release than its own ("behind"), for the
// metrics, the Clients dashboard and the "Client behind the server" alert.

// clientVersionRE is a version a client may say: a release's X.Y.Z, or a
// build of main's X.Y.Z-dev+<commit> (12.1). Anything else is ignored, so a
// label never shows anything else a client sent (R84k).
var clientVersionRE = regexp.MustCompile(`^[0-9]{1,9}\.[0-9]{1,9}\.[0-9]{1,9}(-dev\+[0-9a-f]{7,40})?$`)

var releaseRE = regexp.MustCompile(`^[0-9]{1,9}\.[0-9]{1,9}\.[0-9]{1,9}$`)

// ClientUserAgent is the User-Agent a client sends: its version after the slash.
const ClientUserAgent = "pagerarchive-client/"

// checkedVersion is v if it's a version a client may say, else "".
func checkedVersion(v string) string {
	if clientVersionRE.MatchString(v) {
		return v
	}
	return ""
}

// userAgentVersion is the version in a client's User-Agent, or "": only
// pagerarchive-client/<version>, alone or followed by a space and more.
func userAgentVersion(ua string) string {
	rest, ok := strings.CutPrefix(ua, ClientUserAgent)
	if !ok {
		return ""
	}
	v, _, _ := strings.Cut(rest, " ")
	return checkedVersion(v)
}

func releaseParts(v string) ([3]int, bool) {
	var p [3]int
	if !releaseRE.MatchString(v) {
		return p, false
	}
	for i, s := range strings.SplitN(v, ".", 3) {
		n, err := strconv.Atoi(s)
		if err != nil {
			return p, false
		}
		p[i] = n
	}
	return p, true
}

// Behind is R14b's "behind": the client runs a release older than the
// server's release. A build that isn't a release, on either side, is never
// behind or ahead of anything.
func Behind(client, server string) bool {
	c, ok1 := releaseParts(client)
	s, ok2 := releaseParts(server)
	if !ok1 || !ok2 {
		return false
	}
	for i := range c {
		if c[i] != s[i] {
			return c[i] < s[i]
		}
	}
	return false
}

// clientVersions is what the server last stored for each client, so a page
// post writes only when a client's version changes.
type clientVersions struct {
	mu   sync.Mutex
	seen map[string]string
}

func (c *clientVersions) changed(id, v string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.seen == nil {
		c.seen = map[string]string{}
	}
	if c.seen[id] == v {
		return false
	}
	c.seen[id] = v
	return true
}

func (c *clientVersions) forget(id string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.seen, id)
}

// noteClientVersion stores the version a client says it runs, when it's
// changed, and whether it's behind: a client that was behind keeps its
// "since" while it stays behind, even across its own upgrades (R14c).
func (s *Server) noteClientVersion(ctx context.Context, id, v string) {
	if v == "" || !s.versions.changed(id, v) {
		return
	}
	if err := s.Store.SetClientVersion(ctx, db.SetClientVersionParams{ID: id, Version: v, Behind: Behind(v, s.Version)}); err != nil {
		s.versions.forget(id)
		if !errors.Is(err, context.Canceled) {
			s.Log.ErrorContext(ctx, "client version", "client", id, "error", store.SafeError(err))
		}
	}
}

// RecheckClientVersions works out again, at start-up, which clients are
// behind: the server's own version may have changed (R14c).
func (s *Server) RecheckClientVersions(ctx context.Context) error {
	clients, err := s.Store.ListClients(ctx)
	if err != nil {
		return err
	}
	behind := 0
	for _, c := range clients {
		if c.Version == nil {
			continue
		}
		v := checkedVersion(*c.Version)
		if v == "" {
			continue
		}
		b := Behind(v, s.Version)
		if b {
			behind++
		}
		if b != (c.BehindSince != nil) {
			if err := s.Store.SetClientVersion(ctx, db.SetClientVersionParams{ID: c.ID, Version: v, Behind: b}); err != nil {
				return err
			}
		}
		s.versions.changed(c.ID, v)
	}
	if behind > 0 {
		s.Log.Info("clients behind the server's version", "count", behind, "version", s.Version)
	}
	return nil
}

// ClientUpgradeReport is the body of POST /api/v1/client/auto-upgrade: what
// the client host's automatic upgrades did, as their status file says
// (R84p), and the client's version.
type ClientUpgradeReport struct {
	ClientVersion string          `json:"client_version"`
	Status        json.RawMessage `json:"status"`
}

// ClientUpgradeAnswer is the answer: the server's version, which a client is
// never upgraded past (R14b, R84n).
type ClientUpgradeAnswer struct {
	ClientID      string `json:"client_id"`
	ServerVersion string `json:"server_version"`
}

// postClientAutoUpgrade is POST /api/v1/client/auto-upgrade, with the
// client's own key: its host's automatic upgrades report, and learn the
// server's version. Fields it doesn't know are ignored, so a newer client's
// report still works with an older server in the same major version (R14b).
func (s *Server) postClientAutoUpgrade(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	id, ok := s.authenticate(ctx, w, r)
	if !ok {
		return
	}
	var req ClientUpgradeReport
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10)).Decode(&req); err != nil {
		// Never echoed or logged: the body is the client host's.
		problem(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	var report []byte
	if len(req.Status) > 0 && string(req.Status) != "null" {
		var err error
		if report, err = autoupgrade.Sanitize(req.Status); err != nil {
			problem(w, http.StatusUnprocessableEntity, "status isn't the automatic upgrades' status")
			return
		}
	}
	s.noteClientVersion(ctx, id, checkedVersion(req.ClientVersion))
	if report != nil {
		if err := s.Store.SetClientAutoUpgrade(ctx, db.SetClientAutoUpgradeParams{ID: id, Report: report}); err != nil {
			if s.abandoned(w, r, err) {
				return
			}
			s.Log.ErrorContext(ctx, "client auto-upgrade report", "client", id, "error", store.SafeError(err))
			problem(w, http.StatusInternalServerError, "couldn't keep the report")
			return
		}
	}
	writeJSON(w, http.StatusOK, ClientUpgradeAnswer{ClientID: id, ServerVersion: s.Version})
}

// clientVersionMetrics are R14c's and R84p's metrics (R52), one series per
// client that's said: its version, since when it's been behind, and what its
// host's automatic upgrades last reported.
func (s *Server) clientVersionMetrics(m *exposition, clients []db.ListClientsRow, now time.Time) {
	type reported struct {
		id string
		at time.Time
		u  autoupgrade.Status
	}
	var reports []reported
	m.family("pagerarchive_client_version_info", "gauge", "The version each receiving client last said it runs, as its label (R14c).")
	for _, c := range clients {
		if c.RevokedAt != nil {
			continue
		}
		if c.Version != nil && checkedVersion(*c.Version) != "" {
			m.sample("pagerarchive_client_version_info", labels{"client", c.ID, "version", *c.Version}, 1)
		}
		if c.AutoUpgrade != nil && c.AutoUpgradeAt != nil {
			reports = append(reports, reported{c.ID, *c.AutoUpgradeAt, autoupgrade.Parse(c.AutoUpgrade, now)})
		}
	}
	m.family("pagerarchive_client_behind_since_timestamp_seconds", "gauge", "Since when each receiving client has run an older release than the server's; none for one that isn't behind (R14b, R14c).")
	for _, c := range clients {
		if c.RevokedAt == nil && c.BehindSince != nil {
			m.sample("pagerarchive_client_behind_since_timestamp_seconds", labels{"client", c.ID}, float64(c.BehindSince.Unix()))
		}
	}
	if len(reports) == 0 {
		return
	}
	each := func(name, kind, help string, f func(r reported)) {
		m.family(name, kind, help)
		for _, r := range reports {
			f(r)
		}
	}
	each("pagerarchive_client_auto_upgrade_reported_timestamp_seconds", "gauge", "When the client's host last reported its automatic upgrades (R84p).", func(r reported) {
		m.sample("pagerarchive_client_auto_upgrade_reported_timestamp_seconds", labels{"client", r.id}, float64(r.at.Unix()))
	})
	each("pagerarchive_client_auto_upgrade_enabled", "gauge", "1 if the client's host has automatic upgrades switched on (R84n).", func(r reported) {
		m.sample("pagerarchive_client_auto_upgrade_enabled", labels{"client", r.id}, b2f(r.u.State != autoupgrade.StateOff))
	})
	each("pagerarchive_client_auto_upgrade_last_check_timestamp_seconds", "gauge", "When the client's host last checked for a release: a run's start (R84p).", func(r reported) {
		if r.u.LastCheckAt != nil {
			m.sample("pagerarchive_client_auto_upgrade_last_check_timestamp_seconds", labels{"client", r.id}, float64(r.u.LastCheckAt.Unix()))
		}
	})
	each("pagerarchive_client_auto_upgrade_last_check_result", "gauge", "The result of the client host's last check, as its label: 1 for it (R84p).", func(r reported) {
		if res := r.u.LastCheckResult; res != "" {
			m.sample("pagerarchive_client_auto_upgrade_last_check_result", labels{"client", r.id, "result", res}, 1)
		}
	})
	each("pagerarchive_client_auto_upgrade_last_success_timestamp_seconds", "gauge", "When the client's last automatic upgrade succeeded, and between which versions (R84p).", func(r reported) {
		if u := r.u.LastUpgrade; u != nil {
			m.sample("pagerarchive_client_auto_upgrade_last_success_timestamp_seconds", labels{"client", r.id, "from", u.From, "to", u.To}, float64(u.At.Unix()))
		}
	})
	each("pagerarchive_client_auto_upgrade_failures_total", "counter", "The client's automatic upgrades that failed after they started (R84o).", func(r reported) {
		m.sample("pagerarchive_client_auto_upgrade_failures_total", labels{"client", r.id}, float64(r.u.Failures))
	})
	each("pagerarchive_client_auto_upgrade_failing", "gauge", "1 if the client's last automatic upgrade failed or was rolled back, or one stopped part way (R84p).", func(r reported) {
		m.sample("pagerarchive_client_auto_upgrade_failing", labels{"client", r.id}, b2f(r.u.Failing()))
	})
	each("pagerarchive_client_auto_upgrade_run", "gauge", "What the client host's automatic upgrade run is doing now, 1 for its step; none while idle (R84p).", func(r reported) {
		if r.u.Run != "idle" {
			m.sample("pagerarchive_client_auto_upgrade_run", labels{"client", r.id, "step", r.u.Run}, 1)
		}
	})
	each("pagerarchive_client_auto_upgrade_run_start_timestamp_seconds", "gauge", "When the client host's run going now started; none while idle (R84p).", func(r reported) {
		if r.u.Run != "idle" && r.u.RunningSince != nil {
			m.sample("pagerarchive_client_auto_upgrade_run_start_timestamp_seconds", labels{"client", r.id}, float64(r.u.RunningSince.Unix()))
		}
	})
	each("pagerarchive_client_auto_upgrade_target_info", "gauge", "The release the client host's run going now is upgrading to, as its label; none while idle (R84p).", func(r reported) {
		if r.u.Run != "idle" && r.u.TargetVersion != "" {
			m.sample("pagerarchive_client_auto_upgrade_target_info", labels{"client", r.id, "to", r.u.TargetVersion}, 1)
		}
	})
}
