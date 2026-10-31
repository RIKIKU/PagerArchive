package server

import (
	"context"
	"crypto/subtle"
	"fmt"
	"io"
	"net/http"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/RIKIKU/PagerArchive/internal/integrations"
	"github.com/RIKIKU/PagerArchive/internal/store"
	"github.com/RIKIKU/PagerArchive/internal/telemetry"
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
	// OpenMetrics when the scraper asks for it (Alloy and Prometheus do):
	// the same samples, plus exemplars linking request durations to traces.
	om := strings.Contains(r.Header.Get("Accept"), "application/openmetrics-text")
	m := &exposition{w: &b, om: om}

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

	s.loadMetrics(m)

	if om {
		b.WriteString("# EOF\n")
		w.Header().Set("Content-Type", "application/openmetrics-text; version=1.0.0; charset=utf-8")
	} else {
		w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
	}
	_, _ = io.WriteString(w, b.String())
}

// loadMetrics are requests and load (R72): requests by route and status,
// with their durations; the database pool; open live-update connections;
// and the Go runtime's memory and CPU. Labels are route patterns and status
// codes only, so their number stays bounded, and nothing names a user.
func (s *Server) loadMetrics(m *exposition) {
	series := s.Requests.Snapshot()
	m.family("pagerarchive_http_requests_total", "counter", "API requests since the API started, by route pattern and status.")
	for _, r := range series {
		m.sample("pagerarchive_http_requests_total", labels{"route", r.Route, "status", strconv.Itoa(r.Status)}, float64(r.Count))
	}
	m.family("pagerarchive_http_request_duration_seconds", "histogram", "How long API requests took, by route pattern and status. A live-update stream counts until it's set up.")
	for _, r := range series {
		l := labels{"route", r.Route, "status", strconv.Itoa(r.Status)}
		for i, le := range telemetry.Buckets {
			m.bucket("pagerarchive_http_request_duration_seconds", l, strconv.FormatFloat(le, 'f', -1, 64), r.Buckets[i], r.Exemplars[i])
		}
		m.bucket("pagerarchive_http_request_duration_seconds", l, "+Inf", r.Buckets[len(telemetry.Buckets)], r.Exemplars[len(telemetry.Buckets)])
		m.sample("pagerarchive_http_request_duration_seconds_sum", l, r.Sum)
		m.sample("pagerarchive_http_request_duration_seconds_count", l, float64(r.Count))
	}

	if s.Store != nil && s.Store.Pool != nil {
		st := s.Store.Pool.Stat()
		m.family("pagerarchive_db_pool_connections", "gauge", "Database connections in the API's pool, by state.")
		m.sample("pagerarchive_db_pool_connections", labels{"state", "in_use"}, float64(st.AcquiredConns()))
		m.sample("pagerarchive_db_pool_connections", labels{"state", "idle"}, float64(st.IdleConns()))
		m.sample("pagerarchive_db_pool_connections", labels{"state", "constructing"}, float64(st.ConstructingConns()))
		m.family("pagerarchive_db_pool_max_connections", "gauge", "The most connections the pool will open.")
		m.sample("pagerarchive_db_pool_max_connections", nil, float64(st.MaxConns()))
		m.family("pagerarchive_db_pool_acquires_total", "counter", "Connections taken from the pool since the API started.")
		m.sample("pagerarchive_db_pool_acquires_total", nil, float64(st.AcquireCount()))
		m.family("pagerarchive_db_pool_waited_acquires_total", "counter", "Connections taken that had to wait for one to be free or opened.")
		m.sample("pagerarchive_db_pool_waited_acquires_total", nil, float64(st.EmptyAcquireCount()))
		m.family("pagerarchive_db_pool_acquire_seconds_total", "counter", "Time spent taking connections from the pool.")
		m.sample("pagerarchive_db_pool_acquire_seconds_total", nil, st.AcquireDuration().Seconds())
	}

	if s.Hub != nil {
		m.family("pagerarchive_live_connections", "gauge", "Open live-update connections: browsers' lists and PowerShell watches (R32, R63b).")
		m.sample("pagerarchive_live_connections", nil, float64(s.Hub.Len()))
	}

	var mem runtime.MemStats
	runtime.ReadMemStats(&mem)
	m.family("go_goroutines", "gauge", "Goroutines that exist now.")
	m.sample("go_goroutines", nil, float64(runtime.NumGoroutine()))
	m.family("go_memstats_heap_alloc_bytes", "gauge", "Bytes of allocated heap objects.")
	m.sample("go_memstats_heap_alloc_bytes", nil, float64(mem.HeapAlloc))
	m.family("go_memstats_heap_inuse_bytes", "gauge", "Bytes in in-use heap spans.")
	m.sample("go_memstats_heap_inuse_bytes", nil, float64(mem.HeapInuse))
	m.family("go_memstats_stack_inuse_bytes", "gauge", "Bytes in stack spans.")
	m.sample("go_memstats_stack_inuse_bytes", nil, float64(mem.StackInuse))
	m.family("go_memstats_sys_bytes", "gauge", "Bytes of memory the Go runtime has from the operating system.")
	m.sample("go_memstats_sys_bytes", nil, float64(mem.Sys))
	m.family("go_gc_cycles_total", "counter", "Completed garbage collection cycles.")
	m.sample("go_gc_cycles_total", nil, float64(mem.NumGC))
	m.family("go_gc_pause_seconds_total", "counter", "Time the program was paused for garbage collection.")
	m.sample("go_gc_pause_seconds_total", nil, float64(mem.PauseTotalNs)/1e9)
	if p, ok := readProcess(); ok {
		m.family("process_cpu_seconds_total", "counter", "User and system CPU time the API has used.")
		m.sample("process_cpu_seconds_total", nil, p.cpuSeconds)
		m.family("process_resident_memory_bytes", "gauge", "Resident memory of the API process.")
		m.sample("process_resident_memory_bytes", nil, float64(p.residentBytes))
	}
	m.family("process_start_time_seconds", "gauge", "When the API started, as a Unix time.")
	m.sample("process_start_time_seconds", nil, float64(started.UnixNano())/1e9)
}

var started = time.Now()

func b2f(b bool) float64 {
	if b {
		return 1
	}
	return 0
}

// labels are name, value pairs.
type labels []string

// exposition writes the Prometheus text format, or OpenMetrics (om).
type exposition struct {
	w  io.Writer
	om bool
}

func (e *exposition) family(name, typ, help string) {
	if e.om && typ == "counter" {
		// OpenMetrics names a counter's family without _total; its samples
		// keep it, so the series are the same in Prometheus.
		name = strings.TrimSuffix(name, "_total")
	}
	fmt.Fprintf(e.w, "# HELP %s %s\n# TYPE %s %s\n", name, help, name, typ)
}

var labelEscaper = strings.NewReplacer(`\`, `\\`, `"`, `\"`, "\n", `\n`)

func formatLabels(l labels) string {
	var parts []string
	for i := 0; i+1 < len(l); i += 2 {
		parts = append(parts, fmt.Sprintf(`%s="%s"`, l[i], labelEscaper.Replace(l[i+1])))
	}
	sort.Strings(parts)
	return strings.Join(parts, ",")
}

func formatValue(v float64) string {
	return strconv.FormatFloat(v, 'f', -1, 64) // no exponents: timestamps keep every digit
}

func (e *exposition) sample(name string, l labels, v float64) {
	if len(l) > 0 {
		fmt.Fprintf(e.w, "%s{%s} %s\n", name, formatLabels(l), formatValue(v))
	} else {
		fmt.Fprintf(e.w, "%s %s\n", name, formatValue(v))
	}
}

// bucket writes a histogram bucket, with its exemplar in OpenMetrics: the
// trace of the latest sampled request that fell in it.
func (e *exposition) bucket(name string, l labels, le string, n uint64, ex telemetry.Exemplar) {
	fmt.Fprintf(e.w, "%s_bucket{%s} %d", name, formatLabels(append(append(labels{}, l...), "le", le)), n)
	if e.om && ex.TraceID != "" {
		fmt.Fprintf(e.w, ` # {trace_id="%s"} %s %.3f`, ex.TraceID, formatValue(ex.Value), float64(ex.At.UnixMilli())/1000)
	}
	fmt.Fprintln(e.w)
}
