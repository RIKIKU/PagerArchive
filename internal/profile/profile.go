// Package profile loads, validates and compiles network and agency profiles
// (SPEC.md section 6, ADR 0001 section 2).
package profile

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/RIKIKU/PagerArchive/schemas"
)

// Priority is derived from a page's priority marker. The empty Priority means
// none (null).
type Priority string

const (
	Emergency    Priority = "Emergency"
	NonEmergency Priority = "Non-emergency"
	Admin        Priority = "Admin"
)

// StandardFields are the fields core features rely on (R8).
var StandardFields = []string{"job_number", "event_type", "description", "address", "locality", "grid_ref", "units", "group"}

// ListFields are standard fields that hold a list.
var ListFields = map[string]bool{"units": true}

// Network is a compiled network profile.
type Network struct {
	Name        string
	File        string
	Hash        string
	Markers     map[string]Priority
	Ignored     []CapcodeRange
	Heartbeat   []CapcodeRange
	Redactions  []Redaction
	JobPatterns []*regexp.Regexp
	Categories  []Category
	DatasetPath string // absolute or relative to the working directory
	Attribution string
	StreetTypes map[string]string
	GridSystem  string // how grid references are read (R25); "" if not given
}

// CapcodeRange is an inclusive range of capcodes.
type CapcodeRange struct{ From, To int }

func (r CapcodeRange) Contains(c int) bool { return c >= r.From && c <= r.To }

// Redaction is one named redaction rule. If its pattern has a group named
// "redact", only that group is replaced; otherwise the whole match is.
type Redaction struct {
	Name  string
	Re    *regexp.Regexp
	Group int // index of the "redact" group, or 0 for the whole match
}

// Apply replaces each match (or its "redact" group) with replacement.
func (r Redaction) Apply(s, replacement string) string {
	if r.Group == 0 {
		return r.Re.ReplaceAllLiteralString(s, replacement)
	}
	var b strings.Builder
	last := 0
	for _, m := range r.Re.FindAllStringSubmatchIndex(s, -1) {
		start, end := m[2*r.Group], m[2*r.Group+1]
		if start < 0 || end == start {
			continue
		}
		b.WriteString(s[last:start])
		b.WriteString(replacement)
		last = end
	}
	b.WriteString(s[last:])
	return b.String()
}

// Category maps event-type codes to a report category.
type Category struct {
	Name  string
	Codes []string
}

// Agency is a compiled agency profile.
type Agency struct {
	Name    string
	File    string
	Hash    string
	Formats []Format
	Labels  map[string]map[string]string
}

// Format is one compiled agency format.
type Format struct {
	Name       string
	Priorities map[Priority]bool // nil: every priority, including none
	Re         *regexp.Regexp    // nil: text-only format, matches everything
	Split      map[string]*regexp.Regexp
	AddrSplit  map[string]AddressSplit
}

// AddressSplit names the fields an address_split writes.
type AddressSplit struct{ Description, Address string }

// Set is every profile found in a directory.
type Set struct {
	Networks map[string]*Network
	Agencies map[string]*Agency
}

// Error is a problem in one profile file, at a JSON Pointer path.
type Error struct {
	File, Path, Msg string
}

func (e *Error) Error() string {
	if e.Path == "" {
		return e.File + ": " + e.Msg
	}
	return e.File + ": at " + e.Path + ": " + e.Msg
}

// LoadDir loads every profile under dir. A JSON file is a profile when its
// top-level "kind" is "network" or "agency"; other JSON files (golden tables,
// address datasets) are skipped. All problems are returned, joined.
func LoadDir(dir string) (*Set, error) {
	set := &Set{Networks: map[string]*Network{}, Agencies: map[string]*Agency{}}
	var errs []error
	err := filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || !strings.HasSuffix(d.Name(), ".json") {
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		var head struct {
			Kind string `json:"kind"`
		}
		if err := json.Unmarshal(data, &head); err != nil {
			// Report broken profiles rather than skipping them silently.
			if bytes.Contains(data, []byte(`"kind"`)) {
				errs = append(errs, &Error{File: path, Msg: "not valid JSON: " + err.Error()})
			}
			return nil
		}
		switch head.Kind {
		case "network":
			n, e := loadNetwork(path, data)
			errs = append(errs, e...)
			if n != nil {
				if prev, dup := set.Networks[n.Name]; dup {
					errs = append(errs, &Error{File: path, Path: "/name", Msg: fmt.Sprintf("network profile %q is also defined in %s", n.Name, prev.File)})
				}
				set.Networks[n.Name] = n
			}
		case "agency":
			a, e := loadAgency(path, data)
			errs = append(errs, e...)
			if a != nil {
				if prev, dup := set.Agencies[a.Name]; dup {
					errs = append(errs, &Error{File: path, Path: "/name", Msg: fmt.Sprintf("agency profile %q is also defined in %s", a.Name, prev.File)})
				}
				set.Agencies[a.Name] = a
			}
		}
		return nil
	})
	if err != nil {
		errs = append(errs, err)
	}
	if len(errs) > 0 {
		return nil, errors.Join(errs...)
	}
	return set, nil
}

func hashOf(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func schemaErrors(path, schema string, data []byte) []error {
	var errs []error
	for _, p := range schemas.Validate(schema, data) {
		errs = append(errs, &Error{File: path, Path: p.Path, Msg: p.Message})
	}
	return errs
}

type networkJSON struct {
	Name           string            `json:"name"`
	Markers        map[string]string `json:"priority_markers"`
	Ignored        []string          `json:"ignored_capcodes"`
	Heartbeat      []string          `json:"heartbeat_capcodes"`
	Redactions     []redactionJSON   `json:"redaction_rules"`
	JobPatterns    []string          `json:"job_number_patterns"`
	Categories     []categoryJSON    `json:"event_categories"`
	AddressDataset struct {
		Path        string `json:"path"`
		Attribution string `json:"attribution"`
	} `json:"address_dataset"`
	GridReference struct {
		System string `json:"system"`
	} `json:"grid_reference"`
	StreetTypes map[string]string `json:"street_types"`
}

type redactionJSON struct {
	Name         string   `json:"name"`
	Pattern      string   `json:"pattern"`
	MustNotMatch []string `json:"must_not_match"`
}

type categoryJSON struct {
	Category string   `json:"category"`
	Codes    []string `json:"codes"`
}

func loadNetwork(path string, data []byte) (*Network, []error) {
	if errs := schemaErrors(path, schemas.Network, data); len(errs) > 0 {
		return nil, errs
	}
	var j networkJSON
	if err := json.Unmarshal(data, &j); err != nil {
		return nil, []error{&Error{File: path, Msg: err.Error()}}
	}
	var errs []error
	fail := func(p, msg string) { errs = append(errs, &Error{File: path, Path: p, Msg: msg}) }

	n := &Network{
		Name:        j.Name,
		File:        path,
		Hash:        hashOf(data),
		Markers:     map[string]Priority{},
		Attribution: j.AddressDataset.Attribution,
		StreetTypes: j.StreetTypes,
		GridSystem:  j.GridReference.System,
	}
	for k, v := range j.Markers {
		if utf8.RuneCountInString(k) != 2 {
			fail("/priority_markers/"+k, fmt.Sprintf("marker %q must be exactly 2 characters", k))
		}
		n.Markers[k] = Priority(v)
	}
	n.DatasetPath = j.AddressDataset.Path
	if !filepath.IsAbs(n.DatasetPath) {
		n.DatasetPath = filepath.Join(filepath.Dir(path), n.DatasetPath)
	}
	for i, s := range j.Ignored {
		r, err := parseRange(s)
		if err != nil {
			fail(fmt.Sprintf("/ignored_capcodes/%d", i), err.Error())
		}
		n.Ignored = append(n.Ignored, r)
	}
	for i, s := range j.Heartbeat {
		r, err := parseRange(s)
		if err != nil {
			fail(fmt.Sprintf("/heartbeat_capcodes/%d", i), err.Error())
		}
		n.Heartbeat = append(n.Heartbeat, r)
	}
	seen := map[string]bool{}
	for i, r := range j.Redactions {
		p := fmt.Sprintf("/redaction_rules/%d", i)
		if seen[r.Name] {
			fail(p+"/name", fmt.Sprintf("duplicate rule name %q", r.Name))
		}
		seen[r.Name] = true
		re, err := regexp.Compile(r.Pattern)
		if err != nil {
			fail(p+"/pattern", err.Error())
			continue
		}
		if re.MatchString("") {
			fail(p+"/pattern", "pattern matches empty text")
			continue
		}
		rule := Redaction{Name: r.Name, Re: re}
		if g := re.SubexpIndex("redact"); g > 0 {
			rule.Group = g
		}
		// Section 14: a rule that alters one of its own must-not-match
		// examples stops the server.
		for k, ex := range r.MustNotMatch {
			if rule.Apply(ex, "REDACTED") != ex {
				fail(fmt.Sprintf("%s/must_not_match/%d", p, k), fmt.Sprintf("rule %q alters its own must-not-match example", r.Name))
			}
		}
		n.Redactions = append(n.Redactions, rule)
	}
	for i, s := range j.JobPatterns {
		re, err := regexp.Compile(s)
		if err != nil {
			fail(fmt.Sprintf("/job_number_patterns/%d", i), err.Error())
			continue
		}
		n.JobPatterns = append(n.JobPatterns, re)
	}
	for _, c := range j.Categories {
		n.Categories = append(n.Categories, Category{Name: c.Category, Codes: c.Codes})
	}
	if len(errs) > 0 {
		return nil, errs
	}
	return n, nil
}

func parseRange(s string) (CapcodeRange, error) {
	from, to, isRange := strings.Cut(s, "-")
	a, _ := strconv.Atoi(from)
	b := a
	if isRange {
		b, _ = strconv.Atoi(to)
	}
	if b < a {
		return CapcodeRange{}, fmt.Errorf("range %q ends before it starts", s)
	}
	return CapcodeRange{From: a, To: b}, nil
}

type agencyJSON struct {
	Name    string                       `json:"name"`
	Formats []formatJSON                 `json:"formats"`
	Labels  map[string]map[string]string `json:"labels"`
}

type formatJSON struct {
	Name       string               `json:"name"`
	Priorities []string             `json:"priorities"`
	Match      string               `json:"match"`
	Fields     map[string]fieldJSON `json:"fields"`
}

type fieldJSON struct {
	Split        *string `json:"split"`
	AddressSplit *struct {
		Description string `json:"description"`
		Address     string `json:"address"`
	} `json:"address_split"`
}

var fieldName = regexp.MustCompile(`^[a-z][a-z0-9_]*$`)

func loadAgency(path string, data []byte) (*Agency, []error) {
	if errs := schemaErrors(path, schemas.Agency, data); len(errs) > 0 {
		return nil, errs
	}
	var j agencyJSON
	if err := json.Unmarshal(data, &j); err != nil {
		return nil, []error{&Error{File: path, Msg: err.Error()}}
	}
	var errs []error
	fail := func(p, msg string) { errs = append(errs, &Error{File: path, Path: p, Msg: msg}) }

	a := &Agency{Name: j.Name, File: path, Hash: hashOf(data), Labels: j.Labels}
	names := map[string]bool{}
	for i, fj := range j.Formats {
		p := fmt.Sprintf("/formats/%d", i)
		if names[fj.Name] {
			fail(p+"/name", fmt.Sprintf("duplicate format name %q", fj.Name))
		}
		names[fj.Name] = true
		f := Format{Name: fj.Name, Split: map[string]*regexp.Regexp{}, AddrSplit: map[string]AddressSplit{}}
		if fj.Priorities != nil {
			f.Priorities = map[Priority]bool{}
			for _, pr := range fj.Priorities {
				f.Priorities[Priority(pr)] = true
			}
		}
		groups := map[string]bool{}
		if fj.Match != "" {
			re, err := regexp.Compile(fj.Match)
			if err != nil {
				fail(p+"/match", err.Error())
				continue
			}
			f.Re = re
			for _, g := range re.SubexpNames() {
				if g == "" {
					continue
				}
				if !fieldName.MatchString(strings.TrimPrefix(g, "_")) {
					fail(p+"/match", fmt.Sprintf("group name %q: use lower-case letters, digits and _", g))
				}
				groups[g] = true
			}
		}
		// Every field a format can output.
		outputs := map[string]string{}
		for g := range groups {
			if !strings.HasPrefix(g, "_") {
				outputs[g] = "group"
			}
		}
		keys := make([]string, 0, len(fj.Fields))
		for k := range fj.Fields {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, name := range keys {
			opt := fj.Fields[name]
			fp := p + "/fields/" + name
			if !groups[name] {
				fail(fp, fmt.Sprintf("no group named %q in match", name))
				continue
			}
			switch {
			case opt.Split != nil:
				re, err := regexp.Compile(*opt.Split)
				if err != nil {
					fail(fp+"/split", err.Error())
					continue
				}
				f.Split[name] = re
			case opt.AddressSplit != nil:
				as := AddressSplit{Description: opt.AddressSplit.Description, Address: opt.AddressSplit.Address}
				for _, out := range []string{as.Description, as.Address} {
					if strings.HasPrefix(out, "_") || !fieldName.MatchString(out) {
						fail(fp+"/address_split", fmt.Sprintf("%q is not a field name", out))
					}
					if _, dup := outputs[out]; dup {
						fail(fp+"/address_split", fmt.Sprintf("field %q is also written by another group", out))
					}
					outputs[out] = "address_split"
				}
				if ListFields[as.Description] || ListFields[as.Address] {
					fail(fp+"/address_split", "address_split can't write a list field")
				}
				f.AddrSplit[name] = as
			}
		}
		for g := range groups {
			if ListFields[g] && f.Split[g] == nil {
				fail(p+"/fields/"+g, fmt.Sprintf("%s is a list field: add fields.%s.split", g, g))
			}
		}
		a.Formats = append(a.Formats, f)
	}
	if len(errs) > 0 {
		return nil, errs
	}
	return a, nil
}
