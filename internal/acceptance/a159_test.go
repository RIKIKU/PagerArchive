package acceptance

import (
	"bytes"
	"context"
	"database/sql"
	"log/slog"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	_ "github.com/jackc/pgx/v5/stdlib" // database/sql for goose
	"github.com/pressly/goose/v3"

	"github.com/RIKIKU/PagerArchive/internal/accounts"
	"github.com/RIKIKU/PagerArchive/internal/store"
)

// A159 (R77f, ADR 0055): a database at migration 00040, with users an admin
// had marked to see the switcher, upgrades: the mark's column goes, and the
// API's start beside RadioArchive adds those users to the Authentik group,
// once, only adding.
func TestA159OldMarksMoveIntoTheGroup(t *testing.T) {
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
	count := func(sql string) int {
		t.Helper()
		var n int
		if err := conn.QueryRow(ctx, sql).Scan(&n); err != nil {
			t.Fatalf("%s: %v", sql, err)
		}
		return n
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
	if _, err := provider.UpTo(ctx, 40); err != nil {
		t.Fatal(err)
	}
	// Made up, as everywhere in the tests: B marked and signed in, D marked
	// but never signed in, E not marked.
	exec(`INSERT INTO users (subject, email, email_key, sees_switcher) VALUES
	      ('sub-b', 'b@example.com', 'b@example.com', true),
	      (NULL, 'd@example.com', 'd@example.com', true),
	      ('sub-e', 'e@example.com', 'e@example.com', false)`)

	// The upgrade: what the API runs on start.
	st, err := store.Open(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if n := count(`SELECT count(*) FROM information_schema.columns WHERE table_schema = 'public' AND table_name = 'users' AND column_name = 'sees_switcher'`); n != 0 {
		t.Error("users.sees_switcher is still there")
	}
	waiting := func() int {
		t.Helper()
		return count(`SELECT count(*) FROM switcher_marks_to_move`)
	}
	if n := count(`SELECT count(*) FROM switcher_marks_to_move m JOIN users u ON u.id = m.user_id WHERE u.email IN ('b@example.com', 'd@example.com')`); n != 2 || waiting() != 2 {
		t.Fatalf("waiting to move: %d of B and D, %d in all; want 2 and 2", n, waiting())
	}

	var logs bytes.Buffer
	log := slog.New(slog.NewJSONHandler(&logs, nil))
	acc := &accounts.Service{Store: st, Now: time.Now, Log: log}
	// Standalone: nothing moves.
	acc.MoveSwitcherMarks(ctx, time.Millisecond, 3)
	if waiting() != 2 {
		t.Errorf("standalone: %d waiting, want 2", waiting())
	}

	// Beside RadioArchive, the group holding X, who isn't a PagerArchive
	// user. Authentik doesn't answer at first, then does.
	f := &fakeGroup{members: map[string]bool{"sub-x": true}, down: true}
	acc.Switcher = &accounts.SwitcherGroup{Name: accounts.DefaultSwitcherGroup, Dir: f, Log: log}
	acc.MoveSwitcherMarks(ctx, time.Millisecond, 3)
	if waiting() != 2 || f.has("sub-b") {
		t.Errorf("Authentik down: %d waiting, B in the group %v; want 2 and false", waiting(), f.has("sub-b"))
	}
	done := make(chan struct{})
	go func() { acc.MoveSwitcherMarks(ctx, 50*time.Millisecond, 40); close(done) }()
	time.Sleep(120 * time.Millisecond)
	f.mu.Lock()
	f.down = false
	f.mu.Unlock()
	<-done
	if !f.has("sub-b") || !f.has("sub-x") || f.has("sub-e") || f.removes != 0 {
		t.Errorf("moved: B %v, X kept %v, E added %v, %d removed; want true, true, false, 0", f.has("sub-b"), f.has("sub-x"), f.has("sub-e"), f.removes)
	}
	if n := count(`SELECT count(*) FROM switcher_marks_to_move m JOIN users u ON u.id = m.user_id WHERE u.email = 'd@example.com'`); n != 1 || waiting() != 1 {
		t.Errorf("after the move: D waiting %d, %d in all; want 1 and 1", n, waiting())
	}

	// D signs in; the next start moves them, and nothing is left to move.
	exec(`UPDATE users SET subject = 'sub-d' WHERE email = 'd@example.com'`)
	acc.MoveSwitcherMarks(ctx, time.Millisecond, 3)
	if !f.has("sub-d") || waiting() != 0 || f.removes != 0 {
		t.Errorf("the next start: D in the group %v, %d waiting, %d removed", f.has("sub-d"), waiting(), f.removes)
	}
	adds := f.adds
	acc.MoveSwitcherMarks(ctx, time.Millisecond, 3)
	if f.adds != adds {
		t.Error("a start with nothing waiting changed the group")
	}

	// Going down puts the column back with the marks still waiting (none).
	if _, err := provider.DownTo(ctx, 40); err != nil {
		t.Fatal(err)
	}
	if n := count(`SELECT count(*) FROM users WHERE sees_switcher`); n != 0 {
		t.Errorf("down: %d marked, want 0 (all moved)", n)
	}
	if _, err := provider.Up(ctx); err != nil {
		t.Fatal(err)
	}
}
