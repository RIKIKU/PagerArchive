package telemetry

import (
	"context"
	"net/http"
	"strconv"
	"strings"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/trace"
)

// TraceContext reads and writes W3C traceparent. Baggage is never read or
// sent: it could carry anything into a span (R70).
var TraceContext = propagation.TraceContext{}

// Methods are the request methods a span or metric names; others are _OTHER.
var Methods = []string{http.MethodGet, http.MethodHead, http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete, http.MethodOptions}

// HTTP traces and counts the API's requests (R72): a server span per
// request, named by the mux's route pattern, never the path or query
// (R70). Incoming trace context is ignored, except on requests that
// authenticate as a receiving client (ContinueRequest, R70).
type HTTP struct {
	Tracer  trace.Tracer
	Metrics *RequestMetrics
	// Deferred says the handler starts the request's span itself, once it
	// knows who's asking (ContinueRequest): the clients' page posts.
	Deferred func(*http.Request) bool
	Now      func() time.Time
}

type stateKey struct{}

// request is one request's span, until it ends.
type request struct {
	start time.Time
	span  trace.Span // nil until started
	ended bool
}

// Handler wraps next, which must be the router (or wrap it without
// copying the request), so the request's Pattern is set after it runs.
func (h *HTTP) Handler(next http.Handler) http.Handler {
	tracer, now := Or(h.Tracer), h.Now
	if now == nil {
		now = time.Now
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		st := &request{start: now()}
		ctx := context.WithValue(r.Context(), stateKey{}, st)
		if h.Deferred == nil || !h.Deferred(r) {
			// A new trace, whatever headers came with it.
			ctx, st.span = tracer.Start(ctx, "HTTP "+method(r), trace.WithNewRoot(), trace.WithSpanKind(trace.SpanKindServer), trace.WithTimestamp(st.start))
		}
		r = r.WithContext(ctx)
		sw := &statusWriter{ResponseWriter: w}
		next.ServeHTTP(sw, r)
		if !st.ended {
			h.finish(tracer, st, r, sw.code(), now())
		}
	})
}

// ContinueRequest starts a deferred request's span: as a child of the
// trace the request carries if client is true (it authenticated as a
// receiving client, R14), or as a new trace. It returns the request with
// the span in its context.
func ContinueRequest(t trace.Tracer, r *http.Request, client bool) *http.Request {
	st, _ := r.Context().Value(stateKey{}).(*request)
	if st == nil || st.span != nil {
		return r
	}
	ctx := r.Context()
	opts := []trace.SpanStartOption{trace.WithSpanKind(trace.SpanKindServer), trace.WithTimestamp(st.start)}
	if client {
		ctx = TraceContext.Extract(ctx, propagation.HeaderCarrier(r.Header))
	} else {
		opts = append(opts, trace.WithNewRoot())
	}
	ctx, st.span = Or(t).Start(ctx, "HTTP "+method(r), opts...)
	return r.WithContext(ctx)
}

// EndRequest ends the request's span and counts it now, with status: a
// live-update stream, once it's set up, so a connection that stays open
// for hours isn't an hours-long span (R72, ADR 0013 §6).
func (h *HTTP) EndRequest(r *http.Request, status int) {
	if h == nil {
		return
	}
	st, _ := r.Context().Value(stateKey{}).(*request)
	if st == nil || st.ended {
		return
	}
	now := h.Now
	if now == nil {
		now = time.Now
	}
	h.finish(Or(h.Tracer), st, r, status, now())
}

func (h *HTTP) finish(tracer trace.Tracer, st *request, r *http.Request, status int, end time.Time) {
	st.ended = true
	route := Route(r)
	if st.span == nil {
		_, st.span = tracer.Start(r.Context(), "HTTP "+method(r), trace.WithNewRoot(), trace.WithSpanKind(trace.SpanKindServer), trace.WithTimestamp(st.start))
	}
	span := st.span
	span.SetName(route)
	span.SetAttributes(attribute.String("http.request.method", method(r)), attribute.String("http.route", route), attribute.Int("http.response.status_code", status))
	if status >= 500 {
		span.SetAttributes(attribute.String("error.type", strconv.Itoa(status)))
		span.SetStatus(codes.Error, "")
	}
	span.End(trace.WithTimestamp(end))
	if h.Metrics != nil {
		var traceID string
		if sc := span.SpanContext(); sc.IsSampled() {
			traceID = sc.TraceID().String()
		}
		h.Metrics.Observe(route, status, end.Sub(st.start), traceID)
	}
}

// SetUser puts the signed-in user's number and role on the request's span:
// nothing else about them (R70).
func SetUser(ctx context.Context, id int64, role string) {
	trace.SpanFromContext(ctx).SetAttributes(attribute.Int64("user.id", id), attribute.String("user.role", role))
}

// Route names a request by the pattern the mux matched, with its method:
// "GET /api/v1/messages/{id}" (R70). A request nothing matched is
// "<method> unmatched".
func Route(r *http.Request) string {
	p := r.Pattern
	switch {
	case p == "":
		return method(r) + " unmatched"
	case !strings.Contains(p, " "):
		return method(r) + " " + p
	}
	return p
}

// RouteNames are the span names a router's patterns can produce, for
// AllowSpanNames.
func RouteNames(patterns []string) []string {
	var out []string
	for _, m := range append(Methods, "_OTHER") {
		out = append(out, m+" unmatched", "HTTP "+m)
	}
	for _, p := range patterns {
		if strings.Contains(p, " ") {
			out = append(out, p)
			continue
		}
		for _, m := range append(Methods, "_OTHER") {
			out = append(out, m+" "+p)
		}
	}
	return out
}

func method(r *http.Request) string {
	for _, m := range Methods {
		if r.Method == m {
			return m
		}
	}
	return "_OTHER"
}

// statusWriter remembers the status; Unwrap lets http.ResponseController
// flush a stream through it.
type statusWriter struct {
	http.ResponseWriter
	status int
}

func (w *statusWriter) WriteHeader(code int) {
	if w.status == 0 {
		w.status = code
	}
	w.ResponseWriter.WriteHeader(code)
}

func (w *statusWriter) Write(b []byte) (int, error) {
	if w.status == 0 {
		w.status = http.StatusOK
	}
	return w.ResponseWriter.Write(b)
}

func (w *statusWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }

func (w *statusWriter) code() int {
	if w.status == 0 {
		return http.StatusOK
	}
	return w.status
}
