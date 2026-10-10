package catalog

import (
	"context"
	"encoding/csv"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"

	"github.com/RIKIKU/PagerArchive/internal/store/db"
)

// Mapping says which agency profile each agency's template gets (R30).
// Agencies not listed get a template with no profile (text only).
type Mapping struct {
	AgencyProfiles map[string]string `json:"agency_profiles"`
}

// LoadMapping reads a mapping file: {"agency_profiles": {"<agency>": "<profile>"}}.
// Keys starting with _ are comments.
func LoadMapping(path string) (Mapping, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return Mapping{}, err
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		return Mapping{}, fmt.Errorf("%s: not valid JSON", path)
	}
	var m Mapping
	for k, v := range raw {
		switch {
		case k == "agency_profiles":
			if err := json.Unmarshal(v, &m.AgencyProfiles); err != nil {
				return Mapping{}, fmt.Errorf("%s: agency_profiles must map agency names to profile names", path)
			}
		case strings.HasPrefix(k, "_"):
		default:
			return Mapping{}, fmt.Errorf("%s: unknown key %q", path, k)
		}
	}
	return m, nil
}

// ImportResult counts what an import did. Problems name CSV rows by number
// only, never by content (alias data is private to the deployment).
type ImportResult struct {
	Rows             int
	AliasesAdded     int
	AliasesUpdated   int
	Ignored          int // aliases marked ignored (R27)
	WithoutTemplate  int // agency empty or "null"
	TemplatesCreated []string
	TemplatesKept    []string // already existed; left unchanged
	Skipped          int
	Problems         []string
}

// The CSV columns the import reads. Every other column (an export's own
// row id, plugin settings) is never read, stored or printed.
var importColumns = map[string]bool{"address": true, "alias": true, "agency": true, "icon": true, "color": true, "colour": true, "ignore": true}

// errDryRun rolls back a dry run.
var errDryRun = errors.New("dry run")

type importRow struct {
	line                               int
	pattern, name, agency, icon, color string
	ignored                            bool
}

// clean trims a value; the literal "null" (as some exports write) is empty.
func clean(s string) string {
	s = strings.TrimSpace(s)
	if strings.EqualFold(s, "null") {
		return ""
	}
	return s
}

// ImportCSV adds or updates aliases from a CSV export with a header row
// (address, alias, agency, icon, color, ignore). It creates one template per
// agency, with the most common icon and colour among that agency's aliases
// and the profile from m. An empty or "null" agency means no template. It
// runs in one transaction and then re-parses every message (R23a). With
// dryRun, nothing is changed.
func (c *Catalog) ImportCSV(ctx context.Context, r io.Reader, m Mapping, dryRun bool) (ImportResult, error) {
	var res ImportResult
	rows, err := readImportRows(r, &res)
	if err != nil {
		return res, err
	}

	type look struct{ icon, color string }
	byAgency := map[string]map[look]int{}
	for _, row := range rows {
		if row.agency == "" {
			continue
		}
		if byAgency[row.agency] == nil {
			byAgency[row.agency] = map[look]int{}
		}
		byAgency[row.agency][look{row.icon, row.color}]++
	}
	agencies := make([]string, 0, len(byAgency))
	for a := range byAgency {
		agencies = append(agencies, a)
	}
	sort.Strings(agencies)

	err = c.Store.InTx(ctx, func(q *db.Queries) error {
		ids := map[string]int64{}
		for _, agency := range agencies {
			if tpl, err := q.TemplateByName(ctx, agency); err == nil {
				ids[agency] = tpl.ID
				res.TemplatesKept = append(res.TemplatesKept, agency)
				continue
			}
			// Most common icon and colour; ties go to the first in sort order.
			var best look
			bestN := -1
			for l, n := range byAgency[agency] {
				if n > bestN || (n == bestN && l.icon+l.color < best.icon+best.color) {
					best, bestN = l, n
				}
			}
			var prof *string
			if p, ok := m.AgencyProfiles[agency]; ok && p != "" {
				prof = &p
			}
			id, err := q.CreateTemplate(ctx, db.CreateTemplateParams{Name: agency, Agency: agency, Icon: optional(best.icon), Colour: optional(best.color), AgencyProfile: prof})
			if err != nil {
				return dbErr("create template", err)
			}
			ids[agency] = id
			res.TemplatesCreated = append(res.TemplatesCreated, agency)
		}
		for _, row := range rows {
			var tid *int64
			if row.agency != "" {
				id := ids[row.agency]
				tid = &id
			} else {
				res.WithoutTemplate++
			}
			inserted, err := q.UpsertAlias(ctx, db.UpsertAliasParams{Pattern: row.pattern, Name: row.name, TemplateID: tid, Ignored: row.ignored})
			if err != nil {
				return dbErr(fmt.Sprintf("row %d", row.line), err)
			}
			if inserted {
				res.AliasesAdded++
			} else {
				res.AliasesUpdated++
			}
			if row.ignored {
				res.Ignored++
			}
		}
		if err := reparse(ctx, q, "%", "alias import"); err != nil {
			return dbErr("request re-parse", err)
		}
		if dryRun {
			return errDryRun
		}
		return nil
	})
	if errors.Is(err, errDryRun) {
		err = nil
	}
	return res, err
}

func optional(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

func readImportRows(r io.Reader, res *ImportResult) ([]importRow, error) {
	cr := csv.NewReader(r)
	cr.FieldsPerRecord = -1
	cr.ReuseRecord = true
	header, err := cr.Read()
	if err != nil {
		return nil, errors.New("CSV: can't read the header row")
	}
	col := map[string]int{}
	for i, h := range header {
		h = strings.ToLower(strings.TrimSpace(strings.TrimPrefix(h, string(rune(0xFEFF)))))
		if importColumns[h] {
			col[h] = i
		}
	}
	if _, ok := col["colour"]; ok {
		col["color"] = col["colour"]
	}
	for _, need := range []string{"address", "alias"} {
		if _, ok := col[need]; !ok {
			return nil, fmt.Errorf("CSV: no %q column", need)
		}
	}
	get := func(rec []string, name string) string {
		i, ok := col[name]
		if !ok || i >= len(rec) {
			return ""
		}
		return clean(rec[i])
	}

	var rows []importRow
	seen := map[string]int{} // pattern -> index in rows
	line := 1
	for {
		rec, err := cr.Read()
		line++
		if err == io.EOF {
			break
		}
		if err != nil {
			// csv errors quote the offending text, so report the row only.
			res.Problems = append(res.Problems, fmt.Sprintf("row %d: not valid CSV, skipped", line))
			res.Skipped++
			continue
		}
		res.Rows++
		row := importRow{
			line:   line,
			name:   get(rec, "alias"),
			agency: get(rec, "agency"),
			icon:   get(rec, "icon"),
			color:  get(rec, "color"),
		}
		switch strings.ToLower(get(rec, "ignore")) {
		case "1", "true", "yes":
			row.ignored = true
		}
		pattern, perr := NormalisePattern(get(rec, "address"))
		switch {
		case perr != nil:
			res.Problems = append(res.Problems, fmt.Sprintf("row %d: address isn't a capcode or pattern, skipped", line))
			res.Skipped++
			continue
		case row.name == "":
			res.Problems = append(res.Problems, fmt.Sprintf("row %d: no alias name, skipped", line))
			res.Skipped++
			continue
		}
		row.pattern = pattern
		if i, dup := seen[pattern]; dup {
			res.Problems = append(res.Problems, fmt.Sprintf("row %d: same capcode as row %d; row %d wins", line, rows[i].line, line))
			res.Skipped++
			rows[i] = row
			continue
		}
		seen[pattern] = len(rows)
		rows = append(rows, row)
	}
	return rows, nil
}
