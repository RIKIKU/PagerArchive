package profile

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// ADR 0047: the example agency moves its known call signs, alone, and its
// duty-officer roles from units into call_signs.
func TestExampleInto(t *testing.T) {
	set, err := LoadDir("../../profiles/example")
	if err != nil {
		t.Fatal(err)
	}
	in := set.Agencies["example-fire"].Formats[0].Into["units"]
	if in == nil || in.Field != StandardCallSigns || len(in.Values) != 4 || in.Match == nil {
		t.Fatalf("the example's into: %+v", in)
	}
	for token, want := range map[string]bool{
		"STN1": true, "STN4": true, "DO01": true, "DO99": true,
		"STN2T": false, "STN5": false, "stn1": false, "T1": false, "DO1": false, "DO001": false, "XDO01": false,
	} {
		if got := in.Moves(token); got != want {
			t.Errorf("%s moves: %v, want %v", token, got, want)
		}
	}
}

// intoDoc is an agency profile whose one format splits units, with into.
func intoDoc(into string) string {
	return `{"kind": "agency", "name": "x", "formats": [{"name": "a",
		"match": "^(?P<description>\\S+)(?: ALARM (?P<alarm_level>[0-9]))? UNITS (?P<units>.+)$",
		"fields": {"units": {"split": " ", "into": ` + into + `}}}]}`
}

// loadWith loads doc as x.json beside the files given, by name.
func loadWith(t *testing.T, doc string, files map[string]string) (*Set, error) {
	t.Helper()
	dir := t.TempDir()
	files["x.json"] = doc
	for name, s := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(s), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return LoadDir(dir)
}

// Problems with into stop the server and fail validate-profile, each naming
// the file and the path (R7, A128).
func TestIntoErrors(t *testing.T) {
	signs := map[string]string{"signs.txt": "STN1\nSTN2\n"}
	for _, c := range []struct {
		name, doc string
		files     map[string]string
		want      []string
	}{
		{"without split", `{"kind": "agency", "name": "x", "formats": [{"name": "a", "match": "^(?P<units>.+)$",
			"fields": {"units": {"into": {"field": "call_signs", "match": "DO[0-9]{2}"}}}}]}`, nil,
			[]string{"x.json", "at /formats/0/fields/units", "split"}},
		{"no field", intoDoc(`{"match": "DO[0-9]{2}"}`), nil, []string{"at /formats/0/fields/units/into", "field"}},
		{"the split field itself", intoDoc(`{"field": "units", "match": "DO[0-9]{2}"}`), nil,
			[]string{"at /formats/0/fields/units/into/field", `"units" is the field being split`}},
		{"a single-value standard field", intoDoc(`{"field": "job_number", "match": "DO[0-9]{2}"}`), nil,
			[]string{"at /formats/0/fields/units/into/field", `"job_number" is a single-value field`}},
		{"the call sign", intoDoc(`{"field": "call_sign", "match": "DO[0-9]{2}"}`), nil,
			[]string{"at /formats/0/fields/units/into/field", `"call_sign" is a single-value field`}},
		{"the call sign's old name", intoDoc(`{"field": "group", "match": "DO[0-9]{2}"}`), nil,
			[]string{"at /formats/0/fields/units/into/field", `"group" is a single-value field`}},
		{"not a field name", intoDoc(`{"field": "Call Signs", "match": "DO[0-9]{2}"}`), nil,
			[]string{"at /formats/0/fields/units/into/field", "not a field name"}},
		{"a field the format captures", intoDoc(`{"field": "alarm_level", "match": "DO[0-9]{2}"}`), nil,
			[]string{"at /formats/0/fields/units/into/field", `this format also captures or writes "alarm_level"`}},
		{"nothing moves", intoDoc(`{"field": "call_signs"}`), nil,
			[]string{"at /formats/0/fields/units/into", "into needs values, a values_file or a match"}},
		{"empty values", intoDoc(`{"field": "call_signs", "values": []}`), nil,
			[]string{"at /formats/0/fields/units/into/values"}},
		{"an empty value", intoDoc(`{"field": "call_signs", "values": ["STN1", ""]}`), nil,
			[]string{"at /formats/0/fields/units/into/values/1", "can't be empty"}},
		{"a value listed twice", intoDoc(`{"field": "call_signs", "values": ["STN1", "STN1"]}`), nil,
			[]string{"at /formats/0/fields/units/into/values/1", `value "STN1" is listed twice`}},
		{"a value with a space at its end", intoDoc(`{"field": "call_signs", "values": ["STN1 "]}`), nil,
			[]string{"at /formats/0/fields/units/into/values/0", "starts or ends with a space"}},
		{"a value the split would split", intoDoc(`{"field": "call_signs", "values": ["STN1-A", "DUTY OFFICER"]}`), nil,
			[]string{"at /formats/0/fields/units/into/values/1", `value "DUTY OFFICER" would be split`}},
		{"a value in both the profile and the file", intoDoc(`{"field": "call_signs", "values": ["STN2"], "values_file": "signs.txt"}`), signs,
			[]string{"at /formats/0/fields/units/into/values_file", `signs.txt line 2: value "STN2" is listed twice`}},
		{"a value twice in the file", intoDoc(`{"field": "call_signs", "values_file": "signs.txt"}`), map[string]string{"signs.txt": "STN1\n# a comment\n  STN1  \n"},
			[]string{"at /formats/0/fields/units/into/values_file", `signs.txt line 3: value "STN1" is listed twice`}},
		{"a values file that's missing", intoDoc(`{"field": "call_signs", "values_file": "nowhere.txt"}`), nil,
			[]string{"at /formats/0/fields/units/into/values_file", `values_file "nowhere.txt" can't be read`}},
		{"a values file with no values", intoDoc(`{"field": "call_signs", "values_file": "signs.txt"}`), map[string]string{"signs.txt": "# none yet\n\n"},
			[]string{"at /formats/0/fields/units/into/values_file", `values_file "signs.txt" has no values`}},
		{"a match that doesn't compile", intoDoc(`{"field": "call_signs", "match": "DO[0-9"}`), nil,
			[]string{"at /formats/0/fields/units/into/match", "missing closing ]"}},
		{"any other key", intoDoc(`{"field": "call_signs", "match": "DO[0-9]{2}", "pattern": "x"}`), nil,
			[]string{"at /formats/0/fields/units/into", "pattern"}},
		{"split and address_split", `{"kind": "agency", "name": "x", "formats": [{"name": "a", "match": "^(?P<_rest>.+)$",
			"fields": {"_rest": {"split": " ", "address_split": {"description": "description", "address": "address"}}}}]}`, nil,
			[]string{"at /formats/0/fields/_rest", "split or address_split, not both"}},
		{"large_event_fields on the target", strings.Replace(intoDoc(`{"field": "crew", "match": "DO[0-9]{2}"}`), `"formats":`,
			`"large_event_fields": [{"field": "crew", "values": ["DO01"]}], "formats":`, 1), nil,
			[]string{"at /large_event_fields/0/field", `"crew" is a list field`}},
	} {
		t.Run(c.name, func(t *testing.T) {
			files := map[string]string{}
			for k, v := range c.files {
				files[k] = v
			}
			_, err := loadWith(t, c.doc, files)
			if err == nil {
				t.Fatal("no error")
			}
			t.Log(err)
			for _, w := range c.want {
				if !strings.Contains(err.Error(), w) {
					t.Errorf("error %q doesn't contain %q", err, w)
				}
			}
		})
	}
}

// Every way of saying which tokens move loads: values, a file, a pattern,
// or any of them together; and into may aim at units or an extra field.
func TestIntoLoads(t *testing.T) {
	signs := map[string]string{"signs.txt": string([]byte{0xEF, 0xBB, 0xBF}) + "# Call signs\r\nSTN1\r\n\r\n  STN2\r\n"}
	for name, into := range map[string]string{
		"values":            `{"field": "call_signs", "values": ["STN1"]}`,
		"a file":            `{"field": "call_signs", "values_file": "signs.txt"}`,
		"a pattern":         `{"field": "call_signs", "match": "DO[0-9]{2}"}`,
		"all three":         `{"field": "call_signs", "values": ["STN3"], "values_file": "signs.txt", "match": "DO[0-9]{2}", "_comment": "x"}`,
		"into an extra one": `{"field": "crew", "match": "DO[0-9]{2}"}`,
	} {
		t.Run(name, func(t *testing.T) {
			set, err := loadWith(t, intoDoc(into), map[string]string{"signs.txt": signs["signs.txt"]})
			if err != nil {
				t.Fatal(err)
			}
			if in := set.Agencies["x"].Formats[0].Into["units"]; in == nil {
				t.Fatal("no into")
			}
		})
	}
	set, err := loadWith(t, intoDoc(`{"field": "call_signs", "values_file": "signs.txt"}`), map[string]string{"signs.txt": signs["signs.txt"]})
	if err != nil {
		t.Fatal(err)
	}
	if in := set.Agencies["x"].Formats[0].Into["units"]; len(in.Values) != 2 || !in.Values["STN1"] || !in.Values["STN2"] {
		t.Errorf("the file's values: %v", in.Values)
	}
	// Units taken from another split field.
	doc := `{"kind": "agency", "name": "x", "formats": [{"name": "a", "match": "^(?P<crew>.+)$",
		"fields": {"crew": {"split": " ", "into": {"field": "units", "match": "T[0-9]"}}}}]}`
	if _, err := loadWith(t, doc, map[string]string{}); err != nil {
		t.Errorf("into units: %v", err)
	}
}

// R23a, ADR 0047: the values file's values count in the parse hash, its
// comments and blank lines don't, and a profile without into hashes as it
// did before.
func TestIntoHash(t *testing.T) {
	doc := intoDoc(`{"field": "call_signs", "values_file": "signs.txt"}`)
	hash := func(file string) string {
		t.Helper()
		set, err := loadWith(t, doc, map[string]string{"signs.txt": file})
		if err != nil {
			t.Fatal(err)
		}
		return set.Agencies["x"].Hash
	}
	base := hash("STN1\nSTN2\n")
	if h := hash("# Call signs\nSTN1\n\n STN2\n"); h != base {
		t.Error("a comment and blank lines changed the hash")
	}
	if h := hash("STN1\nSTN3\n"); h == base {
		t.Error("a changed value didn't change the hash")
	}
	if h := hash("STN1\nSTN2\nSTN3\n"); h == base {
		t.Error("a value more didn't change the hash")
	}
	set, err := loadWith(t, doc, map[string]string{"signs.txt": "STN1\nSTN2\n"})
	if err != nil {
		t.Fatal(err)
	}
	a := set.Agencies["x"]
	if a.Hash == agencyHash([]byte(doc), nil) || a.FileHash != hashOf([]byte(doc)) {
		t.Error("the values aren't in the hash, or they're in the file's")
	}
	plain := `{"kind": "agency", "name": "x", "formats": [{"name": "a", "match": ""}]}`
	if agencyHash([]byte(plain), nil) != hashOf([]byte(plain)) {
		t.Error("a profile without into doesn't hash as before")
	}
}
