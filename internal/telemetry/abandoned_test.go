package telemetry_test

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/RIKIKU/PagerArchive/internal/telemetry"
)

// R73a: only the request's own cancellation is an abandoned request.
func TestAbandoned(t *testing.T) {
	live := context.Background()
	gone, cancel := context.WithCancel(live)
	cancel()
	expired, stop := context.WithDeadline(live, time.Now().Add(-time.Second))
	defer stop()
	wrapped := fmt.Errorf("session: %w", context.Canceled)
	for _, c := range []struct {
		name string
		ctx  context.Context
		err  error
		want bool
	}{
		{"the request cancelled", gone, context.Canceled, true},
		{"the request cancelled, the error wrapped", gone, wrapped, true},
		{"cancelled by our own code, the request still live", live, context.Canceled, false},
		{"our own deadline", expired, context.DeadlineExceeded, false},
		{"the request cancelled, but another failure", gone, errors.New("SQLSTATE 23505"), false},
		{"no error", gone, nil, false},
	} {
		if got := telemetry.Abandoned(c.ctx, c.err); got != c.want {
			t.Errorf("%s: Abandoned = %v, want %v", c.name, got, c.want)
		}
	}
}

// R73a: an abandoned request is counted once, as 499, whatever it wrote,
// and never as a server error.
func TestAbandonCountsOnce(t *testing.T) {
	m := &telemetry.RequestMetrics{}
	h := &telemetry.HTTP{Metrics: m}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /a/{id}", func(w http.ResponseWriter, r *http.Request) {
		telemetry.Abandon(r)
		telemetry.Abandon(r) // twice: still one request
		w.WriteHeader(http.StatusInternalServerError)
	})
	mux.HandleFunc("GET /b", func(w http.ResponseWriter, r *http.Request) {})
	srv := h.Handler(mux)
	for _, p := range []string{"/a/1", "/a/2", "/b"} {
		srv.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, p, nil))
	}
	got := m.Abandoned()
	if len(got) != 1 || got["GET /a/{id}"] != 2 {
		t.Errorf("abandoned %v, want GET /a/{id} 2 and nothing else", got)
	}
	for _, s := range m.Snapshot() {
		if s.Status >= 500 {
			t.Errorf("%s counted as %d", s.Route, s.Status)
		}
		if s.Route == "GET /a/{id}" && (s.Status != telemetry.StatusClientClosedRequest || s.Count != 2) {
			t.Errorf("%s: %d requests as %d, want 2 as 499", s.Route, s.Count, s.Status)
		}
	}
}
