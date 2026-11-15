package acceptance

import (
	"context"
	"errors"
	"os"
	"strconv"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	pagerarchive "github.com/RIKIKU/PagerArchive"
	"github.com/RIKIKU/PagerArchive/internal/store"
)

// A102 (R84i): the API's own database role. Given an install from before it
// (tables made by the superuser, with rows in them) and a new one, when
// deploy/postgres/api-role.sql runs (by api-role.sh on the first, by the
// database's first-start script on the second), then the API, connecting as
// pagerarchive, starts and runs its migrations, owns every table, function
// and type of PagerArchive's, finds every row still there, isn't a superuser,
// can't connect to another database, can't run a program or read the
// server's files; running it again changes nothing.
func TestA102APIRole(t *testing.T) {
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
	// Closed after the clean-up below, which uses it.
	t.Cleanup(func() { admin.Close(ctx) })
	base, err := pgx.ParseConfig(url)
	if err != nil {
		t.Fatal(err)
	}
	// Databases of this test's own; the role is the real one's name, which no
	// other test uses. Afterwards every database is open to PUBLIC again, as
	// the other tests expect.
	const old, fresh, other = "pa_a102_old", "pa_a102_new", "pa_a102_other"
	const password = "a102-test-password"
	cleanup := func() {
		for _, db := range []string{old, fresh, other} {
			_, _ = admin.Exec(ctx, `DROP DATABASE IF EXISTS `+db+` WITH (FORCE)`)
		}
		_, _ = admin.Exec(ctx, `DROP OWNED BY pagerarchive`)
		_, _ = admin.Exec(ctx, `DROP ROLE IF EXISTS pagerarchive`)
		rows, err := admin.Query(ctx, `SELECT datname FROM pg_database WHERE datallowconn`)
		if err != nil {
			return
		}
		dbs, _ := pgx.CollectRows(rows, pgx.RowTo[string])
		for _, db := range dbs {
			_, _ = admin.Exec(ctx, `GRANT CONNECT, TEMPORARY ON DATABASE `+pgx.Identifier{db}.Sanitize()+` TO PUBLIC`)
		}
	}
	cleanup()
	t.Cleanup(cleanup)
	for _, db := range []string{old, fresh, other} {
		if _, err := admin.Exec(ctx, `CREATE DATABASE `+db); err != nil {
			t.Fatal(err)
		}
	}
	urlOf := func(db, user, pw string) string {
		u := "postgres://" + user
		if pw != "" {
			u += ":" + pw
		}
		return u + "@" + base.Host + ":" + strconv.Itoa(int(base.Port)) + "/" + db + "?sslmode=disable"
	}
	adminURL := func(db string) string { return urlOf(db, base.User, base.Password) }
	connect := func(u string) *pgx.Conn {
		t.Helper()
		c, err := pgx.Connect(ctx, u)
		if err != nil {
			t.Fatalf("connect: %v", err)
		}
		t.Cleanup(func() { c.Close(ctx) })
		return c
	}
	apply := func(db string) {
		t.Helper()
		c, err := pgx.Connect(ctx, adminURL(db))
		if err != nil {
			t.Fatal(err)
		}
		defer c.Close(ctx)
		if _, err := c.Exec(ctx, pagerarchive.APIRoleSQL); err != nil {
			t.Fatalf("api-role.sql in %s: %v", db, err)
		}
		if _, err := c.Exec(ctx, `ALTER ROLE pagerarchive PASSWORD '`+password+`'`); err != nil {
			t.Fatal(err)
		}
	}
	counts := func(c *pgx.Conn) map[string]int64 {
		t.Helper()
		rows, err := c.Query(ctx, `SELECT c.relname FROM pg_class c WHERE c.relnamespace = 'public'::regnamespace AND c.relkind = 'r' ORDER BY 1`)
		if err != nil {
			t.Fatal(err)
		}
		names, err := pgx.CollectRows(rows, pgx.RowTo[string])
		if err != nil {
			t.Fatal(err)
		}
		out := map[string]int64{}
		for _, n := range names {
			var k int64
			if err := c.QueryRow(ctx, `SELECT count(*) FROM `+pgx.Identifier{n}.Sanitize()).Scan(&k); err != nil {
				t.Fatalf("count %s: %v", n, err)
			}
			out[n] = k
		}
		return out
	}
	// What's in public that isn't the role's, other than an extension's.
	notOwned := func(c *pgx.Conn) []string {
		t.Helper()
		rows, err := c.Query(ctx, `
WITH ext AS (SELECT objid FROM pg_depend WHERE deptype = 'e'),
o AS (
  SELECT 'relation' AS kind, c.oid::regclass::text AS name, c.relowner AS owner,
         c.oid IN (SELECT objid FROM ext) OR (c.relkind = 'i' AND (SELECT indrelid FROM pg_index WHERE indexrelid = c.oid) IN (SELECT objid FROM ext)) AS theirs
  FROM pg_class c WHERE c.relnamespace = 'public'::regnamespace AND c.relkind IN ('r', 'p', 'v', 'm', 'S', 'i')
  UNION ALL
  SELECT 'function', p.oid::regprocedure::text, p.proowner, p.oid IN (SELECT objid FROM ext)
  FROM pg_proc p WHERE p.pronamespace = 'public'::regnamespace
  UNION ALL
  SELECT 'type', t.oid::regtype::text, t.typowner,
         t.oid IN (SELECT objid FROM ext) OR t.typrelid IN (SELECT objid FROM ext) OR t.typelem IN (SELECT objid FROM ext)
         OR (SELECT e.typrelid FROM pg_type e WHERE e.oid = t.typelem) IN (SELECT objid FROM ext)
  FROM pg_type t WHERE t.typnamespace = 'public'::regnamespace
)
SELECT kind || ' ' || name FROM o WHERE owner IS DISTINCT FROM to_regrole('pagerarchive') AND NOT theirs ORDER BY 1`)
		if err != nil {
			t.Fatal(err)
		}
		out, err := pgx.CollectRows(rows, pgx.RowTo[string])
		if err != nil {
			t.Fatal(err)
		}
		return out
	}
	limited := func(db string) {
		t.Helper()
		app := connect(urlOf(db, "pagerarchive", password))
		var super, createRole, createDB bool
		if err := app.QueryRow(ctx, `SELECT rolsuper, rolcreaterole, rolcreatedb FROM pg_roles WHERE rolname = current_user`).Scan(&super, &createRole, &createDB); err != nil {
			t.Fatal(err)
		}
		if super || createRole || createDB {
			t.Errorf("%s: the role is superuser %v, createrole %v, createdb %v", db, super, createRole, createDB)
		}
		var member int
		if err := app.QueryRow(ctx, `SELECT count(*) FROM pg_auth_members WHERE member = 'pagerarchive'::regrole`).Scan(&member); err != nil || member != 0 {
			t.Errorf("%s: the role is in %d other roles (%v)", db, member, err)
		}
		denied := func(what, sql string) {
			t.Helper()
			_, err := app.Exec(ctx, sql)
			var pgErr *pgconn.PgError
			if !errors.As(err, &pgErr) || pgErr.Code != "42501" {
				t.Errorf("%s: %s wasn't refused for want of rights: %v", db, what, err)
			}
		}
		denied("COPY TO PROGRAM", `COPY (SELECT 1) TO PROGRAM 'true'`)
		denied("COPY FROM a server file", `CREATE TEMP TABLE f (l text); COPY f FROM '/etc/passwd'`)
		denied("reading a server file", `SELECT pg_read_file('/etc/passwd')`)
		denied("making a role", `CREATE ROLE pa_a102_made`)
		denied("making a database", `CREATE DATABASE pa_a102_made`)
		denied("an untrusted language", `CREATE FUNCTION pa_a102_c() RETURNS int LANGUAGE c AS 'x', 'y'`)
		for _, db := range []string{other, "template1"} {
			if c, err := pgx.Connect(ctx, urlOf(db, "pagerarchive", password)); err == nil {
				c.Close(ctx)
				t.Errorf("the role connected to the database %s", db)
			}
		}
		// What its migrations will need: a new table.
		if _, err := app.Exec(ctx, `CREATE TABLE pa_a102_new_table (n int); DROP TABLE pa_a102_new_table`); err != nil {
			t.Errorf("%s: the role can't create a table: %v", db, err)
		}
		if n := notOwned(app); len(n) > 0 {
			t.Errorf("%s: not the role's: %v", db, n)
		}
		st, err := store.Open(ctx, urlOf(db, "pagerarchive", password))
		if err != nil {
			t.Fatalf("%s: the API can't start as the role: %v", db, err)
		}
		defer st.Close()
		if su, err := st.Superuser(ctx); err != nil || su {
			t.Errorf("%s: Superuser() = %v, %v", db, su, err)
		}
	}

	// An install from before: the superuser ran the migrations and the API
	// stored things.
	st, err := store.Open(ctx, adminURL(old))
	if err != nil {
		t.Fatal(err)
	}
	if su, err := st.Superuser(ctx); err != nil || !su {
		t.Errorf("as the superuser, Superuser() = %v, %v", su, err)
	}
	for _, id := range []string{"a102-one", "a102-two"} {
		if _, err := st.AddClient(ctx, id, ""); err != nil {
			t.Fatal(err)
		}
	}
	st.Close()
	oldAdmin := connect(adminURL(old))
	before := counts(oldAdmin)
	if len(notOwned(oldAdmin)) == 0 {
		t.Fatal("the old install's objects are already the role's")
	}
	apply(old)
	limited(old)
	apply(old) // again: harmless
	limited(old)
	after := counts(oldAdmin)
	if len(after) != len(before) {
		t.Errorf("tables: %d before, %d after", len(before), len(after))
	}
	for n, k := range before {
		if after[n] != k {
			t.Errorf("%s: %d rows before, %d after", n, k, after[n])
		}
	}
	if before["clients"] != 2 {
		t.Errorf("clients: %d, want 2", before["clients"])
	}

	// A new install: the first-start script runs it before the API's first
	// start, whose migrations then run as the role.
	apply(fresh)
	limited(fresh)
}
