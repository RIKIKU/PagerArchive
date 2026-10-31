package telemetry

import (
	"context"
	"regexp"
	"sort"
	"strings"
	"sync"

	"go.opentelemetry.io/otel/attribute"
	sdklog "go.opentelemetry.io/otel/sdk/log"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
)

// What may be exported (R70, ADR 0013 §3). Everything leaves through these
// lists: an attribute, span name or resource key that isn't here is dropped
// (or the span renamed "unnamed"), whoever added it.

// kind is what an allowed attribute's value must be.
type kind int

const (
	anyKind kind = iota // set only by this code, from fixed values (a route, a version)
	intKind             // a number: an ID, a count, a status code
	boolKind
	codeKind // a short code; other text becomes "other" (ErrorKind)
	idKind   // a receiving client's ID, as an admin named it; else "other"
)

// SpanKeys are the attribute keys spans (and their links) may carry.
var SpanKeys = map[string]kind{
	"http.request.method":           anyKind, // a known method or _OTHER
	"http.route":                    anyKind, // the mux's pattern, never the path
	"http.response.status_code":     intKind,
	"error.type":                    codeKind,
	"user.id":                       intKind, // the signed-in user's number, nothing else about them
	"user.role":                     codeKind,
	"db.system.name":                anyKind,
	"db.operation.name":             anyKind, // the sqlc query's name
	"pagerarchive.client.id":        idKind,
	"pagerarchive.ingest.outcome":   codeKind,
	"pagerarchive.message.id":       intKind, // the ingest span only (R71)
	"pagerarchive.send.attempt":     intKind,
	"pagerarchive.live.connections": intKind,
	"pagerarchive.delivery.kind":    codeKind,
	"pagerarchive.delivery.attempt": intKind,
	"pagerarchive.delivery.result":  codeKind,
	"pagerarchive.delivery.failure": codeKind, // retryable, refused or no_answer; the cause is error.type
	"pagerarchive.stream.caught_up": intKind,
	"pagerarchive.nightly.deleted":  intKind,
	"pagerarchive.nightly.redacted": intKind,
	"pagerarchive.reparse.messages": intKind,
}

// LogKeys are the attribute keys log records may carry, as the code's
// slog calls name them. Values named "error" or "reason" become their kind.
var LogKeys = map[string]kind{
	"client":          idKind,
	"outcome":         codeKind,
	"message_id":      intKind, // the ingest line only (R71)
	"user":            intKind,
	"by":              intKind,
	"owner":           intKind,
	"users":           intKind,
	"role":            codeKind,
	"code":            codeKind,
	"reason":          codeKind,
	"error":           codeKind,
	"op":              anyKind, // the name of what failed, always a constant in the code
	"kind":            codeKind,
	"status":          codeKind,
	"result":          codeKind,
	"count":           intKind,
	"deleted":         intKind,
	"redacted":        intKind,
	"rows":            intKind,
	"requests":        intKind,
	"requests_closed": intKind,
	"integrations":    intKind,
	"aliases":         intKind,
	"entries":         intKind,
	"conflicts":       intKind,
	"streets":         intKind,
	"localities":      intKind,
	"addresses":       intKind,
	"seconds":         intKind,
	"heap_mib":        intKind,
	"uploaded_bytes":  intKind,
	"stored_bytes":    intKind,
	"buffered":        intKind,
	"dropped":         intKind,
	"days":            intKind,
	"agencies":        intKind,
	"ok":              boolKind,
	"dry_run":         boolKind,
	"update":          boolKind,
	"trusted":         boolKind,
	"version":         anyKind,
	"traces":          boolKind,
	"logs":            boolKind,
	"metrics":         boolKind,
}

// ResourceKeys are the resource attributes kept from the program and
// OTEL_RESOURCE_ATTRIBUTES: the standard ones that say which program and
// machine is talking.
var ResourceKeys = map[string]bool{
	"service.name": true, "service.namespace": true, "service.version": true, "service.instance.id": true,
	"deployment.environment.name": true, "deployment.environment": true, "host.name": true, "host.arch": true,
	"telemetry.sdk.name": true, "telemetry.sdk.language": true, "telemetry.sdk.version": true,
}

// MetricKeys are the attribute keys the client's metrics may carry.
var MetricKeys = []attribute.Key{"reason", "kind"} // kind: rtl_fm message kinds (R74a)

// Span names: fixed ones here, routes and query names registered by the
// server and the store at start-up (AllowSpanNames).
var spanNames = struct {
	sync.RWMutex
	m map[string]bool
}{m: map[string]bool{
	// The client (R71).
	"page": true, "decode": true, "buffer": true, "send": true,
	// Ingest and what follows it (R71).
	"ingest": true, "ingest.redact": true, "ingest.parse": true, "ingest.store": true, "ingest.dedup": true, "ingest.locate": true,
	"live.publish": true, "integration.deliver": true,
	// Background work (R72).
	"nightly": true, "nightly.retention": true, "nightly.redaction": true, "nightly.vacuum": true, "reparse": true,
}}

// Unnamed replaces a span name that isn't allowed.
const Unnamed = "unnamed"

// AllowSpanNames adds names spans may have: the router's patterns and the
// store's query names, fixed when the program starts.
func AllowSpanNames(names ...string) {
	spanNames.Lock()
	defer spanNames.Unlock()
	for _, n := range names {
		spanNames.m[n] = true
	}
}

func spanNameAllowed(n string) bool {
	spanNames.RLock()
	defer spanNames.RUnlock()
	return spanNames.m[n]
}

var (
	codeRe     = regexp.MustCompile(`^[a-z][a-z0-9_]{0,40}$`)
	idRe       = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$`)
	sqlstateRe = regexp.MustCompile(`^SQLSTATE [0-9A-Z]{5}$`)
	goTypeRe   = regexp.MustCompile(`^\*?[a-zA-Z_][a-zA-Z0-9_]*(\.[a-zA-Z_][a-zA-Z0-9_]*)+$`)
	httpRe     = regexp.MustCompile(`^HTTP [1-5][0-9][0-9]$`)
	// Kinds the code itself names (store.SafeError, the client, ADR 0006 §9).
	fixedKinds = map[string]bool{
		"context canceled": true, "context deadline exceeded": true, "can't connect to the database": true,
		"connection refused": true, "DNS lookup failed": true, "no answer within 10 s": true, "timeout": true,
	}
)

// ErrorKind is s if it's a kind, not text: a code (clock_ahead), a
// SQLSTATE, a Go type name, an HTTP status or a fixed message the code
// makes. Anything else, which could quote a request, is "other" (R70).
func ErrorKind(s string) string {
	switch {
	case codeRe.MatchString(s), sqlstateRe.MatchString(s), goTypeRe.MatchString(s), httpRe.MatchString(s), fixedKinds[s]:
		return s
	case strings.HasPrefix(s, "HTTP ") && len(s) >= 8 && httpRe.MatchString(s[:8]):
		return s[:8] // "HTTP 503 Service Unavailable"
	}
	return "other"
}

// keep is attr if its key is allowed and its value is of the allowed kind,
// with codes reduced to kinds.
func keep(keys map[string]kind, kv attribute.KeyValue) (attribute.KeyValue, bool) {
	k, ok := keys[string(kv.Key)]
	if !ok {
		return kv, false
	}
	switch k {
	case intKind:
		return kv, kv.Value.Type() == attribute.INT64
	case boolKind:
		return kv, kv.Value.Type() == attribute.BOOL
	case codeKind:
		if kv.Value.Type() != attribute.STRING {
			return kv, kv.Value.Type() == attribute.INT64 || kv.Value.Type() == attribute.BOOL
		}
		return attribute.String(string(kv.Key), ErrorKind(kv.Value.AsString())), true
	case idKind:
		if kv.Value.Type() != attribute.STRING {
			return kv, false
		}
		if idRe.MatchString(kv.Value.AsString()) {
			return kv, true
		}
		return attribute.String(string(kv.Key), "other"), true
	default:
		return kv, kv.Value.Type() == attribute.STRING || kv.Value.Type() == attribute.INT64
	}
}

func filterAttrs(keys map[string]kind, in []attribute.KeyValue) []attribute.KeyValue {
	out := make([]attribute.KeyValue, 0, len(in))
	for _, kv := range in {
		if kv, ok := keep(keys, kv); ok {
			out = append(out, kv)
		}
	}
	return out
}

func filterResource(r *resource.Resource) *resource.Resource {
	var kept []attribute.KeyValue
	for _, kv := range r.Attributes() {
		if ResourceKeys[string(kv.Key)] && kv.Value.Type() == attribute.STRING {
			kept = append(kept, kv)
		}
	}
	return resource.NewWithAttributes(r.SchemaURL(), kept...)
}

// filtered is a span as it may leave: allowed name and attributes, no
// events (they carry exception messages), and a status without its text.
type filtered struct {
	sdktrace.ReadOnlySpan
	name  string
	attrs []attribute.KeyValue
	links []sdktrace.Link
}

func (f filtered) Name() string                     { return f.name }
func (f filtered) Attributes() []attribute.KeyValue { return f.attrs }
func (f filtered) Links() []sdktrace.Link           { return f.links }
func (f filtered) Events() []sdktrace.Event         { return nil }
func (f filtered) DroppedEvents() int               { return 0 }
func (f filtered) Resource() *resource.Resource     { return filterResource(f.ReadOnlySpan.Resource()) }
func (f filtered) Status() sdktrace.Status {
	return sdktrace.Status{Code: f.ReadOnlySpan.Status().Code}
}
func (f filtered) DroppedAttributes() int { return 0 }

func filterSpan(s sdktrace.ReadOnlySpan) sdktrace.ReadOnlySpan {
	name := s.Name()
	if !spanNameAllowed(name) {
		name = Unnamed
	}
	links := make([]sdktrace.Link, len(s.Links()))
	for i, l := range s.Links() {
		links[i] = sdktrace.Link{SpanContext: l.SpanContext, Attributes: filterAttrs(SpanKeys, l.Attributes)}
	}
	return filtered{ReadOnlySpan: s, name: name, attrs: filterAttrs(SpanKeys, s.Attributes()), links: links}
}

// logFilter cuts each log record's attributes to LogKeys before it's
// queued. The message is the slog call's constant (a test checks every
// call), so it's kept.
type logFilter struct{ next sdklog.Processor }

func (p *logFilter) Enabled(ctx context.Context, param sdklog.EnabledParameters) bool {
	return p.next.Enabled(ctx, param)
}

func (p *logFilter) OnEmit(ctx context.Context, r *sdklog.Record) error {
	var kept []attribute.KeyValue
	r.WalkAttributes(func(kv attribute.KeyValue) bool {
		if kv, ok := keep(LogKeys, kv); ok {
			kept = append(kept, kv)
		}
		return true
	})
	r.SetAttributes(kept...)
	return p.next.OnEmit(ctx, r)
}

func (p *logFilter) Shutdown(ctx context.Context) error   { return p.next.Shutdown(ctx) }
func (p *logFilter) ForceFlush(ctx context.Context) error { return p.next.ForceFlush(ctx) }

// metricView keeps only MetricKeys on every metric's data points.
var metricView = sdkmetric.NewView(sdkmetric.Instrument{Name: "*"}, sdkmetric.Stream{AttributeFilter: attribute.NewAllowKeysFilter(MetricKeys...)})

// AllowedSpanNames lists the span names allowed now, for the docs and tests.
func AllowedSpanNames() []string {
	spanNames.RLock()
	defer spanNames.RUnlock()
	out := make([]string, 0, len(spanNames.m))
	for n := range spanNames.m {
		out = append(out, n)
	}
	sort.Strings(out)
	return out
}
