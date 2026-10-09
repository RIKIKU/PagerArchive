// Package golden runs golden tables against profiles (SPEC.md R6, R10) under
// the rules in ADR 0001 section 2.5: a key a row doesn't list means null, or
// false for flags; format is checked only when listed.
package golden

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"

	"github.com/RIKIKU/PagerArchive/internal/decoder"
	"github.com/RIKIKU/PagerArchive/internal/pipeline"
	"github.com/RIKIKU/PagerArchive/internal/profile"
	"github.com/RIKIKU/PagerArchive/internal/streets"
	"github.com/RIKIKU/PagerArchive/schemas"
)

// Table is one golden table file.
type Table struct {
	File    string
	Profile string `json:"profile"`
	Cases   []Case `json:"cases"`
}

// Case is one golden row.
type Case struct {
	Name   string                     `json:"name"`
	Status string                     `json:"status"` // "proposed" rows don't fail the run
	Raw    string                     `json:"raw"`
	Expect map[string]json.RawMessage `json:"expect"`
}

// Proposed reports whether the row awaits the owner's confirmation.
func (c Case) Proposed() bool { return c.Status == "proposed" }

// LoadDir loads every golden-*.json under dir.
func LoadDir(dir string) ([]*Table, error) {
	var tables []*Table
	err := filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || !strings.HasPrefix(d.Name(), "golden-") || !strings.HasSuffix(d.Name(), ".json") {
			return nil
		}
		t, err := LoadFile(path)
		if err != nil {
			return err
		}
		tables = append(tables, t)
		return nil
	})
	return tables, err
}

// LoadFile loads and validates one golden table.
func LoadFile(path string) (*Table, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	if probs := schemas.Validate(schemas.GoldenTable, data); len(probs) > 0 {
		return nil, fmt.Errorf("%s: %s", path, probs[0])
	}
	t := &Table{File: path}
	if err := json.Unmarshal(data, t); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return t, nil
}

// Runner pairs tables with profiles. Agency tables run with Network and
// Streets; network tables run with the network profile they name.
type Runner struct {
	Set     *profile.Set
	Network *profile.Network
	Streets *streets.Index
}

// Result is the outcome of one table.
type Result struct {
	Table *Table
	Kind  string // "network" or "agency"
	Rows  []Row
}

// Row is the outcome of one golden row. It passed if Diffs is empty.
type Row struct {
	Case  Case
	Diffs []string
}

// Counts returns passed and failed rows, split by status.
func (r Result) Counts() (passed, failed, proposedFailed int) {
	for _, row := range r.Rows {
		switch {
		case len(row.Diffs) == 0:
			passed++
		case row.Case.Proposed():
			proposedFailed++
		default:
			failed++
		}
	}
	return
}

// Run runs one table.
func (r Runner) Run(t *Table) (Result, error) {
	res := Result{Table: t}
	if n, ok := r.Set.Networks[t.Profile]; ok {
		res.Kind = "network"
		for _, c := range t.Cases {
			res.Rows = append(res.Rows, Row{Case: c, Diffs: networkRow(n, c)})
		}
		return res, nil
	}
	if a, ok := r.Set.Agencies[t.Profile]; ok {
		if r.Network == nil {
			return res, fmt.Errorf("%s: agency table needs a network profile", t.File)
		}
		res.Kind = "agency"
		for _, c := range t.Cases {
			res.Rows = append(res.Rows, Row{Case: c, Diffs: agencyRow(r.Network, a, r.Streets, c)})
		}
		return res, nil
	}
	return res, fmt.Errorf("%s: no profile named %q", t.File, t.Profile)
}

func networkRow(n *profile.Network, c Case) []string {
	rec, ok := decoder.Multimon{}.Decode(c.Raw)
	if !ok {
		return []string{"raw line isn't a page"}
	}
	p := pipeline.Prepare(n, rec.Capcode, rec.Text)
	got := map[string]any{"priority": nil, "stored_text": nil, "heartbeat": p.Heartbeat}
	if !p.Dropped {
		got["stored_text"] = p.Text
		got["priority"] = priority(p.Priority)
	}
	return compare(c.Expect, got, map[string]any{"heartbeat": false})
}

func agencyRow(n *profile.Network, a *profile.Agency, ix *streets.Index, c Case) []string {
	rec, ok := decoder.Multimon{}.Decode(c.Raw)
	if !ok {
		return []string{"raw line isn't a page"}
	}
	p := pipeline.Prepare(n, rec.Capcode, rec.Text)
	if p.Dropped {
		return []string{"page dropped: ignored capcode"}
	}
	parsed := pipeline.Parse(n, a, ix, p)
	got := map[string]any{
		"capcode":         p.Capcode,
		"priority":        priority(p.Priority),
		"parse_failed":    parsed.ParseFailed,
		"address_unsplit": parsed.AddressUnsplit,
	}
	extra := map[string]any{}
	std := map[string]bool{}
	for _, f := range profile.StandardFields {
		std[f] = true
		got[f] = parsed.Fields[f]
	}
	for k, v := range parsed.Fields {
		if !std[k] {
			extra[k] = v
		}
	}
	got["extra"] = extra
	if _, listed := c.Expect["format"]; listed {
		got["format"] = nilIfEmpty(parsed.Format)
	}
	return compare(c.Expect, got, map[string]any{"parse_failed": false, "address_unsplit": false, "extra": map[string]any{}})
}

func priority(p profile.Priority) any { return nilIfEmpty(string(p)) }

func nilIfEmpty(s string) any {
	if s == "" {
		return nil
	}
	return s
}

// compare checks every key in got. A key missing from want takes its value
// from defaults, else null. Extra fields are compared one by one.
func compare(want map[string]json.RawMessage, got, defaults map[string]any) []string {
	var diffs []string
	keys := make([]string, 0, len(got))
	for k := range got {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		var w any = defaults[k]
		if raw, ok := want[k]; ok {
			w = decode(raw)
		}
		g := normalise(got[k])
		if k == "extra" {
			diffs = append(diffs, compareExtra(w.(map[string]any), g)...)
			continue
		}
		if !reflect.DeepEqual(w, g) {
			diffs = append(diffs, fmt.Sprintf("%s: want %s, got %s", k, show(w), show(g)))
		}
	}
	return diffs
}

func compareExtra(want map[string]any, got any) []string {
	g, _ := got.(map[string]any)
	names := map[string]bool{}
	for k := range want {
		names[k] = true
	}
	for k := range g {
		names[k] = true
	}
	sorted := make([]string, 0, len(names))
	for k := range names {
		sorted = append(sorted, k)
	}
	sort.Strings(sorted)
	var diffs []string
	for _, k := range sorted {
		if !reflect.DeepEqual(want[k], g[k]) {
			diffs = append(diffs, fmt.Sprintf("extra.%s: want %s, got %s", k, show(want[k]), show(g[k])))
		}
	}
	return diffs
}

func decode(raw json.RawMessage) any {
	var v any
	_ = json.Unmarshal(raw, &v)
	return v
}

// normalise gives got values the shapes json.Unmarshal gives want values.
func normalise(v any) any {
	data, _ := json.Marshal(v)
	return decode(data)
}

func show(v any) string {
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(v)
	return strings.TrimSpace(b.String())
}
