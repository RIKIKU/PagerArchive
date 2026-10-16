package integrations

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/RIKIKU/PagerArchive/internal/integrations/standin"
	"github.com/RIKIKU/PagerArchive/internal/profile"
	"github.com/RIKIKU/PagerArchive/internal/store/db"
)

func key(b byte) string { return base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{b}, 32)) }

func TestBoxSealsPerIntegration(t *testing.T) {
	box, err := NewBox(key(1), "")
	if err != nil {
		t.Fatal(err)
	}
	secret := []byte(`{"url":"https://example.org/hook/s3cret"}`)
	blob, err := box.Seal("aaaaaaaaaaaa", secret)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(blob, []byte("s3cret")) {
		t.Fatal("blob holds the secret in plain text")
	}
	if got, err := box.Open("aaaaaaaaaaaa", blob); err != nil || !bytes.Equal(got, secret) {
		t.Fatalf("open: %q, %v", got, err)
	}
	if _, err := box.Open("bbbbbbbbbbbb", blob); !errors.Is(err, ErrCorrupt) {
		t.Errorf("blob moved to another integration: %v, want ErrCorrupt", err)
	}
	other, _ := NewBox(key(2), "")
	if _, err := other.Open("aaaaaaaaaaaa", blob); !errors.Is(err, ErrWrongKey) {
		t.Errorf("another key: %v, want ErrWrongKey", err)
	}
	// Rotation: the new key with the old one alongside opens the old blob,
	// knows it's stale, and seals it again.
	rotating, err := NewBox(key(2), key(1))
	if err != nil {
		t.Fatal(err)
	}
	if !rotating.Stale(blob) {
		t.Error("old blob not reported stale")
	}
	plain, err := rotating.Open("aaaaaaaaaaaa", blob)
	if err != nil {
		t.Fatal(err)
	}
	again, _ := rotating.Seal("aaaaaaaaaaaa", plain)
	if rotating.Stale(again) {
		t.Error("resealed blob still stale")
	}
	if _, err := other.Open("aaaaaaaaaaaa", again); err != nil {
		t.Errorf("new key alone can't open the resealed blob: %v", err)
	}
	if _, err := NewBox("too-short", ""); err == nil {
		t.Error("short key accepted")
	}
	var none *Box
	if _, err := none.Seal("aaaaaaaaaaaa", secret); !errors.Is(err, ErrNoKey) {
		t.Errorf("no key: %v", err)
	}
}

func TestBlockedAddresses(t *testing.T) {
	for addr, want := range map[string]string{
		"127.0.0.1":        "a loopback address",
		"::1":              "a loopback address",
		"10.1.2.3":         "a private address",
		"172.16.0.1":       "a private address",
		"192.168.1.10":     "a private address",
		"fd00::1":          "a private address",
		"169.254.169.254":  "a link-local address",
		"fe80::1":          "a link-local address",
		"0.0.0.0":          "an unspecified address",
		"::":               "an unspecified address",
		"224.0.0.1":        "a link-local address", // link-local multicast
		"239.1.1.1":        "a multicast address",
		"255.255.255.255":  "a broadcast address",
		"100.100.1.1":      "a shared (carrier-grade NAT) address",
		"::ffff:10.0.0.1":  "a private address",
		"::ffff:127.0.0.1": "a loopback address",
		"93.184.215.14":    "",
		"2606:4700::1111":  "",
	} {
		if got := blockedReason(netip.MustParseAddr(addr)); got != want {
			t.Errorf("%s: %q, want %q", addr, got, want)
		}
	}
}

// fakeResolver answers each lookup with the next answer, so a test can
// change what a name resolves to between saving and connecting.
type fakeResolver struct{ answers [][]netip.Addr }

func (f *fakeResolver) LookupNetIP(_ context.Context, _, host string) ([]netip.Addr, error) {
	if len(f.answers) == 0 {
		return nil, &net.DNSError{Err: "no such host", Name: host}
	}
	a := f.answers[0]
	if len(f.answers) > 1 {
		f.answers = f.answers[1:]
	}
	return a, nil
}

func TestGuard(t *testing.T) {
	ctx := context.Background()
	public := netip.MustParseAddr("93.184.215.14")
	private := netip.MustParseAddr("192.168.1.10")
	allow, err := ParseAllowList([]string{"10.20.0.0/16", "HA.lan.", "192.168.5.5"})
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(allow.Entries(), " "); got != "10.20.0.0/16 192.168.5.5 ha.lan" {
		t.Errorf("entries %q", got)
	}
	if _, err := ParseAllowList([]string{"not a host!"}); err == nil {
		t.Error("bad entry accepted")
	}
	g := &Guard{Allow: func(context.Context) (AllowList, error) { return allow, nil }}

	var ae *AddressError
	if _, err := g.Resolve(ctx, "192.168.1.10"); !errors.As(err, &ae) || ae.Error() != "192.168.1.10 is a private address. Ask an admin to allow it." {
		t.Errorf("private: %v", err)
	}
	for _, ok := range []string{"10.20.3.4", "192.168.5.5", "93.184.215.14"} {
		if _, err := g.Resolve(ctx, ok); err != nil {
			t.Errorf("%s: %v", ok, err)
		}
	}
	// An allowed host name may resolve to a private address.
	g.Resolver = &fakeResolver{answers: [][]netip.Addr{{private}}}
	if _, err := g.Resolve(ctx, "ha.lan"); err != nil {
		t.Errorf("allowed host: %v", err)
	}
	// One private address among public ones is enough to refuse.
	g.Resolver = &fakeResolver{answers: [][]netip.Addr{{public, private}}}
	if _, err := g.Resolve(ctx, "mixed.example.org"); !errors.As(err, &ae) {
		t.Errorf("mixed: %v", err)
	}
	// DNS rebinding: public when saved, loopback when connecting. The dial
	// checks again and refuses.
	g.Resolver = &fakeResolver{answers: [][]netip.Addr{{public}, {netip.MustParseAddr("127.0.0.1")}}}
	if _, err := g.Resolve(ctx, "rebind.example.org"); err != nil {
		t.Fatalf("save-time check: %v", err)
	}
	if _, err := g.DialContext(ctx, "tcp", "rebind.example.org:80"); !errors.As(err, &ae) || ae.Reason != "a loopback address" {
		t.Errorf("dial after rebinding: %v, want a loopback refusal", err)
	}
	// The test restriction refuses even an allowed address.
	g.Resolver, g.Restrict = nil, OnlyLoopback
	if _, err := g.Resolve(ctx, "10.20.3.4"); err == nil || !strings.Contains(err.Error(), "test guard") {
		t.Errorf("restrict: %v", err)
	}
	if _, err := g.Resolve(ctx, "unresolvable.invalid"); !errors.Is(err, ErrLookup) {
		t.Errorf("lookup failure: %v", err)
	}
}

func network(t *testing.T) (*profile.Network, map[string]*profile.Agency) {
	t.Helper()
	set, err := profile.LoadDir("../../profiles/example")
	if err != nil {
		t.Fatal(err)
	}
	return set.Networks["example-network"], set.Agencies
}

func sp(s string) *string { return &s }

func TestFilterMatches(t *testing.T) {
	n, _ := network(t)
	fire := db.SearchMessagesRow{Text: "@@EX0000001 HSF1 SMOKE FROM ROOF", Priority: sp("Emergency"), EventType: sp("HSF1"),
		PagingGroup: sp("STN1"), Agency: sp("Example Fire Service"), JobNumber: sp("EX0000001"), Extra: []byte(`{"premises":"OLD MILL"}`)}
	admin := db.SearchMessagesRow{Text: "QDTraining Tuesday", Priority: sp("Admin"), Extra: []byte(`{}`), ParseFailed: true}
	cases := []struct {
		name  string
		f     Filter
		m     db.SearchMessagesRow
		admin bool
		want  bool
	}{
		{"no filter", Filter{}, fire, false, true},
		{"category", Filter{Categories: []string{"Structure fire"}}, fire, false, true},
		{"other category", Filter{Categories: []string{"Grass fire"}}, fire, false, false},
		{"raw code", Filter{Codes: []string{"HSF1"}}, fire, false, true},
		{"code or category", Filter{Codes: []string{"GRS3"}, Categories: []string{"Structure fire"}}, fire, false, true},
		{"no event type is Other", Filter{Categories: []string{"Other"}}, admin, false, true},
		{"no event type isn't a category", Filter{Categories: []string{"Structure fire"}}, admin, false, false},
		{"priority", Filter{Priorities: []string{"Emergency"}}, fire, false, true},
		{"priority miss", Filter{Priorities: []string{"Non-emergency"}}, fire, false, false},
		{"text, any case", Filter{Text: "smoke"}, fire, false, true},
		{"text in an extra field", Filter{Text: "mill"}, fire, false, true},
		{"text miss", Filter{Text: "grass"}, fire, false, false},
		{"group", Filter{Group: "stn1"}, fire, false, true},
		{"agency", Filter{Agency: "fire serv"}, fire, false, true},
		{"job", Filter{Job: "EX0000002"}, fire, false, false},
		{"flag for an admin", Filter{ParseFailed: true}, fire, true, false},
		{"flag ignored for a user", Filter{ParseFailed: true}, fire, false, true},
		{"flag set", Filter{ParseFailed: true}, admin, true, true},
	}
	for _, c := range cases {
		if got := c.f.Matches(c.m, n, c.admin); got != c.want {
			t.Errorf("%s: %v, want %v", c.name, got, c.want)
		}
	}
}

// service is a Service with no database, enough to build and send requests.
func service(t *testing.T, presets Presets) *Service {
	n, agencies := network(t)
	app, _ := url.Parse("https://pager.example.test")
	melbourne, _ := time.LoadLocation("Australia/Melbourne")
	return &Service{Network: n, Agencies: agencies, Presets: presets, AppURL: app, Location: melbourne, Version: "test",
		Guard: &Guard{Allow: func(context.Context) (AllowList, error) { return ParseAllowList([]string{"127.0.0.0/8"}) }, Restrict: OnlyLoopback}}
}

// golden row 1 as stored, with the example profile's label for HSF1.
func row1() db.SearchMessagesRow {
	lon, lat, src := -140.864937, -34.831359, "address"
	agency := "example-fire"
	return db.SearchMessagesRow{ID: 42, Capcode: "0100001", Text: "@@EX0000001 HSF1 SMOKE FROM ROOF @ 12 FICTION ST, MADEUPTON GRID 123456 UNITS T1 T2 #STN1",
		Priority: sp("Emergency"), SentAt: time.Date(2026, 10, 16, 3, 4, 5, 0, time.UTC), AliasName: sp("Station 1"), Agency: sp("Example Fire Service"),
		AgencyProfile: &agency, JobNumber: sp("EX0000001"), EventType: sp("HSF1"), Description: sp("SMOKE FROM ROOF"), Address: sp("12 FICTION ST"),
		Locality: sp("MADEUPTON"), GridRef: sp("123456"), Units: []string{"T1", "T2"}, PagingGroup: sp("STN1"), Extra: []byte(`{}`),
		Lon: &lon, Lat: &lat, LocationSource: &src}
}

func integration(kind Kind, content string, cfg Config) loaded {
	return loaded{row: db.Integration{ID: 7, Uid: "k3x9q2m7vw4d", Name: "Station 1 fires", Kind: string(kind), Content: content}, kind: kind, config: cfg}
}

// pageText and address must never be in a Summary (R50a, A37).
const pageText, address = "SMOKE FROM ROOF", "FICTION ST"

func TestPayload(t *testing.T) {
	s := service(t, DefaultPresets())
	p := s.pageOf(row1(), false)
	full, err := payload(integration(Webhook, Full, Config{}), p)
	if err != nil {
		t.Fatal(err)
	}
	var f map[string]any
	_ = json.Unmarshal(full, &f)
	if f["text"] != "EX0000001 HSF1 SMOKE FROM ROOF @ 12 FICTION ST, MADEUPTON GRID 123456 UNITS T1 T2 #STN1" {
		t.Errorf("text %v: want it without the marker", f["text"])
	}
	for k, want := range map[string]any{"event_type": "page", "test": false, "id": 42.0, "version": 1.0, "content": "full", "capcode": "0100001",
		"alias": "Station 1", "priority": "Emergency", "category": "Structure fire", "event_label": "House fire", "job_number": "EX0000001",
		"url": "https://pager.example.test/messages/42", "sent_at": "2026-10-16T03:04:05Z"} {
		if f[k] != want {
			t.Errorf("full %s = %v, want %v", k, f[k], want)
		}
	}
	if fields := f["fields"].(map[string]any); fields["event_type"] != "HSF1" || fields["address"] != "12 FICTION ST" {
		t.Errorf("fields %v", fields)
	}
	if _, ok := f["flags"]; ok {
		t.Error("a user's integration got the admin-only flags")
	}
	if loc, ok := f["location"].(map[string]any); !ok || loc["source"] != "address" {
		t.Errorf("location %v", f["location"])
	}
	adminFull, _ := payload(integration(Webhook, Full, Config{}), s.pageOf(row1(), true))
	if !bytes.Contains(adminFull, []byte(`"flags":{"parse_failed":false`)) {
		t.Error("an admin's integration has no flags")
	}

	summary, _ := payload(integration(Webhook, Summary, Config{}), p)
	for _, banned := range []string{pageText, address, "MADEUPTON", "123456", "STN1", "T1", "Example Fire Service", `"text"`, `"location"`, `"extra"`, `"description"`} {
		if bytes.Contains(summary, []byte(banned)) {
			t.Errorf("summary contains %s: %s", banned, summary)
		}
	}
	var sm map[string]any
	_ = json.Unmarshal(summary, &sm)
	for _, k := range []string{"version", "event_type", "test", "id", "url", "integration", "content", "sent_at", "capcode", "alias", "priority", "category", "event_label", "job_number", "fields"} {
		if _, ok := sm[k]; !ok {
			t.Errorf("summary has no %s", k)
		}
	}
	if len(sm) != 15 {
		t.Errorf("summary has %d keys, want 15: %s", len(sm), summary)
	}

	test, _ := payload(integration(Webhook, Full, Config{}), testPage(time.Now(), "0100001", sp("Station 1"), nil))
	if !bytes.Contains(test, []byte(`"test":true`)) || !bytes.Contains(test, []byte(`"event_type":"test"`)) || !bytes.Contains(test, []byte(`"id":null`)) {
		t.Errorf("test payload %s", test)
	}
}

// stands are the stand-ins and the presets aimed at them.
func stands(t *testing.T) (map[Kind]*standin.Server, Presets) {
	m := map[Kind]*standin.Server{Webhook: standin.Webhook(), Discord: standin.Discord(), Telegram: standin.Telegram(), Pushover: standin.Pushover(), Ntfy: standin.Ntfy()}
	for _, s := range m {
		t.Cleanup(s.Close)
	}
	return m, Presets{
		DiscordPrefixes: []string{m[Discord].URL + "/api/webhooks/"},
		TelegramBase:    m[Telegram].URL,
		PushoverURL:     m[Pushover].URL + "/1/messages.json",
		NtfyServer:      m[Ntfy].URL,
	}
}

func presetIntegration(kind Kind, content string, m map[Kind]*standin.Server) (loaded, Secrets) {
	switch kind {
	case Webhook:
		return integration(kind, content, Config{AuthHeader: "X-Api-Key"}), Secrets{URL: m[Webhook].URL + "/hook", AuthValue: "hunter2"}
	case Discord:
		return integration(kind, content, Config{}), Secrets{URL: m[Discord].URL + "/api/webhooks/1234567890/AbC-dEf_123"}
	case Telegram:
		return integration(kind, content, Config{ChatID: "-100123456"}), Secrets{BotToken: "123456:ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghij"}
	case Pushover:
		two := 2
		return integration(kind, content, Config{Priority: &two}), Secrets{AppToken: "azGDORePK8gMaC0QOYAMyEEuzJnyUi", UserKey: "uQiRzpo4DXghDmr9QzzfQu27cmVRsG"}
	case Ntfy:
		return integration(kind, content, Config{Server: m[Ntfy].URL}), Secrets{Topic: "pager-test_1", AccessToken: "tk_test"}
	}
	panic(kind)
}

// Each preset's request passes its stand-in's format checks, in Full and
// Summary, including a page longer than every service's limit; a Summary
// has neither text nor address (R50a).
func TestPresetsPassTheirStandIns(t *testing.T) {
	m, presets := stands(t)
	s := service(t, presets)
	long := row1()
	long.Text = "@@" + strings.Repeat("SMOKE FROM ROOF ", 400) + "@ 12 FICTION ST, MADEUPTON #STN1"
	for _, kind := range []Kind{Webhook, Discord, Telegram, Pushover, Ntfy} {
		for _, content := range []string{Full, Summary} {
			for name, r := range map[string]db.SearchMessagesRow{"row 1": row1(), "long": long} {
				l, sec := presetIntegration(kind, content, m)
				before := len(m[kind].Requests())
				res := s.send(context.Background(), l, sec, s.pageOf(r, false), false)
				if res.outcome != delivered {
					t.Errorf("%s %s %s: %+v; violations %v", kind, content, name, res, m[kind].Violations())
					continue
				}
				reqs := m[kind].Requests()
				if len(reqs) != before+1 {
					t.Fatalf("%s: %d requests", kind, len(reqs))
				}
				body := string(reqs[len(reqs)-1].Body)
				if f := reqs[len(reqs)-1].Form; f != nil { // Pushover: look at the decoded values
					body = fmt.Sprint(f)
				}
				if content == Summary && (strings.Contains(body, pageText) || strings.Contains(body, address)) {
					t.Errorf("%s summary contains page text or the address: %s", kind, body)
				}
				if content == Full && name == "row 1" && !strings.Contains(body, pageText) {
					t.Errorf("%s full has no page text: %s", kind, body)
				}
			}
		}
		if v := m[kind].Violations(); len(v) > 0 {
			t.Errorf("%s violations: %v", kind, v)
		}
	}
	if h := m[Webhook].Requests()[0].Header.Get("X-Api-Key"); h != "hunter2" {
		t.Errorf("webhook auth header %q", h)
	}
	if h := m[Ntfy].Requests()[0].Header.Get("Authorization"); h != "Bearer tk_test" {
		t.Errorf("ntfy auth %q", h)
	}
	if f := m[Pushover].Requests()[0].Form; f.Get("retry") != "60" || f.Get("priority") != "2" {
		t.Errorf("pushover emergency priority: %v", f)
	}
}

// The stand-ins really check: a request that breaks each service's format
// is refused and recorded.
func TestStandInsRefuseBadRequests(t *testing.T) {
	m, _ := stands(t)
	post := func(u, ct, body string) int {
		resp, err := http.Post(u, ct, strings.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		return resp.StatusCode
	}
	cases := []struct {
		kind      Kind
		path, ct  string
		body      string
		violation string
	}{
		{Discord, "/api/webhooks/1/tok", "application/json", `{"content":"","allowed_mentions":{"parse":[]}}`, "at least one of"},
		{Discord, "/api/webhooks/1/tok", "application/json", `{"content":"` + strings.Repeat("x", 2001) + `","allowed_mentions":{"parse":[]}}`, "longer than 2000"},
		{Discord, "/api/webhooks/1/tok", "application/json", `{"content":"hi"}`, "mentions off"},
		{Discord, "/api/webhooks/1/tok", "text/plain", `{"content":"hi","allowed_mentions":{"parse":[]}}`, "Content-Type"},
		{Telegram, "/bot1:abc/sendMessage", "application/json", `{"chat_id":1}`, "text must be"},
		{Telegram, "/bot1:abc/sendMessage", "application/json", `{"text":"hi"}`, "chat_id is required"},
		{Pushover, "/1/messages.json", "application/x-www-form-urlencoded", "token=short&user=uQiRzpo4DXghDmr9QzzfQu27cmVRsG&message=hi", "token must be"},
		{Pushover, "/1/messages.json", "application/x-www-form-urlencoded", "token=azGDORePK8gMaC0QOYAMyEEuzJnyUi&user=uQiRzpo4DXghDmr9QzzfQu27cmVRsG&message=hi&priority=2", "priority 2 needs"},
		{Pushover, "/1/messages.json", "application/json", `{"token":"x"}`, "Content-Type"},
		{Ntfy, "/", "application/json", `{"message":"hi"}`, "topic is required"},
		{Ntfy, "/", "application/json", `{"topic":"t","tags":"a,b"}`, "tags must be"},
		{Ntfy, "/", "application/json", `{"topic":"t","priority":9}`, "priority must be"},
	}
	for _, c := range cases {
		before := len(m[c.kind].Violations())
		if code := post(m[c.kind].URL+c.path, c.ct, c.body); code != http.StatusBadRequest {
			t.Errorf("%s %s: status %d, want 400", c.kind, c.violation, code)
		}
		v := m[c.kind].Violations()
		if len(v) == before || !strings.Contains(strings.Join(v[before:], ";"), c.violation) {
			t.Errorf("%s: violations %v, want %q", c.kind, v[before:], c.violation)
		}
	}
}

// Errors never quote the URL, which can carry credentials (ADR 0006 §9).
func TestErrorsNeverQuoteTheURL(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	ln.Close() // nothing listens: connection refused
	s := service(t, DefaultPresets())
	l := integration(Webhook, Full, Config{})
	res := s.send(context.Background(), l, Secrets{URL: "http://" + addr + "/hook/T0PS3CRET"}, s.pageOf(row1(), false), false)
	if res.outcome != retryable || res.err != "connection refused" {
		t.Errorf("refused: %+v", res)
	}
	if strings.Contains(res.err+res.status, "T0PS3CRET") || strings.Contains(res.err, addr) {
		t.Errorf("error quotes the URL: %q", res.err)
	}
	// An address the rules refuse is reported by address, never by URL.
	s.Guard.Allow = nil
	res = s.send(context.Background(), l, Secrets{URL: "http://127.0.0.1:9/hook/T0PS3CRET"}, s.pageOf(row1(), false), false)
	if res.outcome != retryable || res.err != "address not allowed: 127.0.0.1 is a loopback address. Ask an admin to allow it." {
		t.Errorf("not allowed: %+v", res)
	}
}

// A request that was sent but got no answer is not retried (owner, Q2);
// 5xx and 429 are, with Retry-After honoured; other 4xx are refusals.
func TestOutcomes(t *testing.T) {
	status := 0
	hang := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if status == 0 {
			<-hang
			return
		}
		if status == 429 {
			w.Header().Set("Retry-After", "7")
		}
		w.WriteHeader(status)
	}))
	defer srv.Close()
	defer close(hang)
	s := service(t, DefaultPresets())
	l := integration(Webhook, Full, Config{})
	sec := Secrets{URL: srv.URL + "/hook"}
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	res := s.send(ctx, l, sec, s.pageOf(row1(), false), false)
	cancel()
	if res.outcome != noAnswer || res.err != "no answer within 10 s" {
		t.Errorf("no answer: %+v", res)
	}
	for code, want := range map[int]outcome{200: delivered, 204: delivered, 301: refused, 400: refused, 401: refused, 404: refused,
		408: retryable, 429: retryable, 500: retryable, 501: refused, 502: retryable, 503: retryable} {
		status = code
		res := s.send(context.Background(), l, sec, s.pageOf(row1(), false), false)
		if res.outcome != want {
			t.Errorf("HTTP %d: outcome %v, want %v", code, res.outcome, want)
		}
		if code == 429 && res.retryAfter != 7*time.Second {
			t.Errorf("Retry-After: %v", res.retryAfter)
		}
	}
	if d := retryAfter("", []byte(`{"message":"You are being rate limited.","retry_after":1.5,"global":false}`)); d != 1500*time.Millisecond {
		t.Errorf("Discord retry_after: %v", d)
	}
	if d := retryAfter("", []byte(`{"ok":false,"error_code":429,"parameters":{"retry_after":3}}`)); d != 3*time.Second {
		t.Errorf("Telegram retry_after: %v", d)
	}
}

func TestBackoff(t *testing.T) {
	var at time.Duration
	var schedule []time.Duration
	for n := int32(1); at < Cutoff; n++ {
		at += backoff(n)
		schedule = append(schedule, at)
	}
	// 2, 6, 14, 30, 62 s, then every minute: 9 attempts start before the cut-off.
	want := []time.Duration{2, 6, 14, 30, 62, 122, 182, 242, 302}
	for i, w := range want {
		if schedule[i] != w*time.Second {
			t.Fatalf("schedule %v, want %v s", schedule, want)
		}
	}
}

func TestRenderTimesInTheDeploymentZone(t *testing.T) {
	s := service(t, DefaultPresets())
	r := s.render(integration(Discord, Summary, Config{}), s.pageOf(row1(), false))
	if r.Title != "Emergency · Station 1" || r.Body != "HSF1 House fire · Job EX0000001 · 16 Oct 2026, 14:04:05" {
		t.Errorf("rendered %q / %q", r.Title, r.Body)
	}
}
