package acceptance

import (
	"encoding/csv"
	"strings"

	"github.com/RIKIKU/PagerArchive/internal/catalog"
)

// Catalog changes as an admin makes them (R53, R64). Before M8 these tests
// used the command line's catalog functions (R59).

var byAdmin = catalog.By{Source: catalog.SourceAdmin}

// catAdd adds an alias, with a template by name ("" for none).
func (e *env) catAdd(pattern, name, template string, ignored bool) error {
	e.t.Helper()
	in := catalog.AliasInput{Pattern: pattern, Name: name, Ignored: ignored}
	if template != "" {
		id := e.templateID(template)
		in.TemplateID = &id
	}
	_, err := e.cat.CreateAlias(e.ctx, in, byAdmin)
	return err
}

// catSet renames an alias and sets its template.
func (e *env) catSet(pattern, name, template string) error {
	e.t.Helper()
	pattern, _ = catalog.NormalisePattern(pattern)
	var id int64
	if err := e.st.Pool.QueryRow(e.ctx, "SELECT id FROM aliases WHERE pattern = $1", pattern).Scan(&id); err != nil {
		e.t.Fatal(err)
	}
	tpl := e.templateID(template)
	return e.cat.EditAlias(e.ctx, id, catalog.AliasInput{Pattern: pattern, Name: name, TemplateID: &tpl}, byAdmin)
}

// catDelete deletes an alias by capcode or pattern.
func (e *env) catDelete(pattern string) error {
	e.t.Helper()
	var id int64
	if err := e.st.Pool.QueryRow(e.ctx, "SELECT id FROM aliases WHERE pattern = $1", norm(pattern)).Scan(&id); err != nil {
		e.t.Fatal(err)
	}
	_, err := e.cat.RemoveAlias(e.ctx, id, byAdmin)
	return err
}

// profiles are the agency profiles the test server loaded.
func (e *env) profiles() []string {
	var out []string
	for name := range e.srv.Ingest.Agencies {
		out = append(out, name)
	}
	return out
}

// importCSV imports a CSV as the PowerShell module does with Import-Csv
// piped to Import-PagerArchiveAlias: each row's address, alias, agency,
// color and ignore columns become one row, and every other column (an
// export's plugin settings, say) is never read.
func (e *env) importCSV(text string, opts catalog.ImportOptions) (catalog.ImportReport, error) {
	e.t.Helper()
	recs, err := csv.NewReader(strings.NewReader(text)).ReadAll()
	if err != nil {
		e.t.Fatal(err)
	}
	col := map[string]int{}
	for i, h := range recs[0] {
		col[h] = i
	}
	get := func(rec []string, name string) string {
		if i, ok := col[name]; ok && i < len(rec) {
			return rec[i]
		}
		return ""
	}
	var rows []catalog.ImportRow
	for _, rec := range recs[1:] {
		ign := strings.ToLower(strings.TrimSpace(get(rec, "ignore")))
		rows = append(rows, catalog.ImportRow{Pattern: get(rec, "address"), Name: get(rec, "alias"), Template: get(rec, "agency"),
			Colour: get(rec, "color"), Ignored: ign == "1" || ign == "true" || ign == "yes"})
	}
	if opts.Profiles == nil {
		opts.Profiles = e.profiles()
	}
	return e.cat.Import(e.ctx, rows, opts)
}

// results counts an import's row results.
func results(rep catalog.ImportReport) map[string]int {
	out := map[string]int{}
	for _, r := range rep.Rows {
		out[r.Result]++
	}
	return out
}

func norm(pattern string) string {
	p, _ := catalog.NormalisePattern(pattern)
	return p
}
