package pagerarchive

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
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

// R84h (owner, 2026-11-20: "we should make that dashboard reflect in progress
// upgrades too"): during a run, the status has when it started and, once it
// knows, the release it's upgrading to; the last check is the run's start,
// which the run's end doesn't move; at the end both go. Each step stubbed, by
// a clock that moves 100 s each time it's read.
func TestAutoUpgradeRunInProgress(t *testing.T) {
	if _, err := exec.LookPath("jq"); err != nil {
		t.Skip("needs jq")
	}
	dir := installDir(t)
	script := `PA_DIR=$PWD HOME_AU=$PWD/au STATUS_DIR=$PWD/au/status STATUS=$PWD/au/status/status.json LOG=$PWD/au/status/auto-upgrade.log
JQ=jq MON_DIR= RELEASES=$PWD/au/releases.json
mkdir -p "$STATUS_DIR"; echo 1000 >"$PWD/clock"
now() { local n; n=$(cat "$PA_DIR/clock"); echo $((n + 100)) >"$PA_DIR/clock"; echo "$n"; }
snap() { jq -c "{state, running_since, target_version, last_check_at, last_check_result}" "$STATUS"; }
running_version() { echo "$FROM_V"; }
latest_release() { echo v1.19.0; }
release_field() { echo ""; }
backup_busy() { return 1; }
verify() { echo "verifying: $(snap)"; }
back_up() { echo "backing-up: $(snap)"; NIGHT=2026-11-20; }
fingerprint() { echo fp; }
images_of() { :; }
docker() { :; }
dc() { :; }
dbq() { echo 0; }
upgrade() { echo "upgrading: $(snap)"; }
health() { echo "health-check: $(snap)"; PAGE_NOTE=skipped; }
echo '{"enabled":true,"state":"idle","running_since":0,"last_check_at":5,"failures":0,"rollbacks":0,"failed_versions":[],"history":[]}' >"$STATUS"
run >"$PA_DIR/out" 2>&1; grep -E '^(verifying|backing-up|upgrading|health-check):' "$PA_DIR/out"
echo "end: $(snap)"
FROM_V=1.19.0
run >/dev/null 2>&1
echo "up-to-date: $(snap)"`
	out, ok := autoUpgrade(t, dir, "FROM_V=1.18.0\n"+script)
	if !ok {
		t.Fatalf("the run: %s", out)
	}
	t.Log(out)
	// The run's start is the clock's value when it became checking.
	lines := map[string]string{}
	for _, l := range strings.Split(strings.TrimSpace(out), "\n") {
		if k, v, found := strings.Cut(l, ": "); found {
			lines[k] = v
		}
	}
	v := lines["verifying"]
	i := strings.Index(v, `"running_since":`)
	if i < 0 {
		t.Fatalf("no running_since while verifying: %s", out)
	}
	start := strings.SplitN(v[i+len(`"running_since":`):], ",", 2)[0]
	for _, step := range []string{"verifying", "backing-up", "upgrading", "health-check"} {
		want := fmt.Sprintf(`{"state":"%s","running_since":%s,"target_version":"1.19.0","last_check_at":%s,"last_check_result":"in-progress"}`, step, start, start)
		if lines[step] != want {
			t.Errorf("%s: %s, want %s", step, lines[step], want)
		}
	}
	if want := fmt.Sprintf(`{"state":"idle","running_since":0,"target_version":"","last_check_at":%s,"last_check_result":"upgraded"}`, start); lines["end"] != want {
		t.Errorf("after the upgrade: %s, want %s", lines["end"], want)
	}
	if !strings.Contains(lines["up-to-date"], `"state":"idle","running_since":0,"target_version":"","last_check_at":`) ||
		strings.Contains(lines["up-to-date"], `"last_check_at":`+start+",") || !strings.Contains(lines["up-to-date"], `"last_check_result":"up-to-date"`) {
		t.Errorf("a run that found nothing: %s", lines["up-to-date"])
	}
}

// R84m, A138: two upgrades at once on one host, this install's and another's
// (a companion app's copy of the script takes the same lock): the second
// says "another upgrade is running", records a skipped check and exits 0,
// and the first, holding the lock from its check through its backup and
// upgrade, carries on. Each step stubbed; the first waits in its backup.
func TestA138AutoUpgradeHostWideLock(t *testing.T) {
	if _, err := exec.LookPath("jq"); err != nil {
		t.Skip("needs jq")
	}
	if _, err := exec.LookPath("flock"); err != nil {
		t.Skip("needs flock")
	}
	lock := filepath.Join(t.TempDir(), "archive-upgrade.lock")
	stubs := `PA_DIR=$PWD HOME_AU=$PWD/au STATUS_DIR=$PWD/au/status STATUS=$PWD/au/status/status.json LOG=$PWD/au/status/auto-upgrade.log
JQ=jq MON_DIR= RELEASES=$PWD/au/releases.json
mkdir -p "$STATUS_DIR"
running_version() { echo 1.18.0; }
latest_release() { echo v1.19.0; }
release_field() { echo ""; }
backup_busy() { return 1; }
verify() { :; }
back_up() { touch "$PA_DIR/held"; while [ ! -e "$PA_DIR/go" ]; do sleep 0.05; done; NIGHT=2026-11-20; }
fingerprint() { echo fp; }
images_of() { :; }
docker() { :; }
dc() { :; }
dbq() { echo 0; }
upgrade() { :; }
health() { PAGE_NOTE=skipped; }
echo '{"enabled":true,"state":"idle","running_since":0,"running_version":"1.18.0","last_check_at":5,"failures":0,"rollbacks":0,"failed_versions":[],"history":[]}' >"$STATUS"
host_locked_run; echo "exit $?"; jq -c "{state, last_check_result, last_check_note, running_version}" "$STATUS"`
	src, err := filepath.Abs("deploy/auto-upgrade/auto-upgrade.sh")
	if err != nil {
		t.Fatal(err)
	}
	first, second := installDir(t), installDir(t)
	var out1 strings.Builder
	cmd := exec.Command("bash", "-c", `. "$1"; `+stubs, "test", src)
	cmd.Dir = first
	cmd.Env = append(os.Environ(), "PA_TEST_HOST_LOCK="+lock)
	cmd.Stdout, cmd.Stderr = &out1, &out1
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = cmd.Process.Kill() }()
	for i := 0; ; i++ {
		if _, err := os.Stat(filepath.Join(first, "held")); err == nil {
			break
		}
		if i > 400 {
			t.Fatalf("the first run never reached its backup: %s", out1.String())
		}
		time.Sleep(50 * time.Millisecond)
	}

	out, ok := autoUpgrade(t, second, stubs, "PA_TEST_HOST_LOCK="+lock)
	t.Log("second: " + out)
	if !ok || !strings.Contains(out, "another upgrade is running") || !strings.Contains(out, "exit 0") {
		t.Errorf("the second run, while the first holds the lock: %s", out)
	}
	if !strings.Contains(out, `{"state":"idle","last_check_result":"skipped","last_check_note":"another upgrade is running on this host: trying again next hour","running_version":"1.18.0"}`) {
		t.Errorf("the second run's status doesn't record a skipped check: %s", out)
	}
	if _, err := os.Stat(filepath.Join(second, "held")); err == nil {
		t.Error("the second run backed up while the first held the lock")
	}
	log, _ := os.ReadFile(filepath.Join(second, "au", "status", "auto-upgrade.log"))
	if !strings.Contains(string(log), `"step":"check","result":"skipped","from":"1.18.0","to":"","reason":"another upgrade is running"`) {
		t.Errorf("the second run's log line: %s", log)
	}

	if err := os.WriteFile(filepath.Join(first, "go"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := cmd.Wait(); err != nil {
		t.Fatalf("the first run: %v\n%s", err, out1.String())
	}
	t.Log("first: " + out1.String())
	if !strings.Contains(out1.String(), "exit 0") || !strings.Contains(out1.String(), `"last_check_result":"upgraded"`) {
		t.Errorf("the first run didn't finish its upgrade: %s", out1.String())
	}
	// Once it's done, the lock is free: the next run goes ahead.
	_ = os.WriteFile(filepath.Join(second, "go"), nil, 0o644)
	if out, _ := autoUpgrade(t, second, stubs, "PA_TEST_HOST_LOCK="+lock); strings.Contains(out, "another upgrade is running") || !strings.Contains(out, `"last_check_result":"upgraded"`) {
		t.Errorf("the lock was still held after the first run: %s", out)
	}
	if fi, err := os.Stat(lock); err != nil || fi.Mode().Perm() != 0o666 {
		t.Errorf("the lock file: %v %v, want mode 666 so another app's user can open it", fi, err)
	}
}

// R76a, R84l, A136, A137: an automatic upgrade unpacks the release's archive
// over the install and the monitoring stack's own folder: what a companion
// app dropped in their include folders is still there, unchanged, and a
// rollback's copy of the monitoring stack's files leaves them out.
func TestA137UpgradeKeepsDropIns(t *testing.T) {
	assets := releaseAssets(t)
	pa, mon := installDir(t), t.TempDir()
	release := filepath.Join(pa, "au", "releases", "1.19.0")
	if err := os.MkdirAll(release, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, f := range []string{"compose.yaml", "pagerarchive-config.tar.gz", "monitoring-compose.yaml"} {
		data, err := os.ReadFile(filepath.Join(assets, f))
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(release, f), data, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	dropped := map[string]string{
		filepath.Join(pa, "deploy", "authentik", "blueprints.d", "companion.yaml"): "# a companion's blueprint\n",
		filepath.Join(mon, "include", "alloy.d", "companion.alloy"):                "// a companion's config\n",
		filepath.Join(mon, "include", "alerts.d", "companion.yaml"):                "# a companion's rules\n",
		filepath.Join(mon, "include", "dashboards.d", "Companion", "calls.json"):   "{}\n",
		// The names the archive's own files have elsewhere.
		filepath.Join(mon, "include", "alerts.d", "README.md"): "the companion's own README\n",
	}
	for p, data := range dropped {
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(data), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	script := `PA_DIR=$PWD HOME_AU=$PWD/au MON_DIR=$MON TO=1.19.0 PREFIX=ghcr.io/rikiku/ FILES="compose.yaml pagerarchive-config.tar.gz monitoring-compose.yaml"
verified_file() { :; }
install_files || { echo "install_files: $REASON"; exit 1; }`
	out, ok := autoUpgrade(t, pa, script, "MON="+mon)
	if !ok {
		t.Fatalf("install: %s", out)
	}
	for p, data := range dropped {
		got, err := os.ReadFile(p)
		if err != nil || string(got) != data {
			t.Errorf("%s after the upgrade: %q %v, want %q", p, got, err, data)
		}
	}
	for _, f := range []string{"alloy/config.alloy", "include.sh", "grafana/provisioning/alerting/rules.yaml"} {
		if _, err := os.Stat(filepath.Join(mon, f)); err != nil {
			t.Errorf("the upgrade didn't install the monitoring stack's %s: %v", f, err)
		}
	}
	// back_up's list: the rollback puts back what it kept aside, never include/.
	data, err := os.ReadFile("deploy/auto-upgrade/auto-upgrade.sh")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "for f in compose.yaml compose.override.yaml .env alloy grafana include.sh loki prometheus tempo; do") {
		t.Error("back_up doesn't keep the monitoring stack's files aside as this test expects: update both")
	}
}
