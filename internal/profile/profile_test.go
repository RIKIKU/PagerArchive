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

func TestNonProfileJSONIsSkipped(t *testing.T) {
	if err := loadOne(t, "golden-x.json", `{"profile": "x", "cases": []}`); err != nil {
		t.Fatal(err)
	}
	if err := loadOne(t, "list.json", `[1, 2]`); err != nil {
		t.Fatal(err)
	}
}
