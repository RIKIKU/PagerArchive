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
	"strconv"
	"strings"

	"github.com/jackc/pgx/v5"

	"github.com/RIKIKU/PagerArchive/internal/store/db"
)

// Mapping says which agency profile each agency's template gets (R30).
// Agencies not listed get a template with no profile (text only).
type Mapping struct {
	AgencyProfiles map[string]string `json:"agency_profiles"`
}

// Overrides resolve CSV rows that give the same capcode (R27a). Keyed by
// capcode or pattern, as written in the CSV or zero-padded.
type Overrides struct {
	Duplicates map[string]Override `json:"duplicates"`
}

// Override chooses what to import for one duplicated capcode: the row with
// this export id, the row on this CSV line, or nothing.
type Override struct {
	KeepID  string `json:"keep_id,omitempty"`
	KeepRow int    `json:"keep_row,omitempty"`
	Skip    bool   `json:"skip,omitempty"`
}

// ImportOptions configure an import.
type ImportOptions struct {
	Mapping   Mapping
	Overrides Overrides
	DryRun    bool // report only; change nothing
	Update    bool // apply differences to existing aliases (R27a); otherwise only report them
}

// readJSON reads a JSON object with one known key; keys starting with _ are
// comments.
func readJSON(path, key string, into any) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		return fmt.Errorf("%s: not valid JSON", path)
	}
	for k, v := range raw {
		switch {
		case k == key:
			dec := json.NewDecoder(strings.NewReader(string(v)))
			dec.DisallowUnknownFields()
			if err := dec.Decode(into); err != nil {
				return fmt.Errorf("%s: %s: %v", path, key, err)
			}
		case strings.HasPrefix(k, "_"):
		default:
			return fmt.Errorf("%s: unknown key %q", path, k)
		}
	}
	return nil
}

// LoadMapping reads {"agency_profiles": {"<agency>": "<profile>"}}.
func LoadMapping(path string) (Mapping, error) {
	var m Mapping
	err := readJSON(path, "agency_profiles", &m.AgencyProfiles)
	return m, err
}

// LoadOverrides reads {"duplicates": {"<capcode>": {"keep_id": "…"} | {"keep_row": N} | {"skip": true}}}.
func LoadOverrides(path string) (Overrides, error) {
	var o Overrides
	if err := readJSON(path, "duplicates", &o.Duplicates); err != nil {
		return o, err
	}
	for k, v := range o.Duplicates {
		n := 0
		for _, set := range []bool{v.KeepID != "", v.KeepRow != 0, v.Skip} {
			if set {
				n++
			}
		}
		if n != 1 {
			return o, fmt.Errorf("%s: duplicates.%s: give exactly one of keep_id, keep_row or skip", path, k)
		}
	}
	return o, nil
}

// DuplicateRow is one of several CSV rows for the same capcode.
type DuplicateRow struct {
	Line   int
	ID     string // the export's own row id, if it has one
	Name   string
	Agency string
}

// Duplicate is a capcode given by more than one CSV row (R27a).
type Duplicate struct {
	Pattern string
	Rows    []DuplicateRow
}

// RepeatedName is an alias name used for several capcodes. That's allowed
// (a brigade can have several pagers); the report lists it for checking.
type RepeatedName struct {
	Name     string
	Patterns []string
}

// ImportResult says what an import did, or why it stopped.
type ImportResult struct {
	Rows             int
	AliasesAdded     int
	AliasesUpdated   int
	Ignored          int // aliases marked ignored (R27)
	WithoutTemplate  int // agency empty or "null"
	TemplatesCreated []string
	TemplatesKept    []string          // already existed; left unchanged
	Skipped          int               // invalid rows, and duplicates an override skipped or didn't keep
	Problems         []string          // invalid rows, by line number
	Resolved         int               // duplicated capcodes resolved by an override
	Squashed         int               // rows identical to an earlier row for the same capcode, dropped
	Unchanged        int               // existing aliases the CSV agrees with
	Differences      []AliasDifference // existing aliases the CSV differs from

	// Each of these stops the import before anything changes.
	Unresolved     []Duplicate
	StaleOverrides []string // overrides for capcodes with no duplicate rows, or naming no row

	RepeatedNames []RepeatedName
}

// AliasDifference is how an existing alias differs from its CSV row. It is
// applied only with ImportOptions.Update (R27a).
type AliasDifference struct {
	Pattern string
	Changes []string // e.g. name: "Brigade" → "Brigade North"
}

// ErrUnresolved stops an import whose duplicates aren't all resolved.
var ErrUnresolved = errors.New("the CSV has more than one row for some capcodes; resolve each one in an overrides file (R27a)")

// The CSV columns the import reads. Every other column (for example an
// export's plugin settings) is never read, stored, logged or printed.
var importColumns = map[string]bool{"id": true, "address": true, "alias": true, "agency": true, "icon": true, "color": true, "colour": true, "ignore": true}

// errDryRun rolls back a dry run.
var errDryRun = errors.New("dry run")

type importRow struct {
	line                                   int
	id, pattern, name, agency, icon, color string
	ignored                                bool
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
// (id, address, alias, agency, icon, color, ignore). It creates one template
// per agency, with the most common icon and colour among that agency's
// aliases and the profile from the mapping. An empty or "null" agency means
// no template. An alias already in the database for the same capcode is
// left alone and its differences reported, unless opts.Update. Rows for
// one capcode that are identical are squashed; real conflicts must be
// resolved by an override, or nothing is imported (ErrUnresolved). It runs in one
// transaction and then re-parses every message (R23a).
func (c *Catalog) ImportCSV(ctx context.Context, r io.Reader, opts ImportOptions) (ImportResult, error) {
	var res ImportResult
	byPattern, order, err := readImportRows(r, &res)
	if err != nil {
		return res, err
	}
	rows := resolveDuplicates(byPattern, order, opts.Overrides, &res)
	if len(res.Unresolved) > 0 || len(res.StaleOverrides) > 0 {
		return res, ErrUnresolved
	}
	res.RepeatedNames = repeatedNames(rows)

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
			if p, ok := opts.Mapping.AgencyProfiles[agency]; ok && p != "" {
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
			if row.ignored {
				res.Ignored++
			}
			cur, err := q.AliasDetailByKey(ctx, row.pattern)
			if errors.Is(err, pgx.ErrNoRows) {
				if _, err := q.CreateAlias(ctx, db.CreateAliasParams{Pattern: row.pattern, Name: row.name, TemplateID: tid, Ignored: row.ignored}); err != nil {
					return dbErr(fmt.Sprintf("row %d", row.line), err)
				}
				res.AliasesAdded++
				continue
			}
			if err != nil {
				return dbErr(fmt.Sprintf("row %d", row.line), err)
			}
			// An existing alias is changed only with Update (R27a).
			changes := differences(cur, row)
			if len(changes) == 0 {
				res.Unchanged++
				continue
			}
			res.Differences = append(res.Differences, AliasDifference{Pattern: row.pattern, Changes: changes})
			if opts.Update {
				if err := q.UpdateAlias(ctx, db.UpdateAliasParams{ID: cur.ID, Name: row.name, TemplateID: tid, Ignored: row.ignored}); err != nil {
					return dbErr(fmt.Sprintf("row %d", row.line), err)
				}
				res.AliasesUpdated++
			}
		}
		if res.AliasesAdded+res.AliasesUpdated > 0 {
			if err := reparse(ctx, q, "%", "alias import"); err != nil {
				return dbErr("request re-parse", err)
			}
		}
		if opts.DryRun {
			return errDryRun
		}
		return nil
	})
	if errors.Is(err, errDryRun) {
		err = nil
	}
	return res, err
}

// differences lists how an existing alias differs from a CSV row.
func differences(cur db.AliasDetailByKeyRow, row importRow) []string {
	var out []string
	if cur.Name != row.name {
		out = append(out, fmt.Sprintf("name: %q → %q", cur.Name, row.name))
	}
	curTpl := ""
	if cur.TemplateName != nil {
		curTpl = *cur.TemplateName
	}
	if curTpl != row.agency {
		show := func(s string) string {
			if s == "" {
				return "(none)"
			}
			return strconv.Quote(s)
		}
		out = append(out, fmt.Sprintf("template: %s → %s", show(curTpl), show(row.agency)))
	}
	if cur.Ignored != row.ignored {
		out = append(out, fmt.Sprintf("ignored: %v → %v", cur.Ignored, row.ignored))
	}
	return out
}

func optional(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

// resolveDuplicates returns one row per capcode, applying overrides. Rows
// for a capcode without an override are reported, never picked.
func resolveDuplicates(byPattern map[string][]importRow, order []string, ov Overrides, res *ImportResult) []importRow {
	overrides := map[string]Override{}
	for k, v := range ov.Duplicates {
		key, err := NormalisePattern(strings.TrimSpace(k))
		if err != nil {
			res.StaleOverrides = append(res.StaleOverrides, fmt.Sprintf("%s: not a capcode or pattern", k))
			continue
		}
		overrides[key] = v
	}
	var out []importRow
	for _, p := range order {
		cands := byPattern[p]
		o, has := overrides[p]
		delete(overrides, p)
		if len(cands) == 1 {
			if has {
				res.StaleOverrides = append(res.StaleOverrides, fmt.Sprintf("%s: only one row gives this capcode; remove the override", p))
			}
			out = append(out, cands[0])
			continue
		}
		if !has && identical(cands) {
			// Same in every imported field: nothing to choose (R27a).
			res.Squashed += len(cands) - 1
			out = append(out, cands[0])
			continue
		}
		if !has {
			d := Duplicate{Pattern: p}
			for _, c := range cands {
				d.Rows = append(d.Rows, DuplicateRow{Line: c.line, ID: c.id, Name: c.name, Agency: c.agency})
			}
			res.Unresolved = append(res.Unresolved, d)
			continue
		}
		res.Resolved++
		if o.Skip {
			res.Skipped += len(cands)
			continue
		}
		var keep []importRow
		for _, c := range cands {
			if (o.KeepID != "" && c.id == o.KeepID) || (o.KeepRow != 0 && c.line == o.KeepRow) {
				keep = append(keep, c)
			}
		}
		if len(keep) != 1 {
			res.StaleOverrides = append(res.StaleOverrides, fmt.Sprintf("%s: the override matches %d of its %d rows; it must match exactly one", p, len(keep), len(cands)))
			continue
		}
		res.Skipped += len(cands) - 1
		out = append(out, keep[0])
	}
	var left []string
	for k := range overrides {
		left = append(left, k)
	}
	sort.Strings(left)
	for _, k := range left {
		res.StaleOverrides = append(res.StaleOverrides, fmt.Sprintf("%s: no row in the CSV gives this capcode; remove the override", k))
	}
	return out
}

// identical reports whether rows agree in every imported field.
func identical(rows []importRow) bool {
	for _, r := range rows[1:] {
		if r.name != rows[0].name || r.agency != rows[0].agency || r.icon != rows[0].icon || r.color != rows[0].color || r.ignored != rows[0].ignored {
			return false
		}
	}
	return true
}

func repeatedNames(rows []importRow) []RepeatedName {
	by := map[string][]string{}
	for _, r := range rows {
		by[r.name] = append(by[r.name], r.pattern)
	}
	var out []RepeatedName
	for name, ps := range by {
		if len(ps) > 1 {
			sort.Strings(ps)
			out = append(out, RepeatedName{Name: name, Patterns: ps})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// readImportRows reads the CSV, grouping valid rows by normalised capcode.
func readImportRows(r io.Reader, res *ImportResult) (map[string][]importRow, []string, error) {
	cr := csv.NewReader(r)
	cr.FieldsPerRecord = -1
	cr.ReuseRecord = true
	header, err := cr.Read()
	if err != nil {
		return nil, nil, errors.New("CSV: can't read the header row")
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
			return nil, nil, fmt.Errorf("CSV: no %q column", need)
		}
	}
	get := func(rec []string, name string) string {
		i, ok := col[name]
		if !ok || i >= len(rec) {
			return ""
		}
		return clean(rec[i])
	}

	byPattern := map[string][]importRow{}
	var order []string
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
			id:     get(rec, "id"),
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
			res.Problems = append(res.Problems, fmt.Sprintf("row %d: address isn't a capcode or pattern (at most 7 digits, %% and _), skipped", line))
			res.Skipped++
			continue
		case row.name == "":
			res.Problems = append(res.Problems, fmt.Sprintf("row %d: no alias name, skipped", line))
			res.Skipped++
			continue
		}
		row.pattern = pattern
		if _, seen := byPattern[pattern]; !seen {
			order = append(order, pattern)
		}
		byPattern[pattern] = append(byPattern[pattern], row)
	}
	return byPattern, order, nil
}

// String renders a duplicate row for the import report.
func (d DuplicateRow) String() string {
	s := "row " + strconv.Itoa(d.Line)
	if d.ID != "" {
		s += " (id " + d.ID + ")"
	}
	s += ": " + strconv.Quote(d.Name)
	if d.Agency != "" {
		s += ", " + d.Agency
	}
	return s
}
