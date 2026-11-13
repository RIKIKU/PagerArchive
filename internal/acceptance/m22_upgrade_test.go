package acceptance

import (
	"context"
	"database/sql"
	"os"
	"testing"

	"github.com/jackc/pgx/v5"
	_ "github.com/jackc/pgx/v5/stdlib" // database/sql for goose
	"github.com/pressly/goose/v3"

	"github.com/RIKIKU/PagerArchive/internal/store"
	"github.com/RIKIKU/PagerArchive/internal/store/db"
)

// R87-R87d (owner, 2026-11-13): a database that main had taken to migration
// 00018, with large events at levels, integrations selecting levels and
// their deliveries, upgrades to no levels: every event, page and delivery is
// kept, an integration at any level selects large events, a page
// integration stays one, and the level columns and codes are gone. The
// migration goes down and up again too. (A77 checks the upgrade from the
// latest release, which has no large events.)
func TestM22UpgradeFromLevels(t *testing.T) {
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
	defer conn.Close(ctx)
	exec := func(sql string, args ...any) {
		t.Helper()
		if _, err := conn.Exec(ctx, sql, args...); err != nil {
			t.Fatalf("%s: %v", sql, err)
		}
	}
	exec("DROP SCHEMA public CASCADE; CREATE SCHEMA public")
	sqlDB, err := sql.Open("pgx", url)
	if err != nil {
		t.Fatal(err)
	}
	defer sqlDB.Close()
	provider, err := goose.NewProvider(goose.DialectPostgres, sqlDB, os.DirFS("../store/migrations"), goose.WithLogger(goose.NopLogger()))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := provider.UpTo(ctx, 18); err != nil {
		t.Fatal(err)
	}

	// Main's data at 00018: made up, as everywhere in the tests.
	exec(`INSERT INTO users (email, email_key) VALUES ('owner@example.com', 'owner@example.com')`)
	for i, text := range []string{
		"@@EX0000101 GRS3 DG0801 GRASS FIRE @ SAMPLE RD, MADEUPTON #STN1",
		"@@EX0000102 GRS3 DG0802 GRASS FIRE @ SAMPLE RD, MADEUPTON #STN2",
	} {
		exec(`INSERT INTO messages (capcode, text, sent_at, dedup_key, priority, job_number)
		      VALUES ('0100001', $1::text, now() - make_interval(hours => $2::int), sha256(convert_to($1::text, 'UTF8')), 'Emergency', substr($1::text, 3, 9))`, text, 5-5*i)
	}
	exec(`INSERT INTO large_events (first_at, last_at, deployment_at, several_deployments_at)
	      SELECT min(sent_at), max(sent_at), min(sent_at), max(sent_at) FROM messages`)
	exec(`INSERT INTO large_event_messages (message_id, event_id, deployment_codes)
	      SELECT id, 1, ARRAY[substring(text from 'DG[0-9]{4}')] FROM messages`)
	for i, level := range []any{nil, "deployment", "several_deployments", "big_job"} {
		exec(`INSERT INTO integrations (uid, owner_id, name, kind, content, large_events)
		      VALUES ($1, 1, $2, 'webhook', 'summary', $3)`, "upgrade0000"+string(rune('a'+i)), "integration "+string(rune('a'+i)), level)
	}
	exec(`INSERT INTO deliveries (integration_id, message_id, status, event_id, delivered_at) VALUES (2, 1, 'delivered', 1, now())`)
	exec(`INSERT INTO deliveries (integration_id, message_id, status, event_id, next_attempt_at) VALUES (3, 2, 'pending', 1, now())`)

	// The upgrade: what the API runs on start.
	st, err := store.Open(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	count := func(sql string) int {
		t.Helper()
		var n int
		if err := conn.QueryRow(ctx, sql).Scan(&n); err != nil {
			t.Fatalf("%s: %v", sql, err)
		}
		return n
	}
	if n := count(`SELECT count(*) FROM information_schema.columns WHERE table_schema = 'public'
	                 AND column_name IN ('deployment_at', 'several_deployments_at', 'big_job_at', 'deployment_codes')`); n != 0 {
		t.Errorf("%d level or code columns left", n)
	}
	if n := count(`SELECT count(*) FROM large_events`); n != 1 {
		t.Errorf("%d events, want 1", n)
	}
	if n := count(`SELECT count(*) FROM large_event_messages WHERE event_id = 1`); n != 2 {
		t.Errorf("the event holds %d pages, want 2", n)
	}
	if n := count(`SELECT count(*) FROM deliveries WHERE event_id = 1`); n != 2 {
		t.Errorf("%d of the event's deliveries kept, want 2", n)
	}
	var selects []bool
	rows, err := conn.Query(ctx, `SELECT large_events FROM integrations ORDER BY id`)
	if err != nil {
		t.Fatal(err)
	}
	selects, err = pgx.CollectRows(rows, pgx.RowTo[bool])
	if err != nil {
		t.Fatal(err)
	}
	if len(selects) != 4 || selects[0] || !selects[1] || !selects[2] || !selects[3] {
		t.Errorf("integrations select large events: %v, want the page integration false and the three at levels true", selects)
	}
	got, err := st.LargeEventIntegrations(ctx)
	if err != nil || len(got) != 3 {
		t.Errorf("large-event integrations after the upgrade: %v %v, want 3", got, err)
	}
	list, err := st.ListLargeEvents(ctx, db.ListLargeEventsParams{MaxRows: 10})
	if err != nil || len(list) != 1 || list[0].Pages != 2 || list[0].Jobs != 2 {
		t.Errorf("the list after the upgrade: %+v %v", list, err)
	}

	// Down to 00018 and up again.
	if _, err := provider.DownTo(ctx, 18); err != nil {
		t.Fatalf("down: %v", err)
	}
	if n := count(`SELECT count(*) FROM integrations WHERE large_events = 'deployment'`); n != 3 {
		t.Errorf("down: %d integrations at deployment, want 3", n)
	}
	if _, err := provider.Up(ctx); err != nil {
		t.Fatalf("up again: %v", err)
	}
	t.Log("00018 with levels, codes and deliveries upgraded: 1 event of 2 pages, 2 deliveries, 3 integrations selecting large events, 1 page integration")
}
