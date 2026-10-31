package telemetry

import (
	"cmp"
	"slices"
	"sync"
	"time"
)

// Buckets are the request duration histogram's upper bounds, in seconds.
var Buckets = []float64{0.005, 0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1, 2.5, 5, 10}

// RequestMetrics counts requests and their durations by route and status
// for /metrics (R72). Routes are the mux's patterns and statuses the codes
// the handlers write, so the label values are bounded; nothing about the
// user is a label.
type RequestMetrics struct {
	mu     sync.Mutex
	series map[routeKey]*RouteSeries
}

type routeKey struct {
	route  string
	status int
}

// RouteSeries is one route and status's counts.
type RouteSeries struct {
	Route  string
	Status int
	Count  uint64
	Sum    float64 // seconds
	// Buckets counts requests no slower than Buckets[i] (cumulative);
	// the last is +Inf, so it equals Count.
	Buckets [12]uint64
	// Exemplars are the latest sampled request in each bucket, linking the
	// histogram to a trace.
	Exemplars [12]Exemplar
}

// Exemplar is one request's trace and duration.
type Exemplar struct {
	TraceID string
	Value   float64
	At      time.Time
}

// Observe counts one request. traceID is "" for a request that wasn't
// sampled.
func (m *RequestMetrics) Observe(route string, status int, d time.Duration, traceID string) {
	sec := d.Seconds()
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.series == nil {
		m.series = map[routeKey]*RouteSeries{}
	}
	k := routeKey{route, status}
	s := m.series[k]
	if s == nil {
		s = &RouteSeries{Route: route, Status: status}
		m.series[k] = s
	}
	s.Count++
	s.Sum += sec
	first := len(Buckets)
	for i := len(Buckets) - 1; i >= 0 && sec <= Buckets[i]; i-- {
		first = i
	}
	for i := first; i < len(s.Buckets); i++ {
		s.Buckets[i]++
	}
	if traceID != "" {
		s.Exemplars[first] = Exemplar{TraceID: traceID, Value: sec, At: time.Now()}
	}
}

// Snapshot is every series, sorted by route and status.
func (m *RequestMetrics) Snapshot() []RouteSeries {
	if m == nil {
		return nil
	}
	m.mu.Lock()
	out := make([]RouteSeries, 0, len(m.series))
	for _, s := range m.series {
		out = append(out, *s)
	}
	m.mu.Unlock()
	slices.SortFunc(out, func(a, b RouteSeries) int {
		return cmp.Or(cmp.Compare(a.Route, b.Route), cmp.Compare(a.Status, b.Status))
	})
	return out
}
