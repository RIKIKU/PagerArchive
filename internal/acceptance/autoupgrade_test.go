package acceptance

import (
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/RIKIKU/PagerArchive/internal/autoupgrade"
)

// R84h: what the host's automatic upgrades last did, read from their status
// file, for admins only, and in the metrics (R52) for the example monitoring
// stack: off with no file; a rollback is failing. The script's side is A90
// (scripts/a90.sh), and Grafana's the monitoring check's TestA90.
func TestAutoUpgradeStatus(t *testing.T) {
	e := setup(t)
	path := filepath.Join(t.TempDir(), "status.json")
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

	var st autoupgrade.Status
	if code, _ := e.admin.call(http.MethodGet, "/api/v1/admin/auto-upgrade", nil, &st); code != http.StatusOK || st.State != autoupgrade.StateOff {
		t.Errorf("no status file set: %d %q", code, st.State)
	}
	if m := scrape(); strings.Contains(m, "pagerarchive_auto_upgrade_") {
		t.Errorf("metrics with no status file set:\n%s", m)
	}
	e.srv.AutoUpgradeStatusFile = path
	if code, _ := e.admin.call(http.MethodGet, "/api/v1/admin/auto-upgrade", nil, &st); code != http.StatusOK || st.State != autoupgrade.StateOff {
		t.Errorf("no status file yet: %d %q", code, st.State)
	}
	if m := scrape(); !strings.Contains(m, "pagerarchive_auto_upgrade_enabled 0\n") || strings.Contains(m, "pagerarchive_auto_upgrade_failing") {
		t.Errorf("metrics while off:\n%s", m)
	}
	if code, _ := e.user.call(http.MethodGet, "/api/v1/admin/auto-upgrade", nil, nil); code != http.StatusForbidden {
		t.Errorf("a user saw the automatic upgrades: %d", code)
	}

	now := time.Now().Unix()
	write := func(s string) {
		t.Helper()
		if err := os.WriteFile(path, []byte(s), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write(fmt.Sprintf(`{"enabled":true,"state":"idle","last_check_at":%d,"last_check_result":"upgraded","running_version":"1.10.0",
		"latest_version":"1.10.0","failures":0,"rollbacks":0,"failed_versions":[],
		"last_upgrade":{"at":%d,"from":"1.9.0","to":"1.10.0"},"history":[{"at":%d,"from":"1.9.0","to":"1.10.0","result":"upgraded"}]}`, now, now, now))
	if code, _ := e.admin.call(http.MethodGet, "/api/v1/admin/auto-upgrade", nil, &st); code != http.StatusOK || st.State != autoupgrade.StateOK ||
		st.LastUpgrade == nil || st.LastUpgrade.To != "1.10.0" || len(st.History) != 1 {
		t.Errorf("after an upgrade: %d %+v", code, st)
	}
	m := scrape()
	for _, want := range []string{
		"pagerarchive_auto_upgrade_enabled 1\n",
		fmt.Sprintf("pagerarchive_auto_upgrade_last_check_timestamp_seconds %d\n", now),
		fmt.Sprintf(`pagerarchive_auto_upgrade_last_success_timestamp_seconds{from="1.9.0",to="1.10.0"} %d`, now),
		"pagerarchive_auto_upgrade_failing 0\n",
		"pagerarchive_auto_upgrade_failures_total 0\n",
		`pagerarchive_auto_upgrade_state{state="ok"} 1`,
		`pagerarchive_auto_upgrade_run{step="idle"} 1`,
	} {
		if !strings.Contains(m, want) {
			t.Errorf("metrics after an upgrade: no %q in\n%s", want, m)
		}
	}

	write(fmt.Sprintf(`{"enabled":true,"state":"idle","last_check_at":%d,"last_check_result":"rolled-back","running_version":"1.10.0",
		"latest_version":"1.11.0","failures":1,"rollbacks":1,"failed_versions":["1.11.0"],
		"last_upgrade":{"at":%d,"from":"1.9.0","to":"1.10.0"},
		"last_failure":{"at":%d,"from":"1.10.0","to":"1.11.0","step":"health","reason":"no page stored within 30 minutes of the restart","rolled_back":true,"restored":true}}`,
		now, now-3600, now))
	if code, _ := e.admin.call(http.MethodGet, "/api/v1/admin/auto-upgrade", nil, &st); code != http.StatusOK || st.State != autoupgrade.StateFailed ||
		st.LastFailure == nil || !st.LastFailure.RolledBack || !st.LastFailure.Restored || st.FailedVersions[0] != "1.11.0" {
		t.Errorf("after a rollback: %d %+v", code, st)
	}
	m = scrape()
	for _, want := range []string{
		"pagerarchive_auto_upgrade_failing 1\n",
		"pagerarchive_auto_upgrade_failures_total 1\n",
		"pagerarchive_auto_upgrade_rollbacks_total 1\n",
		fmt.Sprintf(`pagerarchive_auto_upgrade_last_failure_timestamp_seconds{step="health",to="1.11.0"} %d`, now),
		`pagerarchive_auto_upgrade_state{state="failed"} 1`,
	} {
		if !strings.Contains(m, want) {
			t.Errorf("metrics after a rollback: no %q in\n%s", want, m)
		}
	}
}
