package monitortest

import (
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// A90's Grafana part (R84h; owner, 2026-11-15: "If an automatic update fails
// ... we should be capturing that in Grafana"): the automatic upgrades' status
// file, in the folder the API and Alloy both mount read-only
// (PA_MON_AUTO_UPGRADE_DIR), reaches Prometheus through the API's metrics and
// the dashboard; the script's log reaches Loki one line per step, with only
// its known fields, and a line that isn't one of the script's is dropped; a
// rolled-back upgrade fires "Upgrade failed" through the same contact point as
// every other alert, and clearing it resolves it. The script itself is A90's
// (scripts/a90.sh), which checks it writes these.
func TestA90UpgradesInGrafana(t *testing.T) {
	s := newStack(t)
	dir := os.Getenv("PA_MON_AUTO_UPGRADE_DIR")
	if dir == "" {
		t.Fatal("PA_MON_AUTO_UPGRADE_DIR is not set")
	}
	hook := &webhook{}
	l, err := net.Listen("tcp", os.Getenv("PA_MON_WEBHOOK_ADDR"))
	if err != nil {
		t.Fatal(err)
	}
	go http.Serve(l, hook)
	defer l.Close()

	write := func(name, data string) {
		t.Helper()
		tmp := filepath.Join(dir, name+".partial")
		if err := os.WriteFile(tmp, []byte(data), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.Rename(tmp, filepath.Join(dir, name)); err != nil {
			t.Fatal(err)
		}
	}
	logLines := func(lines ...string) {
		t.Helper()
		f, err := os.OpenFile(filepath.Join(dir, "auto-upgrade.log"), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
		if err != nil {
			t.Fatal(err)
		}
		defer f.Close()
		for _, line := range lines {
			fmt.Fprintln(f, line)
		}
	}
	at := func(ago time.Duration) int64 { return time.Now().Add(-ago).Unix() }
	stamp := func() string { return time.Now().UTC().Format(time.RFC3339) }
	run := fmt.Sprintf("a90-%d", time.Now().UnixNano())

	// A good upgrade: metrics and the dashboard.
	checked := at(0)
	write("status.json", fmt.Sprintf(`{"enabled":true,"state":"idle","running_since":0,"last_check_at":%d,"last_check_result":"upgraded",
		"running_version":"1.10.0","latest_version":"1.10.0","failures":0,"rollbacks":0,"failed_versions":[],
		"last_upgrade":{"at":%d,"from":"1.9.0","to":"1.10.0"},"history":[]}`, checked, checked))
	for _, step := range []string{"check", "verify", "backup", "upgrade", "health"} {
		logLines(fmt.Sprintf(`{"time":"%s","level":"info","step":"%s","result":"ok","from":"1.9.0","to":"1.10.0","reason":"%s","seconds":3}`, stamp(), step, run))
	}
	// Not the script's: an extra field, and a line that isn't its JSON at all.
	logLines(fmt.Sprintf(`{"time":"%s","level":"info","step":"check","result":"ok","from":"","to":"","reason":"%s extra","seconds":1,"text":"A90 SECRET PAGE TEXT"}`, stamp(), run),
		"A90 SECRET PAGE TEXT on a line of its own")

	if !waitFor(90*time.Second, func() bool {
		return s.query(t, fmt.Sprintf(`pagerarchive_auto_upgrade_last_check_timestamp_seconds{job="pagerarchive"} >= %d`, checked)) == 1 &&
			s.query(t, `pagerarchive_auto_upgrade_failing{job="pagerarchive"} == 0`) == 1
	}) {
		t.Fatal("Prometheus never had the upgrade's status: does the API mount the folder (AUTO_UPGRADE_STATUS_DIR)?")
	}
	one := func(title, ref string) (float64, bool) {
		for _, v := range s.panelValues(t, "pa-backups", title)[ref] {
			return v, true
		}
		return 0, false
	}
	if v, ok := one("Since the last upgrade check", "A"); !ok || v < 0 || v > 300 {
		t.Errorf("dashboard: since the last upgrade check %v s (shown %t), want under 5 minutes", v, ok)
	}
	if v, ok := one("Last upgrade", "A"); !ok || v != 0 {
		t.Errorf("dashboard: last upgrade %v (shown %t), want 0, OK", v, ok)
	}
	if v, ok := one("Last successful upgrade", "A"); !ok || int64(v/1000) != checked {
		t.Errorf("dashboard: last successful upgrade %v (shown %t), want %d000", v, ok, checked)
	}
	t.Log("A90: a good upgrade: in Prometheus, and the dashboard's panels show it")

	// The log in Loki: one line per step, labelled, with only the known fields.
	var lines []lokiLine
	if !waitFor(2*time.Minute, func() bool {
		lines = s.upgradeLines(t, run)
		return len(lines) >= 6
	}) {
		t.Fatalf("the script's log lines didn't reach Loki within 2 minutes: %d of 6 (%s). Loki: %s", len(lines), describeLines(lines), lokiRefusals(t))
	}
	for _, step := range []string{"verify", "backup", "upgrade", "health"} {
		if n := len(s.upgradeLinesWhere(t, fmt.Sprintf(`step="%s", result="ok"`, step), run)); n != 1 {
			t.Errorf("Loki: %d lines labelled step=%s result=ok, want 1", n, step)
		}
	}
	for _, l := range lines {
		if strings.Contains(l.line, "SECRET") || strings.Contains(l.line, `"text"`) || strings.Contains(l.line, `"time"`) {
			t.Errorf("Loki kept more than the known fields: %s", l.line)
		}
	}
	t.Logf("A90: the log's lines in Loki, only their known fields: %s", lines[0].line)

	// A rolled-back upgrade: the alert fires, the dashboard says FAILED.
	since := time.Now()
	failed := at(0)
	status := `{"enabled":true,"state":"idle","running_since":0,"last_check_at":%d,"last_check_result":"rolled-back",
		"running_version":"1.10.0","latest_version":"1.11.0","failures":1,"rollbacks":1,"failed_versions":["1.11.0"],
		"last_upgrade":{"at":%d,"from":"1.9.0","to":"1.10.0"},
		"last_failure":{"at":%d,"from":"1.10.0","to":"1.11.0","step":"health","reason":"no page stored","rolled_back":true,"restored":true%s},"history":[]}`
	write("status.json", fmt.Sprintf(status, failed, checked-60, failed, ""))
	logLines(fmt.Sprintf(`{"time":"%s","level":"error","step":"health","result":"failed","from":"1.10.0","to":"1.11.0","reason":"%s no page stored","seconds":120}`, stamp(), run),
		fmt.Sprintf(`{"time":"%s","level":"info","step":"restore","result":"ok","from":"1.10.0","to":"1.11.0","reason":"%s","seconds":40}`, stamp(), run),
		fmt.Sprintf(`{"time":"%s","level":"warn","step":"rollback","result":"ok","from":"1.10.0","to":"1.11.0","reason":"%s","seconds":90}`, stamp(), run))
	if !waitFor(2*time.Minute, func() bool {
		_, ok := hook.find(since, "Upgrade failed", "firing", "")
		return ok
	}) {
		t.Fatal(`"Upgrade failed" didn't fire within 2 minutes of a rolled-back upgrade`)
	}
	fired, _ := hook.find(since, "Upgrade failed", "firing", "")
	t.Logf("A90: \"Upgrade failed\" fired %s after the rollback was recorded", fired.Sub(since).Round(time.Second))
	if v, ok := one("Last upgrade", "A"); !ok || v != 1 {
		t.Errorf("dashboard: last upgrade %v (shown %t) after a rollback, want 1, FAILED", v, ok)
	}
	if v, ok := one("Upgrades failed and rolled back", "B"); !ok || v != 1 {
		t.Errorf("dashboard: rolled back %v (shown %t), want 1", v, ok)
	}
	waitFor(time.Minute, func() bool { return len(s.upgradeLinesWhere(t, `step="rollback"`, run)) == 1 })
	if n := len(s.upgradeLinesWhere(t, `level="error", step="health", result="failed"`, run)); n != 1 {
		t.Errorf("Loki: %d failed health check lines, want 1", n)
	}
	for _, step := range []string{"restore", "rollback"} {
		if n := len(s.upgradeLinesWhere(t, fmt.Sprintf(`step="%s"`, step), run)); n != 1 {
			t.Errorf("Loki: %d %s lines, want 1", n, step)
		}
	}

	// Cleared (auto-upgrade.sh clear): it resolves.
	since = time.Now()
	write("status.json", fmt.Sprintf(status, failed, checked-60, failed, fmt.Sprintf(`,"cleared_at":%d`, at(0))))
	if !waitFor(3*time.Minute, func() bool {
		_, ok := hook.find(since, "Upgrade failed", "resolved", "")
		return ok
	}) {
		t.Fatal(`"Upgrade failed" didn't resolve after it was cleared`)
	}
	t.Log("A90: it resolved once cleared")
}

// upgradeLines is every line Loki has from the automatic upgrades in the last
// hour whose reason holds run (this test's own).
func (s *stack) upgradeLines(t *testing.T, run string) []lokiLine {
	return s.upgradeLinesWhere(t, "", run)
}

func (s *stack) upgradeLinesWhere(t *testing.T, labels, run string) []lokiLine {
	t.Helper()
	sel := `service_name="pagerarchive-auto-upgrade"`
	if labels != "" {
		sel += ", " + labels
	}
	var logs struct {
		Data struct {
			Result []struct {
				Values [][2]string `json:"values"`
			} `json:"result"`
		} `json:"data"`
	}
	end := time.Now().Add(time.Minute)
	q := fmt.Sprintf(`{%s} |= %q`, sel, run)
	s.grafanaGet(t, fmt.Sprintf("/api/datasources/proxy/uid/loki/loki/api/v1/query_range?query=%s&start=%d&end=%d&limit=5000",
		url.QueryEscape(q), end.Add(-time.Hour).UnixNano(), end.UnixNano()), &logs)
	var out []lokiLine
	for _, r := range logs.Data.Result {
		for _, v := range r.Values {
			out = append(out, lokiLine{line: v[1]})
		}
	}
	return out
}
