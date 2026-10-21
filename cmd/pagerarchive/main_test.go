package main

import (
	"bytes"
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
	for _, want := range []string{"example-network (network,", ": 7 passed, 0 failed", "example-fire (agency,", ": 4 passed, 0 failed"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("output doesn't contain %q:\n%s", want, out.String())
		}
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
