package acceptance

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/RIKIKU/PagerArchive/internal/catalog"
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
"7","0300002","Testville Tanker 2","Example Fire","truck","#b3261e","","0"
"8","0300005","","WX","","","","0"
`

const exportMapping = `{"_comment": "made up", "agency_profiles": {"Example Fire": "example-fire"}}`

// R30: CSV import (owner decisions, 2026-10-11).
func TestAliasImport(t *testing.T) {
	e := setup(t)
	e.mustPost("a", "0300001", "@@EX0000031 HSF1 SHED FIRE @ 3 FICTION ST, MADEUPTON #TV1", e.now)
	e.mustPost("a", "0300004", "QDhello unknown agency", e.now)
	e.mustPost("a", "0500001", "QDnetwork test", e.now)

	mapping := writeTemp(t, "mapping.json", exportMapping)
	m, err := catalog.LoadMapping(mapping)
	if err != nil {
		t.Fatal(err)
	}

	// Dry run: reports, changes nothing.
	dry, err := e.cat.ImportCSV(e.ctx, strings.NewReader(exportCSV), m, true)
	if err != nil {
		t.Fatal(err)
	}
	if aliases, _ := e.st.ListAliases(e.ctx); len(aliases) != 4 || dry.AliasesAdded != 5 {
		t.Fatalf("dry run: %d aliases in the database (want the fixture's 4), result %+v", len(aliases), dry)
	}

	res, err := e.cat.ImportCSV(e.ctx, strings.NewReader(exportCSV), m, false)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("import: %+v", res)
	if res.Rows != 8 || res.AliasesAdded != 5 || res.Ignored != 1 || res.WithoutTemplate != 1 || res.Skipped != 3 {
		t.Errorf("counts: %+v", res)
	}
	if strings.Join(res.TemplatesCreated, ",") != "WX" || strings.Join(res.TemplatesKept, ",") != "Example Fire" {
		t.Errorf("templates: created %v, kept %v", res.TemplatesCreated, res.TemplatesKept)
	}
	// Line 1 is the header, so the CSV's id N is on line N+1.
	want := []string{
		"row 7: address isn't a capcode or pattern, skipped",
		"row 8: same capcode as row 3; row 8 wins",
		"row 9: no alias name, skipped",
	}
	if strings.Join(res.Problems, "|") != strings.Join(want, "|") {
		t.Errorf("problems:\n got %q\nwant %q", res.Problems, want)
	}
	for _, p := range res.Problems {
		if strings.Contains(p, "Too long") || strings.Contains(p, "12345678") || strings.Contains(p, "Tanker") {
			t.Errorf("problem quotes alias data: %q", p)
		}
	}

	// Plugin settings never reach the database (or the log, checked by setup).
	if hits := e.rowsContaining("S3CRET-PLUGIN-KEY"); len(hits) > 0 {
		t.Errorf("plugin settings stored: %v", hits)
	}
	if hits := e.rowsContaining("apikey"); len(hits) > 0 {
		t.Errorf("plugin settings stored: %v", hits)
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
	again, err := e.cat.ImportCSV(e.ctx, strings.NewReader(exportCSV), m, false)
	if err != nil || again.AliasesAdded != 0 || again.AliasesUpdated != 5 {
		t.Errorf("second import: %+v, %v", again, err)
	}
}

func writeTemp(t *testing.T, name, content string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestAliasImportNeedsColumns(t *testing.T) {
	e := setup(t)
	_, err := e.cat.ImportCSV(e.ctx, strings.NewReader("id,capcode,name\n1,0300001,x\n"), catalog.Mapping{}, false)
	if err == nil || !strings.Contains(err.Error(), `no "address" column`) {
		t.Errorf("err = %v", err)
	}
}
