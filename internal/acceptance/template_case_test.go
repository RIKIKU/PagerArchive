package acceptance

import (
	"context"
	"database/sql"
	"errors"
	"net/http"
	"os"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/pressly/goose/v3"

	"github.com/RIKIKU/PagerArchive/internal/catalog"
	"github.com/RIKIKU/PagerArchive/internal/server"
	"github.com/RIKIKU/PagerArchive/internal/store"
)

// R64 (owner, 2026-11-14): a template name that differs from another's only
// in letter case is refused, when adding and renaming, and by the import,
// naming the template that has it. A template's own name in other case is a
// rename it may make. Aliases are unchanged: they may share names (A19).
func TestTemplateNamesUniqueIgnoringCase(t *testing.T) {
	e := setup(t)
	create := func(name string) (int, server.Problem) {
		t.Helper()
		var c server.Created
		return e.admin.call(http.MethodPost, "/api/v1/admin/templates", map[string]any{"name": name, "agency": "Case Agency"}, &c)
	}
	rename := func(id int64, name string) (int, server.Problem) {
		t.Helper()
		return e.admin.call(http.MethodPatch, "/api/v1/admin/templates/"+itoa(id), map[string]any{"name": name}, nil)
	}

	code, p := create("example fire")
	if code != http.StatusConflict || p.Code != "template_exists" || !strings.Contains(p.Error, `"Example Fire"`) || !strings.Contains(p.Error, "only in letter case") {
		t.Errorf("adding \"example fire\" beside \"Example Fire\": %d %+v", code, p)
	}
	if code, p := create("Example Fire"); code != http.StatusConflict || p.Error != `There's already a template called "Example Fire".` {
		t.Errorf("adding the same name: %d %+v", code, p)
	}
	if code, p := create("Example Rescue"); code != http.StatusCreated {
		t.Fatalf("adding another name: %d %+v", code, p)
	}
	rescue := e.adminTemplates()["Example Rescue"].ID
	if code, p := rename(rescue, "EXAMPLE FIRE"); code != http.StatusConflict || p.Code != "template_exists" || !strings.Contains(p.Error, `"Example Fire"`) {
		t.Errorf("renaming to \"EXAMPLE FIRE\": %d %+v", code, p)
	}
	if code, p := rename(rescue, "example rescue"); code != http.StatusNoContent && code != http.StatusOK {
		t.Errorf("a template changing its own name's case: %d %+v", code, p)
	}
	if _, ok := e.adminTemplates()["example rescue"]; !ok {
		t.Error("the rename to its own name in lower case didn't happen")
	}

	// The import's agency names a template: one in other case is refused,
	// and nothing is imported.
	before := e.aliasCount()
	_, err := e.importCSV("address,alias,agency\n0700001,Case Test,EXAMPLE FIRE\n", catalog.ImportOptions{})
	var refusal *catalog.Refusal
	if !errors.As(err, &refusal) || refusal.Code != "template_exists" || !strings.Contains(refusal.Msg, `"Example Fire"`) {
		t.Errorf("an import naming \"EXAMPLE FIRE\": %v", err)
	}
	if n := e.aliasCount(); n != before {
		t.Errorf("the refused import changed the aliases: %d, then %d", before, n)
	}
	if _, ok := e.adminTemplates()["EXAMPLE FIRE"]; ok {
		t.Error("the refused import made a template")
	}

	// A19: aliases may still share names, in any case.
	fire := e.adminTemplates()["Example Fire"].ID
	for _, pattern := range []string{"0700002", "0700003"} {
		name := map[string]string{"0700002": "Brigade", "0700003": "BRIGADE"}[pattern]
		if code, p := e.admin.call(http.MethodPost, "/api/v1/admin/aliases", map[string]any{"pattern": pattern, "name": name, "template_id": fire}, nil); code != http.StatusCreated {
			t.Errorf("alias %s %q: %d %+v", pattern, name, code, p)
		}
	}
}

// R64: an install with template names that differ only in case, from
// before the rule (the owner's EMV and emv), upgrades: nothing is lost or
// renamed, the oldest of each group is under the unique index and the
// others are marked, Administration lists them, and new names can't match
// them. Renaming or deleting sorts them out. The migration goes down and up.
func TestTemplateCaseDuplicatesUpgrade(t *testing.T) {
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
	if _, err := provider.UpTo(ctx, 21); err != nil {
		t.Fatal(err)
	}
	// Made-up names, in the order they were added.
	for _, name := range []string{"EMV", "Example Fire", "emv", "Emv", "Example Water", "example water"} {
		exec(`INSERT INTO templates (name, agency) VALUES ($1, 'Made-up agency')`, name)
	}
	exec(`INSERT INTO aliases (pattern, name, template_id) SELECT '080000' || id, 'Alias ' || id, id FROM templates`)

	st, err := store.Open(ctx, url)
	if err != nil {
		t.Fatalf("the upgrade failed: %v", err)
	}
	defer st.Close()
	names := func(q string) string {
		t.Helper()
		rows, err := conn.Query(ctx, q)
		if err != nil {
			t.Fatal(err)
		}
		out, err := pgx.CollectRows(rows, pgx.RowTo[string])
		if err != nil {
			t.Fatal(err)
		}
		return strings.Join(out, ",")
	}
	if got := names(`SELECT name FROM templates ORDER BY id`); got != "EMV,Example Fire,emv,Emv,Example Water,example water" {
		t.Errorf("templates after the upgrade: %s", got)
	}
	if got := names(`SELECT name FROM templates WHERE case_duplicate ORDER BY id`); got != "emv,Emv,example water" {
		t.Errorf("marked: %s, want emv,Emv,example water (the oldest of each group stays under the index)", got)
	}
	var aliases int
	if err := conn.QueryRow(ctx, `SELECT count(*) FROM aliases`).Scan(&aliases); err != nil || aliases != 6 {
		t.Errorf("aliases after the upgrade: %d %v", aliases, err)
	}

	cat := &catalog.Catalog{Store: st}
	dups, err := cat.CaseDuplicates(ctx)
	if err != nil {
		t.Fatal(err)
	}
	listed := []string{}
	for _, d := range dups {
		listed = append(listed, d.Name+"="+strings.Join(d.SameNameAs, "+"))
	}
	if got := strings.Join(listed, " "); got != "EMV=emv+Emv emv=EMV+Emv Emv=EMV+emv Example Water=example water example water=Example Water" {
		t.Errorf("listed for Administration and the log: %s", got)
	}

	id := func(name string) int64 {
		t.Helper()
		var id int64
		if err := conn.QueryRow(ctx, `SELECT id FROM templates WHERE name = $1`, name).Scan(&id); err != nil {
			t.Fatal(err)
		}
		return id
	}
	refused := func(err error, what string) {
		t.Helper()
		var r *catalog.Refusal
		if !errors.As(err, &r) || r.Code != "template_exists" {
			t.Errorf("%s: %v, want template_exists", what, err)
		}
	}
	// New names can't match them, even through the index alone.
	_, err = cat.CreateTemplate(ctx, catalog.TemplateInput{Name: "eMV", Agency: "x"}, nil)
	refused(err, `adding "eMV"`)
	if _, err := conn.Exec(ctx, `INSERT INTO templates (name, agency) VALUES ('emV', 'x')`); err == nil {
		t.Error("the database took \"emV\" beside \"EMV\": no unique index ignoring case")
	}
	// A marked one keeps working: its other settings change, its name kept.
	agency := "Changed agency"
	if err := cat.EditTemplate(ctx, id("emv"), catalog.TemplateChange{Name: ptr("emv"), Agency: &agency}, nil); err != nil {
		t.Errorf("editing \"emv\" with its own name: %v", err)
	}
	refused(cat.EditTemplate(ctx, id("Emv"), catalog.TemplateChange{Name: ptr("eMv")}, nil), `renaming "Emv" to "eMv"`)
	// Renamed, it's no longer marked.
	if err := cat.EditTemplate(ctx, id("Emv"), catalog.TemplateChange{Name: ptr("EMV North")}, nil); err != nil {
		t.Errorf("renaming \"Emv\" to \"EMV North\": %v", err)
	}
	if dups, err := cat.CaseDuplicates(ctx); err != nil || len(dups) != 4 || dups[0].Name != "EMV" || strings.Join(dups[0].SameNameAs, "+") != "emv" {
		t.Errorf("listed after the rename: %+v %v, want EMV and emv, and the two Example Waters", dups, err)
	}
	// The one under the index deleted (after its alias): the oldest left
	// takes its place, so the index still holds the name.
	exec(`DELETE FROM aliases WHERE template_id = $1`, id("Example Water"))
	if err := cat.RemoveTemplate(ctx, id("Example Water")); err != nil {
		t.Fatalf("deleting \"Example Water\": %v", err)
	}
	if got := names(`SELECT name FROM templates WHERE case_duplicate ORDER BY id`); got != "emv" {
		t.Errorf("marked after the rename and the delete: %s, want emv", got)
	}
	if _, err := conn.Exec(ctx, `INSERT INTO templates (name, agency) VALUES ('EXAMPLE WATER', 'x')`); err == nil {
		t.Error("the database took \"EXAMPLE WATER\" beside \"example water\" after the delete")
	}

	// Down to 00021 and up again.
	if _, err := provider.DownTo(ctx, 21); err != nil {
		t.Fatalf("down: %v", err)
	}
	if _, err := provider.Up(ctx); err != nil {
		t.Fatalf("up again: %v", err)
	}
	if got := names(`SELECT name FROM templates WHERE case_duplicate ORDER BY id`); got != "emv" {
		t.Errorf("marked after down and up: %s", got)
	}
	t.Log("00021 with EMV, emv and Emv, and Example Water and example water: upgraded with nothing renamed, emv, Emv and example water marked, and new names in other case refused")
}
