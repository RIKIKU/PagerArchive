package profile

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func readExample(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("../../profiles/example", name))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func loadOne(t *testing.T, name, doc string) error {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(doc), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := LoadDir(dir)
	return err
}

func TestExampleLoads(t *testing.T) {
	set, err := LoadDir("../../profiles/example")
	if err != nil {
		t.Fatal(err)
	}
	n := set.Networks["example-network"]
	if n == nil || len(n.Redactions) != 3 || len(n.Ignored) != 1 || n.Ignored[0] != (CapcodeRange{40, 49}) {
		t.Fatalf("network: %+v", n)
	}
	a := set.Agencies["example-fire"]
	if a == nil || len(a.Formats) != 3 || a.Formats[2].Re != nil {
		t.Fatalf("agency: %+v", a)
	}
}

// Every problem names the file, the JSON path and the problem (R1, A14).
func TestProfileErrors(t *testing.T) {
	fire := readExample(t, "example-fire.json")
	net := readExample(t, "example-network.json")
	tests := []struct {
		name, file, doc string
		want            []string
	}{
		{
			name: "missing required key",
			file: "fire.json",
			doc:  strings.Replace(fire, `"formats":`, `"formatz":`, 1),
			want: []string{"fire.json", "formats"},
		},
		{
			name: "format missing match",
			file: "fire.json",
			doc:  strings.Replace(fire, `"match": ""`, `"matchx": ""`, 1),
			want: []string{"fire.json", "/formats/2", "match"},
		},
		{
			name: "pattern doesn't compile",
			file: "fire.json",
			doc:  strings.Replace(fire, `^(?P<job_number>EX`, `^(?P<job_number>EX(`, 1),
			want: []string{"fire.json", "at /formats/0/match", "missing closing )"},
		},
		{
			name: "split on a group that doesn't exist",
			file: "fire.json",
			doc:  strings.Replace(fire, `"units": { "split": " " }`, `"unitz": { "split": " " }`, 1),
			want: []string{"at /formats/0/fields/unitz", `no group named "unitz"`},
		},
		{
			name: "list field without split",
			file: "fire.json",
			doc:  strings.Replace(fire, `"units": { "split": " " }`, `"group": { "split": " " }`, 1),
			want: []string{"at /formats/0/fields/units", "list field"},
		},
		{
			name: "redaction alters its must-not-match example",
			file: "net.json",
			doc:  strings.Replace(net, `"must_not_match": ["PERSON COLLAPSED", "STAGE 2"]`, `"must_not_match": ["AGE: 5"]`, 1),
			want: []string{"net.json", "at /redaction_rules/1/must_not_match/0", `rule "age" alters its own must-not-match example`},
		},
		{
			name: "bad priority",
			file: "net.json",
			doc:  strings.Replace(net, `"QD": "Admin"`, `"QD": "Urgent"`, 1),
			want: []string{"net.json", "at /priority_markers/QD"},
		},
		{
			name: "three-character marker",
			file: "net.json",
			doc:  strings.Replace(net, `"QD": "Admin"`, `"QDX": "Admin"`, 1),
			want: []string{"net.json", "at /priority_markers/QDX", "exactly 2 characters"},
		},
		{
			name: "unknown key",
			file: "net.json",
			doc:  strings.Replace(net, `"street_types"`, `"street_typos"`, 1),
			want: []string{"net.json", "street_typos"},
		},
		{
			name: "deployment code pattern doesn't compile",
			file: "net.json",
			doc:  strings.Replace(net, `"deployment_code_pattern": "\\bDG[0-9]{4}\\b"`, `"deployment_code_pattern": "DG[0-9"`, 1),
			want: []string{"net.json", "at /deployment_code_pattern", "missing closing ]"},
		},
		{
			name: "deployment code pattern matches nothing at all",
			file: "net.json",
			doc:  strings.Replace(net, `"deployment_code_pattern": "\\bDG[0-9]{4}\\b"`, `"deployment_code_pattern": "x*"`, 1),
			want: []string{"net.json", "at /deployment_code_pattern", "matches empty text"},
		},
		{
			name: "large event field no format extracts",
			file: "fire.json",
			doc:  strings.Replace(fire, `"field": "alarm_level"`, `"field": "alarm_levle"`, 1),
			want: []string{"fire.json", "at /large_event_fields/0/field", `no format extracts a field named "alarm_levle"`},
		},
		{
			name: "large event field on an intermediate group",
			file: "fire.json",
			doc: strings.Replace(strings.Replace(fire, `(?P<alarm_level>`, `(?P<_alarm_level>`, 1),
				`"field": "alarm_level"`, `"field": "_alarm_level"`, 1),
			want: []string{"at /large_event_fields/0/field", `no format extracts a field named "_alarm_level"`},
		},
		{
			name: "large event field is a list",
			file: "fire.json",
			doc:  strings.Replace(fire, `"field": "alarm_level"`, `"field": "units"`, 1),
			want: []string{"at /large_event_fields/0/field", `"units" is a list field`},
		},
		{
			name: "large event field with an empty value",
			file: "fire.json",
			doc:  strings.Replace(fire, `"values": ["3", "4"`, `"values": ["", "4"`, 1),
			want: []string{"fire.json", "/large_event_fields/0/values/0"},
		},
		{
			name: "large event field with no values",
			file: "fire.json",
			doc:  strings.Replace(fire, `"values": ["3", "4", "5", "6", "7", "8", "9"]`, `"values": []`, 1),
			want: []string{"fire.json", "/large_event_fields/0/values"},
		},
		{
			name: "large event field value listed twice",
			file: "fire.json",
			doc:  strings.Replace(fire, `"values": ["3", "4"`, `"values": ["3", "3"`, 1),
			want: []string{"fire.json", "/large_event_fields/0/values"},
		},
		{
			name: "large event field value with a space at its end",
			file: "fire.json",
			doc:  strings.Replace(fire, `"values": ["3", "4"`, `"values": ["3 ", "4"`, 1),
			want: []string{"at /large_event_fields/0/values/0", "starts or ends with a space"},
		},
		{
			name: "large event rule without values",
			file: "fire.json",
			doc:  strings.Replace(fire, `"values":`, `"valuez":`, 1),
			want: []string{"fire.json", "/large_event_fields/0"},
		},
		{
			name: "large event rule that isn't an object",
			file: "fire.json",
			doc:  strings.Replace(fire, `"large_event_fields": [`, `"large_event_fields": ["alarm_level",`, 1),
			want: []string{"fire.json", "/large_event_fields/0"},
		},
		{
			name: "large event field with two rules",
			file: "fire.json",
			doc: strings.Replace(fire, `"large_event_fields": [`,
				`"large_event_fields": [ { "field": "alarm_level", "values": ["2"] },`, 1),
			want: []string{"at /large_event_fields/1/field", `field "alarm_level" has a rule already`},
		},
		{
			name: "invalid JSON",
			file: "net.json",
			doc:  `{"kind": "network",`,
			want: []string{"net.json", "not valid JSON"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := loadOne(t, tt.file, tt.doc)
			if err == nil {
				t.Fatal("no error")
			}
			for _, w := range tt.want {
				if !strings.Contains(err.Error(), w) {
					t.Errorf("error %q doesn't contain %q", err, w)
				}
			}
		})
	}
}

// R87 (owner, 2026-11-14): a field's listed value opens a large event,
// compared exactly as extracted.
func TestOpensLargeEvent(t *testing.T) {
	set, err := LoadDir("../../profiles/example")
	if err != nil {
		t.Fatal(err)
	}
	a := set.Agencies["example-fire"]
	if len(a.LargeEventFields) != 1 || a.LargeEventFields[0].Field != "alarm_level" || len(a.LargeEventFields[0].Values) != 7 {
		t.Fatalf("the example's rules: %+v", a.LargeEventFields)
	}
	for _, c := range []struct {
		name   string
		fields map[string]any
		want   bool
	}{
		{"listed", map[string]any{"alarm_level": "3"}, true},
		{"highest listed", map[string]any{"alarm_level": "9"}, true},
		{"lower", map[string]any{"alarm_level": "2"}, false},
		{"not listed, though higher by number", map[string]any{"alarm_level": "10"}, false},
		{"no level", map[string]any{"job_number": "EX0000001"}, false},
		{"a listed value in another field", map[string]any{"description": "3"}, false},
		{"a list", map[string]any{"alarm_level": []string{"3"}}, false},
	} {
		if got := a.OpensLargeEvent(c.fields); got != c.want {
			t.Errorf("%s: %v, want %v", c.name, got, c.want)
		}
	}
	var none *Agency
	if none.OpensLargeEvent(map[string]any{"alarm_level": "3"}) {
		t.Error("no agency opened an event")
	}
}

// large_event_fields takes any text field a format extracts: a named group
// or an address_split output, in any format; not one that only a format
// splits into a list.
func TestLargeEventFieldSources(t *testing.T) {
	agency := func(rule string) string {
		return `{"kind": "agency", "name": "made-up", "formats": [
			{"name": "a", "match": "^(?P<level>[0-9]A) (?P<_rest>.*)$", "fields": {"_rest": {"address_split": {"description": "what", "address": "address"}}}},
			{"name": "b", "match": "^(?P<crews>.*)$", "fields": {"crews": {"split": " "}}}
		], "large_event_fields": [` + rule + `]}`
	}
	for _, field := range []string{"level", "what", "address"} {
		if err := loadOne(t, "a.json", agency(`{"field": "`+field+`", "values": ["3A"]}`)); err != nil {
			t.Errorf("%s: %v", field, err)
		}
	}
	err := loadOne(t, "a.json", agency(`{"field": "crews", "values": ["3A"]}`))
	if err == nil || !strings.Contains(err.Error(), `"crews" is a list field`) {
		t.Errorf("a split field: %v", err)
	}
	err = loadOne(t, "a.json", agency(`{"field": "_rest", "values": ["3A"]}`))
	if err == nil || !strings.Contains(err.Error(), `no format extracts a field named "_rest"`) {
		t.Errorf("an address_split's own group: %v", err)
	}
}

func TestNonProfileJSONIsSkipped(t *testing.T) {
	if err := loadOne(t, "golden-x.json", `{"profile": "x", "cases": []}`); err != nil {
		t.Fatal(err)
	}
	if err := loadOne(t, "list.json", `[1, 2]`); err != nil {
		t.Fatal(err)
	}
}
