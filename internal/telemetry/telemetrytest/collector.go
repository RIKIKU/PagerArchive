// Package telemetrytest is a test collector: an OTLP/HTTP receiver that
// keeps everything it's sent, for the telemetry checks (A62-A66).
package telemetrytest

import (
	"compress/gzip"
	"encoding/hex"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"

	collogs "go.opentelemetry.io/proto/otlp/collector/logs/v1"
	colmetrics "go.opentelemetry.io/proto/otlp/collector/metrics/v1"
	coltrace "go.opentelemetry.io/proto/otlp/collector/trace/v1"
	commonpb "go.opentelemetry.io/proto/otlp/common/v1"
	logspb "go.opentelemetry.io/proto/otlp/logs/v1"
	metricspb "go.opentelemetry.io/proto/otlp/metrics/v1"
	resourcepb "go.opentelemetry.io/proto/otlp/resource/v1"
	tracepb "go.opentelemetry.io/proto/otlp/trace/v1"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
)

// Collector keeps every OTLP request it receives.
type Collector struct {
	*httptest.Server

	mu      sync.Mutex
	traces  []*coltrace.ExportTraceServiceRequest
	metrics []*colmetrics.ExportMetricsServiceRequest
	logs    []*collogs.ExportLogsServiceRequest
}

// New starts a collector. Its URL is the OTLP endpoint.
func New() *Collector {
	c := &Collector{}
	c.Server = httptest.NewServer(c)
	return c
}

// ServeHTTP receives OTLP/HTTP: a Collector without a Server can serve on
// a listener of its own (scripts/test-collector).
func (c *Collector) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	var body io.Reader = r.Body
	if r.Header.Get("Content-Encoding") == "gzip" {
		gz, err := gzip.NewReader(r.Body)
		if err != nil {
			http.Error(w, "bad gzip", http.StatusBadRequest)
			return
		}
		body = gz
	}
	data, err := io.ReadAll(body)
	if err != nil {
		http.Error(w, "read", http.StatusBadRequest)
		return
	}
	unmarshal := proto.Unmarshal
	if strings.HasPrefix(r.Header.Get("Content-Type"), "application/json") {
		unmarshal = func(b []byte, m proto.Message) error { return protojson.Unmarshal(b, m) }
	}
	var msg, reply proto.Message
	switch r.URL.Path {
	case "/v1/traces":
		msg, reply = &coltrace.ExportTraceServiceRequest{}, &coltrace.ExportTraceServiceResponse{}
	case "/v1/metrics":
		msg, reply = &colmetrics.ExportMetricsServiceRequest{}, &colmetrics.ExportMetricsServiceResponse{}
	case "/v1/logs":
		msg, reply = &collogs.ExportLogsServiceRequest{}, &collogs.ExportLogsServiceResponse{}
	default:
		http.NotFound(w, r)
		return
	}
	if err := unmarshal(data, msg); err != nil {
		http.Error(w, "bad protobuf", http.StatusBadRequest)
		return
	}
	c.mu.Lock()
	switch m := msg.(type) {
	case *coltrace.ExportTraceServiceRequest:
		c.traces = append(c.traces, m)
	case *colmetrics.ExportMetricsServiceRequest:
		c.metrics = append(c.metrics, m)
	case *collogs.ExportLogsServiceRequest:
		c.logs = append(c.logs, m)
	}
	c.mu.Unlock()
	out, _ := proto.Marshal(reply)
	w.Header().Set("Content-Type", "application/x-protobuf")
	_, _ = w.Write(out)
}

// Span is one received span with its service.
type Span struct {
	Service string
	*tracepb.Span
	Resource *resourcepb.Resource
}

// Attr is a span's attribute as text ("" if it isn't there).
func (s Span) Attr(key string) string { return attrText(s.Attributes, key) }

// HasAttr says whether the span has the attribute.
func (s Span) HasAttr(key string) bool {
	for _, kv := range s.Attributes {
		if kv.Key == key {
			return true
		}
	}
	return false
}

// TraceID is the span's trace ID in hex.
func (s Span) TraceID() string { return hex.EncodeToString(s.Span.TraceId) }

// SpanID is the span's ID in hex.
func (s Span) SpanID() string { return hex.EncodeToString(s.Span.SpanId) }

// ParentID is the span's parent's ID in hex ("" for a root).
func (s Span) ParentID() string { return hex.EncodeToString(s.Span.ParentSpanId) }

// Spans are every span received so far.
func (c *Collector) Spans() []Span {
	c.mu.Lock()
	defer c.mu.Unlock()
	var out []Span
	for _, req := range c.traces {
		for _, rs := range req.ResourceSpans {
			svc := attrText(rs.Resource.GetAttributes(), "service.name")
			for _, ss := range rs.ScopeSpans {
				for _, sp := range ss.Spans {
					out = append(out, Span{Service: svc, Span: sp, Resource: rs.Resource})
				}
			}
		}
	}
	return out
}

// Log is one received log record with its service.
type Log struct {
	Service string
	*logspb.LogRecord
}

// Attr is the record's attribute as text.
func (l Log) Attr(key string) string { return attrText(l.Attributes, key) }

// Logs are every log record received so far.
func (c *Collector) Logs() []Log {
	c.mu.Lock()
	defer c.mu.Unlock()
	var out []Log
	for _, req := range c.logs {
		for _, rl := range req.ResourceLogs {
			svc := attrText(rl.Resource.GetAttributes(), "service.name")
			for _, sl := range rl.ScopeLogs {
				for _, lr := range sl.LogRecords {
					out = append(out, Log{Service: svc, LogRecord: lr})
				}
			}
		}
	}
	return out
}

// Point is one metric data point: its metric, value and attributes.
type Point struct {
	Service, Metric string
	Value           float64
	Attributes      []*commonpb.KeyValue
	TimeUnixNano    uint64
}

// Points are every metric data point received so far, oldest first.
func (c *Collector) Points() []Point {
	c.mu.Lock()
	defer c.mu.Unlock()
	var out []Point
	for _, req := range c.metrics {
		for _, rm := range req.ResourceMetrics {
			svc := attrText(rm.Resource.GetAttributes(), "service.name")
			for _, sm := range rm.ScopeMetrics {
				for _, m := range sm.Metrics {
					var dps []*metricspb.NumberDataPoint
					switch d := m.Data.(type) {
					case *metricspb.Metric_Sum:
						dps = d.Sum.DataPoints
					case *metricspb.Metric_Gauge:
						dps = d.Gauge.DataPoints
					}
					for _, dp := range dps {
						v := dp.GetAsDouble()
						if _, ok := dp.Value.(*metricspb.NumberDataPoint_AsInt); ok {
							v = float64(dp.GetAsInt())
						}
						out = append(out, Point{Service: svc, Metric: m.Name, Value: v, Attributes: dp.Attributes, TimeUnixNano: dp.TimeUnixNano})
					}
				}
			}
		}
	}
	return out
}

// Latest is the newest value of a metric (summed over its attribute sets),
// and whether there is one.
func (c *Collector) Latest(service, metric string) (float64, bool) {
	var newest uint64
	byAttrs := map[string]float64{}
	for _, p := range c.Points() {
		if p.Service != service || p.Metric != metric {
			continue
		}
		if p.TimeUnixNano > newest {
			newest = p.TimeUnixNano
			byAttrs = map[string]float64{}
		}
		if p.TimeUnixNano == newest {
			byAttrs[protoText(p.Attributes)] = p.Value
		}
	}
	if newest == 0 {
		return 0, false
	}
	sum := 0.0
	for _, v := range byAttrs {
		sum += v
	}
	return sum, true
}

// Records counts what was received: spans, log records and metric data
// points.
func (c *Collector) Records() (spans, logs, points int) {
	return len(c.Spans()), len(c.Logs()), len(c.Points())
}

// Dump is everything received, as JSON, one request per line: what A63
// searches for anything private, names, attributes, events, resources,
// labels and log bodies alike.
func (c *Collector) Dump() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	var b strings.Builder
	opts := protojson.MarshalOptions{UseProtoNames: true}
	for _, m := range c.traces {
		b.Write(must(opts.Marshal(m)))
		b.WriteByte('\n')
	}
	for _, m := range c.metrics {
		b.Write(must(opts.Marshal(m)))
		b.WriteByte('\n')
	}
	for _, m := range c.logs {
		b.Write(must(opts.Marshal(m)))
		b.WriteByte('\n')
	}
	return b.String()
}

func must(b []byte, err error) []byte {
	if err != nil {
		panic(err)
	}
	return b
}

func attrText(kvs []*commonpb.KeyValue, key string) string {
	for _, kv := range kvs {
		if kv.Key == key {
			return valueText(kv.Value)
		}
	}
	return ""
}

func valueText(v *commonpb.AnyValue) string {
	switch x := v.GetValue().(type) {
	case *commonpb.AnyValue_StringValue:
		return x.StringValue
	case *commonpb.AnyValue_IntValue:
		return strconv.FormatInt(x.IntValue, 10)
	case *commonpb.AnyValue_BoolValue:
		if x.BoolValue {
			return "true"
		}
		return "false"
	}
	return protoText([]*commonpb.KeyValue{{Value: v}})
}

func protoText(kvs []*commonpb.KeyValue) string {
	var b strings.Builder
	for _, kv := range kvs {
		b.Write(must(protojson.Marshal(kv)))
	}
	return b.String()
}
