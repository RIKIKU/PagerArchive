package pagerarchive

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// clientAutoUpgrade runs script in bash with deploy/client/auto-upgrade.sh
// sourced (only its functions), in a made-up client folder dir, and returns
// its output and whether it exited 0.
func clientAutoUpgrade(t *testing.T, dir, script string, env ...string) (string, bool) {
	t.Helper()
	if _, err := exec.LookPath("bash"); err != nil {
		t.Skip("needs bash")
	}
	src, err := filepath.Abs("deploy/client/auto-upgrade.sh")
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("bash", "-c", `. "$1"; PA_DIR=$PWD SERVICE=client; `+script, "test", src)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), env...)
	out, err := cmd.CombinedOutput()
	return string(out), err == nil
}

func clientDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	for name, data := range map[string]string{"compose.yaml": "services: {}\n", ".env": "SERVER_URL=http://127.0.0.1:1\n", "key": "a155-secret-key\n"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(data), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

// A155 (R14b, R84n, R84k): the client's automatic upgrades take versions only
// whole, never pick a release newer than the server's, a pre-release, a draft
// or another major version, and refuse bad arguments before anything runs.
func TestA155ClientAutoUpgradeVersions(t *testing.T) {
	dir := clientDir(t)
	for _, v := range []string{"", "1.2", "1.2.3.4", " 1.2.3", "1.2.3 ", "1.2.3\n", "1.2.3;id", "1.2.3$(id)", "1.2.3-rc.1", "1.2.3/../x", "v1.2.3"} {
		if out, ok := clientAutoUpgrade(t, dir, `is_release "$V"`, "V="+v); ok {
			t.Errorf("is_release %q passed %s", v, out)
		}
	}
	// The running client: only the first "pagerarchive-client " line.
	out, ok := clientAutoUpgrade(t, dir, `dc() { printf 'pagerarchive-client 1.2.3\npagerarchive-client 1.0;id\n'; }; printf '[%s]' "$(running_version)"`)
	if !ok || out != "[1.2.3]" {
		t.Errorf("running_version: %q", out)
	}
	// What isn't a release is shown only when it's version-like.
	if out, _ := clientAutoUpgrade(t, dir, `printf '[%s][%s]' "$(shown '1.2.3-dev+abc1234')" "$(shown '1.0;id')"`); out != "[1.2.3-dev+abc1234][not a version]" {
		t.Errorf("shown: %q", out)
	}

	if _, err := exec.LookPath("jq"); err != nil {
		t.Skip("needs jq")
	}
	releases := `[{"draft":false,"prerelease":false,"tag_name":"v1.12.0"},{"draft":false,"prerelease":false,"tag_name":"v1.11.0"},
		{"draft":false,"prerelease":false,"tag_name":"v1.13.0"},{"draft":false,"prerelease":true,"tag_name":"v1.12.5"},
		{"draft":true,"prerelease":false,"tag_name":"v1.12.6"},{"draft":false,"prerelease":false,"tag_name":"v2.0.0"},
		{"draft":false,"prerelease":false,"tag_name":"v1.12.1;id"}]`
	pick := `JQ=jq RELEASES=$(mktemp); curl_api() { printf '%s' "$RELS"; }
if pick_release "$MAJOR"; then printf '%s %s' "$TARGET_TAG" "$NEWEST_TAG"; else printf refused; fi`
	for _, c := range []struct{ server, major, want string }{
		{"1.12.0", "1", "v1.12.0 v2.0.0"}, // the server's own, not 1.13.0, 1.12.5 (pre) or 1.12.6 (draft)
		{"1.12.3", "1", "v1.12.0 v2.0.0"}, // a server between releases: the newest not past it
		{"1.11.9", "1", "v1.11.0 v2.0.0"}, // an older server: an older release
		{"1.10.0", "1", " v2.0.0"},        // nothing as old as the server
		{"9.0.0", "1", "v1.13.0 v2.0.0"},  // only the client's major version
		{"9.0.0", "2", "v2.0.0 v2.0.0"},
	} {
		if out, _ := clientAutoUpgrade(t, dir, pick, "RELS="+releases, "SERVER_VERSION="+c.server, "MAJOR="+c.major); out != c.want {
			t.Errorf("server %s, major %s: %q, want %q", c.server, c.major, out, c.want)
		}
	}
	// A tag with a line break isn't taken.
	if out, _ := clientAutoUpgrade(t, dir, pick, `RELS=[{"draft":false,"prerelease":false,"tag_name":"v1.12.0\nv1.0;id"}]`, "SERVER_VERSION=1.12.0", "MAJOR=1"); out != " " {
		t.Errorf("a tag with a line break: %q", out)
	}

	// Arguments: refused before anything runs, with the usage.
	for _, args := range [][]string{{"retry", "1.2.3;id"}, {"retry", ""}, {"verify", "1.2.3"}, {"verify", "v1.2.3;id"}} {
		cmd := exec.Command("bash", append([]string{"deploy/client/auto-upgrade.sh"}, args...)...)
		cmd.Env = append(os.Environ(), "AUTO_UPGRADE_INSTALL_DIR="+dir, "AUTO_UPGRADE_HOME="+filepath.Join(dir, "au"))
		out, err := cmd.CombinedOutput()
		if code := cmd.ProcessState.ExitCode(); err == nil || code != 2 || !strings.Contains(string(out), "usage: client-auto-upgrade.sh "+args[0]) {
			t.Errorf("%q: exit %d: %s", args, code, out)
		}
	}
}

// A155 (R84p): the report goes to the server with the client's key in a
// header file only this user can read, never on curl's command line (which
// any user on the host can see), and brings back the server's version.
func TestA155ClientReportKeepsTheKeyOffTheCommandLine(t *testing.T) {
	if _, err := exec.LookPath("jq"); err != nil {
		t.Skip("needs jq")
	}
	dir := clientDir(t)
	script := `JQ=jq HOME_AU=$PWD/au; mkdir -p au/status; STATUS=au/status/status.json; echo '{"enabled":true,"state":"checking"}' >"$STATUS"
RUNNING=1.11.0; SERVER_URL=http://server.example.test/
curl() {
	local a prev="" hdr=""
	for a in "$@"; do
		case $a in *a155-secret-key*) echo "KEY ON THE COMMAND LINE" >>"$PA_DIR/curl.txt" ;; esac
		[ "$prev" = -H ] && case $a in @*) hdr=${a#@} ;; esac
		prev=$a
	done
	printf 'header:%s mode:%s url:%s body:%s\n' "$(cat "$hdr")" "$(stat -c %a "$hdr")" "${@: -1}" "$(cat)" >>"$PA_DIR/curl.txt"
	printf '{"client_id":"a155","server_version":"1.12.0"}'
}
report && printf 'server says %s; left: %s\n' "$SERVER_SAYS" "$(ls -A au | tr '\n' ' ')"; cat curl.txt`
	out, ok := clientAutoUpgrade(t, dir, script)
	if !ok || strings.Contains(out, "KEY ON THE COMMAND LINE") {
		t.Fatalf("report: %s", out)
	}
	for _, want := range []string{"header:Authorization: Bearer a155-secret-key", "mode:600", "url:http://server.example.test/api/v1/client/auto-upgrade",
		`body:{"client_version":"1.11.0","status":{"enabled":true,"state":"checking"}}`, "server says 1.12.0; left: status "} {
		if !strings.Contains(out, want) {
			t.Errorf("report: no %q in %s", want, out)
		}
	}
}

// R14b, ADR 0054: a release's client-compose.yaml names the client's image by
// the release's own version, so `docker compose pull` never moves it past the
// server; any other build's names main.
func TestR14bClientComposeNamesTheExactVersion(t *testing.T) {
	for v, want := range map[string]string{"1.12.0": "1.12.0", "1.12.1-dev+abc1234": "main", "0.0.0-dev": "main"} {
		out, err := exec.Command("sh", "-c", `. scripts/ci/versions-lib.sh; client_image_tag_for "$1"`, "sh", v).Output()
		if err != nil || strings.TrimSpace(string(out)) != want {
			t.Errorf("client_image_tag_for %s: %q (%v), want %s", v, out, err, want)
		}
	}
	// The repository's file names main; a release build has stamped its own
	// version there before the tests run (stamp-version.sh), as
	// TestR84aComposeNamesThisRelease allows for the server's compose.yaml.
	tag := "main"
	if Version != unstampedVersion {
		out, err := exec.Command("sh", "-c", `. scripts/ci/versions-lib.sh; client_image_tag_for "$1"`, "sh", Version).Output()
		if err != nil {
			t.Fatal(err)
		}
		tag = strings.TrimSpace(string(out))
	}
	data, err := os.ReadFile("deploy/client/compose.yaml")
	if err != nil {
		t.Fatal(err)
	}
	if want := "\n    image: ghcr.io/rikiku/pagerarchive-client:" + tag + "\n"; !strings.Contains(string(data), want) {
		t.Errorf("deploy/client/compose.yaml doesn't name ghcr.io/rikiku/pagerarchive-client:%s (stamp-version.sh writes it)", tag)
	}
}
