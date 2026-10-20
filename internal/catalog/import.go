package catalog

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"sort"
	"strconv"
	"strings"

	"github.com/jackc/pgx/v5"

	"github.com/RIKIKU/PagerArchive/internal/store/db"
)

// ImportRow is one alias to import (R30, R63): the import CSV's address,
// alias, agency, color and ignore columns, as the PowerShell module sends
// them. Template is the agency column, which names the template.
type ImportRow struct {
	Pattern  string `json:"pattern"`
	Name     string `json:"name"`
	Template string `json:"template"`
	Colour   string `json:"colour"`
	Ignored  bool   `json:"ignored"`
}

// ImportOptions configure an import.
type ImportOptions struct {
	// AgencyProfiles says which agency profile each new agency's template
	// gets (R30). Agencies not listed get no profile (text only).
	AgencyProfiles map[string]string
	// Profiles are the agency profiles loaded at start-up. A mapping naming
	// any other refuses the import.
	Profiles []string
	DryRun   bool   // report only; change nothing
	Update   bool   // apply differences to existing aliases (R27a, R58)
	Plan     string // a dry run's Plan: refuse if the outcome has changed since
	By       By     // the signed-in admin, for history (R57)
}

// MaxImportRows caps one import. The module sends every row in one request,
// so the batch stays whole (R27a).
const MaxImportRows = 20000

// Row results.
const (
	Added     = "added"
	Unchanged = "unchanged"
	Different = "different" // differs from the existing alias; applied only with Update
	Updated   = "updated"
	Squashed  = "squashed" // identical to an earlier row for the same capcode
	Invalid   = "invalid"  // refused on its own and skipped
)

// RowResult is what the import does with one row. Index counts from 1, in
// the order the rows were sent.
type RowResult struct {
	Index        int      `json:"index"`
	Pattern      string   `json:"pattern"`
	Name         string   `json:"name"`
	Result       string   `json:"result"`
	Changes      []string `json:"changes,omitempty"` // e.g. name: "Brigade" → "Brigade North"
	SquashedInto int      `json:"squashed_into,omitempty"`
	Problem      string   `json:"problem,omitempty"` // why an invalid row was skipped
}

// Conflict is a capcode or pattern given by rows that differ (R27a).
type Conflict struct {
	Pattern string `json:"pattern"`
	Rows    []int  `json:"rows"` // indexes
}

// RepeatedName is an alias name used for several capcodes. That's allowed
// (a brigade can have several pagers); the report lists it for checking.
type RepeatedName struct {
	Name     string   `json:"name"`
	Patterns []string `json:"patterns"`
}

// ImportReport says what an import did, or would do.
type ImportReport struct {
	Rows             []RowResult    `json:"rows"`
	Conflicts        []Conflict     `json:"conflicts"`
	TemplatesCreated []string       `json:"templates_created"`
	RepeatedNames    []RepeatedName `json:"repeated_names"`
	// Plan fingerprints the outcome, so applying can refuse if anything
	// changed since the dry run (ADR 0009 §8).
	Plan    string `json:"plan"`
	Applied bool   `json:"applied"`
}

// Refusals that stop the whole import. Nothing is changed.
var (
	errConflict    = refuse(http.StatusConflict, "import_conflict", "Some capcodes are given by rows that differ, so nothing was imported. Leave out or fix the rows, and import again.")
	errPlanChanged = refuse(http.StatusConflict, "import_changed", "The aliases changed since the dry run, so nothing was imported. Run the import again to see the new results.")
)

// IsConflict and IsPlanChanged tell a refused import's report apart.
func IsConflict(err error) bool    { return errors.Is(err, errConflict) }
func IsPlanChanged(err error) bool { return errors.Is(err, errPlanChanged) }

// errDryRun rolls back a dry run.
var errDryRun = errors.New("dry run")

// byImport records the import's changes in history (R57) as made by the
// signed-in admin. Each change's re-parse is covered by the one full
// re-parse the import queues.
func byImport(by By) By { return By{Source: SourceImport, UserID: by.UserID} }

type importRow struct {
	index                         int
	pattern, name, agency, colour string
	ignored                       bool
}

// clean trims a value; the literal "null" (as some exports write) is empty.
// It reverses the export's formula guard, so an export imports unchanged.
func clean(s string) string {
	s = strings.TrimSpace(s)
	if strings.EqualFold(s, "null") {
		return ""
	}
	return unguard(s)
}

// Import adds aliases, and with Update changes existing ones, from rows in
// one transaction (R27a, R30, R58). New agencies get a template each, with
// the most common colour among their rows and the profile from the mapping.
// An empty agency means no template. Rows for one capcode that are
// identical are squashed, keeping the first; rows that differ are a
// conflict, and then nothing changes. An existing alias is changed only
// with Update; otherwise its differences are reported. Every change is
// recorded in history as made by the admin, and every message is re-parsed
// once (R23a).
func (c *Catalog) Import(ctx context.Context, in []ImportRow, opts ImportOptions) (ImportReport, error) {
	rep := ImportReport{Rows: make([]RowResult, len(in)), Conflicts: []Conflict{}, TemplatesCreated: []string{}, RepeatedNames: []RepeatedName{}}
	if len(in) > MaxImportRows {
		return rep, refuse(http.StatusRequestEntityTooLarge, "too_many_rows", "An import takes at most %d rows.", MaxImportRows)
	}
	if err := checkProfiles(opts); err != nil {
		return rep, err
	}
	byPattern := map[string][]importRow{}
	var order []string
	for i, r := range in {
		row := importRow{index: i + 1, name: clean(r.Name), agency: clean(r.Template), colour: clean(r.Colour), ignored: r.Ignored}
		res := &rep.Rows[i]
		*res = RowResult{Index: row.index, Pattern: strings.TrimSpace(r.Pattern), Name: row.name}
		pattern, err := NormalisePattern(clean(r.Pattern))
		switch {
		case err != nil:
			res.Result, res.Problem = Invalid, "The address isn't a capcode or pattern (at most 7 digits, % and _)."
			continue
		case row.name == "":
			res.Result, res.Problem = Invalid, "The row has no alias name."
			continue
		}
		row.pattern, res.Pattern = pattern, pattern
		if _, seen := byPattern[pattern]; !seen {
			order = append(order, pattern)
		}
		byPattern[pattern] = append(byPattern[pattern], row)
	}
	// One row per capcode: identical rows squash; any that differ conflict.
	var rows []importRow
	for _, p := range order {
		cands := byPattern[p]
		if !identical(cands) {
			cf := Conflict{Pattern: p}
			for _, c := range cands {
				cf.Rows = append(cf.Rows, c.index)
			}
			rep.Conflicts = append(rep.Conflicts, cf)
			continue
		}
		for _, c := range cands[1:] {
			rep.Rows[c.index-1].Result, rep.Rows[c.index-1].SquashedInto = Squashed, cands[0].index
		}
		rows = append(rows, cands[0])
	}
	if len(rep.Conflicts) > 0 {
		return rep, errConflict
	}
	rep.RepeatedNames = repeatedNames(rows)

	colours := map[string]map[string]int{}
	for _, row := range rows {
		if row.agency == "" {
			continue
		}
		if colours[row.agency] == nil {
			colours[row.agency] = map[string]int{}
		}
		colours[row.agency][row.colour]++
	}
	agencies := make([]string, 0, len(colours))
	for a := range colours {
		agencies = append(agencies, a)
	}
	sort.Strings(agencies)

	by := byImport(opts.By)
	err := c.Store.InTx(ctx, func(q *db.Queries) error {
		ids := map[string]int64{}
		for _, agency := range agencies {
			if tpl, err := q.TemplateByName(ctx, agency); err == nil {
				ids[agency] = tpl.ID
				continue
			} else if !errors.Is(err, pgx.ErrNoRows) {
				return dbErr("template", err)
			}
			// The most common colour; ties go to the first in sort order.
			best, bestN := "", -1
			for col, n := range colours[agency] {
				if n > bestN || (n == bestN && col < best) {
					best, bestN = col, n
				}
			}
			id, err := q.CreateTemplate(ctx, db.CreateTemplateParams{Name: agency, Agency: agency, Colour: optional(best), AgencyProfile: optional(opts.AgencyProfiles[agency])})
			if err != nil {
				return dbErr("create template", err)
			}
			ids[agency] = id
			rep.TemplatesCreated = append(rep.TemplatesCreated, agency)
		}
		changed := 0
		for _, row := range rows {
			res := &rep.Rows[row.index-1]
			var tid *int64
			if row.agency != "" {
				id := ids[row.agency]
				tid = &id
			}
			cur, err := q.AliasDetailByKey(ctx, row.pattern)
			if errors.Is(err, pgx.ErrNoRows) {
				if _, err := addAlias(ctx, q, settings{pattern: row.pattern, name: row.name, tplID: tid, tplName: optional(row.agency), ignored: row.ignored}, by, reparses{}); err != nil {
					return fmt.Errorf("row %d: %w", row.index, err)
				}
				res.Result = Added
				changed++
				continue
			}
			if err != nil {
				return dbErr(fmt.Sprintf("row %d", row.index), err)
			}
			res.Changes = differences(cur, row)
			switch {
			case len(res.Changes) == 0:
				res.Result = Unchanged
			case !opts.Update:
				res.Result = Different
			default:
				ch := change{name: &row.name, setTemplate: true, tplID: tid, tplName: optional(row.agency), ignored: &row.ignored}
				if err := changeAlias(ctx, q, cur.ID, ch, by, reparses{}); err != nil {
					return fmt.Errorf("row %d: %w", row.index, err)
				}
				res.Result = Updated
				changed++
			}
		}
		rep.Plan = plan(rep)
		if opts.Plan != "" && opts.Plan != rep.Plan {
			return errPlanChanged
		}
		if opts.DryRun {
			return errDryRun
		}
		if changed > 0 {
			if err := reparse(ctx, q, "%", "alias import"); err != nil {
				return dbErr("request re-parse", err)
			}
		}
		return nil
	})
	switch {
	case errors.Is(err, errDryRun):
		return rep, nil
	case err != nil:
		return rep, err
	}
	rep.Applied = true
	return rep, nil
}

// checkProfiles refuses a mapping that names a profile that isn't loaded:
// a typing mistake would otherwise make that agency's pages text only.
func checkProfiles(opts ImportOptions) error {
	var unknown []string
	for agency, p := range opts.AgencyProfiles {
		if strings.TrimSpace(agency) == "" {
			return refuse(http.StatusUnprocessableEntity, "unknown_profile", "The agency profile mapping has an empty agency name.")
		}
		if p != "" && !slices.Contains(opts.Profiles, p) && !slices.Contains(unknown, p) {
			unknown = append(unknown, p)
		}
	}
	if len(unknown) == 0 {
		return nil
	}
	sort.Strings(unknown)
	known := slices.Clone(opts.Profiles)
	sort.Strings(known)
	if len(known) == 0 {
		known = []string{"(none)"}
	}
	return refuse(http.StatusUnprocessableEntity, "unknown_profile", "No agency profile is called %s. The loaded profiles are: %s.",
		strings.Join(quoteAll(unknown), ", "), strings.Join(known, ", "))
}

func quoteAll(ss []string) []string {
	out := make([]string, len(ss))
	for i, s := range ss {
		out[i] = strconv.Quote(s)
	}
	return out
}

// plan fingerprints every row's outcome and the templates to create.
func plan(rep ImportReport) string {
	b, _ := json.Marshal(struct {
		Rows      []RowResult `json:"r"`
		Templates []string    `json:"t"`
	}{rep.Rows, rep.TemplatesCreated})
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// differences lists how an existing alias differs from a row.
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

// identical reports whether rows agree in every imported field (R27a).
func identical(rows []importRow) bool {
	for _, r := range rows[1:] {
		if r.name != rows[0].name || r.agency != rows[0].agency || r.colour != rows[0].colour || r.ignored != rows[0].ignored {
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
	out := []RepeatedName{}
	for name, ps := range by {
		if len(ps) > 1 {
			sort.Strings(ps)
			out = append(out, RepeatedName{Name: name, Patterns: ps})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}
