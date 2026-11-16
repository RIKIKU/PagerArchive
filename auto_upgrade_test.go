package pagerarchive

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// autoUpgrade runs script in bash with deploy/auto-upgrade/auto-upgrade.sh
// sourced (only its functions), in a made-up install folder dir, and returns
// its output and whether it exited 0.
func autoUpgrade(t *testing.T, dir, script string, env ...string) (string, bool) {
	t.Helper()
	if _, err := exec.LookPath("bash"); err != nil {
		t.Skip("needs bash")
	}
	src, err := filepath.Abs("deploy/auto-upgrade/auto-upgrade.sh")
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("bash", "-c", `. "$1"; `+script, "test", src)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), env...)
	out, err := cmd.CombinedOutput()
	return string(out), err == nil
}

// installDir is a made-up install: a compose.yaml, .env and deploy/.
func installDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	for name, data := range map[string]string{"compose.yaml": "services: {}\n", ".env": "", "deploy/x": ""} {
		p := filepath.Join(dir, name)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(data), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

// A108 (R84k): a version or tag is taken only whole and on one line, as
// X.Y.Z or vX.Y.Z: from the running API, from GitHub, and as an argument.
func TestA108AutoUpgradeVersionsAreChecked(t *testing.T) {
	dir := installDir(t)
	bad := []string{"", "1.2", "1.2.3.4", "01.2.3x", " 1.2.3", "1.2.3 ", "1.2.3\n", "1.2.3\n1.0;id", "1.0;id\n1.2.3", "1.2.3;id",
		"1.2.3$(id)", "1.2.3-rc.1", "1.2.3/../x", "v1.2.3", "1.2.3'x'"}
	for _, v := range bad {
		if out, ok := autoUpgrade(t, dir, `is_release "$V"`, "V="+v); ok {
			t.Errorf("is_release %q passed %s", v, out)
		}
		if out, ok := autoUpgrade(t, dir, `is_tag "v$V"`, "V="+v); ok && v != "v1.2.3" {
			t.Errorf("is_tag %q passed %s", "v"+v, out)
		}
	}
	for _, v := range []string{"0.0.1", "1.12.0", "123.45.678"} {
		if out, ok := autoUpgrade(t, dir, `is_release "$V" && is_tag "v$V"`, "V="+v); !ok {
			t.Errorf("%s refused: %s", v, out)
		}
	}

	// The running version: only the first "pagerarchive " line, and a run
	// with anything but a release names it only if it's version-like.
	out, ok := autoUpgrade(t, dir, `dc() { printf 'pagerarchive 1.2.3\npagerarchive 1.0;id\n'; }; printf '[%s]' "$(running_version)"`)
	if !ok || out != "[1.2.3]" {
		t.Errorf("running_version: %q", out)
	}

	// GitHub's latest release.
	// Anything that isn't vX.Y.Z whole, a tag with a line break included,
	// isn't taken as a release.
	for tag, want := range map[string]string{`v1.12.0`: "[v1.12.0]", `v1.12.0\n`: "[]", `v1.12.0\nv1.0;id`: "[]", `v1.0;id\nv1.12.0`: "[]", `v1.12.0;id`: "[]"} {
		if _, err := exec.LookPath("jq"); err != nil {
			t.Skip("needs jq")
		}
		script := `JQ=jq RELEASES=$(mktemp)
curl_api() { printf '[{"draft":false,"prerelease":false,"tag_name":"%s"}]' "$TAG"; }
if t=$(latest_release); then printf '[%s]' "$t"; else printf refused; fi`
		if out, _ := autoUpgrade(t, dir, script, "TAG="+tag); out != want {
			t.Errorf("latest release %q: %q, want %q", tag, out, want)
		}
	}

	// Arguments: refused before anything runs, with the usage.
	for _, args := range [][]string{{"retry", "1.2.3;id"}, {"retry", "1.2.3\n1.0"}, {"retry", ""}, {"verify", "1.2.3"}, {"verify", "v1.2.3;id"}, {"verify", "v1.2.3\nv1"}} {
		cmd := exec.Command("bash", append([]string{"deploy/auto-upgrade/auto-upgrade.sh"}, args...)...)
		cmd.Env = append(os.Environ(), "AUTO_UPGRADE_INSTALL_DIR="+dir, "AUTO_UPGRADE_HOME="+filepath.Join(dir, "au"))
		out, err := cmd.CombinedOutput()
		if code := cmd.ProcessState.ExitCode(); err == nil || code != 2 || !strings.Contains(string(out), "usage: auto-upgrade.sh "+args[0]) {
			t.Errorf("%q: exit %d: %s", args, code, out)
		}
	}
	// And verify, called with a bad tag, fetches nothing.
	if out, ok := autoUpgrade(t, dir, `HOME_AU=$PWD/au; curl_api() { echo fetched; }; verify "$TAG" || printf 'refused: %s' "$REASON"`, "TAG=v1.2.3\nv1;id"); out != "refused: that isn't a release's tag (vX.Y.Z)" || ok != true {
		t.Errorf("verify a bad tag: %q", out)
	}
}

// A108 (R84k): the backup set's name reaches the backup container's shell
// only as an argument, and old sets and copies are pruned one path at a
// time: a name with spaces or quotes removes only itself.
func TestA108AutoUpgradeBackupSetIsAnArgument(t *testing.T) {
	dir := installDir(t)
	au := filepath.Join(dir, "au")
	for _, d := range []string{"files/20260101T000000Z-1.0.0-to-1.0.1", "files/0 'keep me'", "keep me"} {
		if err := os.MkdirAll(filepath.Join(au, d), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	script := `PA_DIR=$PWD HOME_AU=$PWD/au MON_DIR= KEEP=1
dc() {
	case $* in
	*"pagerarchive-backup now"*) echo "backup: 2026-11-16 good: made up" ;;
	*) printf '%s\0' "$@" >"$HOME_AU/dc-args" ;;
	esac
}
SET="$BAD"
back_up && echo "took a bad set"; echo "reason: $REASON"
SET=20261116T090000Z-1.2.3-to-1.2.4
cd "$HOME_AU" && back_up || echo "failed: $REASON"`
	out, ok := autoUpgrade(t, dir, script, "BAD=20261116T090000Z-1.2.3\n1.0;id-to-1.2.4")
	if !ok || strings.Contains(out, "took a bad set") || !strings.Contains(out, "reason: the backup set's name isn't one this script makes") || strings.Contains(out, "failed") {
		t.Fatalf("back_up: %s", out)
	}
	raw, err := os.ReadFile(filepath.Join(au, "dc-args"))
	if err != nil {
		t.Fatal(err)
	}
	args := strings.Split(strings.TrimSuffix(string(raw), "\x00"), "\x00")
	// exec -T backup sh -c <script> sh <set> <night> <keep>
	if len(args) != 10 || args[3] != "sh" || args[4] != "-c" || strings.Contains(args[5], "1.2.3") || strings.Contains(args[5], "2026-11-16") ||
		args[6] != "sh" || args[7] != "20261116T090000Z-1.2.3-to-1.2.4" || args[8] != "2026-11-16" || args[9] != "1" {
		t.Errorf("the backup container's command: %q", args)
	}
	// KEEP=1: the newest copy stays; the older two go, each alone.
	for d, want := range map[string]bool{"files/20261116T090000Z-1.2.3-to-1.2.4": true, "files/20260101T000000Z-1.0.0-to-1.0.1": false, "files/0 'keep me'": false, "keep me": true} {
		if _, err := os.Stat(filepath.Join(au, d)); (err == nil) != want {
			t.Errorf("%s: there %v, want %v", d, err == nil, want)
		}
	}
}
