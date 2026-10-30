package acceptance

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"

	"github.com/RIKIKU/PagerArchive/internal/integrations/standin"
	"github.com/RIKIKU/PagerArchive/internal/telemetry"
	"github.com/RIKIKU/PagerArchive/internal/telemetry/telemetrytest"
)

// M12: telemetry (R69-R75, ADR 0013). The API runs in the test, as for
// every acceptance check, and the receiving client is the real program,
// fed decoder lines on stdin. Both export to a test collector that keeps
// everything (telemetrytest).

// otelTestEnv points telemetry at endpoint for this test, with short
// batching so checks don't wait long. The client inherits it.
func otelTestEnv(t *testing.T, endpoint string) {
	t.Helper()
	for k, v := range map[string]string{
		"OTEL_EXPORTER_OTLP_ENDPOINT": endpoint, "OTEL_SDK_DISABLED": "", "OTEL_SERVICE_NAME": "", "OTEL_RESOURCE_ATTRIBUTES": "",
		"OTEL_TRACES_EXPORTER": "", "OTEL_LOGS_EXPORTER": "", "OTEL_METRICS_EXPORTER": "", "OTEL_TRACES_SAMPLER": "",
		"OTEL_BSP_SCHEDULE_DELAY": "200", "OTEL_BLRP_SCHEDULE_DELAY": "200", "OTEL_METRIC_EXPORT_INTERVAL": "500",
	} {
		t.Setenv(k, v)
	}
}

// startTelemetry starts the API's telemetry; what it says about itself
// (dropping data) goes to said.
func startTelemetry(t *testing.T, said io.Writer) *telemetry.Telemetry {
	t.Helper()
	tel := telemetry.Start(context.Background(), telemetry.Settings{ServiceName: "pagerarchive", Version: "test",
		Log: slog.New(slog.NewTextHandler(said, nil))})
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		tel.Shutdown(ctx)
	})
	return tel
}

var clientBin struct {
	once sync.Once
	path string
	err  error
}

// clientBinary builds cmd/pagerarchive-client once per test run.
func clientBinary(t *testing.T) string {
	t.Helper()
	clientBin.once.Do(func() {
		dir, err := os.MkdirTemp("", "pa-m12-client-")
		if err != nil {
			clientBin.err = err
			return
		}
		clientBin.path = filepath.Join(dir, "pagerarchive-client")
		out, err := exec.Command("go", "build", "-o", clientBin.path, "../../cmd/pagerarchive-client").CombinedOutput()
		if err != nil {
			clientBin.err = fmt.Errorf("build the client: %v\n%s", err, out)
		}
	})
	if clientBin.err != nil {
		t.Fatal(clientBin.err)
	}
	return clientBin.path
}

func TestMain(m *testing.M) {
	code := m.Run()
	if clientBin.path != "" {
		os.RemoveAll(filepath.Dir(clientBin.path))
	}
	os.Exit(code)
}

// receiver is the real client program, reading decoder lines on stdin.
type receiver struct {
	t      *testing.T
	stdin  io.WriteCloser
	stdout *syncBuffer // the console (PRINT_PAGES)
	stderr *syncBuffer // its log
	done   chan error
}

// startReceiver runs the client as id, posting to serverURL, with the
// test's OTEL_* settings and extra environment.
func (e *env) startReceiver(id, serverURL string, extra ...string) *receiver {
	e.t.Helper()
	cmd := exec.Command(clientBinary(e.t))
	cmd.Env = append(os.Environ(), "SERVER_URL="+serverURL, "CLIENT_ID="+id, "CLIENT_KEY="+e.keys[id], "BUFFER_SIZE=1000")
	cmd.Env = append(cmd.Env, extra...)
	r := &receiver{t: e.t, stdout: &syncBuffer{}, stderr: &syncBuffer{}, done: make(chan error, 1)}
	cmd.Stdout, cmd.Stderr = r.stdout, r.stderr
	var err error
	if r.stdin, err = cmd.StdinPipe(); err != nil {
		e.t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		e.t.Fatal(err)
	}
	go func() { r.done <- cmd.Wait() }()
	e.t.Cleanup(func() {
		r.stdin.Close()
		select {
		case <-r.done:
		case <-time.After(20 * time.Second):
			cmd.Process.Kill()
		}
	})
	return r
}

// decode feeds the client a page as multimon-ng prints it (R12).
func (r *receiver) decode(capcode, text string) {
	r.t.Helper()
	n, _ := strconv.Atoi(capcode)
	if _, err := fmt.Fprintf(r.stdin, "POCSAG1200: Address: %7d  Function: 3  Alpha:   %s\n", n, text); err != nil {
		r.t.Fatal(err)
	}
}

// stop ends the client's input and waits for it to send what it has and
// exit (it flushes its telemetry on the way out).
func (r *receiver) stop() {
	r.t.Helper()
	r.stdin.Close()
	select {
	case <-r.done:
	case <-time.After(30 * time.Second):
		r.t.Fatal("the client didn't exit")
	}
}

// tcpProxy forwards to target until stopped, so a test can make the API
// unreachable for the client without stopping it for anyone else.
type tcpProxy struct {
	t      *testing.T
	target string
	addr   string
	mu     sync.Mutex
	l      net.Listener
	conns  map[net.Conn]bool
}

func newTCPProxy(t *testing.T, target string) *tcpProxy {
	t.Helper()
	p := &tcpProxy{t: t, target: target, conns: map[net.Conn]bool{}}
	p.listen("127.0.0.1:0")
	t.Cleanup(p.down)
	return p
}

func (p *tcpProxy) URL() string { return "http://" + p.addr }

func (p *tcpProxy) listen(addr string) {
	l, err := net.Listen("tcp", addr)
	if err != nil {
		p.t.Fatal(err)
	}
	p.mu.Lock()
	p.l, p.addr = l, l.Addr().String()
	p.mu.Unlock()
	go func() {
		for {
			c, err := l.Accept()
			if err != nil {
				return
			}
			up, err := net.Dial("tcp", p.target)
			if err != nil {
				c.Close()
				continue
			}
			p.mu.Lock()
			p.conns[c], p.conns[up] = true, true
			p.mu.Unlock()
			go func() { io.Copy(up, c); up.Close() }()
			go func() { io.Copy(c, up); c.Close() }()
		}
	}()
}

// down refuses new connections and cuts open ones: the API is unreachable.
func (p *tcpProxy) down() {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.l != nil {
		p.l.Close()
		p.l = nil
	}
	for c := range p.conns {
		c.Close()
	}
	p.conns = map[net.Conn]bool{}
}

// up accepts connections again, on the same address.
func (p *tcpProxy) up() { p.listen(p.addr) }

// traceOf is every span in the trace whose root is the client's "page"
// span for capcode's newest page... found by the API's ingest span for
// message id.
func traceOf(col *telemetrytest.Collector, traceID string) []telemetrytest.Span {
	var out []telemetrytest.Span
	for _, s := range col.Spans() {
		if s.TraceID() == traceID {
			out = append(out, s)
		}
	}
	return out
}

// ingestSpan finds the API's ingest span for message id with outcome.
func ingestSpan(col *telemetrytest.Collector, id int64, outcome string) (telemetrytest.Span, bool) {
	for _, s := range col.Spans() {
		if s.Name == "ingest" && s.Attr("pagerarchive.message.id") == strconv.FormatInt(id, 10) && s.Attr("pagerarchive.ingest.outcome") == outcome {
			return s, true
		}
	}
	return telemetrytest.Span{}, false
}

func named(spans []telemetrytest.Span, service, name string) []telemetrytest.Span {
	var out []telemetrytest.Span
	for _, s := range spans {
		if s.Service == service && s.Name == name {
			out = append(out, s)
		}
	}
	return out
}

func duration(s telemetrytest.Span) time.Duration {
	return time.Duration(s.EndTimeUnixNano - s.StartTimeUnixNano)
}

// messageID waits for a message with text to be stored.
func (e *env) messageID(text string, within time.Duration) int64 {
	e.t.Helper()
	var id int64
	if !eventually(within, func() bool {
		return e.st.Pool.QueryRow(e.ctx, "SELECT id FROM messages WHERE text = $1 ORDER BY id DESC LIMIT 1", text).Scan(&id) == nil
	}) {
		e.t.Fatalf("page %q wasn't stored within %v", text, within)
	}
	return id
}

// A62. A page's trace: the client's decode and send, the API's ingest
// steps, the database, the live update and each delivery attempt, as one
// trace (R71); a second client's copy is a duplicate; a page held while the
// API is unreachable shows about 30 s of buffering.
func TestA62PageTrace(t *testing.T) {
	col := telemetrytest.New()
	defer col.Close()
	otelTestEnv(t, col.URL)
	e := setup(t, traced{tel: startTelemetry(t, io.Discard)})
	e.allowLoopback()
	hook := standin.Webhook()
	defer hook.Close()
	e.createIntegration(e.user, webhookTo("A62 hook", hook, "0100001"))
	e.runWorker()
	// Live updates: one viewer, so the publish reaches someone.
	viewer := e.stream("")

	proxyA := newTCPProxy(t, strings.TrimPrefix(e.url, "http://"))
	a := e.startReceiver("a", proxyA.URL())
	b := e.startReceiver("b", e.url)

	// The first delivery attempt fails, so the trace shows a retry.
	hook.FailWith(http.StatusServiceUnavailable)
	text := "@@EX0000062 HSF1 SMOKE FROM ROOF @ 12 FICTION ST, MADEUPTON #STN1"
	e.posts = append(e.posts, text)
	a.decode("0100001", text)
	id := e.messageID(text, 5*time.Second)
	select {
	case <-viewer:
	case <-time.After(3 * time.Second):
		t.Error("the live update didn't arrive")
	}
	time.Sleep(time.Second)
	hook.FailWith(0)
	// The same page from client B, within 10 s: a duplicate (R19).
	b.decode("0100001", text)

	var ingest telemetrytest.Span
	if !eventually(20*time.Second, func() bool {
		var ok bool
		ingest, ok = ingestSpan(col, id, "stored")
		return ok && len(named(traceOf(col, ingest.TraceID()), "pagerarchive", "integration.deliver")) >= 2
	}) {
		t.Fatalf("no ingest span with outcome stored and two delivery attempts for message %d; spans: %v", id, spanNames(col.Spans()))
	}
	spans := traceOf(col, ingest.TraceID())
	t.Logf("A62: message %d's trace %s has %d spans: %v", id, ingest.TraceID(), len(spans), spanNames(spans))
	byID := map[string]telemetrytest.Span{}
	for _, s := range spans {
		byID[s.SpanID()] = s
	}
	for _, want := range []struct{ service, name string }{
		{"pagerarchive-client", "page"}, {"pagerarchive-client", "decode"}, {"pagerarchive-client", "buffer"}, {"pagerarchive-client", "send"},
		{"pagerarchive", "POST /api/v1/pages"}, {"pagerarchive", "ingest"}, {"pagerarchive", "ingest.redact"}, {"pagerarchive", "ingest.parse"},
		{"pagerarchive", "ingest.store"}, {"pagerarchive", "ingest.dedup"}, {"pagerarchive", "ingest.locate"}, {"pagerarchive", "InsertMessage"},
		{"pagerarchive", "live.publish"}, {"pagerarchive", "integration.deliver"},
	} {
		if len(named(spans, want.service, want.name)) == 0 {
			t.Errorf("the trace has no %s span %q", want.service, want.name)
		}
	}
	// The API's span continues the client's: its parent is the send.
	for _, s := range named(spans, "pagerarchive", "POST /api/v1/pages") {
		if p, ok := byID[s.ParentID()]; !ok || p.Name != "send" || p.Service != "pagerarchive-client" {
			t.Errorf("the API's request span's parent is %q, want the client's send", p.Name)
		}
	}
	if got := ingest.Attr("pagerarchive.ingest.outcome"); got != "stored" {
		t.Errorf("outcome %q", got)
	}
	if lp := named(spans, "pagerarchive", "live.publish"); len(lp) > 0 && lp[0].Attr("pagerarchive.live.connections") != "1" {
		t.Errorf("live.publish reached %q connections, want 1", lp[0].Attr("pagerarchive.live.connections"))
	}
	var results []string
	for _, s := range named(spans, "pagerarchive", "integration.deliver") {
		results = append(results, s.Attr("pagerarchive.delivery.attempt")+":"+s.Attr("pagerarchive.delivery.result"))
	}
	slices.Sort(results)
	if !slices.Equal(results, []string{"1:retry", "2:delivered"}) {
		t.Errorf("delivery attempts %v, want 1:retry and 2:delivered", results)
	}
	for _, step := range []string{"decode", "buffer", "send"} {
		for _, s := range named(spans, "pagerarchive-client", step) {
			t.Logf("A62: client %s %v", step, duration(s).Round(time.Microsecond))
		}
	}
	for _, step := range []string{"POST /api/v1/pages", "ingest", "ingest.redact", "ingest.parse", "ingest.dedup", "ingest.locate", "ingest.store", "InsertMessage", "live.publish", "integration.deliver"} {
		for _, s := range named(spans, "pagerarchive", step) {
			t.Logf("A62: API %s %v %s", step, duration(s).Round(time.Microsecond), s.Attr("pagerarchive.delivery.result"))
		}
	}

	// Client B's copy: its own trace, outcome duplicate.
	if !eventually(10*time.Second, func() bool { _, ok := ingestSpan(col, id, "duplicate"); return ok }) {
		t.Error("no ingest span with outcome duplicate for client B's copy")
	} else {
		dup, _ := ingestSpan(col, id, "duplicate")
		dupTrace := traceOf(col, dup.TraceID())
		if dup.TraceID() == ingest.TraceID() || len(named(dupTrace, "pagerarchive-client", "send")) == 0 {
			t.Errorf("the duplicate's trace isn't client B's own")
		}
		t.Logf("A62: client B's copy: trace %s, outcome %s, message %s", dup.TraceID(), dup.Attr("pagerarchive.ingest.outcome"), dup.Attr("pagerarchive.message.id"))
	}

	// The API unreachable for 30 s while client A holds a page.
	proxyA.down()
	held := "QDA62 held while the server was unreachable"
	e.posts = append(e.posts, held)
	a.decode("0100002", held)
	time.Sleep(30 * time.Second)
	proxyA.up()
	heldID := e.messageID(held, 70*time.Second)
	var heldIngest telemetrytest.Span
	if !eventually(10*time.Second, func() bool {
		var ok bool
		heldIngest, ok = ingestSpan(col, heldID, "stored")
		return ok && len(named(traceOf(col, heldIngest.TraceID()), "pagerarchive-client", "page")) == 1
	}) {
		t.Fatal("no trace for the held page")
	}
	heldTrace := traceOf(col, heldIngest.TraceID())
	buf := named(heldTrace, "pagerarchive-client", "buffer")
	sends := named(heldTrace, "pagerarchive-client", "send")
	if len(buf) != 1 {
		t.Fatalf("%d buffer spans", len(buf))
	}
	d := duration(buf[0])
	t.Logf("A62: the held page buffered for %v, over %d send attempts", d.Round(time.Millisecond), len(sends))
	if d < 29*time.Second || d > 45*time.Second {
		t.Errorf("buffering %v, want about 30 s", d)
	}
	if len(sends) < 2 {
		t.Errorf("%d send attempts for the held page, want failed ones before the one that worked", len(sends))
	}
	a.stop()
	b.stop()
}

func spanNames(spans []telemetrytest.Span) []string {
	var out []string
	for _, s := range spans {
		out = append(out, s.Service+"/"+s.Name)
	}
	slices.Sort(out)
	return slices.Compact(out)
}

// A63. Nothing private exported: page text, the search term, redacted
// values, tokens, cookies, client keys, secrets and email addresses never
// reach the collector, in any span name, attribute, event, resource,
// metric label or log record (R70); HTTP spans name routes; B's spans carry
// B's number and role. The client prints pages to its console
// (PRINT_PAGES=true), which never reaches the collector (R13a, R73).
func TestA63NothingPrivate(t *testing.T) {
	col := telemetrytest.New()
	defer col.Close()
	otelTestEnv(t, col.URL)
	e := setup(t, traced{tel: startTelemetry(t, io.Discard)})
	e.allowLoopback()
	iss := newIssuer(t, "https://auth.example.test/application/o/pagerarchive-powershell/")
	e.usePowerShell(iss)

	// An integration with secrets: a URL path and an auth header.
	hook := standin.Webhook()
	defer hook.Close()
	const hookSecret, headerSecret = "hook-secret-0d1e9c", "header-secret-77ab21"
	req := webhookTo("A63 hook", hook, "0100001")
	req.Secrets.URL = hook.URL + "/hook/" + hookSecret
	req.Config.AuthHeader = "X-Token"
	req.Secrets.AuthValue = headerSecret
	e.createIntegration(e.user, req)
	e.runWorker()

	// The fixture's pages, redacted details included, through the client
	// with PRINT_PAGES on.
	rc := e.startReceiver("a", e.url, "PRINT_PAGES=true")
	for _, p := range a63Pages {
		e.posts = append(e.posts, p.text)
		rc.decode(p.capcode, p.text)
	}
	shed := e.messageID("@@EX0000063 HSF1 SHED FIRE @ 3 FICTION ST, MADEUPTON #STN1", 10*time.Second)
	e.messageID("QDUNKNOWN PAGER TEXT FOR A63", 10*time.Second)
	if !eventually(10*time.Second, func() bool { return len(hook.Requests()) >= 1 }) {
		t.Error("the integration didn't deliver")
	}

	// User B searches for SHED FIRE and opens a message; a live stream with
	// the search; a request fails with page text in its query.
	if code, _ := e.user.raw(http.MethodGet, "/api/v1/messages?q=SHED+FIRE", nil); code != http.StatusOK {
		t.Errorf("search: %d", code)
	}
	if code, _ := e.user.raw(http.MethodGet, fmt.Sprintf("/api/v1/messages/%d", shed), nil); code != http.StatusOK {
		t.Errorf("open message: %d", code)
	}
	if code, _ := e.user.raw(http.MethodGet, "/api/v1/messages?priority=SHED+FIRE", nil); code != http.StatusBadRequest {
		t.Errorf("a bad search answered %d, want 400", code)
	}
	if code, _ := e.user.raw(http.MethodGet, "/api/v1/SHED%20FIRE?q=SHED+FIRE", nil); code != http.StatusNotFound {
		t.Errorf("an unknown path answered %d, want 404", code)
	}
	e.stream("q=SHED+FIRE")
	time.Sleep(300 * time.Millisecond)
	// An admin signed in with the PowerShell module.
	token := iss.accessToken(t, e.admin.claims.Subject, nil)
	if code, p, _ := e.bearer(http.MethodGet, "/api/v1/admin/templates", token, nil); code != http.StatusOK {
		t.Errorf("the module's request: %d %+v", code, p)
	}
	rc.stop()
	// The API's telemetry is flushed by its shutdown at the end; wait for
	// the batches instead, then check.
	time.Sleep(time.Second)

	secrets := e.privateStrings(a63Pages, map[string]string{"the module's token": token, "the hook's secret URL": hookSecret, "the auth header's secret": headerSecret})
	dump := col.Dump()
	var found []string
	for what, s := range secrets {
		if strings.Contains(dump, s) {
			found = append(found, what)
			i := strings.Index(dump, s)
			t.Errorf("exported telemetry contains %s: …%s…", what, dump[max(0, i-120):min(len(dump), i+len(s)+40)])
		}
	}
	slices.Sort(found)
	// The client's console did print them (R13a).
	if !strings.Contains(rc.stdout.String(), "SHED FIRE") {
		t.Error("PRINT_PAGES=true printed no page text: the check proves nothing")
	}

	spansAll := col.Spans()
	logs := col.Logs()
	points := col.Points()
	routes := map[string]bool{}
	for _, n := range telemetry.AllowedSpanNames() {
		routes[n] = true
	}
	var userSpans, wrongRole, badSpans int
	for _, s := range spansAll {
		bad := false
		if s.Name == telemetry.Unnamed || !routes[s.Name] {
			bad = true
			t.Errorf("span named %q", s.Name)
		}
		if s.Kind.String() == "SPAN_KIND_SERVER" && !regexp.MustCompile(`^[A-Z_]+ (/[^ ]*|unmatched)$`).MatchString(s.Name) {
			bad = true
			t.Errorf("HTTP span named %q, not a route", s.Name)
		}
		if s.Attr("user.id") == strconv.FormatInt(e.user.id, 10) {
			userSpans++
			if s.Attr("user.role") != "user" {
				wrongRole++
				t.Errorf("B's span %q has role %q", s.Name, s.Attr("user.role"))
			}
		}
		for _, k := range []string{"pagerarchive.message.id"} {
			if s.HasAttr(k) && s.Name != "ingest" {
				bad = true
				t.Errorf("span %q carries %s", s.Name, k)
			}
		}
		if s.Attr("user.id") != "" && (s.HasAttr("pagerarchive.message.id") || strings.Contains(s.Name, "{id}") && s.HasAttr("http.target")) {
			bad = true
			t.Errorf("a user's span %q carries an ID of what they looked at", s.Name)
		}
		if bad {
			badSpans++
		}
	}
	if userSpans < 4 {
		t.Errorf("%d spans carry B's user ID, want B's requests (at least 4)", userSpans)
	}
	var serverNames []string
	for _, s := range spansAll {
		if s.Kind.String() == "SPAN_KIND_SERVER" {
			serverNames = append(serverNames, s.Name)
		}
	}
	slices.Sort(serverNames)
	result := "none found"
	if len(found) > 0 {
		result = fmt.Sprintf("FOUND %d: %s", len(found), strings.Join(found, ", "))
	}
	t.Logf("A63: checked %d spans, %d log records and %d metric points (%d bytes) for %d private strings: %s",
		len(spansAll), len(logs), len(points), len(dump), len(secrets), result)
	if badSpans > 0 {
		t.Logf("A63: %d spans with a name or attribute that isn't allowed (above)", badSpans)
	}
	t.Logf("A63: HTTP spans: %v", slices.Compact(serverNames))
	roles := "all with role user"
	if wrongRole > 0 {
		roles = fmt.Sprintf("%d with another role", wrongRole)
	}
	exported := "none exported"
	if len(found) > 0 {
		exported = "some exported (above)"
	}
	t.Logf("A63: %d spans carry B's user ID %d, %s; the client's console printed %d lines of page text, %s",
		userSpans, e.user.id, roles, strings.Count(rc.stdout.String(), "\n"), exported)
	if len(logs) == 0 || len(points) == 0 {
		t.Errorf("no log records (%d) or metric points (%d) reached the collector: the check proves less than it says", len(logs), len(points))
	}
}

// A64. Collector down: every page is stored and delivered on time while
// users browse, and the API and the client each say at most once a minute
// that telemetry is being dropped (R69).
func TestA64CollectorDown(t *testing.T) {
	// "off" is the same run with telemetry off, for comparison.
	for _, mode := range []string{"off", "stopped", "hung"} {
		t.Run(mode, func(t *testing.T) {
			var endpoint string
			switch mode {
			case "off":
			case "stopped": // nothing listens: connections are refused
				l, _ := net.Listen("tcp", "127.0.0.1:0")
				endpoint = "http://" + l.Addr().String()
				l.Close()
			case "hung": // takes the request and never answers
				l, err := net.Listen("tcp", "127.0.0.1:0")
				if err != nil {
					t.Fatal(err)
				}
				defer l.Close()
				go func() {
					for {
						c, err := l.Accept()
						if err != nil {
							return
						}
						go io.Copy(io.Discard, c)
					}
				}()
				endpoint = "http://" + l.Addr().String()
			}
			otelTestEnv(t, endpoint)
			t.Setenv("OTEL_EXPORTER_OTLP_TIMEOUT", "3000")
			said := &syncBuffer{}
			start := time.Now()
			e := setup(t, traced{tel: startTelemetry(t, said)})
			e.allowLoopback()
			hook := standin.Webhook()
			defer hook.Close()
			e.createIntegration(e.user, webhookTo("A64 hook", hook, "0100001"))
			e.runWorker()

			// Users browse meanwhile: lists, searches and a live stream.
			stop := make(chan struct{})
			var browsed sync.WaitGroup
			browsed.Add(1)
			e.stream("")
			go func() {
				defer browsed.Done()
				for {
					select {
					case <-stop:
						return
					default:
					}
					e.user.raw(http.MethodGet, "/api/v1/messages?q=A64", nil)
					e.admin.raw(http.MethodGet, "/api/v1/messages", nil)
				}
			}()

			rc := e.startReceiver("a", e.url)
			posted := time.Now()
			// Ten a second: 600 times the hobby rate (R51a is per page; one
			// integration delivers its pages one at a time, ADR 0006 §3).
			for i := 0; i < 100; i++ {
				text := fmt.Sprintf("QDA64 page %03d", i)
				e.posts = append(e.posts, text)
				rc.decode("0100001", text)
				time.Sleep(100 * time.Millisecond)
			}
			if !eventually(30*time.Second, func() bool { return len(hook.Requests()) >= 100 }) {
				t.Errorf("%d of 100 pages delivered", len(hook.Requests()))
			}
			stored := time.Since(posted)
			var n int
			e.st.Pool.QueryRow(e.ctx, "SELECT count(*) FROM messages WHERE text LIKE 'QDA64 page %'").Scan(&n)
			if n != 100 {
				t.Errorf("%d of 100 pages stored", n)
			}
			// On time (R51a): each delivered within 5 s of being received.
			var late time.Duration
			rows, _ := e.st.Pool.Query(e.ctx, `SELECT EXTRACT(EPOCH FROM d.delivered_at - m.sent_at)::float8 FROM deliveries d JOIN messages m ON m.id = d.message_id WHERE d.status = 'delivered'`)
			for rows.Next() {
				var sec float64
				rows.Scan(&sec)
				late = max(late, time.Duration(sec*float64(time.Second)))
			}
			rows.Close()
			if late > 5*time.Second {
				t.Errorf("a delivery took %v after its page was received, over R51a's 5 s", late)
			}
			// Keep browsing until the exporters have given up at least once.
			if mode != "off" && !eventually(20*time.Second, func() bool {
				return strings.Contains(said.String(), "dropping telemetry") && strings.Contains(rc.stderr.String(), "dropping telemetry")
			}) {
				t.Errorf("never said telemetry was being dropped:\nAPI: %s\nclient: %s", said, rc.stderr)
			}
			close(stop)
			browsed.Wait()
			rc.stop()
			elapsed := time.Since(start)
			allowed := int(elapsed/time.Minute) + 1
			apiN, clientN := strings.Count(said.String(), "dropping telemetry"), strings.Count(rc.stderr.String(), "dropping telemetry")
			if apiN > allowed || clientN > allowed {
				t.Errorf("in %v the API said %d times and the client %d times that telemetry is dropped, at most %d allowed", elapsed, apiN, clientN, allowed)
			}
			t.Logf("A64 (%s collector): 100 pages stored and delivered %v after posting, slowest delivery %v after receipt; in %v the API said %d time(s) and the client %d time(s) that telemetry was dropped",
				mode, stored.Round(time.Millisecond), late.Round(time.Millisecond), elapsed.Round(time.Second), apiN, clientN)
		})
	}
}

// A66. Client telemetry (R74): 5 pages read and sent, the API stopped, 2
// more read: the collector shows 7 decoded, 5 sent, 2 buffered, failed
// sends counting up, and the times of the last decoded line and send.
func TestA66ClientTelemetry(t *testing.T) {
	col := telemetrytest.New()
	defer col.Close()
	otelTestEnv(t, col.URL)
	e := setup(t)
	proxy := newTCPProxy(t, strings.TrimPrefix(e.url, "http://"))
	rc := e.startReceiver("a", proxy.URL())
	const svc = "pagerarchive-client"
	latest := func(m string) float64 { v, _ := col.Latest(svc, m); return v }

	fmt.Fprintln(rc.stdin, "Enabled demodulators: POCSAG512 POCSAG1200 POCSAG2400") // a line that isn't a page
	for i := 1; i <= 5; i++ {
		text := fmt.Sprintf("QDA66 page %d", i)
		e.posts = append(e.posts, text)
		rc.decode("0100001", text)
	}
	if !eventually(10*time.Second, func() bool { return latest("pagerarchive.client.pages_sent") == 5 }) {
		t.Fatalf("pages sent: %v, want 5", latest("pagerarchive.client.pages_sent"))
	}
	sentAt := latest("pagerarchive.client.last_sent_timestamp")
	proxy.down()
	stopped := time.Now()
	for i := 6; i <= 7; i++ {
		text := fmt.Sprintf("QDA66 page %d", i)
		e.posts = append(e.posts, text)
		rc.decode("0100001", text)
	}
	if !eventually(10*time.Second, func() bool {
		return latest("pagerarchive.client.pages_decoded") == 7 && latest("pagerarchive.client.pages_buffered") == 2 &&
			latest("pagerarchive.client.send_failures") >= 2
	}) {
		t.Errorf("decoded %v, buffered %v, failed sends %v", latest("pagerarchive.client.pages_decoded"), latest("pagerarchive.client.pages_buffered"), latest("pagerarchive.client.send_failures"))
	}
	f1 := latest("pagerarchive.client.send_failures")
	time.Sleep(4 * time.Second)
	f2 := latest("pagerarchive.client.send_failures")
	if f2 <= f1 {
		t.Errorf("failed sends didn't count up while the API was unreachable: %v then %v", f1, f2)
	}
	decodedAt := latest("pagerarchive.client.last_decoded_timestamp")
	lastSent := latest("pagerarchive.client.last_sent_timestamp")
	if decodedAt < float64(stopped.Unix())-1 || lastSent != sentAt || lastSent >= decodedAt {
		t.Errorf("last decoded %v, last sent %v (before the API stopped: %v)", decodedAt, lastSent, sentAt)
	}
	t.Logf("A66: lines read %v, decoded %v, sent %v, buffered %v, failed sends %v then %v, retries %v, last decoded %s, last sent %s",
		latest("pagerarchive.client.lines_read"), latest("pagerarchive.client.pages_decoded"), latest("pagerarchive.client.pages_sent"),
		latest("pagerarchive.client.pages_buffered"), f1, f2, latest("pagerarchive.client.send_retries"),
		time.Unix(int64(decodedAt), 0).UTC().Format(time.TimeOnly), time.Unix(int64(lastSent), 0).UTC().Format(time.TimeOnly))
	if latest("pagerarchive.client.lines_read") != 8 {
		t.Errorf("lines read %v, want 8", latest("pagerarchive.client.lines_read"))
	}
	proxy.up()
	if !eventually(70*time.Second, func() bool { return latest("pagerarchive.client.pages_sent") == 7 }) {
		t.Errorf("after the API came back, pages sent %v, want 7", latest("pagerarchive.client.pages_sent"))
	}
	rc.stop()
}

// R72, R75: /metrics keeps its names and adds requests by route and status,
// the database pool, live connections and the Go runtime; label values are
// routes and statuses, never users; OpenMetrics adds exemplars.
func TestM12Metrics(t *testing.T) {
	col := telemetrytest.New()
	defer col.Close()
	otelTestEnv(t, col.URL)
	e := setup(t, traced{tel: startTelemetry(t, io.Discard)})
	e.mustPost("a", "0100001", row1Text, e.now)
	e.user.raw(http.MethodGet, "/api/v1/messages?q=SMOKE", nil)
	e.user.raw(http.MethodGet, "/api/v1/messages/999999", nil)
	e.stream("")
	time.Sleep(200 * time.Millisecond)

	scrape := func(accept string) string {
		req, _ := http.NewRequest(http.MethodGet, e.url+"/metrics", nil)
		req.Header.Set("Authorization", "Bearer "+metricsToken)
		if accept != "" {
			req.Header.Set("Accept", accept)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		return string(b)
	}
	text := scrape("")
	om := scrape("application/openmetrics-text;version=1.0.0,text/plain;version=0.0.4;q=0.5")
	for _, want := range []string{
		// R52's names, unchanged (A32).
		`pagerarchive_pages_received_total{outcome="stored"} 1`, "pagerarchive_client_receiving{", "pagerarchive_client_last_seen_timestamp_seconds{",
		// R72's additions.
		`pagerarchive_http_requests_total{route="GET /api/v1/messages",status="200"} 1`,
		`pagerarchive_http_requests_total{route="GET /api/v1/messages/{id}",status="404"} 1`,
		`pagerarchive_http_requests_total{route="POST /api/v1/pages",status="200"} 1`,
		`pagerarchive_http_request_duration_seconds_bucket{le="+Inf",route="GET /api/v1/messages",status="200"} 1`,
		`pagerarchive_db_pool_connections{state="in_use"}`, "pagerarchive_db_pool_max_connections ", `pagerarchive_live_connections 1`,
		"go_memstats_heap_inuse_bytes ", "go_goroutines ", "process_cpu_seconds_total ", "process_resident_memory_bytes ",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("/metrics lacks %q", want)
		}
	}
	if strings.Contains(text, "SMOKE") || strings.Contains(text, "999999") || strings.Contains(text, "user") && strings.Contains(text, `user="`) {
		t.Error("/metrics carries a query, an ID or a user")
	}
	if !strings.HasSuffix(om, "# EOF\n") || !strings.Contains(om, "# TYPE pagerarchive_pages_received counter") ||
		!regexp.MustCompile(`pagerarchive_http_request_duration_seconds_bucket\{[^}]+\} \d+ # \{trace_id="[0-9a-f]{32}"\} [0-9.]+ [0-9.]+`).MatchString(om) {
		t.Errorf("OpenMetrics without exemplars or EOF:\n%s", om[:min(len(om), 3000)])
	}
	var ex string
	sc := bufio.NewScanner(strings.NewReader(om))
	for sc.Scan() {
		if strings.Contains(sc.Text(), "# {trace_id=") {
			ex = sc.Text()
			break
		}
	}
	t.Logf("R72: %d lines of metrics; an exemplar: %s", strings.Count(text, "\n"), ex)
}

// a63Pages are the fixture's pages, redacted details included, and a
// shed fire for B to search for (A63).
var a63Pages = []struct{ capcode, text string }{
	{"0100001", row1Text},
	{"0100002", "@@EX0000002 GRS3 GRASS FIRE @ SAMPLE RD, MADEUPTON #STN2"},
	{"0100001", row3Text},
	{"0100003", "QDTraining Tuesday 7pm at the station"},
	{"0100001", "QDCheck the shed Saturday. Text 0412 345 678 if available. EX0000001"},
	{"0100001", "@@EX0000005 MED1 PERSON COLLAPSED AGE: 52 SEX: M @ 1 EXAMPLE ST, MADEUPTON #STN1"},
	{"0100001", "@@EX0000063 HSF1 SHED FIRE @ 3 FICTION ST, MADEUPTON #STN1"},
	{"0999999", "QDUNKNOWN PAGER TEXT FOR A63"},
}

// privateStrings is everything A63 says telemetry must never hold, by
// what it is: page text (whole, without the marker, and in pieces), the
// search term as typed and as sent, redacted values, sessions' cookies,
// client keys and email addresses, plus more.
func (e *env) privateStrings(pages []struct{ capcode, text string }, more map[string]string) map[string]string {
	out := map[string]string{
		"search term": "SHED FIRE", "search term (query)": "SHED+FIRE", "search term (path)": "SHED%20FIRE",
		"redacted phone": "0412 345 678", "redacted phone (no spaces)": "0412345678", "redacted age": "AGE: 52", "redacted sex": "SEX: M",
		"B's cookie": e.user.cookie.Value, "the admin's cookie": e.admin.cookie.Value,
		"client A's key": e.keys["a"], "client B's key": e.keys["b"], "an email address": "@example.com",
	}
	for _, p := range pages {
		out["page text "+p.text] = p.text
		out["page text without its marker "+p.text] = p.text[2:]
	}
	for _, frag := range []string{"SMOKE FROM ROOF", "GRASS FIRE", "CREWS RETURNING", "Training Tuesday", "PERSON COLLAPSED", "FICTION ST", "EXAMPLE ST", "SAMPLE RD", "UNKNOWN PAGER"} {
		out["page text "+frag] = frag
	}
	for k, v := range more {
		out[k] = v
	}
	return out
}

// R70 at the source: the spans the code makes, before the export
// allowlist sees them, have allowed names and keys and hold nothing
// private, and a browser's trace context isn't continued. The allowlist is
// the rule; this shows the code doesn't lean on it, so a careless
// attribute fails here even though the allowlist would drop it.
func TestM12SourceSpans(t *testing.T) {
	rec := tracetest.NewSpanRecorder()
	tp := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(rec))
	defer tp.Shutdown(context.Background())
	e := setup(t, traced{tracer: tp.Tracer("pagerarchive")})
	e.allowLoopback()
	iss := newIssuer(t, "https://auth.example.test/application/o/pagerarchive-powershell/")
	e.usePowerShell(iss)
	hook := standin.Webhook()
	defer hook.Close()
	const hookSecret, headerSecret = "hook-secret-5a7c1e", "header-secret-90bb3d"
	req := webhookTo("source hook", hook, "0100001")
	req.Secrets.URL = hook.URL + "/hook/" + hookSecret
	req.Config.AuthHeader = "X-Token"
	req.Secrets.AuthValue = headerSecret
	e.createIntegration(e.user, req)
	e.runWorker()

	var shed int64
	for _, p := range a63Pages {
		pr := e.mustPost("a", p.capcode, p.text, time.Now().UTC())
		if strings.Contains(p.text, "SHED FIRE") {
			shed = pr.MessageID
		}
	}
	if !eventually(10*time.Second, func() bool { return len(hook.Requests()) >= 1 }) {
		t.Error("the integration didn't deliver")
	}
	// B searches, sending a trace context as any browser extension could.
	const sentTrace = "0af7651916cd43dd8448eb211c80319c"
	search, _ := http.NewRequest(http.MethodGet, e.url+"/api/v1/messages?q=SHED+FIRE", nil)
	search.AddCookie(e.user.cookie)
	search.Header.Set("traceparent", "00-"+sentTrace+"-b7ad6b7169203331-01")
	if resp, err := http.DefaultClient.Do(search); err != nil || resp.StatusCode != http.StatusOK {
		t.Fatalf("search: %v %v", err, resp)
	} else {
		resp.Body.Close()
	}
	e.user.raw(http.MethodGet, fmt.Sprintf("/api/v1/messages/%d", shed), nil)
	e.user.raw(http.MethodGet, "/api/v1/messages?priority=SHED+FIRE", nil)
	e.user.raw(http.MethodGet, "/api/v1/SHED%20FIRE?q=SHED+FIRE", nil)
	e.stream("q=SHED+FIRE")
	token := iss.accessToken(t, e.admin.claims.Subject, nil)
	e.bearer(http.MethodGet, "/api/v1/admin/templates", token, nil)
	time.Sleep(300 * time.Millisecond)

	private := e.privateStrings(a63Pages, map[string]string{"the module's token": token, "the hook's secret URL": hookSecret, "the auth header's secret": headerSecret})
	names := map[string]bool{}
	for _, n := range telemetry.AllowedSpanNames() {
		names[n] = true
	}
	spans := rec.Ended()
	attrs, userSpans := 0, 0
	for _, s := range spans {
		if !names[s.Name()] {
			t.Errorf("span named %q, not a route, query or step", s.Name())
		}
		if s.SpanContext().TraceID().String() == sentTrace {
			t.Errorf("span %q continues the trace a browser sent", s.Name())
		}
		if len(s.Events()) > 0 || s.Status().Description != "" {
			t.Errorf("span %q has events or a status message", s.Name())
		}
		for _, kv := range s.Attributes() {
			attrs++
			if _, ok := telemetry.SpanKeys[string(kv.Key)]; !ok {
				t.Errorf("span %q has attribute %s, not on the allowlist", s.Name(), kv.Key)
			}
			v := kv.Value.Emit()
			for what, p := range private {
				if strings.Contains(v, p) || strings.Contains(s.Name(), p) {
					t.Errorf("span %q carries %s in %s", s.Name(), what, kv.Key)
				}
			}
			if kv.Key == "user.id" && kv.Value.AsInt64() == e.user.id {
				userSpans++
			}
		}
	}
	if userSpans < 4 {
		t.Errorf("%d spans carry B's user ID, want B's requests", userSpans)
	}
	result := "allowed names and keys only, nothing private, no browser trace continued"
	if t.Failed() {
		result = "NOT all allowed or private: see the errors above"
	}
	t.Logf("R70: %d spans and %d attributes as the code made them: %s; %d carry B's user ID",
		len(spans), attrs, result, userSpans)
}
