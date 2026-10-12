// Package acceptance runs SPEC.md section 9 checks against a real
// PostgreSQL + PostGIS database, through the HTTP API.
//
// Set PAGERARCHIVE_TEST_DATABASE_URL to a database the tests may wipe. Without
// it the tests skip, unless PAGERARCHIVE_REQUIRE_DB=1 (as in CI).
package acceptance

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/RIKIKU/PagerArchive/internal/accounts"
	"github.com/RIKIKU/PagerArchive/internal/catalog"
	"github.com/RIKIKU/PagerArchive/internal/ingest"
	"github.com/RIKIKU/PagerArchive/internal/live"
	"github.com/RIKIKU/PagerArchive/internal/profile"
	"github.com/RIKIKU/PagerArchive/internal/reparse"
	"github.com/RIKIKU/PagerArchive/internal/server"
	"github.com/RIKIKU/PagerArchive/internal/store"
	"github.com/RIKIKU/PagerArchive/internal/store/db"
	"github.com/RIKIKU/PagerArchive/internal/streets"
)

// env is one test's server, database and clients.
type env struct {
	t     *testing.T
	ctx   context.Context
	st    *store.Store
	url   string
	keys  map[string]string // client ID -> API key
	logs  *syncBuffer
	posts []string // every page text posted, for the log check
	now   time.Time

	srv    *server.Server
	cat    *catalog.Catalog
	worker *reparse.Worker

	// Accounts (M3): a fake Authentik, the first admin and a user. Requests
	// are made as e.as (the user unless a test changes it).
	acc   *accounts.Service
	dir   *fakeDir
	admin *member
	user  *member
	as    *member
}

type syncBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

// setup gives an empty database with the standard test fixture (section 10)
// and two clients, A and B.
func setup(t *testing.T) *env {
	t.Helper()
	url := os.Getenv("PAGERARCHIVE_TEST_DATABASE_URL")
	if url == "" {
		if os.Getenv("PAGERARCHIVE_REQUIRE_DB") == "1" {
			t.Fatal("PAGERARCHIVE_TEST_DATABASE_URL is not set")
		}
		t.Skip("PAGERARCHIVE_TEST_DATABASE_URL is not set")
	}
	ctx := context.Background()
	conn, err := pgx.Connect(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := conn.Exec(ctx, "DROP SCHEMA public CASCADE; CREATE SCHEMA public"); err != nil {
		t.Fatal(err)
	}
	conn.Close(ctx)

	st, err := store.Open(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(st.Close)

	set, err := profile.LoadDir("../../profiles/example")
	if err != nil {
		t.Fatal(err)
	}
	n := set.Networks["example-network"]
	ds, err := streets.LoadFile(n.DatasetPath)
	if err != nil {
		t.Fatal(err)
	}

	// Standard test fixture: template "Example Fire" and Stations 1-4.
	agencyProfile := "example-fire"
	tpl, err := st.CreateTemplate(ctx, db.CreateTemplateParams{Name: "Example Fire", Agency: "Example Fire Service", AgencyProfile: &agencyProfile})
	if err != nil {
		t.Fatal(err)
	}
	for i := 1; i <= 4; i++ {
		if _, err := st.CreateAlias(ctx, db.CreateAliasParams{Pattern: fmt.Sprintf("010000%d", i), Name: fmt.Sprintf("Station %d", i), TemplateID: &tpl}); err != nil {
			t.Fatal(err)
		}
	}

	e := &env{t: t, ctx: ctx, st: st, keys: map[string]string{}, logs: &syncBuffer{}, now: time.Now().UTC().Truncate(time.Second)}
	for _, id := range []string{"a", "b"} {
		key, err := st.AddClient(ctx, id, "Client "+strings.ToUpper(id))
		if err != nil {
			t.Fatal(err)
		}
		e.keys[id] = key
	}
	ix := streets.NewIndex(ds.Streets, n.StreetTypes)
	log := slog.New(slog.NewJSONHandler(e.logs, &slog.HandlerOptions{Level: slog.LevelDebug}))
	hub := live.NewHub()
	e.dir = newFakeDir()
	e.acc = &accounts.Service{Store: st, Dir: e.dir, AllowedDomains: []string{"example.com"}, Now: time.Now, Log: log, SignedOut: hub.CloseUsers}
	srv := &server.Server{
		Store:    st,
		Ingest:   &ingest.Service{Store: st, Network: n, Agencies: set.Agencies, Streets: ix},
		Log:      log,
		Now:      time.Now,
		Hub:      hub,
		Location: time.UTC,
		Accounts: e.acc,
	}
	e.srv = srv
	e.cat = &catalog.Catalog{Store: st}
	e.worker = &reparse.Worker{Store: st, Network: n, Agencies: set.Agencies, Streets: ix, Log: log, Poll: time.Minute}
	hs := httptest.NewServer(srv.Handler())
	t.Cleanup(hs.Close)
	e.url = hs.URL
	e.admin = e.newMember("admin@example.com", accounts.RoleAdmin, nil)
	e.user = e.newMember("user@example.com", accounts.RoleUser, e.admin)
	e.as = e.user
	// P1: no server log line contains page text.
	t.Cleanup(func() {
		logs := e.logs.String()
		for _, text := range e.posts {
			if strings.Contains(logs, text) {
				t.Errorf("server log contains page text %q", text)
			}
		}
	})
	return e
}

// post sends a page as a client and returns the HTTP status and response.
func (e *env) post(client, capcode, text string, at time.Time) (int, server.PageResponse) {
	e.t.Helper()
	e.posts = append(e.posts, text)
	body, _ := json.Marshal(server.PageRequest{ClientID: client, Capcode: capcode, Text: text, ReceivedAt: at, Baud: 512})
	return e.postRaw(e.keys[client], body)
}

func (e *env) postRaw(key string, body []byte) (int, server.PageResponse) {
	e.t.Helper()
	req, _ := http.NewRequest(http.MethodPost, e.url+"/api/v1/pages", bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+key)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		e.t.Fatal(err)
	}
	defer resp.Body.Close()
	var pr server.PageResponse
	_ = json.NewDecoder(resp.Body).Decode(&pr)
	return resp.StatusCode, pr
}

func (e *env) mustPost(client, capcode, text string, at time.Time) server.PageResponse {
	e.t.Helper()
	code, pr := e.post(client, capcode, text, at)
	if code != http.StatusOK {
		e.t.Fatalf("post returned %d", code)
	}
	return pr
}

type message struct {
	ID             int64
	Capcode        string
	Text           string
	Priority       *string
	SentAt         time.Time
	Format         *string
	JobNumber      *string
	ParseFailed    bool
	AddressUnsplit bool
}

func (e *env) messages() []message {
	e.t.Helper()
	rows, err := e.st.Pool.Query(e.ctx, `SELECT id, capcode, text, priority, sent_at, format, job_number, parse_failed, address_unsplit FROM messages ORDER BY id`)
	if err != nil {
		e.t.Fatal(err)
	}
	ms, err := pgx.CollectRows(rows, pgx.RowToStructByPos[message])
	if err != nil {
		e.t.Fatal(err)
	}
	return ms
}

type reception struct {
	ClientID   string
	ReceivedAt time.Time
}

func (e *env) receptions(id int64) []reception {
	e.t.Helper()
	rows, err := e.st.Pool.Query(e.ctx, `SELECT client_id, received_at FROM receptions WHERE message_id = $1 ORDER BY received_at`, id)
	if err != nil {
		e.t.Fatal(err)
	}
	rs, err := pgx.CollectRows(rows, pgx.RowToStructByPos[reception])
	if err != nil {
		e.t.Fatal(err)
	}
	return rs
}

// rowsContaining returns "table: row" for every row in every application
// table whose text form contains s.
func (e *env) rowsContaining(s string) []string {
	e.t.Helper()
	rows, err := e.st.Pool.Query(e.ctx, `SELECT table_name FROM information_schema.tables
		WHERE table_schema = 'public' AND table_type = 'BASE TABLE' AND table_name <> 'spatial_ref_sys'`)
	if err != nil {
		e.t.Fatal(err)
	}
	tables, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		e.t.Fatal(err)
	}
	if len(tables) < 5 {
		e.t.Fatalf("only found tables %v", tables)
	}
	var hits []string
	for _, tbl := range tables {
		rows, err := e.st.Pool.Query(e.ctx, fmt.Sprintf(`SELECT t::text FROM %s t`, pgx.Identifier{tbl}.Sanitize()))
		if err != nil {
			e.t.Fatal(err)
		}
		texts, err := pgx.CollectRows(rows, pgx.RowTo[string])
		if err != nil {
			e.t.Fatal(err)
		}
		for _, row := range texts {
			if strings.Contains(row, s) {
				hits = append(hits, tbl+": "+row)
			}
		}
	}
	return hits
}

func (e *env) clockAhead(id string) bool {
	e.t.Helper()
	clients, err := e.st.ListClients(e.ctx)
	if err != nil {
		e.t.Fatal(err)
	}
	for _, c := range clients {
		if c.ID == id {
			return c.ClockAheadSince != nil
		}
	}
	e.t.Fatalf("no client %s", id)
	return false
}

func deref(s *string) string {
	if s == nil {
		return "<null>"
	}
	return *s
}

// The database is the pinned PostGIS image, with the extensions the
// migrations need.
func TestPostGIS(t *testing.T) {
	e := setup(t)
	var version string
	if err := e.st.Pool.QueryRow(e.ctx, `SELECT postgis_full_version()`).Scan(&version); err != nil {
		t.Fatal(err)
	}
	t.Logf("postgis: %.60s", version)
	var n int
	if err := e.st.Pool.QueryRow(e.ctx, `SELECT count(*) FROM pg_extension WHERE extname IN ('postgis', 'pg_trgm')`).Scan(&n); err != nil || n != 2 {
		t.Fatalf("extensions: %d, %v", n, err)
	}
}

const row1 = "@@EX0000001 HSF1 SMOKE FROM ROOF @ 12 FICTION ST, MADEUPTON GRID 123456 UNITS T1 T2 #STN1"

// A2. Deduplication across clients.
func TestA2DeduplicationAcrossClients(t *testing.T) {
	e := setup(t)
	t0 := e.now.Add(-time.Minute)
	first := e.mustPost("a", "0100001", row1, t0)
	second := e.mustPost("b", "0100001", row1, t0.Add(3*time.Second))
	if first.Outcome != ingest.Stored || second.Outcome != ingest.Duplicate || second.MessageID != first.MessageID {
		t.Fatalf("outcomes %+v, %+v", first, second)
	}
	ms := e.messages()
	if len(ms) != 1 {
		t.Fatalf("%d messages stored, want 1", len(ms))
	}
	if !ms[0].SentAt.Equal(t0) {
		t.Errorf("sent time %v, want client A's receive time %v", ms[0].SentAt, t0)
	}
	rs := e.receptions(ms[0].ID)
	if len(rs) != 2 || rs[0].ClientID != "a" || !rs[0].ReceivedAt.Equal(t0) || rs[1].ClientID != "b" || !rs[1].ReceivedAt.Equal(t0.Add(3*time.Second)) {
		t.Errorf("receptions %+v", rs)
	}

	// 11 seconds after the first: a new transmission.
	third := e.mustPost("a", "0100001", row1, t0.Add(11*time.Second))
	if third.Outcome != ingest.Stored || len(e.messages()) != 2 {
		t.Fatalf("11 s later: %+v, %d messages", third, len(e.messages()))
	}
	t.Logf("A2: 3 s apart -> 1 message with receptions %v; 11 s later -> %d messages", rs, len(e.messages()))
}

// Section 14: the window is symmetric, and sent time becomes the earliest.
func TestDedupWindowIsSymmetric(t *testing.T) {
	e := setup(t)
	t0 := e.now.Add(-time.Minute)
	e.mustPost("a", "0100002", "QDHall unlocked", t0)
	late := e.mustPost("b", "0100002", "QDHall unlocked", t0.Add(-10*time.Second)) // buffered copy, earlier
	ms := e.messages()
	if late.Outcome != ingest.Duplicate || len(ms) != 1 || !ms[0].SentAt.Equal(t0.Add(-10*time.Second)) {
		t.Fatalf("outcome %s, messages %+v", late.Outcome, ms)
	}
	// Every capcode's copy is its own message (R20).
	e.mustPost("a", "0100003", "QDHall unlocked", t0)
	if len(e.messages()) != 2 {
		t.Fatal("copy on another capcode wasn't stored")
	}
}

// A5. Unparseable page.
func TestA5UnparseablePage(t *testing.T) {
	e := setup(t)
	e.mustPost("a", "0100001", "@@GARBLED 123", e.now)
	ms := e.messages()
	if len(ms) != 1 {
		t.Fatalf("%d messages", len(ms))
	}
	m := ms[0]
	if deref(m.Priority) != "Emergency" || !m.ParseFailed || m.Text != "@@GARBLED 123" || m.Format != nil {
		t.Errorf("got priority %s, parse_failed %v, text %q, format %s", deref(m.Priority), m.ParseFailed, m.Text, deref(m.Format))
	}
}

// A6 (storage). Unknown capcode: stored, text only, not flagged.
func TestA6UnknownCapcodeStorage(t *testing.T) {
	e := setup(t)
	e.mustPost("a", "999999", "QDFirst page to an unknown pager", e.now.Add(-time.Minute))
	e.mustPost("b", "0999999", "@@Second page to an unknown pager", e.now)
	ms := e.messages()
	if len(ms) != 2 {
		t.Fatalf("%d messages, want 2", len(ms))
	}
	for _, m := range ms {
		if m.Capcode != "0999999" || m.ParseFailed || m.AddressUnsplit || m.Format != nil {
			t.Errorf("message %+v", m)
		}
	}
	if ms[0].Text != "QDFirst page to an unknown pager" || ms[1].Text != "@@Second page to an unknown pager" {
		t.Errorf("texts %q, %q", ms[0].Text, ms[1].Text)
	}
}

// A7. Redaction and must-not-match.
func TestA7RedactionAndMustNotMatch(t *testing.T) {
	e := setup(t)
	e.mustPost("a", "0100001", "QDCheck the shed Saturday. Text 0412 345 678 if available. EX0000001", e.now)
	ms := e.messages()
	if len(ms) != 1 {
		t.Fatalf("%d messages", len(ms))
	}
	if want := "QDCheck the shed Saturday. Text REDACTED if available. EX0000001"; ms[0].Text != want {
		t.Errorf("stored %q, want %q", ms[0].Text, want)
	}
	for _, s := range []string{"0412 345 678", "0412345678"} {
		if hits := e.rowsContaining(s); len(hits) > 0 {
			t.Errorf("%q found in %v", s, hits)
		}
	}
	if deref(ms[0].JobNumber) != "EX0000001" {
		t.Errorf("job %s, want EX0000001", deref(ms[0].JobNumber))
	}
}

// A8. Sensitive details never stored.
func TestA8SensitiveDetailsNeverStored(t *testing.T) {
	e := setup(t)
	e.mustPost("a", "0100001", "@@EX0000005 MED1 PERSON COLLAPSED AGE: 52 SEX: M @ 1 EXAMPLE ST, MADEUPTON #STN1", e.now)
	ms := e.messages()
	if len(ms) != 1 {
		t.Fatalf("%d messages", len(ms))
	}
	for _, s := range []string{"PERSON COLLAPSED", "1 EXAMPLE ST, MADEUPTON"} {
		if !strings.Contains(ms[0].Text, s) {
			t.Errorf("stored text %q doesn't contain %q", ms[0].Text, s)
		}
	}
	for _, s := range []string{"AGE: 52", "SEX: M"} {
		if hits := e.rowsContaining(s); len(hits) > 0 {
			t.Errorf("%q found in %v", s, hits)
		}
	}
}

// A9. Ignored capcode: nothing stored, but the client's last-seen time moves (R16).
func TestA9IgnoredCapcode(t *testing.T) {
	e := setup(t)
	code, pr := e.post("a", "0000042", "@@SYSTEM CLOCK 0000", e.now)
	if code != http.StatusOK || pr.Outcome != ingest.Ignored {
		t.Fatalf("got %d %+v", code, pr)
	}
	if n := len(e.messages()); n != 0 {
		t.Fatalf("%d messages stored, want 0", n)
	}
	if hits := e.rowsContaining("SYSTEM CLOCK"); len(hits) > 0 {
		t.Errorf("text recorded: %v", hits)
	}
	clients, err := e.st.ListClients(e.ctx)
	if err != nil {
		t.Fatal(err)
	}
	if clients[0].ID != "a" || clients[0].LastSeenAt == nil || !clients[0].LastSeenAt.Equal(e.now) {
		t.Errorf("client a last seen %v, want %v", clients[0].LastSeenAt, e.now)
	}
}

// R14: per-client API keys.
func TestClientAuthentication(t *testing.T) {
	e := setup(t)
	body, _ := json.Marshal(server.PageRequest{Capcode: "0100001", Text: "QDx", ReceivedAt: e.now})
	if code, _ := e.postRaw("", body); code != http.StatusUnauthorized {
		t.Errorf("no key: %d", code)
	}
	if code, _ := e.postRaw("pa_wrong", body); code != http.StatusUnauthorized {
		t.Errorf("wrong key: %d", code)
	}
	// The key decides the client; a mismatched client_id is refused.
	if code, _ := e.post("a", "0100001", "QDx", e.now); code != http.StatusOK {
		t.Errorf("good key: %d", code)
	}
	spoof, _ := json.Marshal(server.PageRequest{ClientID: "b", Capcode: "0100001", Text: "QDx", ReceivedAt: e.now})
	if code, _ := e.postRaw(e.keys["a"], spoof); code != http.StatusForbidden {
		t.Errorf("spoofed client_id: %d", code)
	}
	if code, _ := e.post("a", "0100001", "QDfuture", time.Now().Add(2*time.Minute)); code != http.StatusUnprocessableEntity {
		t.Errorf("future receive time: %d", code)
	}
	if !e.clockAhead("a") {
		t.Error("client a should be flagged clock ahead")
	}
	// The next accepted page clears the flag.
	if code, _ := e.post("a", "0100001", "QDnow", e.now); code != http.StatusOK || e.clockAhead("a") {
		t.Errorf("accepted page: %d, clock ahead %v", code, e.clockAhead("a"))
	}
	if n, err := e.st.RevokeClient(e.ctx, "a"); err != nil || n != 1 {
		t.Fatal(err, n)
	}
	if code, _ := e.post("a", "0100001", "QDy", e.now); code != http.StatusUnauthorized {
		t.Errorf("revoked key: %d", code)
	}
	if code, _ := e.post("b", "0100001", "QDy", e.now); code != http.StatusOK {
		t.Errorf("other client after revoke: %d", code)
	}
	if code, _ := e.postRaw(e.keys["b"], []byte(`{"capcode": "0100001", "text": "QDsecret`)); code != http.StatusBadRequest {
		t.Errorf("broken JSON: %d", code)
	}
	e.posts = append(e.posts, "QDsecret")
}

// Golden row 1 parses end to end into stored fields.
func TestStoredFields(t *testing.T) {
	e := setup(t)
	e.mustPost("a", "0100001", row1, e.now)
	var units []string
	var desc, addr, loc, grid, group, typ string
	var extra []byte
	err := e.st.Pool.QueryRow(e.ctx, `SELECT units, description, address, locality, grid_ref, paging_group, event_type, extra FROM messages`).
		Scan(&units, &desc, &addr, &loc, &grid, &group, &typ, &extra)
	if err != nil {
		t.Fatal(err)
	}
	got := fmt.Sprint(units, desc, "|", addr, "|", loc, "|", grid, "|", group, "|", typ, "|", string(extra))
	want := "[T1 T2]SMOKE FROM ROOF|12 FICTION ST|MADEUPTON|123456|STN1|HSF1|{}"
	if got != want {
		t.Errorf("got %s, want %s", got, want)
	}
}
