package server

import (
	"context"
	"crypto/subtle"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"

	"github.com/RIKIKU/PagerArchive/internal/integrations"
	"github.com/RIKIKU/PagerArchive/internal/store"
)

// LockCounter counts accounts locked by the guessing protection (R44a).
type LockCounter interface {
	Locked(ctx context.Context) (int, error)
}

// pageCounts counts posted pages by outcome since the API started (R52).
type pageCounts struct {
	mu sync.Mutex
	n  map[string]uint64
}

func (c *pageCounts) add(outcome string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.n == nil {
		c.n = map[string]uint64{}
	}
	c.n[outcome]++
}

func (c *pageCounts) get(outcome string) uint64 {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.n[outcome]
}

// metrics is the Prometheus endpoint for external monitoring (R52, A32).
// It's off unless METRICS_TOKEN is set, needs that token as a bearer
// token, and never has page text, alias, integration or user names in it.
func (s *Server) metrics(w http.ResponseWriter, r *http.Request) {
	if s.MetricsToken == "" {
		problem(w, http.StatusNotFound, "metrics are off: set METRICS_TOKEN")
		return
	}
	token, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
	if !ok || subtle.ConstantTimeCompare([]byte(token), []byte(s.MetricsToken)) != 1 {
		w.Header().Set("WWW-Authenticate", `Bearer realm="metrics"`)
		problem(w, http.StatusUnauthorized, "metrics need the bearer token")
		return
	}
	ctx := r.Context()
	var b strings.Builder
	m := &exposition{w: &b}

	m.family("pagerarchive_build_info", "gauge", "The running version.")
	m.sample("pagerarchive_build_info", labels{"version", s.Version}, 1)

	clients, err := s.Store.ListClients(ctx)
	if err != nil {
		s.Log.Error("metrics: clients", "error", store.SafeError(err))
		problem(w, http.StatusInternalServerError, "metrics failed")
		return
	}
	now := s.Now()
	m.family("pagerarchive_client_last_seen_timestamp_seconds", "gauge", "When each receiving client last sent a page of any kind (R39).")
	for _, c := range clients {
		if c.RevokedAt == nil && c.LastSeenAt != nil {
			m.sample("pagerarchive_client_last_seen_timestamp_seconds", labels{"client", c.ID}, float64(c.LastSeenAt.Unix()))
		}
	}
	m.family("pagerarchive_client_receiving", "gauge", "1 if the client sent a page in the last 30 minutes, 0 if it's not receiving (R39).")
	for _, c := range clients {
		if c.RevokedAt == nil {
			m.sample("pagerarchive_client_receiving", labels{"client", c.ID}, b2f(receiving(c, now)))
		}
	}
	m.family("pagerarchive_client_clock_ahead", "gauge", "1 if the client's last page was refused because its clock is ahead.")
	for _, c := range clients {
		if c.RevokedAt == nil {
			m.sample("pagerarchive_client_clock_ahead", labels{"client", c.ID}, b2f(c.ClockAheadSince != nil))
		}
	}

	m.family("pagerarchive_pages_received_total", "counter", "Pages posted by clients since the API started, by outcome.")
	for _, o := range []string{"stored", "duplicate", "ignored", "clock_ahead"} {
		m.sample("pagerarchive_pages_received_total", labels{"outcome", o}, float64(s.pages.get(o)))
	}

	flags, err := s.Store.CountFlagged(ctx)
	if err != nil {
		s.Log.Error("metrics: flags", "error", store.SafeError(err))
		problem(w, http.StatusInternalServerError, "metrics failed")
		return
	}
	m.family("pagerarchive_messages_flagged", "gauge", "Stored messages with each admin flag now.")
	m.sample("pagerarchive_messages_flagged", labels{"flag", "parse_failed"}, float64(flags.ParseFailed))
	m.sample("pagerarchive_messages_flagged", labels{"flag", "address_unsplit"}, float64(flags.AddressUnsplit))
	m.sample("pagerarchive_messages_flagged", labels{"flag", "location_mismatch"}, float64(flags.LocationMismatch))

	m.family("pagerarchive_integration_deliveries_total", "counter", "Integration deliveries since the API started, by destination kind and result.")
	var counts map[[2]string]uint64
	if s.Integrations != nil {
		counts = s.Integrations.Stats.Counts()
	}
	for _, k := range integrations.Kinds {
		for _, res := range []string{"delivered", "failed", "too_old", "cancelled"} {
			m.sample("pagerarchive_integration_deliveries_total", labels{"kind", string(k), "result", res}, float64(counts[[2]string{string(k), res}]))
		}
	}
	byStatus, err := s.Store.CountIntegrationsByStatus(ctx)
	if err != nil {
		s.Log.Error("metrics: integrations", "error", store.SafeError(err))
		problem(w, http.StatusInternalServerError, "metrics failed")
		return
	}
	n := map[string]int64{}
	for _, r := range byStatus {
		n[r.Status] = r.N
	}
	m.family("pagerarchive_integrations", "gauge", "Integrations by status.")
	m.sample("pagerarchive_integrations", labels{"status", "active"}, float64(n["active"]))
	m.sample("pagerarchive_integrations", labels{"status", "paused"}, float64(n["paused"]))

	if s.Locks != nil {
		if locked, err := s.Locks.Locked(ctx); err != nil {
			s.Log.Warn("metrics: locked accounts unavailable", "error", store.SafeError(err))
		} else {
			m.family("pagerarchive_accounts_locked", "gauge", "Accounts locked now by the guessing protection (R44a).")
			m.sample("pagerarchive_accounts_locked", nil, float64(locked))
		}
	}

	if s.Nightly != nil {
		if last, err := s.Nightly.LastResult(ctx); err == nil && last != nil {
			m.family("pagerarchive_nightly_last_run_timestamp_seconds", "gauge", "When the nightly retention and redaction run last ran.")
			m.sample("pagerarchive_nightly_last_run_timestamp_seconds", nil, float64(last.At.Unix()))
			m.family("pagerarchive_nightly_last_run_success", "gauge", "1 if the last nightly run succeeded.")
			m.sample("pagerarchive_nightly_last_run_success", nil, b2f(last.OK))
			m.family("pagerarchive_nightly_last_run_deleted", "gauge", "Messages the last nightly run deleted (R45).")
			m.sample("pagerarchive_nightly_last_run_deleted", nil, float64(last.Deleted))
			m.family("pagerarchive_nightly_last_run_redacted", "gauge", "Messages the last nightly run redacted again (P1).")
			m.sample("pagerarchive_nightly_last_run_redacted", nil, float64(last.Redacted))
		}
	}

	w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
	_, _ = io.WriteString(w, b.String())
}

func b2f(b bool) float64 {
	if b {
		return 1
	}
	return 0
}

// labels are name, value pairs.
type labels []string

// exposition writes the Prometheus text format.
type exposition struct{ w io.Writer }

func (e *exposition) family(name, typ, help string) {
	fmt.Fprintf(e.w, "# HELP %s %s\n# TYPE %s %s\n", name, help, name, typ)
}

var labelEscaper = strings.NewReplacer(`\`, `\\`, `"`, `\"`, "\n", `\n`)

func (e *exposition) sample(name string, l labels, v float64) {
	var parts []string
	for i := 0; i+1 < len(l); i += 2 {
		parts = append(parts, fmt.Sprintf(`%s="%s"`, l[i], labelEscaper.Replace(l[i+1])))
	}
	sort.Strings(parts)
	val := strconv.FormatFloat(v, 'f', -1, 64) // no exponents: timestamps keep every digit
	if len(parts) > 0 {
		fmt.Fprintf(e.w, "%s{%s} %s\n", name, strings.Join(parts, ","), val)
	} else {
		fmt.Fprintf(e.w, "%s %s\n", name, val)
	}
}
