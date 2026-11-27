package acceptance

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/RIKIKU/PagerArchive/internal/server"
)

// A156 (R14b, R14c, R84p; ADR 0054): the server knows which version each
// client runs, from its User-Agent and its host's automatic upgrades' report,
// and since when it's been behind the server's release; the report, with the
// client's own key, answers with the server's version and keeps only
// versions, known words, times and counts; the metrics have all of it, per
// client. The script's side is A155 (scripts/a90.sh), Grafana's TestA157.
func TestA156ClientVersions(t *testing.T) {
	e := setup(t)
	e.srv.Version = "1.12.0"
	scrape := func() string {
		t.Helper()
		req, _ := http.NewRequest(http.MethodGet, e.url+"/metrics", nil)
		req.Header.Set("Authorization", "Bearer "+metricsToken)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		return string(b)
	}
	has := func(m string, want ...string) {
		t.Helper()
		for _, w := range want {
			if !strings.Contains(m, w) {
				t.Errorf("metrics: no %q", w)
			}
		}
	}
	hasNot := func(m string, not ...string) {
		t.Helper()
		for _, n := range not {
			if strings.Contains(m, n) {
				t.Errorf("metrics have %q", n)
			}
		}
	}
	post := func(ua string) {
		t.Helper()
		text := fmt.Sprintf("A156 PAGE %d", time.Now().UnixNano())
		e.posts = append(e.posts, text)
		body, _ := json.Marshal(server.PageRequest{ClientID: "a", Capcode: "0100001", Text: text, ReceivedAt: time.Now().UTC(), Baud: 512})
		req, _ := http.NewRequest(http.MethodPost, e.url+"/api/v1/pages", bytes.NewReader(body))
		req.Header.Set("Authorization", "Bearer "+e.keys["a"])
		req.Header.Set("User-Agent", ua)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("post: %d", resp.StatusCode)
		}
	}
	report := func(key, body string) (int, server.ClientUpgradeAnswer) {
		t.Helper()
		req, _ := http.NewRequest(http.MethodPost, e.url+"/api/v1/client/auto-upgrade", strings.NewReader(body))
		if key != "" {
			req.Header.Set("Authorization", "Bearer "+key)
		}
		req.Header.Set("Content-Type", "application/json")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		var a server.ClientUpgradeAnswer
		_ = json.NewDecoder(resp.Body).Decode(&a)
		return resp.StatusCode, a
	}

	// Before a client says, nothing.
	m := scrape()
	hasNot(m, `pagerarchive_client_version_info{client="a"`, `pagerarchive_client_behind_since_timestamp_seconds{client="a"}`, `pagerarchive_client_auto_upgrade_enabled{`)

	// An older release's client: behind, since now.
	before := time.Now().Add(-time.Second).Unix()
	post("pagerarchive-client/1.11.0")
	m = scrape()
	has(m, `pagerarchive_client_version_info{client="a",version="1.11.0"} 1`, `pagerarchive_client_behind_since_timestamp_seconds{client="a"} `)
	var since time.Time
	if err := e.st.Pool.QueryRow(e.ctx, `SELECT behind_since FROM clients WHERE id = 'a'`).Scan(&since); err != nil || since.Unix() < before {
		t.Errorf("behind since %v (%v), want about now", since, err)
	}
	// Another older release: still behind, and since when doesn't move.
	post("pagerarchive-client/1.11.5 (linux)")
	var since2 time.Time
	if err := e.st.Pool.QueryRow(e.ctx, `SELECT behind_since FROM clients WHERE id = 'a'`).Scan(&since2); err != nil || !since2.Equal(since) {
		t.Errorf("behind since %v after another older release, want %v kept (%v)", since2, since, err)
	}
	// Anything that isn't a version is ignored (R84k).
	for _, ua := range []string{"pagerarchive-client/1.12.0;id", "pagerarchive-client/1.12", "Go-http-client/1.1", "pagerarchive-client/1.12.0-rc.1", "x pagerarchive-client/1.12.0"} {
		post(ua)
	}
	m = scrape()
	has(m, `pagerarchive_client_version_info{client="a",version="1.11.5"} 1`)
	// The server's version: not behind.
	post("pagerarchive-client/1.12.0")
	m = scrape()
	has(m, `pagerarchive_client_version_info{client="a",version="1.12.0"} 1`)
	hasNot(m, `pagerarchive_client_behind_since_timestamp_seconds{client="a"}`, `version="1.11.5"`)

	// The report: the client's key only.
	running := time.Now().Add(-2 * time.Minute).Unix()
	status := fmt.Sprintf(`{"enabled":true,"state":"health-check","running_since":%d,"target_version":"1.12.0","last_check_at":%d,
		"last_check_result":"in-progress","last_check_note":"A156 SECRET NOTE","running_version":"1.11.0","latest_version":"1.11.0",
		"server_version":"1.12.0","failures":1,"rollbacks":1,"failed_versions":["1.11.9","1.0;id"],
		"last_upgrade":{"at":%d,"from":"1.10.0","to":"1.11.0"},
		"last_failure":{"at":%d,"from":"1.10.0","to":"1.11.9","step":"health","reason":"A156 SECRET REASON","rolled_back":true},
		"history":[{"at":1,"from":"1.0.0","to":"1.1.0","result":"upgraded"}]}`, running, running, running-86400, running-90000)
	body := fmt.Sprintf(`{"client_version":"1.11.0","status":%s,"later_field":1}`, status)
	if code, _ := report("", body); code != http.StatusUnauthorized {
		t.Errorf("a report with no key: %d", code)
	}
	if code, _ := report("not-a-key", body); code != http.StatusUnauthorized {
		t.Errorf("a report with a wrong key: %d", code)
	}
	code, answer := report(e.keys["b"], body)
	if code != http.StatusOK || answer.ServerVersion != "1.12.0" || answer.ClientID != "b" {
		t.Fatalf("the report: %d %+v", code, answer)
	}
	m = scrape()
	has(m,
		`pagerarchive_client_version_info{client="b",version="1.11.0"} 1`,
		`pagerarchive_client_behind_since_timestamp_seconds{client="b"} `,
		`pagerarchive_client_auto_upgrade_enabled{client="b"} 1`,
		fmt.Sprintf(`pagerarchive_client_auto_upgrade_last_check_timestamp_seconds{client="b"} %d`, running),
		`pagerarchive_client_auto_upgrade_last_check_result{client="b",result="in-progress"} 1`,
		`pagerarchive_client_auto_upgrade_run{client="b",step="health-check"} 1`,
		fmt.Sprintf(`pagerarchive_client_auto_upgrade_run_start_timestamp_seconds{client="b"} %d`, running),
		`pagerarchive_client_auto_upgrade_target_info{client="b",to="1.12.0"} 1`,
		fmt.Sprintf(`pagerarchive_client_auto_upgrade_last_success_timestamp_seconds{client="b",from="1.10.0",to="1.11.0"} %d`, running-86400),
		`pagerarchive_client_auto_upgrade_failures_total{client="b"} 1`,
		`pagerarchive_client_auto_upgrade_failing{client="b"} 0`,
		`pagerarchive_client_auto_upgrade_reported_timestamp_seconds{client="b"} `,
	)
	hasNot(m, `pagerarchive_client_auto_upgrade_enabled{client="a"}`)
	var kept string
	if err := e.st.Pool.QueryRow(e.ctx, `SELECT auto_upgrade::text FROM clients WHERE id = 'b'`).Scan(&kept); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(kept, "SECRET") || strings.Contains(kept, "1.0;id") || strings.Contains(kept, `"result"`) {
		t.Errorf("the server kept the host's own text: %s", kept)
	}
	// A run that ended in a rollback: failing, and no run going.
	code, _ = report(e.keys["b"], fmt.Sprintf(`{"client_version":"1.11.0","status":{"enabled":true,"state":"idle","last_check_at":%d,
		"last_check_result":"rolled-back","failures":2,"last_upgrade":{"at":%d,"from":"1.10.0","to":"1.11.0"},
		"last_failure":{"at":%d,"from":"1.11.0","to":"1.12.0","step":"health","rolled_back":true}}}`, running, running-86400, running))
	if code != http.StatusOK {
		t.Fatalf("the second report: %d", code)
	}
	m = scrape()
	has(m, `pagerarchive_client_auto_upgrade_failing{client="b"} 1`, `pagerarchive_client_auto_upgrade_last_check_result{client="b",result="rolled-back"} 1`)
	hasNot(m, `pagerarchive_client_auto_upgrade_run{client="b"`, `pagerarchive_client_auto_upgrade_target_info{client="b"`)
	// Not a status: refused, and the last report kept.
	for _, bad := range []string{`{"status":[1,2]}`, `{"status":"x"}`, `not json`} {
		if code, _ := report(e.keys["b"], bad); code != http.StatusUnprocessableEntity && code != http.StatusBadRequest {
			t.Errorf("report %s: %d", bad, code)
		}
	}
	// Only the version, no status: the version, and nothing else changes.
	if code, _ := report(e.keys["b"], `{"client_version":"1.11.0"}`); code != http.StatusOK {
		t.Errorf("a report with only the version: %d", code)
	}
	has(scrape(), `pagerarchive_client_auto_upgrade_failing{client="b"} 1`)

	// At start-up the server works it out again for its own version (R14c):
	// a's 1.12.0 and b's 1.11.0 against 1.11.0, 1.13.0 and a build of main.
	for _, c := range []struct {
		server string
		a, b   bool
	}{{"1.11.0", false, false}, {"1.13.0", true, true}, {"1.13.0-dev+abc1234", false, false}, {"1.12.0", false, true}} {
		e.srv.Version = c.server
		if err := e.srv.RecheckClientVersions(e.ctx); err != nil {
			t.Fatal(err)
		}
		m = scrape()
		for id, want := range map[string]bool{"a": c.a, "b": c.b} {
			if got := strings.Contains(m, fmt.Sprintf(`pagerarchive_client_behind_since_timestamp_seconds{client="%s"}`, id)); got != want {
				t.Errorf("server %s: client %s behind %t, want %t", c.server, id, got, want)
			}
		}
	}
}

// R14b: "behind" is an older release than the server's; a build that isn't
// a release is neither behind nor ahead.
func TestR14bBehind(t *testing.T) {
	for _, c := range []struct {
		client, server string
		want           bool
	}{
		{"1.11.0", "1.12.0", true}, {"1.12.0", "1.12.1", true}, {"1.9.9", "1.10.0", true}, {"0.20.0", "1.0.0", true},
		{"1.12.0", "1.12.0", false}, {"1.12.1", "1.12.0", false}, {"2.0.0", "1.12.0", false},
		{"1.11.0-dev+abc1234", "1.12.0", false}, {"1.11.0", "1.12.0-dev+abc1234", false}, {"", "1.12.0", false}, {"1.11.0", "test", false},
	} {
		if got := server.Behind(c.client, c.server); got != c.want {
			t.Errorf("Behind(%q, %q) = %t, want %t", c.client, c.server, got, c.want)
		}
	}
}
