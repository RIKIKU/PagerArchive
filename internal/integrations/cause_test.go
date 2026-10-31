package integrations

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"go.opentelemetry.io/otel/attribute"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"

	"github.com/RIKIKU/PagerArchive/internal/telemetry"
)

// aimedAt aims an integration of kind at base ("http://127.0.0.1:1234"),
// wherever that kind's address comes from: its secrets (webhook, Discord),
// the presets (Telegram, Pushover) or its settings (ntfy, MQTT).
func aimedAt(kind Kind, base string) (loaded, Secrets, Presets) {
	p := Presets{DiscordPrefixes: []string{base + "/api/webhooks/"}, TelegramBase: base, PushoverURL: base + "/1/messages.json", NtfyServer: base}
	switch kind {
	case Webhook:
		return integration(kind, Full, Config{}), Secrets{URL: base + "/hook"}, p
	case Discord:
		return integration(kind, Full, Config{}), Secrets{URL: base + "/api/webhooks/1234567890/AbC-dEf_123"}, p
	case Telegram:
		return integration(kind, Full, Config{ChatID: "-100123456"}), Secrets{BotToken: "123456:ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghij"}, p
	case Pushover:
		return integration(kind, Full, Config{}), Secrets{AppToken: "azGDORePK8gMaC0QOYAMyEEuzJnyUi", UserKey: "uQiRzpo4DXghDmr9QzzfQu27cmVRsG"}, p
	case Ntfy:
		return integration(kind, Full, Config{Server: base}), Secrets{Topic: "pager-test_1"}, p
	case MQTT:
		return integration(kind, Full, Config{Broker: base, Topic: "pagerarchive/test"}), Secrets{}, p
	}
	panic(kind)
}

// listen runs serve on each connection to a new loopback listener.
func listen(t *testing.T, serve func(net.Conn)) string {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func() { defer c.Close(); serve(c) }()
		}
	}()
	return ln.Addr().String()
}

// broker answers MQTT CONNECT with return code rc, then reads and ignores
// everything: a PUBLISH is never acknowledged.
func broker(t *testing.T, rc byte) string {
	return listen(t, func(c net.Conn) {
		buf := make([]byte, 512)
		if _, err := c.Read(buf); err != nil {
			return
		}
		_, _ = c.Write([]byte{0x20, 0x02, 0x00, rc}) // CONNACK
		_, _ = io.Copy(io.Discard, c)
	})
}

// Every way an attempt can fail, for every kind of integration, puts its
// cause on the attempt's span as error.type, a kind the telemetry filter
// keeps as it is (R70), and what it means for the page as
// pagerarchive.delivery.failure. Without a cause the span would say only
// "retry" (the M12 hands-on: an MQTT broker that was down).
func TestFailureCauses(t *testing.T) {
	closed := func() string {
		ln, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		defer ln.Close()
		return ln.Addr().String() // nothing listens: connection refused
	}()
	status := func(code int) string {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(code) }))
		t.Cleanup(srv.Close)
		return srv.URL
	}
	hang := make(chan struct{})
	hung := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { <-hang }))
	t.Cleanup(hung.Close)
	t.Cleanup(func() { close(hang) }) // runs first, so Close isn't left waiting
	tlsSrv := httptest.NewTLSServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	t.Cleanup(tlsSrv.Close)
	plain := status(200)
	host := func(u string) string { return strings.TrimPrefix(strings.TrimPrefix(u, "http://"), "https://") }

	type way struct {
		name, base, cause, failure string
		setup                      func(*Service, context.Context) (context.Context, context.CancelFunc)
	}
	expired := func(_ *Service, ctx context.Context) (context.Context, context.CancelFunc) {
		return context.WithDeadline(ctx, time.Now().Add(-time.Second))
	}
	shortWait := func(_ *Service, ctx context.Context) (context.Context, context.CancelFunc) {
		return context.WithTimeout(ctx, 300*time.Millisecond)
	}
	notAllowed := func(s *Service, ctx context.Context) (context.Context, context.CancelFunc) {
		s.Guard.Allow = nil
		return ctx, func() {}
	}
	noDNS := func(s *Service, ctx context.Context) (context.Context, context.CancelFunc) {
		s.Guard.Resolver = &fakeResolver{}
		return ctx, func() {}
	}
	httpWays := []way{
		{name: "connection refused", base: "http://" + closed, cause: "connection_refused", failure: "retryable"},
		{name: "DNS lookup failed", base: "http://hooks.example.test", cause: "dns_lookup_failed", failure: "retryable", setup: noDNS},
		{name: "address not allowed", base: "http://" + closed, cause: "address_not_allowed", failure: "retryable", setup: notAllowed},
		{name: "timeout", base: "http://" + closed, cause: "timeout", failure: "retryable", setup: expired},
		{name: "TLS certificate not trusted", base: tlsSrv.URL, cause: "tls_not_trusted", failure: "retryable"},
		{name: "not TLS", base: "https://" + host(plain), cause: "tls_not_tls", failure: "retryable"},
		{name: "HTTP 503", base: status(503), cause: "HTTP 503", failure: "retryable"},
		{name: "HTTP 429", base: status(429), cause: "HTTP 429", failure: "retryable"},
		{name: "HTTP 401", base: status(401), cause: "HTTP 401", failure: "refused"},
		{name: "HTTP 404", base: status(404), cause: "HTTP 404", failure: "refused"},
		{name: "redirect", base: status(302), cause: "HTTP 302", failure: "refused"},
		{name: "no answer", base: hung.URL, cause: "no_answer", failure: "no_answer", setup: shortWait},
		{name: "address not valid", base: "http://bad host", cause: "bad_request", failure: "refused"},
	}
	mqttWays := []way{
		{name: "connection refused", base: "mqtt://" + closed, cause: "connection_refused", failure: "retryable"},
		{name: "DNS lookup failed", base: "mqtt://broker.example.test:1883", cause: "dns_lookup_failed", failure: "retryable", setup: noDNS},
		{name: "address not allowed", base: "mqtt://" + closed, cause: "address_not_allowed", failure: "retryable", setup: notAllowed},
		{name: "no answer to the connection", base: "mqtt://" + listen(t, func(c net.Conn) { _, _ = io.Copy(io.Discard, c) }), cause: "timeout", failure: "retryable"},
		{name: "TLS certificate not trusted", base: "mqtts://" + host(tlsSrv.URL), cause: "tls_not_trusted", failure: "retryable"},
		{name: "not TLS", base: "mqtts://" + host(plain), cause: "tls_not_tls", failure: "retryable"},
		{name: "bad user name or password", base: "mqtt://" + broker(t, 4), cause: "auth_refused", failure: "refused"},
		{name: "not authorised", base: "mqtt://" + broker(t, 5), cause: "auth_refused", failure: "refused"},
		{name: "protocol refused", base: "mqtt://" + broker(t, 1), cause: "mqtt_protocol_refused", failure: "refused"},
		{name: "unavailable", base: "mqtt://" + broker(t, 3), cause: "mqtt_unavailable", failure: "retryable"},
		{name: "publish not acknowledged", base: "mqtt://" + broker(t, 0), cause: "no_answer", failure: "no_answer"},
		{name: "address not valid", base: "mqtt://", cause: "bad_request", failure: "refused"},
	}

	var mu sync.Mutex
	causes := map[Kind]map[string]bool{}
	var wrong []string
	t.Run("kinds", func(t *testing.T) {
		for _, kind := range Kinds {
			ways := httpWays
			if kind == MQTT {
				ways = mqttWays
			}
			for _, w := range ways {
				t.Run(string(kind)+"/"+w.name, func(t *testing.T) {
					t.Parallel()
					l, sec, presets := aimedAt(kind, w.base)
					s := service(t, presets)
					ctx, cancel := context.Background(), context.CancelFunc(func() {})
					if w.setup != nil {
						ctx, cancel = w.setup(s, ctx)
					}
					res := s.send(ctx, l, sec, s.pageOf(row1(), false), false)
					cancel()

					rec := tracetest.NewSpanRecorder()
					tp := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(rec))
					sctx, span := tp.Tracer("test").Start(context.Background(), "integration.deliver")
					status := "retry"
					if res.outcome != retryable {
						status = statusFailed
					}
					attemptResult(sctx, status, res)
					span.End()
					got := map[attribute.Key]string{}
					for _, kv := range rec.Ended()[0].Attributes() {
						got[kv.Key] = kv.Value.Emit()
					}
					mu.Lock()
					defer mu.Unlock()
					if got["error.type"] != w.cause || got["pagerarchive.delivery.failure"] != w.failure {
						wrong = append(wrong, fmt.Sprintf("%s %s: error.type %q, failure %q (err %q); want %q, %q",
							kind, w.name, got["error.type"], got["pagerarchive.delivery.failure"], res.err, w.cause, w.failure))
						t.Errorf("error.type %q, pagerarchive.delivery.failure %q (the owner sees %q); want %q, %q",
							got["error.type"], got["pagerarchive.delivery.failure"], res.err, w.cause, w.failure)
						return
					}
					if telemetry.ErrorKind(w.cause) != w.cause {
						t.Errorf("the filter turns cause %q into %q", w.cause, telemetry.ErrorKind(w.cause))
					}
					if causes[kind] == nil {
						causes[kind] = map[string]bool{}
					}
					causes[kind][w.cause] = true
				})
			}
		}
	})
	if t.Failed() {
		t.Logf("R71: a failed delivery's span DOESN'T always say why: %d of %d ways wrong:\n  %s",
			len(wrong), 5*len(httpWays)+len(mqttWays), strings.Join(wrong, "\n  "))
		return
	}
	for _, kind := range Kinds {
		var cs []string
		for c := range causes[kind] {
			cs = append(cs, c)
		}
		sort.Strings(cs)
		t.Logf("R71: %s: %d ways to fail, each with its cause on the span: %s", kind, len(cs), strings.Join(cs, ", "))
	}
}

// A delivery skipped because the page got too old says so; one that was
// tried keeps its last attempt's cause.
func TestTooOldCause(t *testing.T) {
	for _, c := range []struct {
		res  result
		want string
	}{
		{result{cause: "too_old", err: "skipped: too old"}, "too_old"},
		{result{outcome: retryable, cause: "connection_refused", err: "connection refused"}, "connection_refused"},
		{result{outcome: retryable}, "other"},
	} {
		rec := tracetest.NewSpanRecorder()
		tp := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(rec))
		ctx, span := tp.Tracer("test").Start(context.Background(), "integration.deliver")
		attemptResult(ctx, statusTooOld, c.res)
		span.End()
		for _, kv := range rec.Ended()[0].Attributes() {
			if kv.Key == "error.type" && kv.Value.AsString() != c.want {
				t.Errorf("%+v: error.type %q, want %q", c.res, kv.Value.AsString(), c.want)
			}
		}
	}
}
