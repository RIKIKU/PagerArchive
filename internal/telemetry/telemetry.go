// Package telemetry exports traces, logs and (for the client) metrics over
// OTLP to the deployment's collector (SPEC.md R69-R76, ADR 0013). It's off
// unless OTEL_EXPORTER_OTLP_ENDPOINT, or a signal's own endpoint, is set.
//
// Nothing leaves without passing the allowlists in filter.go (R70): span
// names, attribute keys, log record attributes and resource attributes not
// on them are dropped, whatever a library or a new line of code adds.
// Exporting never blocks the app: spans and log records go through bounded
// batch queues, and when the collector is missing, slow or down they're
// dropped, with one log line a minute saying so (R69, A64).
package telemetry

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/go-logr/logr"
	"go.opentelemetry.io/contrib/bridges/otelslog"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/exporters/otlp/otlplog/otlploghttp"
	"go.opentelemetry.io/otel/exporters/otlp/otlpmetric/otlpmetrichttp"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp"
	"go.opentelemetry.io/otel/metric"
	metricnoop "go.opentelemetry.io/otel/metric/noop"
	sdklog "go.opentelemetry.io/otel/sdk/log"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/trace"
	"go.opentelemetry.io/otel/trace/noop"
)

// Settings say what a program exports and as whom.
type Settings struct {
	// ServiceName is service.name unless OTEL_SERVICE_NAME sets another.
	ServiceName string
	Version     string
	// InstanceID is service.instance.id ("" for none) unless
	// OTEL_RESOURCE_ATTRIBUTES sets it: the client's ID for a client.
	InstanceID string
	// Metrics exports metrics over OTLP: the client's (R74). The API's stay
	// on /metrics, scraped by the collector (R75).
	Metrics bool
	// Log is the plain stdout logger, for what this package has to say
	// (never exported, so a collector that's down can't loop).
	Log *slog.Logger
}

// Telemetry is a program's exporters. The zero value and nil are off.
type Telemetry struct {
	tp    *sdktrace.TracerProvider
	mp    *sdkmetric.MeterProvider
	lp    *sdklog.LoggerProvider
	drops *dropLog
}

var (
	noopTracer = noop.NewTracerProvider().Tracer("")
	noopMeter  = metricnoop.NewMeterProvider().Meter("")
)

// Or is t, or a tracer that does nothing when t is nil.
func Or(t trace.Tracer) trace.Tracer {
	if t == nil {
		return noopTracer
	}
	return t
}

// Start sets up the exporters the OTEL_* environment asks for. It never
// fails: a setting it can't use is logged and leaves that signal off, so
// telemetry can't stop a receiver or the API (R69).
func Start(ctx context.Context, set Settings) *Telemetry {
	t := &Telemetry{drops: &dropLog{log: set.Log, every: time.Minute, now: time.Now}}
	traces, logs, metrics := signalOn(set.Log, "TRACES"), signalOn(set.Log, "LOGS"), set.Metrics && signalOn(set.Log, "METRICS")
	if !traces && !logs && !metrics {
		return t
	}
	// The SDK's own errors go to the once-a-minute line; its internal
	// logging goes nowhere (it could print export errors on every batch).
	otel.SetErrorHandler(otel.ErrorHandlerFunc(func(err error) { t.drops.failed("sdk", 0, err) }))
	otel.SetLogger(logr.Discard())

	res := t.resource(ctx, set)
	// Each provider merges OTEL_RESOURCE_ATTRIBUTES into its resource again,
	// which would bring back the keys filterResource left out; it's read
	// above, so it's hidden while they're made.
	restore := hideEnv("OTEL_RESOURCE_ATTRIBUTES")
	defer restore()
	// No retries: a batch the collector doesn't take within
	// OTEL_EXPORTER_OTLP_TIMEOUT is dropped and said so, rather than held
	// for up to a minute while the queue behind it fills (ADR 0013 §2).
	if traces {
		exp, err := otlptracehttp.New(ctx, otlptracehttp.WithRetry(otlptracehttp.RetryConfig{Enabled: false}))
		if err != nil {
			set.Log.Error("telemetry: traces are off", "error", err.Error())
		} else {
			// Batched, with the SDK's bounded queue (OTEL_BSP_*): a full queue
			// drops spans, never blocks (R69). The sampler is
			// OTEL_TRACES_SAMPLER's, every trace by default.
			t.tp = sdktrace.NewTracerProvider(sdktrace.WithResource(res), sdktrace.WithBatcher(&spanExporter{next: exp, drops: t.drops}))
		}
	}
	if logs {
		exp, err := otlploghttp.New(ctx, otlploghttp.WithRetry(otlploghttp.RetryConfig{Enabled: false}))
		if err != nil {
			set.Log.Error("telemetry: logs are off", "error", err.Error())
		} else {
			batch := sdklog.NewBatchProcessor(&logExporter{next: exp, drops: t.drops})
			t.lp = sdklog.NewLoggerProvider(sdklog.WithResource(res), sdklog.WithProcessor(&logFilter{next: batch}))
		}
	}
	if metrics {
		exp, err := otlpmetrichttp.New(ctx, otlpmetrichttp.WithRetry(otlpmetrichttp.RetryConfig{Enabled: false}))
		if err != nil {
			set.Log.Error("telemetry: metrics are off", "error", err.Error())
		} else {
			t.mp = sdkmetric.NewMeterProvider(sdkmetric.WithResource(res),
				sdkmetric.WithReader(sdkmetric.NewPeriodicReader(&metricExporter{Exporter: exp, drops: t.drops})),
				sdkmetric.WithView(metricView))
		}
	}
	set.Log.Info("telemetry on", "traces", t.tp != nil, "logs", t.lp != nil, "metrics", t.mp != nil)
	return t
}

// signalOn reports whether OTEL_* turns on exporting a signal (TRACES, LOGS
// or METRICS) over OTLP: an endpoint is set, the SDK isn't disabled, the
// signal's exporter isn't "none", and the protocol is OTLP over HTTP.
func signalOn(log *slog.Logger, signal string) bool {
	if strings.EqualFold(strings.TrimSpace(os.Getenv("OTEL_SDK_DISABLED")), "true") {
		return false
	}
	switch exp := strings.TrimSpace(os.Getenv("OTEL_" + signal + "_EXPORTER")); exp {
	case "", "otlp":
	case "none":
		return false
	default:
		log.Error("telemetry: only the otlp exporter is supported; this signal is off", "signal", strings.ToLower(signal), "setting", "OTEL_"+signal+"_EXPORTER")
		return false
	}
	if firstEnv("OTEL_EXPORTER_OTLP_"+signal+"_ENDPOINT", "OTEL_EXPORTER_OTLP_ENDPOINT") == "" {
		return false
	}
	switch p := firstEnv("OTEL_EXPORTER_OTLP_"+signal+"_PROTOCOL", "OTEL_EXPORTER_OTLP_PROTOCOL"); p {
	case "", "http/protobuf", "http/json":
		return true
	default:
		log.Error("telemetry: PagerArchive exports OTLP over HTTP only (http/protobuf); this signal is off", "signal", strings.ToLower(signal), "setting", "OTEL_EXPORTER_OTLP_PROTOCOL")
		return false
	}
}

var envMu sync.Mutex

// hideEnv unsets name until the returned function puts it back.
func hideEnv(name string) func() {
	envMu.Lock()
	v, ok := os.LookupEnv(name)
	os.Unsetenv(name)
	return func() {
		if ok {
			os.Setenv(name, v)
		}
		envMu.Unlock()
	}
}

func firstEnv(names ...string) string {
	for _, n := range names {
		if v := strings.TrimSpace(os.Getenv(n)); v != "" {
			return v
		}
	}
	return ""
}

// resource is who's exporting: service.name, service.version and
// service.instance.id, then OTEL_SERVICE_NAME and OTEL_RESOURCE_ATTRIBUTES,
// cut to the allowed resource keys (filter.go).
func (t *Telemetry) resource(ctx context.Context, set Settings) *resource.Resource {
	attrs := []attribute.KeyValue{attribute.String("service.name", set.ServiceName), attribute.String("service.version", set.Version)}
	if set.InstanceID != "" {
		attrs = append(attrs, attribute.String("service.instance.id", set.InstanceID))
	}
	res, err := resource.New(ctx, resource.WithAttributes(attrs...), resource.WithFromEnv(), resource.WithTelemetrySDK())
	if err != nil && res == nil {
		set.Log.Warn("telemetry: OTEL_RESOURCE_ATTRIBUTES ignored", "error", "invalid")
		res = resource.NewSchemaless(attrs...)
	}
	return filterResource(res)
}

// On reports whether anything is exported.
func (t *Telemetry) On() bool { return t != nil && (t.tp != nil || t.lp != nil || t.mp != nil) }

// Tracer is the tracer for name, or one that does nothing.
func (t *Telemetry) Tracer(name string) trace.Tracer {
	if t == nil || t.tp == nil {
		return noopTracer
	}
	return t.tp.Tracer(name)
}

// Meter is the meter for name, or one that does nothing.
func (t *Telemetry) Meter(name string) metric.Meter {
	if t == nil || t.mp == nil {
		return noopMeter
	}
	return t.mp.Meter(name)
}

// LogHandler is h, plus exporting each record over OTLP when logs are on
// (R73). h (stdout) is unchanged either way.
func (t *Telemetry) LogHandler(h slog.Handler, name string) slog.Handler {
	if t == nil || t.lp == nil {
		return h
	}
	return fanout{h, otelslog.NewHandler(name, otelslog.WithLoggerProvider(t.lp))}
}

// Shutdown sends what's queued, waiting at most until ctx ends.
func (t *Telemetry) Shutdown(ctx context.Context) {
	if t == nil {
		return
	}
	var errs []error
	if t.tp != nil {
		errs = append(errs, t.tp.Shutdown(ctx))
	}
	if t.lp != nil {
		errs = append(errs, t.lp.Shutdown(ctx))
	}
	if t.mp != nil {
		errs = append(errs, t.mp.Shutdown(ctx))
	}
	if err := errors.Join(errs...); err != nil {
		t.drops.failed("shutdown", 0, err)
	}
}

// fanout sends each record to every handler.
type fanout []slog.Handler

func (f fanout) Enabled(ctx context.Context, l slog.Level) bool {
	for _, h := range f {
		if h.Enabled(ctx, l) {
			return true
		}
	}
	return false
}

func (f fanout) Handle(ctx context.Context, r slog.Record) error {
	var errs []error
	for _, h := range f {
		if h.Enabled(ctx, r.Level) {
			errs = append(errs, h.Handle(ctx, r.Clone()))
		}
	}
	return errors.Join(errs...)
}

func (f fanout) WithAttrs(attrs []slog.Attr) slog.Handler {
	out := make(fanout, len(f))
	for i, h := range f {
		out[i] = h.WithAttrs(attrs)
	}
	return out
}

func (f fanout) WithGroup(name string) slog.Handler {
	out := make(fanout, len(f))
	for i, h := range f {
		out[i] = h.WithGroup(name)
	}
	return out
}

// dropLog says, at most once a minute, that telemetry is being dropped
// (R69, A64).
type dropLog struct {
	log   *slog.Logger
	every time.Duration
	now   func() time.Time

	mu      sync.Mutex
	last    time.Time
	pending int
}

// failed notes n items of signal that couldn't be exported.
func (d *dropLog) failed(signal string, n int, err error) {
	d.mu.Lock()
	d.pending += n
	now := d.now()
	if !d.last.IsZero() && now.Sub(d.last) < d.every {
		d.mu.Unlock()
		return
	}
	d.last, n = now, d.pending
	d.pending = 0
	d.mu.Unlock()
	// The error names the collector's address, which is the operator's own
	// setting; it never holds app data. This line goes to stdout only.
	d.log.Warn("telemetry: the collector isn't taking data; dropping telemetry (said at most once a minute)",
		"signal", signal, "dropped", n, "error", err.Error())
}

// spanExporter filters spans (filter.go) and reports failed exports as
// drops instead of errors, so the SDK doesn't print one per batch.
type spanExporter struct {
	next  sdktrace.SpanExporter
	drops *dropLog
}

func (e *spanExporter) ExportSpans(ctx context.Context, spans []sdktrace.ReadOnlySpan) error {
	out := make([]sdktrace.ReadOnlySpan, len(spans))
	for i, s := range spans {
		out[i] = filterSpan(s)
	}
	if err := e.next.ExportSpans(ctx, out); err != nil {
		e.drops.failed("traces", len(spans), err)
	}
	return nil
}

func (e *spanExporter) Shutdown(ctx context.Context) error { return e.next.Shutdown(ctx) }

type logExporter struct {
	next  sdklog.Exporter
	drops *dropLog
}

func (e *logExporter) Export(ctx context.Context, records []sdklog.Record) error {
	if err := e.next.Export(ctx, records); err != nil {
		e.drops.failed("logs", len(records), err)
	}
	return nil
}

func (e *logExporter) Shutdown(ctx context.Context) error   { return e.next.Shutdown(ctx) }
func (e *logExporter) ForceFlush(ctx context.Context) error { return e.next.ForceFlush(ctx) }

type metricExporter struct {
	sdkmetric.Exporter
	drops *dropLog
}

func (e *metricExporter) Export(ctx context.Context, rm *metricdata.ResourceMetrics) error {
	rm.Resource = filterResource(rm.Resource)
	if err := e.Exporter.Export(ctx, rm); err != nil {
		n := 0
		for _, sm := range rm.ScopeMetrics {
			n += len(sm.Metrics)
		}
		e.drops.failed("metrics", n, err)
	}
	return nil
}
