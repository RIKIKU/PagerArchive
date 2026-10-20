package acceptance

import (
	"errors"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/RIKIKU/PagerArchive/internal/catalog"
	"github.com/RIKIKU/PagerArchive/internal/server"
	"github.com/RIKIKU/PagerArchive/internal/store/db"
)

// A made-up export in the shape the owner's tool writes, including a
// plugin-settings column that must never be read or stored.
const exportCSV = `"id","address","alias","agency","icon","color","pluginconf","ignore"
"1","0300001","Testville Brigade","Example Fire","fire","#b3261e","{""apikey"":""S3CRET-PLUGIN-KEY""}","0"
"2","0300002","Testville Tanker","Example Fire","truck","#b3261e","{}","0"
"3","0300003","Weather office","WX","cloud","#1565c0","null","0"
"4","0300004","Unknown agency pager","null","null","null","null","0"
"5","05000%","Network test pages","WX","cloud","#1565c0","","1"
"6","12345678","Too long","WX","","","","0"
"8","0300005","","WX","","","","0"
`

var exportMapping = map[string]string{"Example Fire": "example-fire"}

func (e *env) aliasCount() int {
	e.t.Helper()
	rows, err := e.st.ListAliases(e.ctx)
	if err != nil {
		e.t.Fatal(err)
	}
	return len(rows)
}

func resultList(rep catalog.ImportReport) string {
	var out []string
	for _, r := range rep.Rows {
		out = append(out, r.Result)
	}
	return strings.Join(out, ",")
}

// R30: the import (owner decisions, 2026-10-10 and 2026-10-11; from M8
// rows from the PowerShell pipeline, R63).
func TestAliasImport(t *testing.T) {
	e := setup(t)
	e.mustPost("a", "0300001", "@@EX0000031 HSF1 SHED FIRE @ 3 FICTION ST, MADEUPTON #TV1", e.now)
	e.mustPost("a", "0300004", "QDhello unknown agency", e.now)
	e.mustPost("a", "0500001", "QDnetwork test", e.now)

	// Dry run: reports, changes nothing.
	dry, err := e.importCSV(exportCSV, catalog.ImportOptions{AgencyProfiles: exportMapping, DryRun: true})
	if err != nil {
		t.Fatal(err)
	}
	if n := e.aliasCount(); n != 4 || dry.Applied || resultList(dry) != "added,added,added,added,added,invalid,invalid" {
		t.Fatalf("dry run: %d aliases in the database (want the fixture's 4), results %s, applied %v", n, resultList(dry), dry.Applied)
	}

	res, err := e.importCSV(exportCSV, catalog.ImportOptions{AgencyProfiles: exportMapping})
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("import: %s, templates created %v", resultList(res), res.TemplatesCreated)
	if !res.Applied || res.Plan != dry.Plan || resultList(res) != resultList(dry) {
		t.Errorf("import: %+v (dry run's plan %s)", res, dry.Plan)
	}
	if strings.Join(res.TemplatesCreated, ",") != "WX" {
		t.Errorf("templates created: %v", res.TemplatesCreated)
	}
	if p := res.Rows[5]; p.Index != 6 || !strings.Contains(p.Problem, "isn't a capcode or pattern") {
		t.Errorf("row 6: %+v", p)
	}
	if p := res.Rows[6]; p.Index != 7 || p.Problem != "The row has no alias name." {
		t.Errorf("row 7: %+v", p)
	}

	// Plugin settings never reach the database or the log.
	for _, secret := range []string{"S3CRET-PLUGIN-KEY", "apikey"} {
		if hits := e.rowsContaining(secret); len(hits) > 0 {
			t.Errorf("plugin settings stored: %v", hits)
		}
	}
	if strings.Contains(e.logs.String(), "S3CRET") {
		t.Error("plugin settings logged")
	}

	// Re-parse (R23a): the mapped agency parses, the unmapped one is text only,
	// the "null" agency has a name but no template, the ignored one is hidden.
	if _, err := e.worker.RunOnce(e.ctx); err != nil {
		t.Fatal(err)
	}
	if ms := e.list("capcode=0300001"); len(ms) != 1 || s(ms[0].Alias) != "Testville Brigade" || s(ms[0].Format) != "emergency" || s(ms[0].Group) != "TV1" {
		t.Errorf("mapped agency: %+v", ms)
	}
	if ms := e.list("capcode=0300004"); len(ms) != 1 || s(ms[0].Alias) != "Unknown agency pager" || ms[0].Agency != nil || ms[0].Format != nil {
		t.Errorf("null agency: %+v", ms)
	}
	if ms := e.list("capcode=0500001"); len(ms) != 0 {
		t.Errorf("ignored alias shown by default: %+v", ms)
	}
	if ms := e.list("capcode=0500001&ignored=true"); len(ms) != 1 || s(ms[0].Agency) != "WX" {
		t.Errorf("ignored alias with filter: %+v", ms)
	}
	tpls, _ := e.st.ListTemplates(e.ctx)
	for _, tp := range tpls {
		// The CSV's icon column is ignored (R30): icons are images (R65).
		if tp.Name == "WX" && (tp.AgencyProfile != nil || tp.Icon != nil || s(tp.Colour) != "#1565c0") {
			t.Errorf("WX template: %+v", tp)
		}
	}

	// Importing the same rows again changes nothing.
	again, err := e.importCSV(exportCSV, catalog.ImportOptions{AgencyProfiles: exportMapping})
	if err != nil || resultList(again) != "unchanged,unchanged,unchanged,unchanged,unchanged,invalid,invalid" {
		t.Errorf("second import: %s, %v", resultList(again), err)
	}
}

// A21. A re-import reports differences and applies them only with Update.
func TestA21ReimportDoesNotChangeExistingAliases(t *testing.T) {
	e := setup(t)
	first := "id,address,alias,agency,ignore\n1,0300001,Brigade,WX,0\n"
	if _, err := e.importCSV(first, catalog.ImportOptions{}); err != nil {
		t.Fatal(err)
	}
	second := "id,address,alias,agency,ignore\n1,0300001,Brigade North,WX,1\n2,0300002,Tanker,WX,0\n"

	res, err := e.importCSV(second, catalog.ImportOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if resultList(res) != "different,added" {
		t.Fatalf("default re-import: %+v", res)
	}
	d := res.Rows[0]
	if d.Pattern != "0300001" || strings.Join(d.Changes, "; ") != `name: "Brigade" → "Brigade North"; ignored: false → true` {
		t.Errorf("difference: %+v", d)
	}
	rows, _ := e.st.ListAliases(e.ctx)
	if !hasAlias(rows, "0300001", "Brigade") || !hasAlias(rows, "0300002", "Tanker") {
		t.Errorf("default re-import changed an existing alias or didn't add the new one: %+v", rows)
	}
	t.Logf("A21: without update: %+v", d)

	res, err = e.importCSV(second, catalog.ImportOptions{Update: true})
	if err != nil || resultList(res) != "updated,unchanged" {
		t.Fatalf("update: %v %+v", err, res)
	}
	if got := e.aliasName("0300001"); got != "Brigade North" {
		t.Errorf("after update: %q", got)
	}
}

// A17. A duplicate alias is rejected with a message naming the existing one.
func TestA17DuplicateAliasRejectedWithGuidance(t *testing.T) {
	e := setup(t)
	err := e.catAdd("100001", "Another name", "Example Fire", false)
	var refusal *catalog.Refusal
	if !errors.As(err, &refusal) || refusal.Code != "duplicate_alias" {
		t.Fatalf("err = %v", err)
	}
	for _, want := range []string{`"Station 1"`, `edit "Station 1" instead`} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("message %q doesn't contain %q", err, want)
		}
	}
	t.Logf("A17: %v", err)
	if rows, _ := e.st.ListAliases(e.ctx); rows[0].Pattern != "0100001" || rows[0].Name != "Station 1" {
		t.Errorf("existing alias changed: %+v", rows[0])
	}
}

// A16. One alias per capcode, enforced by the database itself.
func TestA16OneAliasPerCapcodeInDatabase(t *testing.T) {
	e := setup(t)
	if _, err := e.st.Pool.Exec(e.ctx, `INSERT INTO aliases (pattern, name) VALUES ('0240241', 'First')`); err != nil {
		t.Fatal(err)
	}
	_, err := e.st.Pool.Exec(e.ctx, `INSERT INTO aliases (pattern, name) VALUES ('240241', 'Second')`)
	if err == nil || !strings.Contains(err.Error(), "aliases_one_per_capcode") {
		t.Fatalf("second alias for the same capcode: err = %v", err)
	}
	t.Logf("A16: %v", err)
	// Patterns with wildcards are compared as written.
	if _, err := e.st.Pool.Exec(e.ctx, `INSERT INTO aliases (pattern, name) VALUES ('024%', 'Pattern')`); err != nil {
		t.Errorf("pattern: %v", err)
	}
	if _, err := e.st.Pool.Exec(e.ctx, `INSERT INTO aliases (pattern, name) VALUES ('024%', 'Pattern again')`); err == nil {
		t.Error("same pattern twice was accepted")
	}
}

// A18. A wildcard pattern overlapping an exact capcode is allowed; R27
// precedence decides which applies.
func TestA18OverlappingPatternAllowed(t *testing.T) {
	e := setup(t)
	if err := e.catAdd("0100%", "Stations block", "Example Fire", false); err != nil {
		t.Fatalf("overlapping pattern rejected: %v", err)
	}
	e.mustPost("a", "0100001", "QDexact", e.now)
	e.mustPost("a", "0100009", "QDpattern", e.now)
	if ms := e.list("capcode=0100001"); len(ms) != 1 || s(ms[0].Alias) != "Station 1" {
		t.Errorf("0100001 shows %v", ms)
	}
	if ms := e.list("capcode=0100009"); len(ms) != 1 || s(ms[0].Alias) != "Stations block" {
		t.Errorf("0100009 shows %v", ms)
	}
}

// A19. The same name on different capcodes is allowed and reported.
func TestA19RepeatedNamesAllowedAndReported(t *testing.T) {
	e := setup(t)
	csvText := "id,address,alias,agency\n1,0300001,Testville Brigade,WX\n2,300002,Testville Brigade,WX\n3,0300003,Other,WX\n"
	res, err := e.importCSV(csvText, catalog.ImportOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if resultList(res) != "added,added,added" || len(res.RepeatedNames) != 1 || res.RepeatedNames[0].Name != "Testville Brigade" ||
		strings.Join(res.RepeatedNames[0].Patterns, ",") != "0300001,0300002" {
		t.Fatalf("result %+v", res)
	}
	t.Logf("A19: repeated names %+v", res.RepeatedNames)
}

// A20. Rows that differ for one capcode stop the import and change nothing;
// leaving one out (from M8, in the pipeline) lets it through. Identical
// rows are squashed.
func TestA20DifferingRowsStopTheImport(t *testing.T) {
	e := setup(t)
	csvText := "id,address,alias,agency\n" +
		"10,0300001,Brigade (old name),WX\n" +
		"11,0300002,Tanker,WX\n" +
		"12,300001,Brigade,WX\n" // same capcode as id 10 after padding
	before := e.aliasCount()

	res, err := e.importCSV(csvText, catalog.ImportOptions{})
	if !catalog.IsConflict(err) || len(res.Conflicts) != 1 || res.Conflicts[0].Pattern != "0300001" || fmt.Sprint(res.Conflicts[0].Rows) != "[1 3]" {
		t.Fatalf("conflict: err %v, report %+v", err, res)
	}
	if e.aliasCount() != before || res.Applied {
		t.Fatal("a stopped import changed the database")
	}
	t.Logf("A20: stopped: %v; %+v", err, res.Conflicts)

	// Leave out the first row, as Where-Object would.
	res, err = e.importCSV("id,address,alias,agency\n11,0300002,Tanker,WX\n12,300001,Brigade,WX\n", catalog.ImportOptions{})
	if err != nil || resultList(res) != "added,added" || e.aliasName("0300001") != "Brigade" {
		t.Fatalf("without the first row: %v %+v", err, res)
	}

	// Rows identical in every imported field are squashed into the first;
	// a real conflict alongside them still stops the import.
	identicalCSV := "id,address,alias,agency,icon,color,ignore\n" +
		"20,0300003,Weather,WX,cloud,#1565c0,0\n" +
		"21,300003,Weather,WX,cloud,#1565c0,0\n"
	res, err = e.importCSV(identicalCSV, catalog.ImportOptions{})
	if err != nil || resultList(res) != "added,squashed" || res.Rows[1].SquashedInto != 1 {
		t.Fatalf("identical rows: %v %+v", err, res)
	}
	t.Logf("A20: identical rows: %+v", res.Rows)
	conflict := identicalCSV + "22,0300003,Weather (new),WX,cloud,#1565c0,0\n"
	if _, err := e.importCSV(conflict, catalog.ImportOptions{}); !catalog.IsConflict(err) {
		t.Errorf("identical rows plus a conflicting one: err = %v", err)
	}
	// Differing only in colour is a conflict too (R27a).
	if _, err := e.importCSV("address,alias,agency,color\n0300009,A,WX,red\n0300009,A,WX,blue\n", catalog.ImportOptions{}); !catalog.IsConflict(err) {
		t.Errorf("rows differing in colour: err = %v", err)
	}
}

// ADR 0009 §8: an unknown agency profile name refuses the whole import.
func TestImportRefusesUnknownProfile(t *testing.T) {
	e := setup(t)
	before := e.aliasCount()
	_, err := e.importCSV("address,alias,agency\n0300001,Brigade,New Agency\n", catalog.ImportOptions{AgencyProfiles: map[string]string{"New Agency": "example-fier"}})
	var refusal *catalog.Refusal
	if !errors.As(err, &refusal) || refusal.Code != "unknown_profile" || !strings.Contains(refusal.Msg, `"example-fier"`) || !strings.Contains(refusal.Msg, "example-fire") {
		t.Fatalf("err = %v", err)
	}
	if e.aliasCount() != before {
		t.Error("a refused import changed the database")
	}
	t.Logf("unknown profile: %v", err)
}

// ADR 0009 §8: applying a plan refuses if anything changed since the dry run.
func TestImportPlanChanged(t *testing.T) {
	e := setup(t)
	rows := "address,alias,agency\n0300001,Brigade,WX\n0300002,Tanker,WX\n"
	dry, err := e.importCSV(rows, catalog.ImportOptions{DryRun: true})
	if err != nil {
		t.Fatal(err)
	}
	if err := e.catAdd("0300002", "Tanker (added meanwhile)", "", false); err != nil {
		t.Fatal(err)
	}
	res, err := e.importCSV(rows, catalog.ImportOptions{Plan: dry.Plan})
	if !catalog.IsPlanChanged(err) || res.Applied || resultList(res) != "added,different" {
		t.Fatalf("changed plan: %v %+v", err, res)
	}
	if e.aliasCount() != 5 {
		t.Errorf("a refused plan changed the database: %d aliases", e.aliasCount())
	}
	// The same plan again, unchanged, applies.
	dry, _ = e.importCSV(rows, catalog.ImportOptions{DryRun: true})
	if res, err := e.importCSV(rows, catalog.ImportOptions{Plan: dry.Plan}); err != nil || !res.Applied {
		t.Fatalf("unchanged plan: %v %+v", err, res)
	}
}

// The import through the API (R63): admins only; a conflict answers 409
// with every row; changes are recorded as the signed-in admin's (A54).
func TestImportAPI(t *testing.T) {
	e := setup(t)
	req := server.ImportRequest{Rows: []catalog.ImportRow{{Pattern: "0300001", Name: "Brigade", Template: "WX"}}}
	if code, _ := e.user.raw(http.MethodPost, "/api/v1/admin/aliases/import", req); code != http.StatusForbidden {
		t.Errorf("user imports: %d", code)
	}
	var rep catalog.ImportReport
	if code, p := e.admin.call(http.MethodPost, "/api/v1/admin/aliases/import", req, &rep); code != http.StatusOK || !rep.Applied {
		t.Fatalf("admin imports: %d %+v %+v", code, p, rep)
	}
	h := e.adminAlias(e.aliasID("Brigade")).History
	if len(h) != 1 || h[0].Source != catalog.SourceImport || !strings.Contains(h[0].MadeBy, "admin@example.com") {
		t.Errorf("history: %+v", h)
	}
	t.Logf("A54: history made by %q", h[0].MadeBy)

	conflict := server.ImportRequest{Rows: []catalog.ImportRow{{Pattern: "0300009", Name: "A"}, {Pattern: "300009", Name: "B"}}}
	code, body := e.admin.raw(http.MethodPost, "/api/v1/admin/aliases/import", conflict)
	if code != http.StatusConflict || !strings.Contains(body, `"code":"import_conflict"`) || !strings.Contains(body, `"rows":[1,2]`) {
		t.Errorf("conflict: %d %s", code, body)
	}
	t.Logf("conflict: %d %s", code, body)

	tooMany := server.ImportRequest{Rows: make([]catalog.ImportRow, catalog.MaxImportRows+1)}
	if code, p := e.admin.call(http.MethodPost, "/api/v1/admin/aliases/import", tooMany, nil); code != http.StatusRequestEntityTooLarge || p.Code != "too_many_rows" {
		t.Errorf("too many rows: %d %+v", code, p)
	}
}

func hasAlias(rows []dbAlias, pattern, name string) bool {
	for _, r := range rows {
		if r.Pattern == pattern && r.Name == name {
			return true
		}
	}
	return false
}

type dbAlias = db.ListAliasesRow
