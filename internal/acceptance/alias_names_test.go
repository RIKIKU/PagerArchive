package acceptance

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
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

// A19 (R27b, owner 2026-11-15): alias names are unique ignoring letter case.
// Adding or renaming an alias to a name another alias has, in any case, is
// refused, naming that alias: by an admin through the API (the browser and
// the PowerShell module use it), in a user's request when it's submitted and
// when it's approved, and in the import, where such rows are conflicts and
// nothing is imported. An alias may change its own name's case.
func TestA19AliasNamesUnique(t *testing.T) {
	e := setup(t)
	fire := e.templateID("Example Fire")
	add := func(pattern, name string) (int, server.Problem) {
		t.Helper()
		var c server.Created
		return e.admin.call(http.MethodPost, "/api/v1/admin/aliases", catalog.AliasInput{Pattern: pattern, Name: e.private(name), TemplateID: &fire}, &c)
	}
	rename := func(id int64, name string) (int, server.Problem) {
		t.Helper()
		a := e.adminAlias(id)
		return e.admin.call(http.MethodPut, fmt.Sprintf("/api/v1/admin/aliases/%d", id), catalog.AliasInput{Pattern: a.Pattern, Name: e.private(name), TemplateID: a.TemplateID, Ignored: a.Ignored}, nil)
	}
	taken := func(what string, code int, p server.Problem, want ...string) {
		t.Helper()
		if code != http.StatusConflict || p.Code != "duplicate_name" {
			t.Errorf("%s: %d %+v, want 409 duplicate_name", what, code, p)
			return
		}
		for _, w := range want {
			if !strings.Contains(p.Error, w) {
				t.Errorf("%s: %q doesn't say %q", what, p.Error, w)
			}
		}
		t.Logf("%s: %s", what, p.Error)
	}

	if code, p := add("0300001", "Testville Brigade"); code != http.StatusCreated {
		t.Fatalf("adding Testville Brigade: %d %+v", code, p)
	}
	brigade := e.aliasID("Testville Brigade")
	before := e.aliasCount()

	// An admin, through the API.
	code, p := add("0300002", "testville brigade")
	taken(`adding "testville brigade"`, code, p, `"Testville Brigade" (0300001)`, "only in letter case")
	code, p = add("0300002", "Testville Brigade")
	taken(`adding "Testville Brigade" again`, code, p, `There's already an alias called "Testville Brigade" (0300001). Alias names are unique`)
	station1 := e.aliasID("Station 1")
	code, p = rename(station1, "TESTVILLE BRIGADE")
	taken(`renaming Station 1 "TESTVILLE BRIGADE"`, code, p, `"Testville Brigade" (0300001)`)
	if n := e.aliasName("0100001"); n != "Station 1" {
		t.Errorf("Station 1 after the refused rename: %q", n)
	}

	// A user's requests, when they're submitted.
	code, p = e.user.call(http.MethodPost, "/api/v1/alias-requests", catalog.RequestInput{Kind: catalog.KindAdd, Pattern: "0300002", Name: ptr(e.private("TESTVILLE BRIGADE")), TemplateID: &fire}, nil)
	taken("requesting a new alias \"TESTVILLE BRIGADE\"", code, p, `"Testville Brigade" (0300001)`)
	code, p = e.user.call(http.MethodPost, "/api/v1/alias-requests", catalog.RequestInput{Kind: catalog.KindChange, AliasID: ptr(e.aliasID("Station 2")), Name: ptr("testville brigade")}, nil)
	taken("requesting Station 2 be renamed \"testville brigade\"", code, p, `"Testville Brigade" (0300001)`)

	// ... and when they're approved: the name was taken meanwhile.
	tanker := e.request(e.user, catalog.RequestInput{Kind: catalog.KindAdd, Pattern: "0300005", Name: ptr(e.private("Testville Tanker")), TemplateID: &fire})
	three := e.request(e.user, catalog.RequestInput{Kind: catalog.KindChange, AliasID: ptr(e.aliasID("Station 3")), Name: ptr(e.private("Station Three"))})
	if code, p := add("0300006", "Testville Tanker"); code != http.StatusCreated {
		t.Fatalf("adding Testville Tanker: %d %+v", code, p)
	}
	if code, p := add("0300007", "STATION THREE"); code != http.StatusCreated {
		t.Fatalf("adding STATION THREE: %d %+v", code, p)
	}
	for _, r := range []struct {
		id   int64
		want string
	}{{tanker, `"Testville Tanker" (0300006)`}, {three, `"STATION THREE" (0300007)`}} {
		code, p := e.admin.call(http.MethodPost, fmt.Sprintf("/api/v1/admin/alias-requests/%d/approve", r.id), nil, nil)
		taken(fmt.Sprintf("approving request %d", r.id), code, p, "Can't approve", r.want, "Reject this request")
		if got := e.myRequest(e.user, r.id); got.Status != catalog.StatusWaiting {
			t.Errorf("request %d after the refused approval: %s", r.id, got.Status)
		}
	}
	if n := e.aliasName("0100003"); n != "Station 3" {
		t.Errorf("Station 3 after the refused approval: %q", n)
	}
	before = e.aliasCount()

	// The import: rows giving a name another alias has, or another row
	// gives, are conflicts, and nothing is imported.
	csvText := "address,alias,agency\n0300002,Pump A,Example Fire\n0300003,pump a,Example Fire\n0300004,TESTVILLE BRIGADE,Example Fire\n0300008,Hilltop,Example Fire\n"
	rep, err := e.importCSV(csvText, catalog.ImportOptions{})
	if !catalog.IsConflict(err) {
		t.Fatalf("an import with names that are taken: %v", err)
	}
	if got := resultList(rep); got != "conflict,conflict,conflict," {
		t.Errorf("results: %s", got)
	}
	wantProblems := []string{
		`Row 2 (0300003) gives the same name. Alias names are unique ignoring case.`,
		`Row 1 (0300002) gives the same name. Alias names are unique ignoring case.`,
		`There's already an alias called "Testville Brigade" (0300001). Alias names are unique ignoring case.`,
	}
	for i, want := range wantProblems {
		if rep.Rows[i].Problem != want {
			t.Errorf("row %d's problem: %q, want %q", i+1, rep.Rows[i].Problem, want)
		}
	}
	if len(rep.Conflicts) != 2 || rep.Conflicts[0].Kind != catalog.ConflictName || rep.Conflicts[0].Name != "Pump A" || fmt.Sprint(rep.Conflicts[0].Rows) != "[1 2]" ||
		len(rep.Conflicts[1].Aliases) != 1 || rep.Conflicts[1].Aliases[0].Pattern != "0300001" || fmt.Sprint(rep.Conflicts[1].Rows) != "[3]" {
		t.Errorf("conflicts: %+v", rep.Conflicts)
	}
	if n := e.aliasCount(); n != before {
		t.Errorf("the stopped import changed the aliases: %d, then %d", before, n)
	}
	t.Logf("A19: the import's conflicts: %+v", rep.Conflicts)
	// Both kinds at once: rows that differ for one capcode, and a name.
	rep, err = e.importCSV("address,alias\n0300009,One\n300009,Two\n0300010,Testville Brigade\n", catalog.ImportOptions{})
	if !catalog.IsConflict(err) || len(rep.Conflicts) != 2 || rep.Conflicts[0].Kind != catalog.ConflictCapcode || rep.Conflicts[1].Kind != catalog.ConflictName {
		t.Errorf("both kinds of conflict: %v %+v", err, rep.Conflicts)
	}
	// Left out, the rest is imported.
	rep, err = e.importCSV("address,alias,agency\n0300002,Pump A,Example Fire\n0300008,Hilltop,Example Fire\n", catalog.ImportOptions{})
	if err != nil || resultList(rep) != "added,added" {
		t.Errorf("the import without the conflicting rows: %v %s", err, resultList(rep))
	}

	// With the update option, renaming an alias to another's name is a
	// conflict; without it, the difference is only reported. An alias's own
	// row may change its name's case.
	if rep, err := e.importCSV("address,alias,agency\n0100001,Station 2,Example Fire\n", catalog.ImportOptions{}); err != nil || resultList(rep) != "different" {
		t.Errorf("a different name without the update option: %v %s", err, resultList(rep))
	}
	rep, err = e.importCSV("address,alias,agency\n0100001,station 2,Example Fire\n", catalog.ImportOptions{Update: true})
	if !catalog.IsConflict(err) || !strings.Contains(rep.Rows[0].Problem, `"Station 2" (0100002)`) {
		t.Errorf("renaming Station 1 \"station 2\" with the update option: %v %+v", err, rep.Rows)
	}
	if rep, err := e.importCSV("address,alias,agency\n0100001,STATION 1,Example Fire\n", catalog.ImportOptions{Update: true}); err != nil || resultList(rep) != "updated" {
		t.Errorf("Station 1 changing its own name's case in the import: %v %s", err, resultList(rep))
	}

	// An alias may change its own name's case.
	if code, p := rename(brigade, "TESTVILLE BRIGADE"); code != http.StatusNoContent {
		t.Errorf("Testville Brigade changing its own name's case: %d %+v", code, p)
	}
	if n := e.aliasName("0300001"); n != "TESTVILLE BRIGADE" {
		t.Errorf("after the rename to its own name in capitals: %q", n)
	}
	// The database refuses it too.
	if _, err := e.st.Pool.Exec(e.ctx, `INSERT INTO aliases (pattern, name) VALUES ('0300099', 'testville brigade')`); err == nil {
		t.Error("the database took a second alias called \"testville brigade\": no unique index ignoring case")
	}
	// No names shared from before the rule here.
	var shared []server.SharedName
	if code, p := e.admin.call(http.MethodGet, "/api/v1/admin/aliases/shared-names", nil, &shared); code != http.StatusOK || len(shared) != 0 {
		t.Errorf("shared names: %d %+v %+v", code, p, shared)
	}
}

// A100 through the API: names shared from before the rule (put straight
// into the database, as an upgrade leaves them) are listed for
// Administration, and each alias's page names the others.
func TestA100SharedNamesInAdministration(t *testing.T) {
	e := setup(t)
	if _, err := e.st.Pool.Exec(e.ctx, `INSERT INTO aliases (pattern, name, name_duplicate) VALUES ('0300011', 'Station 9', false), ('0300012', 'Station 9', true), ('0300013', 'station 9', true)`); err != nil {
		t.Fatal(err)
	}
	var shared []server.SharedName
	if code, p := e.admin.call(http.MethodGet, "/api/v1/admin/aliases/shared-names", nil, &shared); code != http.StatusOK {
		t.Fatalf("shared names: %d %+v", code, p)
	}
	if len(shared) != 1 || len(shared[0].Aliases) != 3 || shared[0].Aliases[0].Pattern != "0300011" || shared[0].Aliases[2].Name != "station 9" {
		t.Errorf("shared names: %+v", shared)
	}
	if code, _ := e.user.call(http.MethodGet, "/api/v1/admin/aliases/shared-names", nil, nil); code != http.StatusForbidden {
		t.Errorf("a user asks for the shared names: %d", code)
	}
	var id int64
	if err := e.st.Pool.QueryRow(e.ctx, `SELECT id FROM aliases WHERE pattern = '0300013'`).Scan(&id); err != nil {
		t.Fatal(err)
	}
	a := e.adminAlias(id)
	if len(a.SameNameAs) != 2 || a.SameNameAs[0].Pattern != "0300011" || a.SameNameAs[1].Pattern != "0300012" {
		t.Errorf("0300013's page: same name as %+v", a.SameNameAs)
	}
	if a := e.adminAlias(e.aliasID("Station 1")); len(a.SameNameAs) != 0 {
		t.Errorf("Station 1's page: same name as %+v", a.SameNameAs)
	}
	// An export of them imports unchanged: no row changes a name.
	if rep, err := e.importCSV("address,alias\n0300011,Station 9\n0300012,Station 9\n0300013,station 9\n", catalog.ImportOptions{Update: true}); err != nil || resultList(rep) != "unchanged,unchanged,unchanged" {
		t.Errorf("importing the shared names as they are: %v %s", err, resultList(rep))
	}
	// Edited with its name unchanged: saved. Renamed to the shared name in
	// other case: refused.
	if code, p := e.admin.call(http.MethodPut, fmt.Sprintf("/api/v1/admin/aliases/%d", id), catalog.AliasInput{Pattern: "0300013", Name: "station 9", Ignored: true}, nil); code != http.StatusNoContent {
		t.Errorf("editing 0300013 with its name unchanged: %d %+v", code, p)
	}
	if code, p := e.admin.call(http.MethodPut, fmt.Sprintf("/api/v1/admin/aliases/%d", id), catalog.AliasInput{Pattern: "0300013", Name: "STATION 9"}, nil); code != http.StatusConflict || p.Code != "duplicate_name" {
		t.Errorf("renaming 0300013 \"STATION 9\": %d %+v", code, p)
	}
}

// A100 (R27b): an install with aliases sharing names, from before the rule,
// upgrades: nothing is lost or renamed, the oldest of each group is under
// the unique index and the others are marked, they're listed, and new names
// can't match them. Renaming or deleting sorts them out. The migration goes
// down and up.
func TestA100AliasNamesSharedUpgrade(t *testing.T) {
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
	// 00022 is the last migration before this rule's (00024; 00023 is M24's).
	if _, err := provider.UpTo(ctx, 22); err != nil {
		t.Fatal(err)
	}
	// Made-up names, in the order they were added.
	exec(`INSERT INTO templates (name, agency) VALUES ('Example Fire', 'Made-up agency')`)
	for _, a := range [][2]string{{"0300011", "Station 9"}, {"0300012", "Station 9"}, {"0300013", "station 9"}, {"0300014", "Hilltop"}, {"0300015", "Tanker"}, {"0300016", "TANKER"}} {
		exec(`INSERT INTO aliases (pattern, name, template_id) SELECT $1, $2, id FROM templates`, a[0], a[1])
	}

	st, err := store.Open(ctx, url)
	if err != nil {
		t.Fatalf("the upgrade failed: %v", err)
	}
	defer st.Close()
	strs := func(q string) string {
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
	if got := strs(`SELECT pattern || '=' || name FROM aliases ORDER BY id`); got != "0300011=Station 9,0300012=Station 9,0300013=station 9,0300014=Hilltop,0300015=Tanker,0300016=TANKER" {
		t.Errorf("aliases after the upgrade: %s", got)
	}
	marked := func() string { t.Helper(); return strs(`SELECT pattern FROM aliases WHERE name_duplicate ORDER BY id`) }
	if got := marked(); got != "0300012,0300013,0300016" {
		t.Errorf("marked: %s, want 0300012,0300013,0300016 (the oldest of each group stays under the index)", got)
	}

	cat := &catalog.Catalog{Store: st}
	listed := func() string {
		t.Helper()
		groups, err := cat.NameDuplicates(ctx)
		if err != nil {
			t.Fatal(err)
		}
		var out []string
		for _, g := range groups {
			var each []string
			for _, a := range g {
				each = append(each, a.Pattern)
			}
			out = append(out, strings.Join(each, "+"))
		}
		return strings.Join(out, " ")
	}
	if got := listed(); got != "0300011+0300012+0300013 0300015+0300016" {
		t.Errorf("listed for Administration and the log: %s", got)
	}

	var tpl int64
	if err := conn.QueryRow(ctx, `SELECT id FROM templates`).Scan(&tpl); err != nil {
		t.Fatal(err)
	}
	aliasID := func(pattern string) int64 {
		t.Helper()
		var id int64
		if err := conn.QueryRow(ctx, `SELECT id FROM aliases WHERE pattern = $1`, pattern).Scan(&id); err != nil {
			t.Fatal(err)
		}
		return id
	}
	edit := func(pattern, name string, ignored bool) error {
		return cat.EditAlias(ctx, aliasID(pattern), catalog.AliasInput{Pattern: pattern, Name: name, TemplateID: &tpl, Ignored: ignored}, byAdmin)
	}
	refused := func(err error, what string) {
		t.Helper()
		var r *catalog.Refusal
		if !errors.As(err, &r) || r.Code != "duplicate_name" {
			t.Errorf("%s: %v, want duplicate_name", what, err)
		}
	}
	// New names can't match them, even through the index alone.
	_, err = cat.CreateAlias(ctx, catalog.AliasInput{Pattern: "0300017", Name: "STATION 9", TemplateID: &tpl}, byAdmin)
	refused(err, `adding "STATION 9"`)
	if _, err := conn.Exec(ctx, `INSERT INTO aliases (pattern, name) VALUES ('0300018', 'sTATION 9')`); err == nil {
		t.Error("the database took \"sTATION 9\" beside \"Station 9\": no unique index ignoring case")
	}
	// A marked one keeps working: its other settings change, its name kept.
	if err := edit("0300013", "station 9", true); err != nil {
		t.Errorf("editing 0300013 with its own name: %v", err)
	}
	refused(edit("0300013", "Station 9", false), `renaming 0300013 "Station 9"`)
	refused(edit("0300013", "STATION 9", false), `renaming 0300013 "STATION 9"`)
	// Renamed, it's no longer marked or listed.
	if err := edit("0300013", "Station 9 North", false); err != nil {
		t.Errorf("renaming 0300013 \"Station 9 North\": %v", err)
	}
	if got := listed(); got != "0300011+0300012 0300015+0300016" {
		t.Errorf("listed after the rename: %s", got)
	}
	if got := marked(); got != "0300012,0300016" {
		t.Errorf("marked after the rename: %s", got)
	}
	// The one under the index deleted: the one left takes its place, so the
	// index still holds the name, and none of that group is listed.
	if _, err := cat.RemoveAlias(ctx, aliasID("0300011"), byAdmin); err != nil {
		t.Fatalf("deleting 0300011: %v", err)
	}
	if got := marked(); got != "0300016" {
		t.Errorf("marked after the delete: %s, want 0300016", got)
	}
	if got := listed(); got != "0300015+0300016" {
		t.Errorf("listed after the delete: %s", got)
	}
	if _, err := conn.Exec(ctx, `INSERT INTO aliases (pattern, name) VALUES ('0300018', 'STATION 9')`); err == nil {
		t.Error("the database took \"STATION 9\" beside the remaining \"Station 9\" after the delete")
	}

	// Down to 00022 and up again.
	if _, err := provider.DownTo(ctx, 22); err != nil {
		t.Fatalf("down: %v", err)
	}
	if _, err := provider.Up(ctx); err != nil {
		t.Fatalf("up again: %v", err)
	}
	if got := marked(); got != "0300016" {
		t.Errorf("marked after down and up: %s", got)
	}
	t.Log("00022 with Station 9 twice, station 9, Tanker and TANKER: upgraded with nothing renamed, the newer of each marked, and new names in any case refused")
}
