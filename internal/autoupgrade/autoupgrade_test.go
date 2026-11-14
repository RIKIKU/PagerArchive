package autoupgrade

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// R84h: off, ok, failed (rolled back or not), stale and stopped, from the file
// the host's script writes, by a clock A90 can't wait for.
func TestStates(t *testing.T) {
	now := time.Date(2026, 11, 15, 12, 0, 0, 0, time.UTC)
	ago := func(d time.Duration) int64 { return now.Add(-d).Unix() }
	path := filepath.Join(t.TempDir(), "status.json")
	for _, c := range []struct {
		name    string
		json    string
		want    string
		failing bool
	}{
		{"switched off", fmt.Sprintf(`{"enabled":false,"disabled_at":%d}`, ago(time.Hour)), StateOff, false},
		{"just switched on, no check yet", fmt.Sprintf(`{"enabled":true,"enabled_at":%d}`, ago(time.Minute)), StateOK, false},
		{"checked an hour ago", fmt.Sprintf(`{"enabled":true,"last_check_at":%d,"last_check_result":"up-to-date"}`, ago(time.Hour)), StateOK, false},
		{"no check for 3 hours", fmt.Sprintf(`{"enabled":true,"last_check_at":%d}`, ago(3*time.Hour+time.Minute)), StateStale, false},
		{"rolled back", fmt.Sprintf(`{"enabled":true,"last_check_at":%d,"last_failure":{"at":%d,"from":"1.9.0","to":"1.10.0","step":"health","reason":"x","rolled_back":true}}`,
			ago(time.Hour), ago(time.Hour)), StateFailed, true},
		{"rolled back, then a later release upgraded", fmt.Sprintf(`{"enabled":true,"last_check_at":%d,"last_failure":{"at":%d,"to":"1.10.0"},"last_upgrade":{"at":%d,"from":"1.9.0","to":"1.11.0"}}`,
			ago(time.Hour), ago(48*time.Hour), ago(time.Hour)), StateOK, false},
		{"a failure, stale too", fmt.Sprintf(`{"enabled":true,"last_check_at":%d,"last_failure":{"at":%d,"to":"1.10.0"}}`, ago(5*time.Hour), ago(5*time.Hour)), StateFailed, true},
		{"a run stopped part way", fmt.Sprintf(`{"enabled":true,"last_check_at":%d,"last_check_result":"interrupted"}`, ago(time.Minute)), StateStopped, true},
	} {
		if err := os.WriteFile(path, []byte(c.json), 0o644); err != nil {
			t.Fatal(err)
		}
		got := Read(path, now)
		if got.State != c.want || got.Failing() != c.failing {
			t.Errorf("%s: %s (failing %t), want %s (failing %t)", c.name, got.State, got.Failing(), c.want, c.failing)
		}
	}
}

// Off when there's no file, or none mounted (compose.yaml mounts /dev/null in
// the folder's place when AUTO_UPGRADE_STATUS_DIR isn't set), or no setting.
func TestOffWithoutAFile(t *testing.T) {
	now := time.Now()
	for _, path := range []string{"", filepath.Join(t.TempDir(), "status.json"), "/dev/null/status.json"} {
		if got := Read(path, now); got.State != StateOff || got.Failing() {
			t.Errorf("%q: %s", path, got.State)
		}
	}
}

func TestUnreadableIsAFailure(t *testing.T) {
	path := filepath.Join(t.TempDir(), "status.json")
	if err := os.WriteFile(path, []byte("{half"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := Read(path, time.Now()); !got.Failing() || got.LastFailure == nil {
		t.Errorf("an unreadable status: %+v", got)
	}
}

// Only versions, known steps and results come through: nothing else in the
// file reaches a page or a metric's label.
func TestOnlyVersionsAndKnownWords(t *testing.T) {
	path := filepath.Join(t.TempDir(), "status.json")
	data := `{"enabled":true,"state":"pwned","last_check_at":1,"running_version":"1.9.0 <b>","latest_version":"1.10.0",
		"failed_versions":["1.10.0","x y"],
		"last_failure":{"at":5,"from":"1.9.0","to":"\"}","step":"elsewhere"},
		"history":[{"at":5,"from":"1.9.0","to":"1.10.0","result":"something"}]}`
	if err := os.WriteFile(path, []byte(data), 0o644); err != nil {
		t.Fatal(err)
	}
	got := Read(path, time.Unix(10, 0))
	if got.Run != "idle" || got.RunningVersion != "" || got.LatestVersion != "1.10.0" || len(got.FailedVersions) != 1 ||
		got.LastFailure.To != "" || got.LastFailure.Step != "" || got.History[0].Result != "" {
		t.Errorf("%+v %+v %+v", got, got.LastFailure, got.History)
	}
}
