package acceptance

import (
	"context"
	"os"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/RIKIKU/PagerArchive/internal/authentik"
)

// R84: the API sets up the locked-accounts metric's role itself, on every
// start, over and over, with the same rights as before (R52, ADR 0006): call
// one function and read nothing else. A changed password takes effect.
func TestM17MetricsRoleOnStart(t *testing.T) {
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
		// Named for this test: the roles and database of a real Authentik aren't here.
		_, _ = admin.Exec(ctx, `DROP DATABASE IF EXISTS authentik WITH (FORCE)`)
		_, _ = admin.Exec(ctx, `DROP ROLE IF EXISTS pagerarchive_metrics`)
		_, _ = admin.Exec(ctx, `DROP ROLE IF EXISTS authentik`)
	}
	drop()
	t.Cleanup(drop)
	// What Authentik's first-start script makes (deploy/postgres/10-authentik.sh).
	for _, sql := range []string{`CREATE ROLE authentik LOGIN PASSWORD 'x'`, `CREATE DATABASE authentik OWNER authentik`} {
		if _, err := admin.Exec(ctx, sql); err != nil {
			t.Fatal(err)
		}
	}

	// A password with a quote in it, and no Authentik tables yet: a new install.
	first := `it's a first password`
	if err := authentik.EnsureLocksRole(ctx, url, first); err != nil {
		t.Fatalf("on a new install: %v", err)
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
	// Authentik's tables, as far as the function reads them.
	if _, err := asAdmin.Exec(ctx, `
CREATE TABLE authentik_core_user (id int, username text);
CREATE TABLE authentik_events_event (action text, context jsonb, "user" jsonb, created timestamptz);
CREATE TABLE authentik_stages_authenticator_email_emaildevice (user_id int, throttling_failure_count int, throttling_failure_timestamp timestamptz);
ALTER TABLE authentik_core_user OWNER TO authentik;
ALTER TABLE authentik_events_event OWNER TO authentik;
ALTER TABLE authentik_stages_authenticator_email_emaildevice OWNER TO authentik`); err != nil {
		t.Fatal(err)
	}

	asMetrics := func(password string) (*pgx.Conn, error) {
		c := cfg.Copy()
		c.User, c.Password = "pagerarchive_metrics", password
		return pgx.ConnectConfig(ctx, c)
	}
	check := func(password string) {
		t.Helper()
		m, err := asMetrics(password)
		if err != nil {
			t.Fatalf("the role can't sign in: %v", err)
		}
		defer m.Close(ctx)
		var n int
		if err := m.QueryRow(ctx, `SELECT public.pagerarchive_locked_accounts()`).Scan(&n); err != nil || n != 0 {
			t.Fatalf("the function: %d, %v", n, err)
		}
		if _, err := m.Exec(ctx, `SELECT count(*) FROM authentik_core_user`); err == nil || !strings.Contains(err.Error(), "permission denied") {
			t.Errorf("the role read a table: %v", err)
		}
	}
	check(first)

	// Again, as on every start, and with a new password.
	if err := authentik.EnsureLocksRole(ctx, url, first); err != nil {
		t.Fatalf("a second start: %v", err)
	}
	check(first)
	if err := authentik.EnsureLocksRole(ctx, url, "a new password"); err != nil {
		t.Fatalf("a new password: %v", err)
	}
	check("a new password")
	if m, err := asMetrics(first); err == nil {
		m.Close(ctx)
		t.Error("the old password still works")
	}

	// An install from before the role existed: dropped, then one start.
	for _, sql := range []string{`DROP FUNCTION public.pagerarchive_locked_accounts()`, `REVOKE CONNECT ON DATABASE authentik FROM pagerarchive_metrics`, `DROP ROLE pagerarchive_metrics`} {
		if _, err := asAdmin.Exec(ctx, sql); err != nil {
			t.Fatal(err)
		}
	}
	if err := authentik.EnsureLocksRole(ctx, url, first); err != nil {
		t.Fatalf("after dropping the role: %v", err)
	}
	check(first)
}
