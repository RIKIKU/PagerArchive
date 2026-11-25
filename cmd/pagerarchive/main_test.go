package main

import (
	"bytes"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// A14: given an agency profile with a missing required key, when the server
// starts, it exits with an error naming the file and the missing key. The
// server binary is built and started as a real process.
func TestA14InvalidProfileStopsServer(t *testing.T) {
	dir := t.TempDir()
	bin := filepath.Join(dir, "pagerarchive")
	if out, err := exec.Command("go", "build", "-o", bin, ".").CombinedOutput(); err != nil {
		t.Fatalf("build: %v\n%s", err, out)
	}
	profiles := filepath.Join(dir, "profiles")
	if err := os.CopyFS(profiles, os.DirFS("../../profiles/example")); err != nil {
		t.Fatal(err)
	}
	fire := filepath.Join(profiles, "example-fire.json")
	data, err := os.ReadFile(fire)
	if err != nil {
		t.Fatal(err)
	}
	broken := strings.Replace(string(data), `"formats":`, `"_removed":`, 1)
	if broken == string(data) {
		t.Fatal("couldn't remove the formats key")
	}
	if err := os.WriteFile(fire, []byte(broken), 0o600); err != nil {
		t.Fatal(err)
	}

	cmd := exec.Command(bin, "serve")
	// A database that doesn't exist: profiles must be checked first.
	cmd.Env = append(os.Environ(), "PROFILES_DIR="+profiles, "DATABASE_URL=postgres://nobody@127.0.0.1:1/none")
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	err = cmd.Run()
	exit, ok := err.(*exec.ExitError)
	if !ok || exit.ExitCode() == 0 {
		t.Fatalf("server didn't exit with an error: %v", err)
	}
	msg := stderr.String()
	t.Logf("exit code %d, stderr:\n%s", exit.ExitCode(), msg)
	for _, want := range []string{fire, "missing property 'formats'"} {
		if !strings.Contains(msg, want) {
			t.Errorf("stderr doesn't contain %q", want)
		}
	}
}

func TestValidateProfileExample(t *testing.T) {
	var out, errOut bytes.Buffer
	if code := run([]string{"validate-profile", "-v", "../../profiles/example"}, &out, &errOut); code != 0 {
		t.Fatalf("exit %d\n%s%s", code, out.String(), errOut.String())
	}
	for _, want := range []string{"example-network (network,", ": 10 passed, 0 failed", "example-fire (agency,", ": 9 passed, 0 failed"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("output doesn't contain %q:\n%s", want, out.String())
		}
	}
}

// R87 (owner, 2026-11-14): validate-profile refuses a large_event_fields
// rule on a field no format extracts, naming the file and the path.
func TestValidateProfileLargeEventFieldErrors(t *testing.T) {
	dir := t.TempDir()
	if err := os.CopyFS(dir, os.DirFS("../../profiles/example")); err != nil {
		t.Fatal(err)
	}
	fire := filepath.Join(dir, "example-fire.json")
	data, err := os.ReadFile(fire)
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct{ name, from, to, want string }{
		{"unknown field", `"field": "alarm_level"`, `"field": "alarm"`, `at /large_event_fields/0/field: no format extracts a field named "alarm"`},
		{"empty value", `"values": ["3",`, `"values": ["",`, "/large_event_fields/0/values/0"},
		{"malformed rule", `"field": "alarm_level",`, `"fields": "alarm_level",`, "/large_event_fields/0"},
	} {
		broken := strings.Replace(string(data), c.from, c.to, 1)
		if broken == string(data) {
			t.Fatalf("%s: nothing replaced", c.name)
		}
		if err := os.WriteFile(fire, []byte(broken), 0o600); err != nil {
			t.Fatal(err)
		}
		var out, errOut bytes.Buffer
		code := run([]string{"validate-profile", dir}, &out, &errOut)
		msg := out.String() + errOut.String()
		if code == 0 || !strings.Contains(msg, fire) || !strings.Contains(msg, c.want) {
			t.Errorf("%s: exit %d, output:\n%s\nwant %q", c.name, code, msg, c.want)
		}
		t.Logf("%s: exit %d: %s", c.name, code, strings.TrimSpace(errOut.String()))
	}
}

// A128 (R7, ADR 0047): validate-profile passes the example's into and its
// proposed row, and fails an into whose values file is missing or that
// names a single-value field, naming the file and the path.
func TestValidateProfileInto(t *testing.T) {
	var out, errOut bytes.Buffer
	if code := run([]string{"validate-profile", "-v", "../../profiles/example"}, &out, &errOut); code != 0 {
		t.Fatalf("exit %d\n%s%s", code, out.String(), errOut.String())
	}
	if s := out.String(); !strings.Contains(s, "PASS example-fire: emergency whose units name call signs and a role moves them to call_signs") {
		t.Errorf("the proposed row isn't reported passing:\n%s", s)
	}
	dir := t.TempDir()
	if err := os.CopyFS(dir, os.DirFS("../../profiles/example")); err != nil {
		t.Fatal(err)
	}
	fire := filepath.Join(dir, "example-fire.json")
	data, err := os.ReadFile(fire)
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct{ name, from, to, want string }{
		{"missing values file", `"values_file": "example-call-signs.txt"`, `"values_file": "example-call-sign.txt"`, `at /formats/0/fields/units/into/values_file: values_file "example-call-sign.txt" can't be read`},
		{"single-value field", `"field": "call_signs"`, `"field": "call_sign"`, `at /formats/0/fields/units/into/field: "call_sign" is a single-value field`},
	} {
		broken := strings.Replace(string(data), c.from, c.to, 1)
		if broken == string(data) {
			t.Fatalf("%s: nothing replaced", c.name)
		}
		if err := os.WriteFile(fire, []byte(broken), 0o600); err != nil {
			t.Fatal(err)
		}
		out.Reset()
		errOut.Reset()
		code := run([]string{"validate-profile", dir}, &out, &errOut)
		msg := out.String() + errOut.String()
		if code == 0 || !strings.Contains(msg, fire) || !strings.Contains(msg, c.want) {
			t.Errorf("%s: exit %d, output:\n%s\nwant %q", c.name, code, msg, c.want)
		}
		t.Logf("%s: exit %d: %s", c.name, code, strings.TrimSpace(errOut.String()))
	}
}

// A114 (R88): validate-profile checks each layer's areas file, and fails on
// one that's broken or missing, naming the layer and the file; the server
// starts without such a layer instead (R88d).
func TestValidateProfileAreas(t *testing.T) {
	var out, errOut bytes.Buffer
	if code := run([]string{"validate-profile", "../../profiles/example"}, &out, &errOut); code != 0 {
		t.Fatalf("exit %d\n%s%s", code, out.String(), errOut.String())
	}
	for _, want := range []string{`areas zones: `, `example-areas.geojson (2, each a "Zone", solid outline)`, `areas regions: `, `example-regions.geojson (2, each a "Region", dashed outline)`} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("output doesn't contain %q:\n%s", want, out.String())
		}
	}
	for _, c := range []struct {
		name, want string
		change     func(dir string) error
	}{
		{"broken", `areas "zones":` + "\n" + `example-areas.geojson: at /features: has no features`, func(dir string) error {
			return os.WriteFile(filepath.Join(dir, "example-areas.geojson"), []byte(`{"type": "FeatureCollection", "features": []}`), 0o600)
		}},
		{"missing", `areas "regions":`, func(dir string) error { return os.Remove(filepath.Join(dir, "example-regions.geojson")) }},
	} {
		dir := t.TempDir()
		if err := os.CopyFS(dir, os.DirFS("../../profiles/example")); err != nil {
			t.Fatal(err)
		}
		if err := c.change(dir); err != nil {
			t.Fatal(err)
		}
		var out, errOut bytes.Buffer
		code := run([]string{"validate-profile", dir}, &out, &errOut)
		msg := out.String() + errOut.String()
		if code == 0 || !strings.Contains(msg, c.want) {
			t.Errorf("%s: exit %d, output:\n%s\nwant %q", c.name, code, msg, c.want)
		}
		t.Logf("%s: exit %d: %s", c.name, code, strings.TrimSpace(errOut.String()))
	}
}

// A54 (R59): pagerarchive keeps only the server's jobs; templates and
// aliases are the PowerShell module's.
func TestA54HelpListsNoTemplateOrAliasCommands(t *testing.T) {
	var out, errOut bytes.Buffer
	if code := run([]string{"help"}, &out, &errOut); code != 0 {
		t.Fatalf("help exited %d: %s", code, errOut.String())
	}
	t.Logf("A54: pagerarchive help:\n%s", out.String())
	commands := strings.SplitN(out.String(), "serve reads its settings", 2)[0]
	for _, line := range strings.Split(commands, "\n") {
		f := strings.Fields(line)
		if len(f) > 0 && (f[0] == "template" || f[0] == "alias") {
			t.Errorf("help lists %q", line)
		}
	}
	for _, cmd := range []string{"serve", "validate-profile", "client", "version"} {
		if !strings.Contains(commands, "  "+cmd) {
			t.Errorf("help doesn't list %s", cmd)
		}
	}
	for _, removed := range [][]string{{"template", "list"}, {"alias", "list"}} {
		if code := run(removed, &out, &errOut); code == 0 {
			t.Errorf("pagerarchive %s still runs", strings.Join(removed, " "))
		}
	}
}

// R84: /dev/null mounted in place of a deployment's file means the built-in
// example. Anything else stays what it is, so a mistyped path in .env is an
// error and never quietly the example.
func TestMountedOr(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "map.pmtiles")
	if err := os.WriteFile(file, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		path, want string
		builtin    bool
	}{
		{"/dev/null", "built-in", true},
		{file, file, false},
		{dir, dir, false},
		{filepath.Join(dir, "missing"), filepath.Join(dir, "missing"), false},
	} {
		if got, builtin := mountedOr(c.path, "built-in"); got != c.want || builtin != c.builtin {
			t.Errorf("mountedOr(%s) = %s, %v; want %s, %v", c.path, got, builtin, c.want, c.builtin)
		}
	}
}

// ADR 0040 (R8): a profile and golden table from before the rename, which
// still name the call sign group, pass with a warning to rename it, and the
// rows still check its value; a format that captures both fails.
func TestValidateProfileOldCallSign(t *testing.T) {
	dir := t.TempDir()
	if err := os.CopyFS(dir, os.DirFS("../../profiles/example")); err != nil {
		t.Fatal(err)
	}
	fire, golden := filepath.Join(dir, "example-fire.json"), filepath.Join(dir, "golden-example-fire.json")
	replace := func(path, from, to string) {
		t.Helper()
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(data), from) {
			t.Fatalf("%s: no %q", path, from)
		}
		if err := os.WriteFile(path, []byte(strings.ReplaceAll(string(data), from, to)), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	replace(fire, "(?P<call_sign>", "(?P<group>")
	replace(golden, `"call_sign":`, `"group":`)
	var out, errOut bytes.Buffer
	if code := run([]string{"validate-profile", dir}, &out, &errOut); code != 0 {
		t.Fatalf("exit %d\n%s%s", code, out.String(), errOut.String())
	}
	for _, want := range []string{
		"warning: " + fire + ": at /formats/0/match: the call sign's field is now call_sign (ADR 0040): rename group to call_sign",
		"warning: " + fire + ": at /formats/1/match: ",
		"warning: " + golden + ": at /cases/0/expect/group: ",
		"example-fire (agency, " + golden + "): 9 passed, 0 failed",
	} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("output doesn't contain %q:\n%s", want, out.String())
		}
	}
	t.Logf("old name: %s", strings.TrimSpace(out.String()))

	// A wrong value under the old name still fails its row.
	replace(golden, `"group": "STN2"`, `"group": "STN9"`)
	out.Reset()
	if code := run([]string{"validate-profile", dir}, &out, &errOut); code == 0 || !strings.Contains(out.String(), "STN9") {
		t.Errorf("a wrong call sign under the old name: exit %d\n%s", code, out.String())
	}

	// Both names in one format: an error.
	replace(fire, `.*#(?P<group>\\S+)$`, `.*#(?P<group>\\S+) (?P<call_sign>\\S+)$`)
	out.Reset()
	errOut.Reset()
	code := run([]string{"validate-profile", dir}, &out, &errOut)
	if msg := errOut.String(); code == 0 || !strings.Contains(msg, "at /formats/1/match: captures both group and call_sign") {
		t.Errorf("both names: exit %d\n%s%s", code, out.String(), msg)
	}
}

// R92: RADIOARCHIVE_URL is an absolute http(s) URL, its trailing slash
// dropped; unset or empty, PagerArchive stands alone.
func TestRadioArchiveURL(t *testing.T) {
	for in, want := range map[string]string{
		"":                               "",
		"  ":                             "",
		"https://radio.example.com":      "https://radio.example.com",
		"https://radio.example.com/":     "https://radio.example.com",
		"http://radio.example.test:8080": "http://radio.example.test:8080",
		"https://example.com/radio/":     "https://example.com/radio",
	} {
		if got, err := radioArchiveURL(in); err != nil || got != want {
			t.Errorf("radioArchiveURL(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
	for _, in := range []string{"radio.example.com", "/radio", "ftp://radio.example.com", "https://", "https://u:p@radio.example.com", "https://radio.example.com/?x=1", "https://radio.example.com/#a", "javascript:alert(1)"} {
		if _, err := radioArchiveURL(in); err == nil || !strings.Contains(err.Error(), "RADIOARCHIVE_URL") {
			t.Errorf("radioArchiveURL(%q): %v, want an error naming the setting", in, err)
		}
	}
}

// R92b: LOOK_COOKIE_DOMAIN is a domain name that APP_URL's host is in, in
// lower case and without a leading dot; unset or empty, there's no shared look.
func TestLookCookieDomain(t *testing.T) {
	app, _ := url.Parse("https://pager.example.org")
	for in, want := range map[string]string{
		"":                  "",
		"  ":                "",
		"example.org":       "example.org",
		".example.org":      "example.org",
		"Example.ORG":       "example.org",
		"pager.example.org": "pager.example.org",
	} {
		if got, err := lookCookieDomain(in, app); err != nil || got != want {
			t.Errorf("lookCookieDomain(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
	for _, in := range []string{"other.example", "le.org", "example.org; Secure", "exa mple.org", "-example.org", "https://example.org", "example.org/", "..example.org"} {
		if _, err := lookCookieDomain(in, app); err == nil || !strings.Contains(err.Error(), "LOOK_COOKIE_DOMAIN") {
			t.Errorf("lookCookieDomain(%q): %v, want an error naming the setting", in, err)
		}
	}
}
