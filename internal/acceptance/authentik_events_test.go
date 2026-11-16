package acceptance

import (
	"context"
	"io"
	"net/http"
	"os"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/RIKIKU/PagerArchive/internal/authentik"
)

// R89b: the metrics count Authentik's sign-ins, failed sign-ins by the stage
// they failed at, and its errors, in the last 5 minutes, through the metrics
// role's second function: every kind and reason, zero included, and never a
// username, an address or an event's text (P1, R70).
func TestAuthentikEventsMetric(t *testing.T) {
	url := os.Getenv("PAGERARCHIVE_TEST_DATABASE_URL")
	if url == "" {
		if os.Getenv("PAGERARCHIVE_REQUIRE_DB") == "1" {
			t.Fatal("PAGERARCHIVE_TEST_DATABASE_URL is not set")
		}
		t.Skip("PAGERARCHIVE_TEST_DATABASE_URL is not set")
	}
	ctx := context.Background()
	admin, err := pgx.Connect(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	defer admin.Close(ctx)
	drop := func() {
		_, _ = admin.Exec(ctx, `DROP DATABASE IF EXISTS authentik WITH (FORCE)`)
		_, _ = admin.Exec(ctx, `DROP ROLE IF EXISTS pagerarchive_metrics`)
		_, _ = admin.Exec(ctx, `DROP ROLE IF EXISTS authentik`)
	}
	drop()
	t.Cleanup(drop)
	for _, sql := range []string{`CREATE ROLE authentik LOGIN PASSWORD 'x'`, `CREATE DATABASE authentik OWNER authentik`} {
		if _, err := admin.Exec(ctx, sql); err != nil {
			t.Fatal(err)
		}
	}
	const password = "a metrics password"
	if err := authentik.EnsureLocksRole(ctx, url, password); err != nil {
		t.Fatal(err)
	}
	cfg, err := pgx.ParseConfig(url)
	if err != nil {
		t.Fatal(err)
	}
	cfg.Database = "authentik"
	asAdmin, err := pgx.ConnectConfig(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer asAdmin.Close(ctx)
	// Authentik's event log, as far as the function reads it, with made-up
	// events: some now, one too old, one of a kind that isn't counted, and
	// usernames and addresses that must never come out.
	if _, err := asAdmin.Exec(ctx, `
CREATE TABLE authentik_events_event (action text, context jsonb, "user" jsonb, client_ip inet, created timestamptz);
ALTER TABLE authentik_events_event OWNER TO authentik;
INSERT INTO authentik_events_event (action, context, "user", client_ip, created) VALUES
 ('login', '{}', '{"username": "someone@example.test"}', '192.0.2.7', now() - interval '1 minute'),
 ('login', '{}', '{"username": "someone@example.test"}', '192.0.2.7', now() - interval '2 minutes'),
 ('login_failed', '{"username": "guess@example.test", "stage": {"model_name": "passwordstage"}}', '{}', '192.0.2.8', now() - interval '10 seconds'),
 ('login_failed', '{"username": "guess@example.test", "stage": {"model_name": "passwordstage"}}', '{}', '192.0.2.8', now() - interval '20 seconds'),
 ('login_failed', '{"username": "guess@example.test", "stage": {"model_name": "passwordstage"}}', '{}', '192.0.2.8', now() - interval '30 seconds'),
 ('login_failed', '{"username": "akadmin", "stage": {"model_name": "authenticatorvalidatestage"}}', '{}', '192.0.2.9', now() - interval '1 minute'),
 ('login_failed', '{"username": "other@example.test"}', '{}', '192.0.2.9', now() - interval '1 minute'),
 ('login_failed', '{"username": "old@example.test", "stage": {"model_name": "passwordstage"}}', '{}', '192.0.2.9', now() - interval '6 minutes'),
 ('system_exception', '{"message": "a traceback naming someone@example.test"}', '{}', NULL, now() - interval '1 minute'),
 ('model_created', '{}', '{}', NULL, now())`); err != nil {
		t.Fatal(err)
	}

	mcfg, err := pgxpool.ParseConfig(url)
	if err != nil {
		t.Fatal(err)
	}
	mcfg.ConnConfig.Database, mcfg.ConnConfig.User, mcfg.ConnConfig.Password = "authentik", "pagerarchive_metrics", password
	pool, err := pgxpool.NewWithConfig(ctx, mcfg)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	locks := &authentik.Locks{Pool: pool}
	rows, err := locks.Events(ctx)
	if err != nil {
		t.Fatalf("the metrics role can't call the function: %v", err)
	}
	got := map[string]int{}
	for _, r := range rows {
		got[r.Action+"/"+r.Reason] = r.N
	}
	want := map[string]int{
		"login/": 2, "login_failed/password": 3, "login_failed/second_factor": 1, "login_failed/other": 1,
		"system_exception/": 1, "system_task_exception/": 0, "configuration_error/": 0, "policy_exception/": 0, "property_mapping_exception/": 0,
	}
	if len(got) != len(want) {
		t.Errorf("rows: %v, want %v", got, want)
	}
	for k, n := range want {
		if got[k] != n {
			t.Errorf("%s: %d, want %d", k, got[k], n)
		}
	}
	t.Logf("R89b: the function counts %v", got)
	if _, err := pool.Exec(ctx, `SELECT count(*) FROM authentik_events_event`); err == nil || !strings.Contains(err.Error(), "permission denied") {
		t.Errorf("the metrics role read the event log: %v", err)
	}

	// Scraped, as Alloy does.
	e := setup(t)
	e.srv.Locks = locks
	req, _ := http.NewRequest(http.MethodGet, e.url+"/metrics", nil)
	req.Header.Set("Authorization", "Bearer "+metricsToken)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	body := string(b)
	for _, line := range []string{
		"# TYPE pagerarchive_authentik_events_5m gauge",
		`pagerarchive_authentik_events_5m{action="login",reason=""} 2`,
		`pagerarchive_authentik_events_5m{action="login_failed",reason="password"} 3`,
		`pagerarchive_authentik_events_5m{action="login_failed",reason="second_factor"} 1`,
		`pagerarchive_authentik_events_5m{action="system_exception",reason=""} 1`,
		`pagerarchive_authentik_events_5m{action="configuration_error",reason=""} 0`,
	} {
		if !strings.Contains(body, line) {
			t.Errorf("metrics have no %q", line)
		}
	}
	for _, banned := range []string{"example.test", "akadmin", "192.0.2.", "traceback", "model_created"} {
		if strings.Contains(body, banned) {
			t.Errorf("metrics contain %q", banned)
		}
	}
}
