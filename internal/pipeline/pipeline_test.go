package pipeline

import (
	"bytes"
	"testing"

	"github.com/RIKIKU/PagerArchive/internal/profile"
	"github.com/RIKIKU/PagerArchive/internal/streets"
)

func example(t *testing.T) (*profile.Network, *profile.Agency) {
	t.Helper()
	set, err := profile.LoadDir("../../profiles/example")
	if err != nil {
		t.Fatal(err)
	}
	return set.Networks["example-network"], set.Agencies["example-fire"]
}

func TestCleanControlTokens(t *testing.T) {
	tests := map[string]string{
		"Hall unlocked<NUL><NUL>": "Hall unlocked",
		"line one<CR><LF>two":     "line one  two",
		"<ETX>x<DC3>y<DEL>":       "xy",
		"keep <b> and <FOO>":      "keep <b> and <FOO>",
		"[GRP1_] <FS>":            "[GRP1_] ",
	}
	for in, want := range tests {
		if got := CleanControlTokens(in); got != want {
			t.Errorf("CleanControlTokens(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestPrepare(t *testing.T) {
	n, _ := example(t)
	tests := []struct {
		capcode, raw string
		want         Prepared
	}{
		{"0100001", "@@EX0000001 X", Prepared{Capcode: "0100001", Text: "@@EX0000001 X", Priority: profile.Emergency}},
		{"0100001", "HbRE", Prepared{Capcode: "0100001", Text: "HbRE", Priority: profile.NonEmergency}},
		{"0100001", "qdlower", Prepared{Capcode: "0100001", Text: "qdlower"}}, // markers are case-sensitive
		{"0100001", "Q", Prepared{Capcode: "0100001", Text: "Q"}},
		{"0000040", "@@x", Prepared{Capcode: "0000040", Dropped: true}},
		{"0000049", "@@x", Prepared{Capcode: "0000049", Dropped: true}},
		{"0000050", "QDbeat", Prepared{Capcode: "0000050", Text: "QDbeat", Priority: profile.Admin, Heartbeat: true}},
		{"0000051", "QDbeat", Prepared{Capcode: "0000051", Text: "QDbeat", Priority: profile.Admin}},
		// Each match becomes one REDACTED; dedup and storage see only this.
		{"0100001", "QDCall 0412 345 678 or 0412345678", Prepared{Capcode: "0100001", Text: "QDCall REDACTED or REDACTED", Priority: profile.Admin}},
	}
	for _, tt := range tests {
		if got := Prepare(n, tt.capcode, tt.raw); got != tt.want {
			t.Errorf("Prepare(%s, %q) = %+v, want %+v", tt.capcode, tt.raw, got, tt.want)
		}
	}
}

// A rule with a "redact" group replaces only that group: here a mid-text
// NOTE: up to, but not including, a trailing [group].
func TestRedactGroup(t *testing.T) {
	set, err := loadAgencyJSON(t, `{
	  "kind": "network", "name": "n",
	  "priority_markers": {"QD": "Admin"}, "ignored_capcodes": [], "heartbeat_capcodes": [],
	  "redaction_rules": [{
	    "name": "note",
	    "pattern": "\\s(?P<redact>NOTE:.*?)(?: ?\\[[^\\[\\]]*\\])?$",
	    "must_not_match": ["QDNOTE: leading label is a page type [GRP1]"]
	  }],
	  "job_number_patterns": [], "event_categories": [],
	  "address_dataset": {"path": "x.json", "attribution": "x"}, "street_types": {}
	}`)
	if err != nil {
		t.Fatal(err)
	}
	n := set.Networks["n"]
	tests := map[string]string{
		"QDsee NOTE: secret stuff [GRP1]": "QDsee REDACTED [GRP1]",
		"QDsee NOTE: secret [x] y [GRP1]": "QDsee REDACTED [GRP1]",
		"QDsee NOTE: secret to the end":   "QDsee REDACTED",
		"QDNOTE: leading stays [GRP1]":    "QDNOTE: leading stays [GRP1]",
		"QDno label here [GRP1]":          "QDno label here [GRP1]",
	}
	for in, want := range tests {
		if got := Redact(n, in); got != want {
			t.Errorf("Redact(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestDedupKeyUsesRedactedText(t *testing.T) {
	n, _ := example(t)
	a := Prepare(n, "0100001", "QDCall 0412 345 678")
	b := Prepare(n, "0100001", "QDCall 0499 999 999")
	if !bytes.Equal(DedupKey(a), DedupKey(b)) {
		t.Error("pages that differ only in redacted text should have the same key")
	}
	c := Prepare(n, "0100002", "QDCall 0412 345 678")
	if bytes.Equal(DedupKey(a), DedupKey(c)) {
		t.Error("different capcodes must have different keys (R20)")
	}
}

func TestParseTextOnlyAndFailures(t *testing.T) {
	n, ag := example(t)
	// No agency profile: text only, not flagged, network job number still found.
	p := Parse(n, nil, nil, Prepare(n, "0999999", "@@EX0000009 ANYTHING"))
	if p.ParseFailed || p.Format != "" || p.String("job_number") != "EX0000009" || len(p.Fields) != 1 {
		t.Errorf("text only: %+v", p)
	}
	// Emergency page matching no Emergency format is parse_failed (A5).
	p = Parse(n, ag, nil, Prepare(n, "0100001", "@@GARBLED 123"))
	if !p.ParseFailed || p.Format != "" || len(p.Fields) != 0 {
		t.Errorf("garbled: %+v", p)
	}
	// Null priority on a profiled capcode, no format: parse_failed (section 14).
	p = Parse(n, ag, nil, Prepare(n, "0100001", "no marker"))
	if !p.ParseFailed {
		t.Errorf("null priority: %+v", p)
	}
	// Admin page mentioning a job number joins the job (R23, A7).
	p = Parse(n, ag, nil, Prepare(n, "0100001", "QDsee EX0000001 notes"))
	if p.ParseFailed || p.Format != "freeform" || p.String("job_number") != "EX0000001" {
		t.Errorf("freeform: %+v", p)
	}
}

func TestAddressSplitFormat(t *testing.T) {
	n, _ := example(t)
	set, err := loadAgencyJSON(t, `{
	  "kind": "agency", "name": "split",
	  "formats": [{
	    "name": "f",
	    "match": "^(?P<_span>.+) (?P<job_number>EX[0-9]{7})$",
	    "fields": {"_span": {"address_split": {"description": "description", "address": "address"}}}
	  }]
	}`)
	if err != nil {
		t.Fatal(err)
	}
	ag := set.Agencies["split"]
	ix := streets.NewIndex([]streets.Entry{{Street: "FICTION ST", Locality: "MADEUPTON"}}, n.StreetTypes)

	p := Parse(n, ag, ix, Prepare(n, "0100001", "@@SMOKE SEEN 4 FICTION STREET MADEUPTON EX0000001"))
	if p.String("description") != "SMOKE SEEN" || p.String("address") != "4 FICTION STREET MADEUPTON" || p.AddressUnsplit || p.ParseFailed {
		t.Errorf("split: %+v", p)
	}
	// Unknown street: whole span in description, address null, flag raised,
	// not a parse failure; other fields stand.
	p = Parse(n, ag, ix, Prepare(n, "0100001", "@@SMOKE SEEN 4 NOWHERE RD MADEUPTON EX0000001"))
	if p.String("description") != "SMOKE SEEN 4 NOWHERE RD MADEUPTON" || p.Fields["address"] != nil || !p.AddressUnsplit || p.ParseFailed || p.String("job_number") != "EX0000001" {
		t.Errorf("unsplit: %+v", p)
	}
	// Empty street list: same graceful result.
	p = Parse(n, ag, streets.NewIndex(nil, nil), Prepare(n, "0100001", "@@SMOKE SEEN 4 FICTION ST MADEUPTON EX0000001"))
	if !p.AddressUnsplit || p.Fields["address"] != nil {
		t.Errorf("empty index: %+v", p)
	}
	// Adding the street clears the flag when re-parsed.
	p = Parse(n, ag, streets.NewIndex([]streets.Entry{{Street: "NOWHERE RD", Locality: "MADEUPTON"}}, nil), Prepare(n, "0100001", "@@SMOKE SEEN 4 NOWHERE RD MADEUPTON EX0000001"))
	if p.AddressUnsplit || p.String("address") != "4 NOWHERE RD MADEUPTON" {
		t.Errorf("after adding street: %+v", p)
	}
}

func TestCategory(t *testing.T) {
	n, _ := example(t)
	for code, want := range map[string]string{"HSF1": "Structure fire", "GRS3": "Grass fire", "MED9": "Medical", "XYZ1": "Other", "": "Other"} {
		if got := Category(n, code); got != want {
			t.Errorf("Category(%q) = %q, want %q", code, got, want)
		}
	}
}
