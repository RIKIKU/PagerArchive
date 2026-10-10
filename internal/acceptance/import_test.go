package acceptance

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/RIKIKU/PagerArchive/internal/catalog"
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

const exportMapping = `{"_comment": "made up", "agency_profiles": {"Example Fire": "example-fire"}}`

func writeTemp(t *testing.T, name, content string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func (e *env) mapping() catalog.Mapping {
	e.t.Helper()
	m, err := catalog.LoadMapping(writeTemp(e.t, "mapping.json", exportMapping))
	if err != nil {
		e.t.Fatal(err)
	}
	return m
}

func (e *env) aliasCount() int {
	e.t.Helper()
	rows, err := e.st.ListAliases(e.ctx)
	if err != nil {
		e.t.Fatal(err)
	}
	return len(rows)
}

// R30: CSV import (owner decisions, 2026-10-10 and 2026-10-11).
func TestAliasImport(t *testing.T) {
	e := setup(t)
	e.mustPost("a", "0300001", "@@EX0000031 HSF1 SHED FIRE @ 3 FICTION ST, MADEUPTON #TV1", e.now)
	e.mustPost("a", "0300004", "QDhello unknown agency", e.now)
	e.mustPost("a", "0500001", "QDnetwork test", e.now)
	m := e.mapping()

	// Dry run: reports, changes nothing.
	dry, err := e.cat.ImportCSV(e.ctx, strings.NewReader(exportCSV), catalog.ImportOptions{Mapping: m, DryRun: true})
	if err != nil {
		t.Fatal(err)
	}
	if n := e.aliasCount(); n != 4 || dry.AliasesAdded != 5 {
		t.Fatalf("dry run: %d aliases in the database (want the fixture's 4), result %+v", n, dry)
	}

	res, err := e.cat.ImportCSV(e.ctx, strings.NewReader(exportCSV), catalog.ImportOptions{Mapping: m})
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("import: %+v", res)
	if res.Rows != 7 || res.AliasesAdded != 5 || res.Ignored != 1 || res.WithoutTemplate != 1 || res.Skipped != 2 {
		t.Errorf("counts: %+v", res)
	}
	if strings.Join(res.TemplatesCreated, ",") != "WX" || strings.Join(res.TemplatesKept, ",") != "Example Fire" {
		t.Errorf("templates: created %v, kept %v", res.TemplatesCreated, res.TemplatesKept)
	}
	want := []string{
		"row 7: address isn't a capcode or pattern (at most 7 digits, % and _), skipped",
		"row 8: no alias name, skipped",
	}
	if strings.Join(res.Problems, "|") != strings.Join(want, "|") {
		t.Errorf("problems:\n got %q\nwant %q", res.Problems, want)
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
		if tp.Name == "WX" && (tp.AgencyProfile != nil || s(tp.Icon) != "cloud" || s(tp.Colour) != "#1565c0") {
			t.Errorf("WX template: %+v", tp)
		}
	}

	// Importing again updates in place.
	again, err := e.cat.ImportCSV(e.ctx, strings.NewReader(exportCSV), catalog.ImportOptions{Mapping: m})
	if err != nil || again.AliasesAdded != 0 || again.AliasesUpdated != 5 {
		t.Errorf("second import: %+v, %v", again, err)
	}
}

func TestAliasImportNeedsColumns(t *testing.T) {
	e := setup(t)
	_, err := e.cat.ImportCSV(e.ctx, strings.NewReader("id,capcode,name\n1,0300001,x\n"), catalog.ImportOptions{})
	if err == nil || !strings.Contains(err.Error(), `no "address" column`) {
		t.Errorf("err = %v", err)
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

// A17. A duplicate alias is rejected with a message naming the existing one.
func TestA17DuplicateAliasRejectedWithGuidance(t *testing.T) {
	e := setup(t)
	err := e.cat.AddAlias(e.ctx, catalog.Alias{Pattern: "100001", Name: "Another name", Template: "Example Fire"})
	var dup *catalog.DuplicateAliasError
	if !errors.As(err, &dup) || dup.Existing != "Station 1" {
		t.Fatalf("err = %v", err)
	}
	for _, want := range []string{`"Station 1"`, "edit it instead", "alias set 0100001"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("message %q doesn't contain %q", err, want)
		}
	}
	t.Logf("A17: %v", err)
	if rows, _ := e.st.ListAliases(e.ctx); rows[0].Pattern != "0100001" || rows[0].Name != "Station 1" {
		t.Errorf("existing alias changed: %+v", rows[0])
	}
}

// A18. A wildcard pattern overlapping an exact capcode is allowed; R27
// precedence decides which applies.
func TestA18OverlappingPatternAllowed(t *testing.T) {
	e := setup(t)
	if err := e.cat.AddAlias(e.ctx, catalog.Alias{Pattern: "0100%", Name: "Stations block", Template: "Example Fire"}); err != nil {
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
	res, err := e.cat.ImportCSV(e.ctx, strings.NewReader(csvText), catalog.ImportOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if res.AliasesAdded != 3 || len(res.RepeatedNames) != 1 || res.RepeatedNames[0].Name != "Testville Brigade" ||
		strings.Join(res.RepeatedNames[0].Patterns, ",") != "0300001,0300002" {
		t.Fatalf("result %+v", res)
	}
	t.Logf("A19: repeated names %+v", res.RepeatedNames)
}

// A20. Duplicate CSV rows stop the import unless an override resolves them.
func TestA20CSVDuplicatesNeedOverrides(t *testing.T) {
	e := setup(t)
	csvText := "id,address,alias,agency\n" +
		"10,0300001,Brigade (old name),WX\n" +
		"11,0300002,Tanker,WX\n" +
		"12,300001,Brigade,WX\n" // same capcode as id 10 after padding
	before := e.aliasCount()

	res, err := e.cat.ImportCSV(e.ctx, strings.NewReader(csvText), catalog.ImportOptions{})
	if !errors.Is(err, catalog.ErrUnresolved) || len(res.Unresolved) != 1 || res.Unresolved[0].Pattern != "0300001" || len(res.Unresolved[0].Rows) != 2 {
		t.Fatalf("no overrides: err %v, result %+v", err, res)
	}
	if res.Unresolved[0].Rows[0].String() != `row 2 (id 10): "Brigade (old name)", WX` {
		t.Errorf("listing: %s", res.Unresolved[0].Rows[0])
	}
	if e.aliasCount() != before {
		t.Fatal("a stopped import changed the database")
	}
	t.Logf("A20: stopped, listing %+v", res.Unresolved)

	// Overrides naming a capcode with no duplicates, or no row, are errors.
	stale := catalog.Overrides{Duplicates: map[string]catalog.Override{"0300001": {KeepID: "12"}, "0300002": {KeepID: "11"}, "0399999": {Skip: true}}}
	res, err = e.cat.ImportCSV(e.ctx, strings.NewReader(csvText), catalog.ImportOptions{Overrides: stale})
	if !errors.Is(err, catalog.ErrUnresolved) || len(res.StaleOverrides) != 2 || e.aliasCount() != before {
		t.Fatalf("stale overrides: err %v, stale %q", err, res.StaleOverrides)
	}

	// keep_id chooses the row (keys may be written unpadded).
	ov := catalog.Overrides{Duplicates: map[string]catalog.Override{"300001": {KeepID: "12"}}}
	res, err = e.cat.ImportCSV(e.ctx, strings.NewReader(csvText), catalog.ImportOptions{Overrides: ov})
	if err != nil || res.AliasesAdded != 2 || res.Resolved != 1 || res.Skipped != 1 {
		t.Fatalf("keep_id: err %v, result %+v", err, res)
	}
	if rows, _ := e.st.ListAliases(e.ctx); !hasAlias(rows, "0300001", "Brigade") {
		t.Errorf("kept the wrong row")
	}

	// keep_row and skip work too.
	ov = catalog.Overrides{Duplicates: map[string]catalog.Override{"0300001": {KeepRow: 2}}}
	if _, err := e.cat.ImportCSV(e.ctx, strings.NewReader(csvText), catalog.ImportOptions{Overrides: ov}); err != nil {
		t.Fatal(err)
	}
	if rows, _ := e.st.ListAliases(e.ctx); !hasAlias(rows, "0300001", "Brigade (old name)") {
		t.Errorf("keep_row kept the wrong row")
	}
	ov = catalog.Overrides{Duplicates: map[string]catalog.Override{"0300001": {Skip: true}}}
	if res, err := e.cat.ImportCSV(e.ctx, strings.NewReader(csvText), catalog.ImportOptions{Overrides: ov}); err != nil || res.Skipped != 2 {
		t.Fatalf("skip: %v %+v", err, res)
	}

	// An overrides file must give exactly one choice per capcode.
	bad := writeTemp(t, "overrides.json", `{"duplicates": {"0300001": {"keep_id": "10", "skip": true}}}`)
	if _, err := catalog.LoadOverrides(bad); err == nil || !strings.Contains(err.Error(), "exactly one") {
		t.Errorf("bad overrides file: %v", err)
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
