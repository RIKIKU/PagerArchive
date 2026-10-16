package acceptance

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
	mqttserver "github.com/mochi-mqtt/server/v2"
	"github.com/mochi-mqtt/server/v2/hooks/auth"
	"github.com/mochi-mqtt/server/v2/listeners"
	"github.com/mochi-mqtt/server/v2/packets"

	"github.com/RIKIKU/PagerArchive/internal/integrations"
	"github.com/RIKIKU/PagerArchive/internal/integrations/standin"
	"github.com/RIKIKU/PagerArchive/internal/server"
)

// Golden rows 1 and 3, and a grass fire, as posted (SPEC.md section 10).
const (
	row1Text  = "@@EX0000001 HSF1 SMOKE FROM ROOF @ 12 FICTION ST, MADEUPTON GRID 123456 UNITS T1 T2 #STN1"
	row3Text  = "HbRE EX0000001 CREWS RETURNING #STN1"
	grassText = "@@EX0000002 GRS3 GRASS FIRE @ SAMPLE RD, MADEUPTON #STN1"
)

// allowLoopback has the admin allow 127.0.0.0/8, where the test receivers
// listen (R51).
func (e *env) allowLoopback() {
	e.t.Helper()
	if code, p := e.admin.call(http.MethodPut, "/api/v1/admin/integration-hosts", server.AllowListBody{Entries: []string{"127.0.0.0/8"}}, nil); code != http.StatusOK {
		e.t.Fatalf("allow list: %d %s", code, p.Error)
	}
}

func (e *env) createIntegration(m *member, req integrations.Request) integrations.View {
	e.t.Helper()
	var v integrations.View
	if code, p := m.call(http.MethodPost, "/api/v1/integrations", req, &v); code != http.StatusCreated {
		e.t.Fatalf("create integration %q: %d %s", req.Name, code, p.Error)
	}
	return v
}

func webhookTo(name string, r *standin.Server, capcodes ...string) integrations.Request {
	return integrations.Request{Name: name, Kind: integrations.Webhook, Capcodes: capcodes, Secrets: integrations.Secrets{URL: r.URL + "/hook"}}
}

// runWorker runs the delivery worker for the rest of the test.
func (e *env) runWorker() {
	ctx, cancel := context.WithCancel(e.ctx)
	done := make(chan struct{})
	go func() { e.integ.Run(ctx); close(done) }()
	e.t.Cleanup(func() { cancel(); <-done })
}

// eventually waits up to d for cond.
func eventually(d time.Duration, cond func() bool) bool {
	end := time.Now().Add(d)
	for time.Now().Before(end) {
		if cond() {
			return true
		}
		time.Sleep(20 * time.Millisecond)
	}
	return cond()
}

// raw makes a request as m and returns the status and body.
func (m *member) raw(method, path string, body any) (int, string) {
	m.e.t.Helper()
	resp, err := m.do(method, m.e.url+path, body)
	if err != nil {
		m.e.t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(b)
}

func (e *env) deliveries(m *member, id int64) []integrations.Delivery {
	e.t.Helper()
	var out []integrations.Delivery
	if code, p := m.call(http.MethodGet, fmt.Sprintf("/api/v1/integrations/%d/deliveries", id), nil, &out); code != http.StatusOK {
		e.t.Fatalf("deliveries: %d %s", code, p.Error)
	}
	return out
}

func (e *env) integration(m *member, id int64) integrations.View {
	e.t.Helper()
	var v integrations.View
	if code, p := m.call(http.MethodGet, fmt.Sprintf("/api/v1/integrations/%d", id), nil, &v); code != http.StatusOK {
		e.t.Fatalf("integration: %d %s", code, p.Error)
	}
	return v
}

func (e *env) aliasID(name string) int64 {
	e.t.Helper()
	var id int64
	if err := e.st.Pool.QueryRow(e.ctx, "SELECT id FROM aliases WHERE name = $1", name).Scan(&id); err != nil {
		e.t.Fatal(err)
	}
	return id
}

// noPageText fails if s holds any posted page text.
func (e *env) noPageText(what, s string) {
	e.t.Helper()
	for _, text := range e.posts {
		body := text[min(2, len(text)):] // with or without the marker
		if strings.Contains(s, text) || strings.Contains(s, body) {
			e.t.Errorf("%s contains page text %q", what, text)
		}
	}
	for _, frag := range []string{"SMOKE FROM ROOF", "GRASS FIRE", "CREWS RETURNING", "FICTION ST"} {
		if strings.Contains(s, frag) {
			e.t.Errorf("%s contains page text %q", what, frag)
		}
	}
}

// A30. Integration selection and filter.
func TestA30IntegrationSelectionAndFilter(t *testing.T) {
	e := setup(t)
	e.allowLoopback()
	e.runWorker()
	hook := standin.Webhook()
	defer hook.Close()
	// User A: alias "Station 1" (0100001), Structure fire only. User B (the
	// admin here) has no integrations.
	req := integrations.Request{Name: "Station 1 structure fires", Kind: integrations.Webhook, AliasIDs: []int64{e.aliasID("Station 1")},
		Filter: integrations.Filter{Categories: []string{"Structure fire"}}, Secrets: integrations.Secrets{URL: hook.URL + "/hook"}}
	e.createIntegration(e.user, req)

	start := time.Now()
	hsf := e.mustPost("a", "0100001", row1Text, e.now).MessageID
	e.mustPost("a", "0100001", grassText, e.now)
	e.mustPost("a", "0100002", "@@EX0000003 HSF1 SMOKE FROM SHED @ 3 FICTION ST, MADEUPTON #STN2", e.now)

	if !eventually(5*time.Second-time.Since(start), func() bool { return len(hook.Requests()) >= 1 }) {
		t.Fatalf("no request within 5 s")
	}
	t.Logf("first request %v after the posts", time.Since(start).Round(time.Millisecond))
	time.Sleep(time.Second) // anything else would have arrived by now
	reqs := hook.Requests()
	if len(reqs) != 1 {
		t.Fatalf("webhook got %d requests, want exactly 1", len(reqs))
	}
	got := reqs[0].JSON
	if got["id"] != float64(hsf) || got["capcode"] != "0100001" {
		t.Errorf("request is for %v on %v, want message %d on 0100001", got["id"], got["capcode"], hsf)
	}
	if got["text"] != row1Text[2:] {
		t.Errorf("text %q, want it without the marker", got["text"])
	}
	// Nothing was queued for anyone else: B has no integrations.
	var rows int
	if err := e.st.Pool.QueryRow(e.ctx, "SELECT count(*) FROM deliveries").Scan(&rows); err != nil || rows != 1 {
		t.Errorf("delivery rows: %d (%v), want 1", rows, err)
	}
}

// A31. Integration safety: private addresses refused, and an integration
// that keeps failing is paused.
func TestA31IntegrationSafety(t *testing.T) {
	e := setup(t)
	for addr, want := range map[string]string{
		"http://127.0.0.1:8080/": "127.0.0.1 is a loopback address. Ask an admin to allow it.",
		"http://192.168.1.10/":   "192.168.1.10 is a private address. Ask an admin to allow it.",
	} {
		req := integrations.Request{Name: "Home", Kind: integrations.Webhook, Capcodes: []string{"0100001"}, Secrets: integrations.Secrets{URL: addr}}
		code, p := e.user.call(http.MethodPost, "/api/v1/integrations", req, nil)
		if code != http.StatusUnprocessableEntity || p.Error != want {
			t.Errorf("%s: %d %q, want 422 %q", addr, code, p.Error, want)
		}
	}
	// Once the admin allows the range, it can be saved.
	if code, _ := e.admin.call(http.MethodPut, "/api/v1/admin/integration-hosts", server.AllowListBody{Entries: []string{"192.168.1.0/24", "127.0.0.0/8"}}, nil); code != http.StatusOK {
		t.Fatal("allow list")
	}
	// (The test guard refuses anything outside 127.0.0.0/8, so 192.168.1.10
	// is checked against the rules but never reached.)
	hook := standin.Webhook()
	defer hook.Close()
	hook.FailWith(http.StatusInternalServerError)
	v := e.createIntegration(e.user, webhookTo("Failing", hook, "0100001"))

	for i := 1; i <= 3; i++ {
		e.mustPost("a", "0100001", fmt.Sprintf("@@EX000001%d HSF1 SMOKE FROM ROOF @ 12 FICTION ST, MADEUPTON #STN1", i), e.now)
	}
	// Each page is retried until its cut-off (controlled clock), then skipped.
	for elapsed := time.Duration(0); elapsed <= 6*time.Minute; elapsed += 20 * time.Second {
		e.integ.Drain(e.ctx)
		e.advance(20 * time.Second)
	}
	code, body := e.user.raw(http.MethodGet, fmt.Sprintf("/api/v1/integrations/%d", v.ID), nil)
	var got integrations.View
	_ = json.Unmarshal([]byte(body), &got)
	if code != http.StatusOK || got.Status != "paused" || got.PausedBy == nil || *got.PausedBy != "failing" {
		t.Fatalf("after 3 failed pages: %d status %q by %v, want paused by failing", code, got.Status, got.PausedBy)
	}
	if got.LastError == nil || *got.LastError != "HTTP 500 Internal Server Error" {
		t.Errorf("last error %v, want HTTP 500 Internal Server Error", got.LastError)
	}
	t.Logf("owner sees: status %s, paused_by %s, last_error %q", got.Status, *got.PausedBy, *got.LastError)
	e.noPageText("the owner's view", body)
	_, dbody := e.user.raw(http.MethodGet, fmt.Sprintf("/api/v1/integrations/%d/deliveries", v.ID), nil)
	e.noPageText("the owner's delivery list", dbody)
	for _, d := range e.deliveries(e.user, v.ID) {
		if d.Status != "too_old" || d.Attempts == 0 {
			t.Errorf("delivery %+v, want too_old after attempts", d)
		}
	}
	// While paused, new pages aren't queued; Resume starts it again.
	e.mustPost("a", "0100001", "@@EX0000019 HSF1 SMOKE FROM ROOF @ 12 FICTION ST, MADEUPTON #STN1", time.Now())
	if n := len(e.deliveries(e.user, v.ID)); n != 3 {
		t.Errorf("paused integration queued a page (%d deliveries)", n)
	}
	if code, p := e.user.call(http.MethodPost, fmt.Sprintf("/api/v1/integrations/%d/resume", v.ID), nil, nil); code != http.StatusNoContent {
		t.Errorf("resume: %d %s", code, p.Error)
	}
	if got := e.integration(e.user, v.ID); got.Status != "active" || got.FailedPages != 0 {
		t.Errorf("after resume: %s, %d failed pages", got.Status, got.FailedPages)
	}
}

// A32. Metrics.
func TestA32Metrics(t *testing.T) {
	e := setup(t)
	e.mustPost("a", "0100001", row1Text, e.now)
	e.mustPost("b", "0100002", grassText, e.now)
	if _, err := e.st.Pool.Exec(e.ctx, `UPDATE clients SET last_seen_at = CASE id WHEN 'a' THEN now() - interval '31 minutes' ELSE now() - interval '5 minutes' END`); err != nil {
		t.Fatal(err)
	}
	scrape := func(token string) (int, string) {
		req, _ := http.NewRequest(http.MethodGet, e.url+"/metrics", nil)
		if token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, string(b)
	}
	code, body := scrape(metricsToken)
	if code != http.StatusOK {
		t.Fatalf("scrape: %d %s", code, body)
	}
	for _, want := range []string{
		`pagerarchive_client_receiving{client="a"} 0`,
		`pagerarchive_client_receiving{client="b"} 1`,
		`pagerarchive_pages_received_total{outcome="stored"} 2`,
		`pagerarchive_messages_flagged{flag="parse_failed"} 0`,
		`pagerarchive_integration_deliveries_total{kind="webhook",result="delivered"} 0`,
		"# TYPE pagerarchive_client_last_seen_timestamp_seconds gauge",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("metrics have no %q", want)
		}
	}
	for _, line := range strings.Split(body, "\n") {
		if strings.HasPrefix(line, "pagerarchive_client") && strings.Contains(line, "receiving") {
			t.Logf("%s", line)
		}
	}
	e.noPageText("the metrics", body)
	if strings.Contains(body, "Client A") || strings.Contains(body, "Station 1") {
		t.Error("metrics name clients or aliases")
	}
	if code, _ := scrape(""); code != http.StatusUnauthorized {
		t.Errorf("no credentials: %d, want 401", code)
	}
	if code, _ := scrape("wrong-token"); code != http.StatusUnauthorized {
		t.Errorf("wrong credentials: %d, want 401", code)
	}
	// A session isn't enough either: metrics need the token.
	if code, _ := e.admin.raw(http.MethodGet, "/metrics", nil); code != http.StatusUnauthorized {
		t.Errorf("admin session: %d, want 401", code)
	}
	e.srv.MetricsToken = ""
	if code, _ := scrape(metricsToken); code != http.StatusNotFound {
		t.Errorf("METRICS_TOKEN unset: %d, want 404", code)
	}
}

// A35. Two actions, one capcode.
func TestA35TwoActionsOneCapcode(t *testing.T) {
	e := setup(t)
	e.allowLoopback()
	e.runWorker()
	emergency, other := standin.Webhook(), standin.Webhook()
	defer emergency.Close()
	defer other.Close()
	a := webhookTo("Emergency", emergency, "0100001")
	a.Filter.Priorities = []string{"Emergency"}
	b := webhookTo("Non-emergency", other, "0100001")
	b.Filter.Priorities = []string{"Non-emergency"}
	e.createIntegration(e.user, a)
	e.createIntegration(e.user, b)

	e.mustPost("a", "0100001", row1Text, e.now)
	e.mustPost("a", "0100001", row3Text, e.now)
	if !eventually(5*time.Second, func() bool { return len(emergency.Requests()) >= 1 && len(other.Requests()) >= 1 }) {
		t.Fatalf("requests: emergency %d, non-emergency %d", len(emergency.Requests()), len(other.Requests()))
	}
	time.Sleep(time.Second)
	for name, c := range map[string]struct {
		r    *standin.Server
		text string
	}{"Emergency": {emergency, row1Text[2:]}, "Non-emergency": {other, row3Text[2:]}} {
		reqs := c.r.Requests()
		if len(reqs) != 1 || reqs[0].JSON["text"] != c.text {
			t.Errorf("%s receiver: %d requests (%v), want 1 with %q", name, len(reqs), reqs, c.text)
		}
	}
}

// A36. Too old.
func TestA36TooOld(t *testing.T) {
	e := setup(t)
	e.allowLoopback()
	hook := standin.Webhook()
	defer hook.Close()
	v := e.createIntegration(e.user, webhookTo("Gate", hook, "0100001"))

	// The endpoint is down for 6 minutes while a matching page arrives.
	hook.Down(true)
	id := e.mustPost("a", "0100001", row1Text, e.now).MessageID
	for elapsed := time.Duration(0); elapsed < 6*time.Minute; elapsed += 15 * time.Second {
		e.integ.Drain(e.ctx)
		e.advance(15 * time.Second)
	}
	hook.Down(false)
	e.integ.Drain(e.ctx)
	if n := len(hook.Requests()); n != 0 {
		t.Fatalf("the page was delivered after the cut-off (%d requests)", n)
	}
	d := e.deliveries(e.user, v.ID)
	if len(d) != 1 || d[0].MessageID != id || d[0].Status != "too_old" {
		t.Fatalf("deliveries %+v, want message %d too_old", d, id)
	}
	t.Logf("delivery: status %s after %d attempts, last result %v", d[0].Status, d[0].Attempts, deref(d[0].LastResult))
	if got := e.integration(e.user, v.ID); got.Status != "active" {
		t.Errorf("one skipped page paused the integration")
	}

	// A page a client received 10 minutes earlier: no request at all.
	late := e.mustPost("b", "0100001", "@@EX0000007 HSF1 SMOKE FROM SHED @ 3 FICTION ST, MADEUPTON #STN1", e.clock().Add(-10*time.Minute)).MessageID
	e.integ.Drain(e.ctx)
	if n := len(hook.Requests()); n != 0 {
		t.Errorf("a page received 10 minutes earlier was sent (%d requests)", n)
	}
	for _, x := range e.deliveries(e.user, v.ID) {
		if x.MessageID == late && (x.Status != "too_old" || x.Attempts != 0) {
			t.Errorf("late page: %+v, want too_old with no attempt", x)
		}
	}
}

// A37. Content: a Discord preset on its default (Summary), a webhook on Full.
func TestA37Content(t *testing.T) {
	e := setup(t)
	e.allowLoopback()
	e.runWorker()
	discord, hook := standin.Discord(), standin.Webhook()
	defer discord.Close()
	defer hook.Close()
	e.integ.Presets.DiscordPrefixes = []string{discord.URL + "/api/webhooks/"}
	d := e.createIntegration(e.user, integrations.Request{Name: "Discord", Kind: integrations.Discord, Capcodes: []string{"0100001"},
		Secrets: integrations.Secrets{URL: discord.URL + "/api/webhooks/1234567890/AbCdEf_-123"}})
	if d.Content != integrations.Summary {
		t.Errorf("Discord preset's default content %q, want summary", d.Content)
	}
	full := webhookTo("Full", hook, "0100001")
	full.Content = integrations.Full
	e.createIntegration(e.user, full)

	e.mustPost("a", "0100001", row1Text, e.now) // 12 FICTION ST, MADEUPTON
	if !eventually(5*time.Second, func() bool { return len(discord.Requests()) >= 1 && len(hook.Requests()) >= 1 }) {
		t.Fatalf("discord %d, webhook %d requests; discord format violations %v", len(discord.Requests()), len(hook.Requests()), discord.Violations())
	}
	if v := discord.Violations(); len(v) > 0 {
		t.Errorf("not Discord's webhook format: %v", v)
	}
	dbody := string(discord.Requests()[0].Body)
	t.Logf("Discord request: %s", dbody)
	if strings.Contains(dbody, "SMOKE FROM ROOF") || strings.Contains(dbody, "12 FICTION ST") || strings.Contains(dbody, "MADEUPTON") {
		t.Errorf("the Discord request contains the page text or the address")
	}
	if !strings.Contains(dbody, "Station 1") || !strings.Contains(dbody, "EX0000001") {
		t.Errorf("the Discord summary lacks the alias or the job number")
	}
	if got := hook.Requests()[0].JSON["text"]; got != row1Text[2:] {
		t.Errorf("Full webhook text %q", got)
	}
}

// startBroker starts an in-process MQTT broker on 127.0.0.1.
func startBroker(t *testing.T) (*mqttserver.Server, string) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	ln.Close()
	b := mqttserver.New(&mqttserver.Options{InlineClient: true, Logger: slog.New(slog.NewTextHandler(io.Discard, nil))})
	if err := b.AddHook(new(auth.AllowHook), nil); err != nil {
		t.Fatal(err)
	}
	if err := b.AddListener(listeners.NewTCP(listeners.Config{ID: "test", Address: addr})); err != nil {
		t.Fatal(err)
	}
	go func() { _ = b.Serve() }()
	t.Cleanup(func() { _ = b.Close() })
	for i := 0; i < 50; i++ {
		if c, err := net.Dial("tcp", addr); err == nil {
			c.Close()
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	return b, addr
}

// A38. Home Assistant discovery.
func TestA38HomeAssistantDiscovery(t *testing.T) {
	e := setup(t)
	e.allowLoopback()
	e.runWorker()
	broker, addr := startBroker(t)
	events := make(chan packets.Packet, 10)
	if err := broker.Subscribe("pagerarchive/#", 1, func(_ *mqttserver.Client, _ packets.Subscription, pk packets.Packet) { events <- pk }); err != nil {
		t.Fatal(err)
	}
	v := e.createIntegration(e.user, integrations.Request{Name: "Station 1 pager", Kind: integrations.MQTT, Capcodes: []string{"0100001"},
		Config: integrations.Config{Broker: "mqtt://" + addr, HADiscovery: true}})
	if v.Discovery == nil || !v.Discovery.OK {
		t.Fatalf("discovery on save: %+v", v.Discovery)
	}
	topic := "homeassistant/event/pagerarchive/" + v.UID + "/config"
	retained := broker.Topics.Messages(topic)
	if len(retained) != 1 {
		t.Fatalf("broker holds %d retained configs at %s", len(retained), topic)
	}
	t.Logf("retained at %s: %s", topic, retained[0].Payload)
	var cfg struct {
		Name       string   `json:"name"`
		UniqueID   string   `json:"unique_id"`
		StateTopic string   `json:"state_topic"`
		EventTypes []string `json:"event_types"`
		Device     struct {
			Identifiers []string `json:"identifiers"`
			Name        string   `json:"name"`
		} `json:"device"`
		Origin struct {
			Name string `json:"name"`
		} `json:"origin"`
	}
	if err := json.Unmarshal(retained[0].Payload, &cfg); err != nil {
		t.Fatal(err)
	}
	if cfg.Device.Name != "PagerArchive" || len(cfg.Device.Identifiers) != 1 || cfg.UniqueID != "pagerarchive_"+v.UID || cfg.Name != "Station 1 pager" ||
		cfg.StateTopic != "pagerarchive/"+v.UID || strings.Join(cfg.EventTypes, ",") != "page,test" || cfg.Origin.Name != "PagerArchive" {
		t.Errorf("discovery config %+v", cfg)
	}

	id := e.mustPost("a", "0100001", row1Text, e.now).MessageID
	select {
	case pk := <-events:
		var ev map[string]any
		_ = json.Unmarshal(pk.Payload, &ev)
		if pk.TopicName != cfg.StateTopic || ev["event_type"] != "page" || ev["id"] != float64(id) {
			t.Errorf("event on %s: %s", pk.TopicName, pk.Payload)
		}
		t.Logf("event on %s: event_type %v, id %v", pk.TopicName, ev["event_type"], ev["id"])
	case <-time.After(5 * time.Second):
		t.Fatal("no event within 5 s")
	}

	// Turning discovery off removes the entity.
	off := integrations.Request{Name: "Station 1 pager", Capcodes: []string{"0100001"}, Config: integrations.Config{Broker: "mqtt://" + addr}}
	var after integrations.View
	if code, p := e.user.call(http.MethodPut, fmt.Sprintf("/api/v1/integrations/%d", v.ID), off, &after); code != http.StatusOK {
		t.Fatalf("turn discovery off: %d %s", code, p.Error)
	}
	if after.Discovery == nil || !after.Discovery.OK {
		t.Errorf("removal: %+v", after.Discovery)
	}
	if n := len(broker.Topics.Messages(topic)); n != 0 {
		t.Errorf("config still retained after turning discovery off")
	}
}

// A39. One page, one delivery; Send test.
func TestA39OnePageOneDelivery(t *testing.T) {
	e := setup(t)
	e.allowLoopback()
	e.runWorker()
	hook := standin.Webhook()
	defer hook.Close()
	v := e.createIntegration(e.user, webhookTo("Gate", hook, "0100001"))

	// Two clients receive the same page.
	first := e.mustPost("a", "0100001", row1Text, e.now)
	second := e.mustPost("b", "0100001", row1Text, e.now.Add(2*time.Second))
	if second.Outcome != "duplicate" || second.MessageID != first.MessageID {
		t.Fatalf("second copy: %+v", second)
	}
	if !eventually(5*time.Second, func() bool { return len(hook.Requests()) >= 1 }) {
		t.Fatal("no request")
	}
	time.Sleep(time.Second)
	reqs := hook.Requests()
	if len(reqs) != 1 || reqs[0].JSON["id"] != float64(first.MessageID) {
		t.Fatalf("%d requests (%v), want 1 carrying id %d", len(reqs), reqs, first.MessageID)
	}
	// The database refuses a second delivery of one page to one integration.
	_, err := e.st.Pool.Exec(e.ctx, "INSERT INTO deliveries (integration_id, message_id, status) VALUES ($1, $2, 'delivered')", v.ID, first.MessageID)
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) || pgErr.Code != "23505" {
		t.Errorf("second delivery row: %v, want a unique violation", err)
	}

	// Send test.
	var res integrations.TestResult
	if code, p := e.user.call(http.MethodPost, fmt.Sprintf("/api/v1/integrations/%d/test", v.ID), nil, &res); code != http.StatusOK {
		t.Fatalf("send test: %d %s", code, p.Error)
	}
	if !res.OK || res.Result != "HTTP 200 OK" || res.Body != "received" {
		t.Errorf("the owner sees %+v, want HTTP 200 OK and the body", res)
	}
	t.Logf("Send test answer: %+v", res)
	reqs = hook.Requests()
	if len(reqs) != 2 || reqs[1].JSON["test"] != true || reqs[1].JSON["event_type"] != "test" {
		t.Errorf("test request %v", reqs[len(reqs)-1].JSON)
	}
	// Tests are limited to one every few seconds.
	if code, _ := e.user.call(http.MethodPost, fmt.Sprintf("/api/v1/integrations/%d/test", v.ID), nil, nil); code != http.StatusTooManyRequests {
		t.Errorf("second test straight away: %d, want 429", code)
	}
}

// R47: integrations are their owner's. Admins see that they exist and can
// pause them, never their secrets or destinations; only an admin can lift
// an admin's pause (owner, Q3).
func TestIntegrationsBelongToTheirOwner(t *testing.T) {
	e := setup(t)
	e.allowLoopback()
	hook := standin.Webhook()
	defer hook.Close()
	req := webhookTo("Private", hook, "0100001")
	req.Secrets.URL = hook.URL + "/hook/T0PS3CRET"
	req.Config.AuthHeader, req.Secrets.AuthValue = "X-Api-Key", "hunter2"
	v := e.createIntegration(e.user, req)

	code, body := e.user.raw(http.MethodGet, fmt.Sprintf("/api/v1/integrations/%d", v.ID), nil)
	if code != http.StatusOK || strings.Contains(body, "T0PS3CRET") || strings.Contains(body, "hunter2") {
		t.Errorf("owner's view %d shows a secret: %s", code, body)
	}
	if v.Config.Target != hook.URL || !v.SecretsSet["url"] || !v.SecretsSet["auth_value"] {
		t.Errorf("owner sees target %q, secrets set %v", v.Config.Target, v.SecretsSet)
	}
	var stored string
	if err := e.st.Pool.QueryRow(e.ctx, "SELECT encode(secrets, 'escape') || config::text FROM integrations WHERE id = $1", v.ID).Scan(&stored); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(stored, "T0PS3CRET") || strings.Contains(stored, "hunter2") {
		t.Error("the database row holds a secret in plain text")
	}

	// Another user, admins included, can't open it.
	if code, _ := e.admin.raw(http.MethodGet, fmt.Sprintf("/api/v1/integrations/%d", v.ID), nil); code != http.StatusNotFound {
		t.Errorf("admin opening someone's integration: %d, want 404", code)
	}
	if code, _ := e.admin.raw(http.MethodPost, fmt.Sprintf("/api/v1/integrations/%d/test", v.ID), nil); code != http.StatusNotFound {
		t.Errorf("admin testing someone's integration: %d, want 404", code)
	}
	var list server.IntegrationsPage
	e.admin.call(http.MethodGet, "/api/v1/integrations", nil, &list)
	if !list.Enabled || len(list.Integrations) != 0 {
		t.Errorf("admin's own list: %+v", list)
	}
	// Admins see it on its owner's page in Administration (R44b, A41).
	var ownerID int64
	if err := e.st.Pool.QueryRow(e.ctx, "SELECT owner_id FROM integrations WHERE id = $1", v.ID).Scan(&ownerID); err != nil {
		t.Fatal(err)
	}
	code, body = e.admin.raw(http.MethodGet, fmt.Sprintf("/api/v1/admin/users/%d", ownerID), nil)
	if code != http.StatusOK || !strings.Contains(body, `"name":"Private"`) || strings.Contains(body, "T0PS3CRET") || strings.Contains(body, "127.0.0.1") {
		t.Errorf("owner's admin page %d: %s", code, body)
	}
	if code, _ := e.user.raw(http.MethodGet, fmt.Sprintf("/api/v1/admin/users/%d", ownerID), nil); code != http.StatusForbidden {
		t.Errorf("user reading an admin page: %d", code)
	}

	// Admin pause: the owner can't resume it; an admin can.
	if code, _ := e.admin.raw(http.MethodPost, fmt.Sprintf("/api/v1/admin/integrations/%d/pause", v.ID), nil); code != http.StatusNoContent {
		t.Fatalf("admin pause: %d", code)
	}
	if got := e.integration(e.user, v.ID); got.Status != "paused" || got.PausedBy == nil || *got.PausedBy != "admin" {
		t.Errorf("after admin pause: %s %v", got.Status, got.PausedBy)
	}
	code, p := e.user.call(http.MethodPost, fmt.Sprintf("/api/v1/integrations/%d/resume", v.ID), nil, nil)
	if code != http.StatusForbidden || !strings.Contains(p.Error, "Only an admin can resume it") {
		t.Errorf("owner resuming an admin's pause: %d %q", code, p.Error)
	}
	if code, _ := e.admin.raw(http.MethodPost, fmt.Sprintf("/api/v1/admin/integrations/%d/resume", v.ID), nil); code != http.StatusNoContent {
		t.Errorf("admin resume: %d", code)
	}

	// Editing with secrets left empty keeps them; Send test proves it.
	edit := webhookTo("Private, renamed", hook, "0100001")
	edit.Secrets = integrations.Secrets{}
	edit.Config.AuthHeader = "X-Api-Key"
	if code, p := e.user.call(http.MethodPut, fmt.Sprintf("/api/v1/integrations/%d", v.ID), edit, nil); code != http.StatusOK {
		t.Fatalf("edit: %d %s", code, p.Error)
	}
	var res integrations.TestResult
	e.user.call(http.MethodPost, fmt.Sprintf("/api/v1/integrations/%d/test", v.ID), nil, &res)
	reqs := hook.Requests()
	if !res.OK || len(reqs) != 1 || reqs[0].Path != "/hook/T0PS3CRET" || reqs[0].Header.Get("X-Api-Key") != "hunter2" {
		t.Errorf("after an edit without secrets: %+v, %d requests", res, len(reqs))
	}
}

func TestIntegrationRules(t *testing.T) {
	e := setup(t)
	e.allowLoopback()
	hook := standin.Webhook()
	defer hook.Close()
	// R48: a selection is required.
	req := webhookTo("Everything", hook)
	if code, p := e.user.call(http.MethodPost, "/api/v1/integrations", req, nil); code != http.StatusUnprocessableEntity || !strings.Contains(p.Error, "at least one capcode or alias") {
		t.Errorf("no selection: %d %q", code, p.Error)
	}
	// A Discord preset only posts to Discord.
	d := integrations.Request{Name: "D", Kind: integrations.Discord, Capcodes: []string{"0100001"}, Secrets: integrations.Secrets{URL: hook.URL + "/api/webhooks/1/x"}}
	if code, _ := e.user.call(http.MethodPost, "/api/v1/integrations", d, nil); code != http.StatusUnprocessableEntity {
		t.Errorf("Discord preset aimed elsewhere: %d", code)
	}
	// R49: no backfill. A page stored before the integration isn't sent.
	e.mustPost("a", "0100009", row1Text, e.now)
	v := e.createIntegration(e.user, webhookTo("Later", hook, "100009")) // capcodes are zero-padded
	if len(v.Capcodes) != 1 || v.Capcodes[0] != "0100009" {
		t.Errorf("capcodes %v", v.Capcodes)
	}
	e.integ.Drain(e.ctx)
	if n := len(hook.Requests()) + len(e.deliveries(e.user, v.ID)); n != 0 {
		t.Errorf("an earlier page was sent (%d)", n)
	}
	// Without a key, integrations are off.
	box := e.integ.Box
	e.integ.Box = nil
	if code, p := e.user.call(http.MethodPost, "/api/v1/integrations", webhookTo("No key", hook, "0100001"), nil); code != http.StatusServiceUnavailable || !strings.Contains(p.Error, "INTEGRATION_SECRETS_KEY") {
		t.Errorf("no key: %d %q", code, p.Error)
	}
	e.integ.Box = box
}

// R51: deactivating a user stops their integrations; deleting the account
// deletes them (R43c).
func TestDeactivationStopsIntegrations(t *testing.T) {
	e := setup(t)
	e.allowLoopback()
	hook := standin.Webhook()
	defer hook.Close()
	hook.Down(true)
	v := e.createIntegration(e.user, webhookTo("Gate", hook, "0100001"))
	e.mustPost("a", "0100001", row1Text, e.now)
	e.integ.Drain(e.ctx) // fails once; retry pending
	if code, p := e.admin.call(http.MethodPost, fmt.Sprintf("/api/v1/admin/users/%d/deactivate", e.user.id), server.DeactivateRequest{}, nil); code != http.StatusOK {
		t.Fatalf("deactivate: %d %s", code, p.Error)
	}
	var status string
	if err := e.st.Pool.QueryRow(e.ctx, "SELECT status FROM deliveries WHERE integration_id = $1", v.ID).Scan(&status); err != nil || status != "cancelled" {
		t.Errorf("pending delivery after deactivation: %q %v, want cancelled", status, err)
	}
	hook.Down(false)
	e.mustPost("a", "0100001", grassText, e.now)
	e.advance(time.Minute)
	e.integ.Drain(e.ctx)
	var rows int
	_ = e.st.Pool.QueryRow(e.ctx, "SELECT count(*) FROM deliveries WHERE integration_id = $1", v.ID).Scan(&rows)
	if rows != 1 || len(hook.Requests()) != 0 {
		t.Errorf("deactivated owner's integration: %d deliveries, %d requests", rows, len(hook.Requests()))
	}

	// Deleting an account deletes its integrations.
	other := e.inviteAndEnrol(e.admin, "c@example.com")
	e.createIntegration(other, webhookTo("C's", hook, "0100002"))
	if code, p := other.call(http.MethodDelete, "/api/v1/me", nil, nil); code != http.StatusNoContent {
		t.Fatalf("delete account: %d %s", code, p.Error)
	}
	var left int
	_ = e.st.Pool.QueryRow(e.ctx, "SELECT count(*) FROM integrations WHERE owner_id = $1", other.id).Scan(&left)
	if left != 0 {
		t.Errorf("%d integrations left after deleting the account", left)
	}
}

// Presets stop at high priority: repeat-until-acknowledged alarms are
// operational alerting, which is out of scope (owner, 2026-10-16; ADR 0006 §8).
func TestPresetPrioritiesStopAtHigh(t *testing.T) {
	e := setup(t)
	e.allowLoopback()
	pushover, ntfy := standin.Pushover(), standin.Ntfy()
	defer pushover.Close()
	defer ntfy.Close()
	e.integ.Presets.PushoverURL = pushover.URL + "/1/messages.json"
	prio := func(n int) *int { return &n }
	po := func(p *int) integrations.Request {
		return integrations.Request{Name: "Pushover", Kind: integrations.Pushover, Capcodes: []string{"0100001"}, Config: integrations.Config{Priority: p},
			Secrets: integrations.Secrets{AppToken: "azGDORePK8gMaC0QOYAMyEEuzJnyUi", UserKey: "uQiRzpo4DXghDmr9QzzfQu27cmVRsG"}}
	}
	nt := func(p *int) integrations.Request {
		return integrations.Request{Name: "ntfy", Kind: integrations.Ntfy, Capcodes: []string{"0100001"}, Config: integrations.Config{Server: ntfy.URL, Priority: p},
			Secrets: integrations.Secrets{Topic: "pager-test"}}
	}
	for name, c := range map[string]struct {
		req  integrations.Request
		want string
	}{
		"Pushover 2": {po(prio(2)), "Pushover's priority goes from -2 (lowest) to 1 (high)."},
		"ntfy 5":     {nt(prio(5)), "ntfy's priority goes from 1 (min) to 4 (high)."},
	} {
		code, p := e.user.call(http.MethodPost, "/api/v1/integrations", c.req, nil)
		if code != http.StatusUnprocessableEntity || p.Error != c.want {
			t.Errorf("%s: %d %q, want 422 %q", name, code, p.Error, c.want)
		}
	}
	// High is the most there is, and it's accepted.
	v := e.createIntegration(e.user, po(prio(1)))
	e.createIntegration(e.user, nt(prio(4)))
	// An existing integration can't be changed to 2 either.
	if code, _ := e.user.call(http.MethodPut, fmt.Sprintf("/api/v1/integrations/%d", v.ID), po(prio(2)), nil); code != http.StatusUnprocessableEntity {
		t.Errorf("changing to priority 2: %d, want 422", code)
	}
}

// A40. One selection field (R48): typing a capcode offers its own alias
// when it has one, and otherwise the capcode itself, with the range alias
// covering it named beside it, never the range.
func TestA40OneSelectionField(t *testing.T) {
	e := setup(t)
	e.allowLoopback()
	hook := standin.Webhook()
	defer hook.Close()
	block := e.addAlias("0100%", "Stations block")
	station1 := e.aliasID("Station 1")

	suggest := func(q string) []integrations.Suggestion {
		t.Helper()
		var out []integrations.Suggestion
		if code, p := e.user.call(http.MethodGet, "/api/v1/integrations/selection?q="+q, nil, &out); code != http.StatusOK {
			t.Fatalf("selection %q: %d %s", q, code, p.Error)
		}
		return out
	}
	got := suggest("0100001")
	t.Logf("typing 0100001 offers %+v", got)
	if len(got) != 1 || got[0].Kind != "alias" || got[0].AliasID != station1 || got[0].Name != "Station 1" || got[0].Pattern != "0100001" {
		t.Errorf("typing 0100001: %+v, want only the alias Station 1 (0100001)", got)
	}
	got = suggest("0100009")
	t.Logf("typing 0100009 offers %+v", got)
	if len(got) != 1 || got[0].Kind != "capcode" || got[0].Capcode != "0100009" || got[0].CoveredBy != "Stations block" {
		t.Errorf("typing 0100009: %+v, want only the capcode 0100009, covered by Stations block", got)
	}
	// Typing a name offers aliases, ranges included: that's choosing one.
	got = suggest("station")
	names := []string{}
	for _, s := range got {
		names = append(names, s.Name)
	}
	if strings.Join(names, ",") != "Station 1,Station 2,Station 3,Station 4,Stations block" || !got[4].Range || got[0].Range {
		t.Errorf("typing station: %+v", got)
	}

	// Saving what was chosen: the alias, and the capcode with its range
	// alias named beside it. A capcode with its own alias sent through the
	// API is saved as that alias too.
	v := e.createIntegration(e.user, integrations.Request{Name: "Picked", Kind: integrations.Webhook, AliasIDs: []int64{station1},
		Capcodes: []string{"0100009"}, Secrets: integrations.Secrets{URL: hook.URL + "/hook"}})
	t.Logf("saved: aliases %+v, capcodes %v, covered by %v", v.Aliases, v.Capcodes, v.CoveredBy)
	if len(v.Aliases) != 1 || v.Aliases[0].ID != station1 || len(v.Capcodes) != 1 || v.Capcodes[0] != "0100009" ||
		v.CoveredBy["0100009"] != "Stations block" {
		t.Errorf("saved %+v %v %v", v.Aliases, v.Capcodes, v.CoveredBy)
	}
	w := e.createIntegration(e.user, webhookTo("By capcode", hook, "100001", "0100009"))
	if len(w.Aliases) != 1 || w.Aliases[0].ID != station1 || len(w.Capcodes) != 1 || w.Capcodes[0] != "0100009" {
		t.Errorf("capcode 100001 through the API: aliases %+v, capcodes %v", w.Aliases, w.Capcodes)
	}
	for _, a := range w.Aliases {
		if a.ID == block {
			t.Error("a capcode selected the range")
		}
	}

	// Only 0100009 is selected, not the range: a page to 0100002, which the
	// range covers but which wasn't chosen, isn't sent.
	e.mustPost("a", "0100002", "@@EX0000003 HSF1 SMOKE FROM SHED @ 12 FICTION ST, MADEUPTON #STN2", e.now)
	e.mustPost("a", "0100009", "@@EX0000004 HSF1 SMOKE FROM BARN @ 12 FICTION ST, MADEUPTON #STN9", e.now)
	e.integ.Drain(e.ctx)
	reqs := hook.Requests()
	t.Logf("requests: %d", len(reqs))
	if len(reqs) != 2 { // one per integration, both for 0100009
		t.Fatalf("%d requests, want 2", len(reqs))
	}
	for _, r := range reqs {
		if !strings.Contains(string(r.Body), `"capcode":"0100009"`) {
			t.Errorf("a request for another capcode: %s", r.Body)
		}
	}
}

// addAlias adds an alias with the fixture's template.
func (e *env) addAlias(pattern, name string) int64 {
	e.t.Helper()
	var id int64
	if err := e.st.Pool.QueryRow(e.ctx, `INSERT INTO aliases (pattern, name, template_id)
		SELECT $1, $2, id FROM templates WHERE name = 'Example Fire' RETURNING id`, pattern, name).Scan(&id); err != nil {
		e.t.Fatal(err)
	}
	return id
}

// A41. Administration (R44b): one Admin area, refused to users; each user's
// page lists their integrations without destinations, and nothing lists
// everyone's. (The header's Admin link is checked in the browser.)
func TestA41AdministrationArea(t *testing.T) {
	e := setup(t)
	e.allowLoopback()
	hook := standin.Webhook()
	defer hook.Close()
	b := e.user
	req := webhookTo("B's pager", hook, "0100009")
	req.Secrets.URL = hook.URL + "/hook/T0PS3CRET"
	bi := e.createIntegration(b, req)
	ai := e.createIntegration(e.admin, webhookTo("A's pager", hook, "0100008"))

	// B: every admin endpoint is refused.
	var bID int64
	if err := e.st.Pool.QueryRow(e.ctx, "SELECT id FROM users WHERE email = 'user@example.com'").Scan(&bID); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"/api/v1/admin/users", fmt.Sprintf("/api/v1/admin/users/%d", bID), "/api/v1/admin/clients",
		"/api/v1/admin/invitations", "/api/v1/admin/retention", "/api/v1/admin/integration-hosts"} {
		code, _ := b.raw(http.MethodGet, path, nil)
		t.Logf("user B GET %s: %d", path, code)
		if code != http.StatusForbidden {
			t.Errorf("user B GET %s: %d, want 403", path, code)
		}
	}
	if code, _ := b.raw(http.MethodPost, fmt.Sprintf("/api/v1/admin/integrations/%d/pause", ai.ID), nil); code != http.StatusForbidden {
		t.Errorf("user B pausing as admin: %d", code)
	}

	// A: B's page shows B's integration, never where it sends.
	code, body := e.admin.raw(http.MethodGet, fmt.Sprintf("/api/v1/admin/users/%d", bID), nil)
	t.Logf("admin GET B's page: %d %s", code, body)
	var page server.AdminUser
	if err := json.Unmarshal([]byte(body), &page); code != http.StatusOK || err != nil {
		t.Fatalf("B's page: %d %v", code, err)
	}
	if page.User.Email != "user@example.com" || len(page.Integrations) != 1 || page.Integrations[0].ID != bi.ID ||
		page.Integrations[0].Name != "B's pager" || page.Integrations[0].Kind != integrations.Webhook || page.Integrations[0].Status != "active" {
		t.Errorf("B's page: %+v", page)
	}
	for _, leak := range []string{"T0PS3CRET", "127.0.0.1", hook.URL, `"config"`, `"secrets`, `"capcodes"`, "A's pager"} {
		if strings.Contains(body, leak) {
			t.Errorf("B's page shows %q", leak)
		}
	}
	// No page lists every user's integrations.
	if code, _ := e.admin.raw(http.MethodGet, "/api/v1/admin/integrations", nil); code != http.StatusNotFound {
		t.Errorf("GET /api/v1/admin/integrations: %d, want 404", code)
	}

	// The users list counts integrations and marks those paused for failing.
	e.st.Pool.Exec(e.ctx, "UPDATE integrations SET status = 'paused', paused_by = 'failing', paused_at = now() WHERE id = $1", bi.ID)
	var users []server.UserRow
	e.admin.call(http.MethodGet, "/api/v1/admin/users", nil, &users)
	for _, u := range users {
		t.Logf("users list: %s integrations=%d paused for failing=%d", u.Email, u.Integrations, u.IntegrationsFailing)
		want := map[string][2]int64{"user@example.com": {1, 1}, "admin@example.com": {1, 0}}[u.Email]
		if u.Integrations != want[0] || u.IntegrationsFailing != want[1] {
			t.Errorf("%s: %d integrations, %d failing, want %v", u.Email, u.Integrations, u.IntegrationsFailing, want)
		}
	}
	// Resume from B's page works as before; an unknown user is 404.
	if code, _ := e.admin.raw(http.MethodPost, fmt.Sprintf("/api/v1/admin/integrations/%d/resume", bi.ID), nil); code != http.StatusNoContent {
		t.Errorf("admin resume: %d", code)
	}
	if code, _ := e.admin.raw(http.MethodGet, "/api/v1/admin/users/999999", nil); code != http.StatusNotFound {
		t.Errorf("unknown user: %d", code)
	}
}
