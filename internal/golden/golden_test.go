package golden

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/RIKIKU/PagerArchive/internal/profile"
	"github.com/RIKIKU/PagerArchive/internal/streets"
)

const exampleDir = "../../profiles/example"

func exampleRunner(t *testing.T) Runner {
	t.Helper()
	set, err := profile.LoadDir(exampleDir)
	if err != nil {
		t.Fatal(err)
	}
	n := set.Networks["example-network"]
	ds, err := streets.LoadFile(n.DatasetPath)
	if err != nil {
		t.Fatal(err)
	}
	return Runner{Set: set, Network: n, Streets: streets.NewIndex(ds.Streets, n.StreetTypes)}
}

// TestExampleGoldenTables runs every golden row in profiles/example/.
// Confirmed rows must pass; proposed rows are reported only (SPEC.md, How to use 10).
func TestExampleGoldenTables(t *testing.T) {
	r := exampleRunner(t)
	tables, err := LoadDir(exampleDir)
	if err != nil {
		t.Fatal(err)
	}
	if len(tables) != 2 {
		t.Fatalf("found %d golden tables, want 2", len(tables))
	}
	for _, tbl := range tables {
		res, err := r.Run(tbl)
		if err != nil {
			t.Fatal(err)
		}
		for _, row := range res.Rows {
			t.Run(tbl.Profile+"/"+row.Case.Name, func(t *testing.T) {
				for _, d := range row.Diffs {
					if row.Case.Proposed() {
						t.Logf("proposed row (not counted): %s", d)
					} else {
						t.Error(d)
					}
				}
			})
		}
	}
}

// The runner must catch a wrong expectation, an unlisted non-null field, and
// a wrong flag.
func TestRunnerDetectsMismatches(t *testing.T) {
	r := exampleRunner(t)
	raw := "POCSAG512: Address:  100002  Function: 0  Alpha:   @@EX0000002 GRS3 GRASS FIRE @ SAMPLE RD, MADEUPTON #STN2"
	full := `{"capcode":"0100002","priority":"Emergency","job_number":"EX0000002","event_type":"GRS3","description":"GRASS FIRE","address":"SAMPLE RD","locality":"MADEUPTON","group":"STN2"}`
	tests := []struct {
		name   string
		expect string
		diffs  int
	}{
		{"all correct, format unlisted", full, 0},
		{"wrong value", `{"capcode":"0100002","priority":"Emergency","job_number":"EX0000002","event_type":"GRS3","description":"GRASS","address":"SAMPLE RD","locality":"MADEUPTON","group":"STN2"}`, 1},
		{"unlisted field means null", `{"capcode":"0100002","priority":"Emergency","job_number":"EX0000002","event_type":"GRS3","description":"GRASS FIRE","address":"SAMPLE RD","group":"STN2"}`, 1},
		{"wrong format when listed", full[:len(full)-1] + `,"format":"follow-up"}`, 1},
		{"flag expected", full[:len(full)-1] + `,"parse_failed":true}`, 1},
		{"extra field expected", full[:len(full)-1] + `,"extra":{"area":"X"}}`, 1},
	}
	for _, tt := range tests {
		var expect map[string]json.RawMessage
		if err := json.Unmarshal([]byte(tt.expect), &expect); err != nil {
			t.Fatal(err)
		}
		res, err := r.Run(&Table{Profile: "example-fire", Cases: []Case{{Name: tt.name, Raw: raw, Expect: expect}}})
		if err != nil {
			t.Fatal(err)
		}
		if got := len(res.Rows[0].Diffs); got != tt.diffs {
			t.Errorf("%s: %d diffs %v, want %d", tt.name, got, res.Rows[0].Diffs, tt.diffs)
		}
	}
}

// opens_large_event (R87; owner, 2026-11-14), checked only when listed: an
// Emergency page with a field value the agency's large_event_fields lists,
// or a network code, opens a large event; a lower value, no value or
// another priority doesn't. Network tables can't check it.
func TestOpensLargeEventRows(t *testing.T) {
	r := exampleRunner(t)
	const head = "POCSAG512: Address:  100002  Function: 0  Alpha:   "
	tests := []struct {
		name, raw, expect string
		diffs             int
	}{
		{"listed level", "@@EX0000002 GRS3 GRASS FIRE @ SAMPLE RD, MADEUPTON ALARM 4 #STN2",
			`{"extra":{"alarm_level":"4"},"opens_large_event":true}`, 0},
		{"listed level, expected not to", "@@EX0000002 GRS3 GRASS FIRE @ SAMPLE RD, MADEUPTON ALARM 4 #STN2",
			`{"extra":{"alarm_level":"4"},"opens_large_event":false}`, 1},
		{"lower level", "@@EX0000002 GRS3 GRASS FIRE @ SAMPLE RD, MADEUPTON ALARM 1 #STN2",
			`{"extra":{"alarm_level":"1"},"opens_large_event":false}`, 0},
		{"lower level, expected to", "@@EX0000002 GRS3 GRASS FIRE @ SAMPLE RD, MADEUPTON ALARM 1 #STN2",
			`{"extra":{"alarm_level":"1"},"opens_large_event":true}`, 1},
		{"no level", "@@EX0000002 GRS3 GRASS FIRE @ SAMPLE RD, MADEUPTON #STN2", `{"opens_large_event":false}`, 0},
		{"a code", "@@EX0000002 GRS3 DG0801 GRASS FIRE @ SAMPLE RD, MADEUPTON #STN2", `{"opens_large_event":true}`, 0},
		{"Non-emergency", "HbRE EX0000002 ALARM 3 DG0801 #STN2", `{"opens_large_event":false}`, 0},
	}
	for _, tt := range tests {
		var expect map[string]json.RawMessage
		if err := json.Unmarshal([]byte(tt.expect), &expect); err != nil {
			t.Fatal(err)
		}
		res, err := r.Run(&Table{Profile: "example-fire", Cases: []Case{{Name: tt.name, Raw: head + tt.raw, Expect: expect}}})
		if err != nil {
			t.Fatal(err)
		}
		// Only the keys under test: the row lists no others, so each other
		// non-null field is a diff of its own.
		var diffs []string
		for _, d := range res.Rows[0].Diffs {
			if strings.HasPrefix(d, "opens_large_event") || strings.HasPrefix(d, "extra.") {
				diffs = append(diffs, d)
			}
		}
		if len(diffs) != tt.diffs {
			t.Errorf("%s: diffs %v, want %d", tt.name, diffs, tt.diffs)
		}
	}
	// In a network table it's an error, not silently skipped.
	res, err := r.Run(&Table{Profile: "example-network", Cases: []Case{{Name: "network", Raw: head + "@@DG0801",
		Expect: map[string]json.RawMessage{"priority": json.RawMessage(`"Emergency"`), "stored_text": json.RawMessage(`"@@DG0801"`), "opens_large_event": json.RawMessage(`true`)}}}})
	if err != nil {
		t.Fatal(err)
	}
	if d := res.Rows[0].Diffs; len(d) != 1 || !strings.Contains(d[0], "agency tables only") {
		t.Errorf("a network row listing opens_large_event: %v", d)
	}
}
