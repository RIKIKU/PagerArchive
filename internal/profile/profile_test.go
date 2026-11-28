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
	// The example agency's into reads its call signs from beside it.
	if err := os.WriteFile(filepath.Join(dir, exampleCallSigns), []byte(readExample(t, exampleCallSigns)), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := LoadDir(dir)
	return err
}

const exampleCallSigns = "example-call-signs.txt"

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
			doc:  strings.Replace(fire, `"units": {`, `"unitz": {`, 1),
			want: []string{"at /formats/0/fields/unitz", `no group named "unitz"`},
		},
		{
			name: "list field without split",
			file: "fire.json",
			doc:  strings.Replace(fire, `"units": {`, `"call_sign": {`, 1),
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
			name: "fireground channel pattern doesn't compile",
			file: "net.json",
			doc:  strings.Replace(net, `"fireground_channel_pattern": "\\bFGND ?(?P<number>[0-9A-Z]+)\\b"`, `"fireground_channel_pattern": "FGND (?P<number>[0-9"`, 1),
			want: []string{"net.json", "at /fireground_channel_pattern", "missing closing ]"},
		},
		{
			name: "fireground channel pattern matches empty text",
			file: "net.json",
			doc:  strings.Replace(net, `"fireground_channel_pattern": "\\bFGND ?(?P<number>[0-9A-Z]+)\\b"`, `"fireground_channel_pattern": "[0-9]*"`, 1),
			want: []string{"net.json", "at /fireground_channel_pattern", "matches empty text"},
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

// R88d: a network profile's areas are left out of its parse hash, wherever
// the member is, so adding, changing or removing them re-parses nothing;
// anything else changes it.
func TestParseHashLeavesOutAreas(t *testing.T) {
	const areas = `"areas": [{ "id": "zones", "path": "zones.geojson", "label": "Zone", "name_property": "zone", "attribution": "Made up" }]`
	without := "{\n  \"kind\": \"network\",\n  \"name\": \"n\",\n  \"street_types\": {}\n}\n"
	for name, with := range map[string]string{
		"last":   "{\n  \"kind\": \"network\",\n  \"name\": \"n\",\n  \"street_types\": {},\n  " + areas + "\n}\n",
		"first":  "{\n  " + areas + ",\n  \"kind\": \"network\",\n  \"name\": \"n\",\n  \"street_types\": {}\n}\n",
		"middle": "{\n  \"kind\": \"network\",\n  " + areas + ",\n  \"name\": \"n\",\n  \"street_types\": {}\n}\n",
	} {
		if parseHash([]byte(with)) != hashOf([]byte(without)) {
			t.Errorf("areas %s: the hash isn't the profile's without them", name)
		}
	}
	if parseHash([]byte(without)) != hashOf([]byte(without)) {
		t.Error("with no areas, the hash isn't the file's")
	}
	if parseHash([]byte(strings.Replace(without, `"n"`, `"m"`, 1))) == parseHash([]byte(without)) {
		t.Error("a change outside the areas left the hash as it was")
	}
	// An "areas" key deeper down is the profile's own business.
	nested := strings.Replace(without, `"street_types": {}`, `"street_types": {"areas": "AREAS"}`, 1)
	if parseHash([]byte(nested)) == hashOf([]byte(without)) {
		t.Error("an areas key inside another member was left out")
	}

	// The example: its areas were added after its pages were parsed, so its
	// hash is still what it was without them.
	b, err := os.ReadFile("../../profiles/example/example-network.json")
	if err != nil {
		t.Fatal(err)
	}
	s := string(b)
	i := strings.Index(s, ",\n  \"areas\": [")
	if i < 0 {
		t.Fatal("the example names no areas")
	}
	j := strings.Index(s[i:], "\n  ]") + i + len("\n  ]")
	n, errs := loadNetwork("example-network.json", b)
	if len(errs) > 0 {
		t.Fatal(errs)
	}
	if n.Hash != hashOf([]byte(s[:i]+s[j:])) || len(n.Areas) != 2 || n.Areas[0].Label != "Zone" || filepath.Base(n.Areas[0].Path) != "example-areas.geojson" || n.Areas[1].ID != "regions" || n.Areas[1].Outline != "dashed" {
		t.Errorf("the example: hash %s, areas %+v", n.Hash, n.Areas)
	}
}

// R88: layers of areas: each one's outline defaults by its place in the
// list, and two with one id are refused, naming where.
func TestAreasLayers(t *testing.T) {
	b, err := os.ReadFile("../../profiles/example/example-network.json")
	if err != nil {
		t.Fatal(err)
	}
	s := strings.ReplaceAll(string(b), ",\n      \"outline\": \"dashed\"", "")
	s = strings.ReplaceAll(s, ",\n      \"outline\": \"solid\"", "")
	n, errs := loadNetwork("example-network.json", []byte(s))
	if len(errs) > 0 {
		t.Fatal(errs)
	}
	if n.Areas[0].Outline != "solid" || n.Areas[1].Outline != "dashed" {
		t.Errorf("default outlines: %q %q, want solid, dashed", n.Areas[0].Outline, n.Areas[1].Outline)
	}
	_, errs = loadNetwork("example-network.json", []byte(strings.Replace(string(b), `"id": "regions"`, `"id": "zones"`, 1)))
	if len(errs) != 1 || !strings.Contains(errs[0].Error(), "/areas/1/id") || !strings.Contains(errs[0].Error(), `duplicate areas id "zones"`) {
		t.Errorf("two layers named zones: %v", errs)
	}
	_, errs = loadNetwork("example-network.json", []byte(strings.Replace(string(b), `"outline": "dashed"`, `"outline": "wavy"`, 1)))
	if len(errs) == 0 {
		t.Error("an outline that isn't solid, dashed or dotted was accepted")
	}
}

// ADR 0040 (R8): the call sign's old name, group, is read as call_sign
// wherever an agency profile names it (a capture in either spelling, its
// fields options, labels and a large_event_fields rule), with a warning
// for each; naming both is an error. The hashes are the file's, so an
// unchanged profile re-parses nothing.
func TestOldCallSignName(t *testing.T) {
	const old = `{"kind": "agency", "name": "made-up", "formats": [
		{"name": "a", "match": "^(?P<job_number>[0-9]+) #(?P<group>\\S+)$"},
		{"name": "b", "match": "^RE #(?<group>\\S+) \\(?P<group>$"}
	], "labels": {"group": {"STN1": "Station 1"}},
	"large_event_fields": [{"field": "group", "values": ["STN9"]}]}`
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "a.json"), []byte(old), 0o600); err != nil {
		t.Fatal(err)
	}
	set, err := LoadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	a := set.Agencies["made-up"]
	if got := a.Formats[0].Re.SubexpNames(); got[2] != "call_sign" {
		t.Errorf("format a's captures: %q", got)
	}
	// The second capture is text, after a backslash: left as it is.
	if got := a.Formats[1].Re.String(); got != `^RE #(?<call_sign>\S+) \(?P<group>$` {
		t.Errorf("format b: %s", got)
	}
	if a.Labels["call_sign"]["STN1"] != "Station 1" || a.Labels["group"] != nil {
		t.Errorf("labels: %v", a.Labels)
	}
	if len(a.LargeEventFields) != 1 || a.LargeEventFields[0].Field != "call_sign" {
		t.Errorf("large_event_fields: %+v", a.LargeEventFields)
	}
	want := []string{"/formats/0/match", "/formats/1/match", "/labels/group", "/large_event_fields/0/field"}
	if w := set.Warnings(); len(w) != len(want) {
		t.Errorf("warnings: %q", w)
	} else {
		for i, p := range want {
			if !strings.Contains(w[i], "at "+p+": the call sign's field is now call_sign (ADR 0040)") {
				t.Errorf("warning %d: %q, want one at %s", i, w[i], p)
			}
		}
	}
	if a.FileHash != hashOf([]byte(old)) || a.Hash != agencyHash([]byte(old), nil) {
		t.Error("the hash isn't the file's")
	}

	for name, doc := range map[string]string{
		"both captures": `{"kind": "agency", "name": "x", "formats": [{"name": "a", "match": "^(?P<group>\\S+) (?P<call_sign>\\S+)$"}]}`,
		"both labels":   `{"kind": "agency", "name": "x", "formats": [{"name": "a", "match": "^(?P<call_sign>\\S+)$"}], "labels": {"group": {}, "call_sign": {}}}`,
		"both options":  `{"kind": "agency", "name": "x", "formats": [{"name": "a", "match": "^(?P<call_sign>\\S+)$", "fields": {"group": {"split": " "}, "call_sign": {"split": " "}}}]}`,
		"address_split": `{"kind": "agency", "name": "x", "formats": [{"name": "a", "match": "^(?P<_rest>.*)$", "fields": {"_rest": {"address_split": {"description": "group", "address": "address"}}}}]}`,
	} {
		if err := loadOne(t, "x.json", doc); err == nil || !strings.Contains(err.Error(), "old name (ADR 0040)") {
			t.Errorf("%s: %v", name, err)
		}
	}
}

// R7, R49a: an agency's level fields, in order, and what's refused.
func TestLevels(t *testing.T) {
	set, err := LoadDir("../../profiles/example")
	if err != nil {
		t.Fatal(err)
	}
	lf := set.Agencies["example-fire"].LevelField("alarm_level")
	if lf == nil || lf.Label != "Alarm level" || len(lf.Levels) != 9 || lf.Levels[0] != (Level{"1", "First alarm"}) {
		t.Fatalf("the example's levels: %+v", lf)
	}
	for v, want := range map[string]int{"1": 0, "2": 1, "9": 8, "10": -1, "02": -1, "": -1} {
		if got := lf.Rank(v); got != want {
			t.Errorf("rank of %q: %d, want %d", v, got, want)
		}
	}
	if set.Agencies["example-fire"].LevelField("call_sign") != nil {
		t.Error("a field with no levels has some")
	}
	const formats = `"formats": [{"name": "a", "match": "^(?P<job_number>[0-9]+) (?P<level>\\S+) (?P<units>.*)$", "fields": {"units": {"split": " "}}}]`
	doc := func(levels string) string {
		return `{"kind": "agency", "name": "x", ` + formats + `, "levels": [` + levels + `]}`
	}
	if err := loadOne(t, "x.json", doc(`{"field": "level", "label": "Level", "values": [{"value": "L1", "label": "One"}, {"value": "L2", "label": "Two"}]}`)); err != nil {
		t.Errorf("a valid list: %v", err)
	}
	for name, c := range map[string]struct{ levels, want string }{
		"unknown field":  {`{"field": "nope", "label": "L", "values": [{"value": "1", "label": "One"}]}`, `/levels/0/field: no format extracts a field named "nope"`},
		"list field":     {`{"field": "units", "label": "L", "values": [{"value": "1", "label": "One"}]}`, `/levels/0/field: "units" is a list field`},
		"two lists":      {`{"field": "level", "label": "L", "values": [{"value": "1", "label": "One"}]}, {"field": "level", "label": "M", "values": [{"value": "1", "label": "One"}]}`, `/levels/1/field: field "level" has a level list already`},
		"value twice":    {`{"field": "level", "label": "L", "values": [{"value": "1", "label": "One"}, {"value": "1", "label": "Uno"}]}`, `/levels/0/values/1/value: value "1" is listed twice`},
		"label twice":    {`{"field": "level", "label": "L", "values": [{"value": "1", "label": "One"}, {"value": "2", "label": "One"}]}`, `/levels/0/values/1/label: label "One" is given to two levels`},
		"spaced value":   {`{"field": "level", "label": "L", "values": [{"value": "1 ", "label": "One"}]}`, `starts or ends with a space`},
		"blank label":    {`{"field": "level", "label": "L", "values": [{"value": "1", "label": " "}]}`, `/levels/0/values/0/label: a level's label can't be blank`},
		"no field label": {`{"field": "level", "values": [{"value": "1", "label": "One"}]}`, `/levels/0`},
		"no level label": {`{"field": "level", "label": "L", "values": [{"value": "1"}]}`, `/levels/0/values/0`},
		"no values":      {`{"field": "level", "label": "L", "values": []}`, `/levels/0/values`},
		"another key":    {`{"field": "level", "label": "L", "values": [{"value": "1", "label": "One"}], "order": "up"}`, `/levels/0`},
	} {
		if err := loadOne(t, "x.json", doc(c.levels)); err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: %v, want %q", name, err, c.want)
		}
	}
}

// ADR 0045: an agency profile's hash leaves out its labels, levels,
// description and comments, so changing only those re-parses nothing;
// anything else, a format above all, changes it. FileHash is the whole
// file's, as the hash was before.
func TestAgencyHashLeavesOutDisplayOnly(t *testing.T) {
	path := "../../profiles/example/example-fire.json"
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	s := string(b)
	load := func(doc string) *Agency {
		t.Helper()
		a, errs := loadAgency(path, []byte(doc))
		if len(errs) > 0 {
			t.Fatal(errs)
		}
		return a
	}
	base := load(s)
	if base.FileHash != hashOf(b) || base.Hash == base.FileHash {
		t.Errorf("hashes: %s, file %s", base.Hash, base.FileHash)
	}
	for name, doc := range map[string]string{
		"a label":          strings.Replace(s, `"House fire"`, `"Building fire"`, 1),
		"a level's label":  strings.Replace(s, `"Second alarm"`, `"Two alarm"`, 1),
		"the field label":  strings.Replace(s, `"label": "Alarm level"`, `"label": "Alarm"`, 1),
		"a level more":     strings.Replace(s, `{ "value": "9", "label": "Ninth alarm" }`, `{ "value": "9", "label": "Ninth alarm" }, { "value": "0", "label": "Tenth alarm" }`, 1),
		"the description":  strings.Replace(s, `"Example Fire Service"`, `"Example Fire Brigade"`, 1),
		"a comment":        strings.Replace(s, `(SPEC.md section 10).`, `(SPEC.md section 10, made up).`, 1),
		"no levels at all": cutLevels(t, s),
		"field names": strings.Replace(s, `"labels": {`, `"field_names": {"alarm_level": "Alarm"},
  "labels": {`, 1),
	} {
		if doc == s {
			t.Fatalf("%s: nothing changed", name)
		}
		if a := load(doc); a.Hash != base.Hash || a.FileHash == base.FileHash {
			t.Errorf("%s: the hash changed (%v) or the file's didn't (%v)", name, a.Hash != base.Hash, a.FileHash == base.FileHash)
		}
	}
	for name, doc := range map[string]string{
		"a format":           strings.Replace(s, `(?: GRID (?P<grid_ref>[0-9]{6}))?`, `(?: GRID (?P<grid_ref>[0-9]{5,6}))?`, 1),
		"large_event_fields": strings.Replace(s, `"values": ["3", "4",`, `"values": ["4",`, 1),
	} {
		if doc == s {
			t.Fatalf("%s: nothing changed", name)
		}
		if load(doc).Hash == base.Hash {
			t.Errorf("%s: the hash didn't change", name)
		}
	}
	// With none of those members, the hash is the file's.
	bare := `{"kind": "agency", "name": "x", "formats": [{"name": "a", "match": ""}]}`
	if a := load(bare); a.Hash != hashOf([]byte(bare)) {
		t.Error("with nothing to leave out, the hash isn't the file's")
	}
}

// cutLevels is the example agency profile without its levels member.
func cutLevels(t *testing.T, s string) string {
	t.Helper()
	i := strings.Index(s, ",\n  \"levels\": [")
	j := strings.Index(s, "\n  ],\n  \"labels\"")
	if i < 0 || j < i {
		t.Fatal("the example's levels aren't where they were")
	}
	return s[:i] + s[j+len("\n  ]"):]
}
