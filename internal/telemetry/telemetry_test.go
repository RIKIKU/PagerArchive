package telemetry_test

import (
	"bytes"
	"context"
	"go/ast"
	"go/parser"
	"go/token"
	"io"
	"io/fs"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"

	"github.com/RIKIKU/PagerArchive/internal/telemetry"
	"github.com/RIKIKU/PagerArchive/internal/telemetry/telemetrytest"
)

func quiet() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

// otelEnv points every signal at endpoint, with short batching so tests
// don't wait, and clears anything the environment set.
func otelEnv(t *testing.T, endpoint string) {
	t.Helper()
	for _, k := range []string{"OTEL_SDK_DISABLED", "OTEL_TRACES_EXPORTER", "OTEL_LOGS_EXPORTER", "OTEL_METRICS_EXPORTER",
		"OTEL_EXPORTER_OTLP_PROTOCOL", "OTEL_SERVICE_NAME", "OTEL_RESOURCE_ATTRIBUTES", "OTEL_TRACES_SAMPLER",
		"OTEL_EXPORTER_OTLP_TRACES_ENDPOINT", "OTEL_EXPORTER_OTLP_LOGS_ENDPOINT", "OTEL_EXPORTER_OTLP_METRICS_ENDPOINT"} {
		t.Setenv(k, "")
	}
	t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", endpoint)
	t.Setenv("OTEL_BSP_SCHEDULE_DELAY", "50")
	t.Setenv("OTEL_BLRP_SCHEDULE_DELAY", "50")
	t.Setenv("OTEL_METRIC_EXPORT_INTERVAL", "100")
	t.Setenv("OTEL_EXPORTER_OTLP_TIMEOUT", "1000")
}

func flush(t *testing.T, tel *telemetry.Telemetry) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	tel.Shutdown(ctx)
}

// R69: with no endpoint set, nothing is exported and nothing connects,
// even with something listening on OTLP's default port.
func TestOffUnlessConfigured(t *testing.T) {
	otelEnv(t, "")
	var accepted atomic.Int32
	l, err := net.Listen("tcp", "127.0.0.1:4318")
	if err == nil {
		defer l.Close()
		go func() {
			for {
				c, err := l.Accept()
				if err != nil {
					return
				}
				accepted.Add(1)
				c.Close()
			}
		}()
	} else {
		t.Logf("port 4318 is taken (%v); checking without a listener there", err)
	}
	tel := telemetry.Start(context.Background(), telemetry.Settings{ServiceName: "test", Metrics: true, Log: quiet()})
	if tel.On() {
		t.Fatal("telemetry on without OTEL_EXPORTER_OTLP_ENDPOINT")
	}
	_, span := tel.Tracer("t").Start(context.Background(), "page")
	if span.IsRecording() {
		t.Error("a span records with telemetry off")
	}
	span.End()
	slog.New(tel.LogHandler(slog.NewTextHandler(io.Discard, nil), "t")).Info("page")
	flush(t, tel)
	time.Sleep(200 * time.Millisecond)
	if n := accepted.Load(); n != 0 {
		t.Errorf("%d connections to the default OTLP port with telemetry off", n)
	}
	t.Logf("R69: telemetry off: no span recorded, %d connections to 127.0.0.1:4318", accepted.Load())

	// Each way of turning it off with an endpoint set.
	for k, v := range map[string]string{"OTEL_SDK_DISABLED": "true", "OTEL_TRACES_EXPORTER": "none", "OTEL_EXPORTER_OTLP_PROTOCOL": "grpc"} {
		otelEnv(t, "http://127.0.0.1:1")
		t.Setenv(k, v)
		var logs bytes.Buffer
		tel := telemetry.Start(context.Background(), telemetry.Settings{ServiceName: "test", Log: slog.New(slog.NewTextHandler(&logs, nil))})
		if _, span := tel.Tracer("t").Start(context.Background(), "page"); span.IsRecording() {
			t.Errorf("%s=%s: traces still on", k, v)
		}
		flush(t, tel)
		t.Logf("%s=%s: traces off; said: %q", k, v, strings.TrimSpace(logs.String()))
	}
}

// BenchmarkRequestOff is the cost of the request middleware with telemetry
// off, against the bare handler (R69).
func BenchmarkRequestOff(b *testing.B) {
	h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusNoContent) })
	mux := http.NewServeMux()
	mux.Handle("GET /api/v1/messages/{id}", h)
	off := (&telemetry.HTTP{Metrics: &telemetry.RequestMetrics{}}).Handler(mux)
	req := httptest.NewRequest(http.MethodGet, "/api/v1/messages/1", nil)
	b.Run("bare", func(b *testing.B) {
		b.ReportAllocs()
		for b.Loop() {
			mux.ServeHTTP(httptest.NewRecorder(), req)
		}
	})
	b.Run("telemetry-off", func(b *testing.B) {
		b.ReportAllocs()
		for b.Loop() {
			off.ServeHTTP(httptest.NewRecorder(), req)
		}
	})
}

// R70: only allowed span names and attribute keys leave, statuses lose
// their text, events go, and the resource keeps only standard keys.
func TestAllowlist(t *testing.T) {
	col := telemetrytest.New()
	defer col.Close()
	otelEnv(t, col.URL)
	t.Setenv("OTEL_RESOURCE_ATTRIBUTES", "deployment.environment.name=test,owner.email=someone@example.com")
	tel := telemetry.Start(context.Background(), telemetry.Settings{ServiceName: "test", Log: quiet()})
	tr := tel.Tracer("t")
	telemetry.AllowSpanNames("GET /api/v1/messages/{id}")

	_, span := tr.Start(context.Background(), "GET /api/v1/messages/{id}", trace.WithAttributes(
		attribute.Int("http.response.status_code", 404),
		attribute.String("url.full", "http://x/api/v1/messages/1?q=SHED+FIRE"),
		attribute.String("page.text", "SHED FIRE"),
		attribute.String("user.id", "someone@example.com"), // must be a number
		attribute.String("error.type", "pq: value \"SHED FIRE\" too long"),
	))
	span.AddEvent("exception", trace.WithAttributes(attribute.String("exception.message", "SHED FIRE")))
	span.RecordError(io.ErrUnexpectedEOF)
	span.End()
	_, other := tr.Start(context.Background(), "/api/v1/messages/123?q=SHED+FIRE")
	other.End()
	log := slog.New(tel.LogHandler(slog.NewTextHandler(io.Discard, nil), "t"))
	log.Info("page", "client", "hill-receiver.2", "text", "SHED FIRE", "error", "bad value \"SHED FIRE\"", "user", int64(7), "email", "someone@example.com")
	flush(t, tel)

	dump := col.Dump()
	for _, bad := range []string{"SHED", "someone@example.com", "url.full", "page.text", "exception", "unexpected EOF"} {
		if strings.Contains(dump, bad) {
			t.Errorf("exported telemetry contains %q", bad)
		}
	}
	spans := col.Spans()
	if len(spans) != 2 {
		t.Fatalf("%d spans, want 2", len(spans))
	}
	names := []string{spans[0].Name, spans[1].Name}
	if names[0] != "GET /api/v1/messages/{id}" || names[1] != telemetry.Unnamed {
		t.Errorf("span names %q, want the route and %q", names, telemetry.Unnamed)
	}
	if got := spans[0].Attr("http.response.status_code"); got != "404" {
		t.Errorf("status code %q", got)
	}
	if got := spans[0].Attr("error.type"); got != "other" {
		t.Errorf("error.type %q, want other", got)
	}
	if spans[0].HasAttr("user.id") {
		t.Error("user.id that isn't a number was kept")
	}
	if !strings.Contains(dump, "deployment.environment.name") {
		t.Error("a standard resource attribute was dropped")
	}
	logs := col.Logs()
	if len(logs) != 1 || logs[0].Attr("client") != "hill-receiver.2" || logs[0].Attr("user") != "7" || logs[0].Attr("error") != "other" {
		t.Errorf("log record: %v", dump)
	}
	t.Logf("R70: exported %d spans and %d log records; names %q; nothing outside the allowlist", len(spans), len(logs), names)
}

func TestErrorKind(t *testing.T) {
	for in, want := range map[string]string{
		"SQLSTATE 23505":               "SQLSTATE 23505",
		"*url.Error":                   "*url.Error",
		"clock_ahead":                  "clock_ahead",
		"HTTP 503 Service Unavailable": "HTTP 503",
		"context deadline exceeded":    "context deadline exceeded",
		"value \"SHED FIRE\" too long": "other",
		"someone@example.com":          "other",
		"Bearer pa_abc":                "other",
	} {
		if got := telemetry.ErrorKind(in); got != want {
			t.Errorf("ErrorKind(%q) = %q, want %q", in, got, want)
		}
	}
}

// A64: a collector that's down is said at most once a minute, and nothing
// waits for it.
func TestDroppedOnceAMinute(t *testing.T) {
	l, _ := net.Listen("tcp", "127.0.0.1:0")
	dead := "http://" + l.Addr().String()
	l.Close()
	otelEnv(t, dead)
	var logs bytes.Buffer
	tel := telemetry.Start(context.Background(), telemetry.Settings{ServiceName: "test", Metrics: true, Log: slog.New(slog.NewTextHandler(&logs, nil))})
	tr := tel.Tracer("t")
	start := time.Now()
	for i := 0; i < 2000; i++ {
		_, s := tr.Start(context.Background(), "page")
		s.End()
		if i%200 == 0 {
			time.Sleep(60 * time.Millisecond)
		}
	}
	if d := time.Since(start); d > 2*time.Second {
		t.Errorf("2000 spans took %v with the collector down", d)
	}
	flush(t, tel)
	n := strings.Count(logs.String(), "dropping telemetry")
	if n != 1 {
		t.Errorf("said %d times in %v that telemetry is dropped, want once:\n%s", n, time.Since(start), logs.String())
	}
	t.Logf("A64: collector down: 2000 spans in %v, %d line(s) about dropping", time.Since(start).Round(time.Millisecond), n)
}

// R70: spans are named by route, the trace a browser sends is ignored, and
// only a client's is continued.
func TestRequestSpans(t *testing.T) {
	col := telemetrytest.New()
	defer col.Close()
	otelEnv(t, col.URL)
	tel := telemetry.Start(context.Background(), telemetry.Settings{ServiceName: "test", Log: quiet()})
	tr := tel.Tracer("t")
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/v1/messages/{id}", func(w http.ResponseWriter, r *http.Request) {
		telemetry.SetUser(r.Context(), 42, "user")
	})
	h := &telemetry.HTTP{Tracer: tr, Metrics: &telemetry.RequestMetrics{}, Deferred: func(r *http.Request) bool { return r.Method == http.MethodPost }}
	mux.HandleFunc("POST /api/v1/pages", func(w http.ResponseWriter, r *http.Request) {
		telemetry.ContinueRequest(tr, r, r.Header.Get("Authorization") == "Bearer client")
	})
	telemetry.AllowSpanNames(telemetry.RouteNames([]string{"GET /api/v1/messages/{id}", "POST /api/v1/pages"})...)
	srv := httptest.NewServer(h.Handler(mux))
	defer srv.Close()

	const parent = "00-0af7651916cd43dd8448eb211c80319c-b7ad6b7169203331-01"
	do := func(method, path, auth string) {
		req, _ := http.NewRequest(method, srv.URL+path, nil)
		req.Header.Set("traceparent", parent)
		if auth != "" {
			req.Header.Set("Authorization", auth)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
	}
	do(http.MethodGet, "/api/v1/messages/123?q=SHED+FIRE", "")
	do(http.MethodPost, "/api/v1/pages", "Bearer browser")
	do(http.MethodPost, "/api/v1/pages", "Bearer client")
	do(http.MethodGet, "/nowhere/SHED-FIRE", "")
	flush(t, tel)

	spans := col.Spans()
	if len(spans) != 4 {
		t.Fatalf("%d spans, want 4", len(spans))
	}
	want := []string{"GET /api/v1/messages/{id}", "POST /api/v1/pages", "POST /api/v1/pages", "GET unmatched"}
	for i, s := range spans {
		if s.Name != want[i] {
			t.Errorf("span %d named %q, want %q", i, s.Name, want[i])
		}
		continued := s.TraceID() == "0af7651916cd43dd8448eb211c80319c"
		if continued != (i == 2) {
			t.Errorf("span %q (%d): continued the sent trace: %v", s.Name, i, continued)
		}
	}
	if spans[0].Attr("user.id") != "42" || spans[0].Attr("user.role") != "user" {
		t.Errorf("user attributes: %q %q", spans[0].Attr("user.id"), spans[0].Attr("user.role"))
	}
	if d := col.Dump(); strings.Contains(d, "SHED") || strings.Contains(d, "123") {
		t.Error("a span carries the path or query")
	}
	t.Logf("R70: spans %q; only the client's post continued the sent trace", want)
}

// R73, R70: every slog call's message is a constant, so a log record's
// body can't carry page text; values go in attributes, which the
// allowlist filters.
func TestLogMessagesAreConstant(t *testing.T) {
	root := filepath.Join("..", "..")
	n := 0
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() && (d.Name() == "node_modules" || d.Name() == "web" || strings.HasPrefix(d.Name(), ".")) && path != root {
			return filepath.SkipDir
		}
		if d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		f, err := parser.ParseFile(token.NewFileSet(), path, nil, 0)
		if err != nil {
			return err
		}
		ast.Inspect(f, func(node ast.Node) bool {
			call, ok := node.(*ast.CallExpr)
			if !ok {
				return true
			}
			sel, ok := call.Fun.(*ast.SelectorExpr)
			if !ok {
				return true
			}
			method := sel.Sel.Name
			ctxFirst := strings.HasSuffix(method, "Context")
			switch strings.TrimSuffix(method, "Context") {
			case "Debug", "Info", "Warn", "Error":
			default:
				return true
			}
			recv := strings.ToLower(exprText(sel.X))
			if !strings.HasSuffix(recv, "log") {
				return true
			}
			i := 0
			if ctxFirst {
				i = 1
			}
			if len(call.Args) <= i {
				return true
			}
			n++
			if lit, ok := call.Args[i].(*ast.BasicLit); !ok || lit.Kind != token.STRING {
				t.Errorf("%s: %s.%s's message isn't a constant string", path, exprText(sel.X), method)
			}
			return true
		})
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("R73: %d log calls, every message a constant", n)
}

func exprText(e ast.Expr) string {
	switch x := e.(type) {
	case *ast.Ident:
		return x.Name
	case *ast.SelectorExpr:
		return exprText(x.X) + "." + x.Sel.Name
	}
	return ""
}
