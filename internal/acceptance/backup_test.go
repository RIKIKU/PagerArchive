package acceptance

import (
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/RIKIKU/PagerArchive/internal/backup"
	"github.com/RIKIKU/PagerArchive/internal/server"
)

// R86a, Back up now: admins only; the API writes the request and says queued,
// and while one is queued or running another is refused, naming since when.
// The backup service's side is scripts/backup-checks.sh, and both together A80.
func TestBackUpNow(t *testing.T) {
	e := setup(t)
	dir := t.TempDir()
	status, requests := filepath.Join(dir, "status", "status.json"), filepath.Join(dir, "requests")
	request := filepath.Join(requests, "request.json")
	writeStatus := func(running int64) {
		t.Helper()
		data := fmt.Sprintf(`{"since":%d,"last_run_at":%d,"last_run_ok":true,"running_since":%d,"last_good_at":%d,"nights":2,"error":""}`,
			time.Now().Add(-48*time.Hour).Unix(), time.Now().Add(-5*time.Hour).Unix(), running, time.Now().Add(-5*time.Hour).Unix())
		if err := os.WriteFile(status, []byte(data), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	for _, d := range []string{filepath.Dir(status), requests} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	writeStatus(0)

	// A server with no backup service, or a compose.yaml from before this.
	var st backup.Status
	if code, p := e.admin.call(http.MethodPost, "/api/v1/admin/backup", nil, nil); code != http.StatusConflict || p.Code != server.CodeBackupOff {
		t.Errorf("no backup service: %d %+v", code, p)
	}
	e.srv.BackupStatusFile = status
	if code, _ := e.admin.call(http.MethodGet, "/api/v1/admin/backup", nil, &st); code != http.StatusOK || st.Run != backup.RunOff {
		t.Errorf("no request file: %d, run %q", code, st.Run)
	}
	if code, p := e.admin.call(http.MethodPost, "/api/v1/admin/backup", nil, nil); code != http.StatusConflict || p.Code != server.CodeBackupOff {
		t.Errorf("no request file: %d %+v", code, p)
	}
	e.srv.BackupRequestFile = request

	// Admins only, for the status and the request.
	if code, _ := e.user.call(http.MethodPost, "/api/v1/admin/backup", nil, nil); code != http.StatusForbidden {
		t.Errorf("a user asked for a backup: %d", code)
	}
	if code, _ := e.user.call(http.MethodGet, "/api/v1/admin/backup", nil, nil); code != http.StatusForbidden {
		t.Errorf("a user saw the backups: %d", code)
	}
	if _, err := os.Stat(request); err == nil {
		t.Fatal("a user's refused request left a file")
	}

	if code, _ := e.admin.call(http.MethodGet, "/api/v1/admin/backup", nil, &st); code != http.StatusOK || st.Run != backup.RunIdle {
		t.Fatalf("before: %d, run %q", code, st.Run)
	}
	if code, p := e.admin.call(http.MethodPost, "/api/v1/admin/backup", nil, &st); code != http.StatusAccepted || st.Run != backup.RunQueued || st.RequestedAt == nil {
		t.Fatalf("asked: %d %+v, %+v", code, p, st)
	}
	if _, err := os.Stat(request); err != nil {
		t.Fatalf("no request file: %v", err)
	}
	if !strings.Contains(e.logs.String(), `"msg":"backup asked for"`) {
		t.Error("the log doesn't say who asked")
	}
	if code, _ := e.admin.call(http.MethodGet, "/api/v1/admin/backup", nil, &st); code != http.StatusOK || st.Run != backup.RunQueued || st.State != backup.StateOK {
		t.Errorf("after: %d, run %q, state %q", code, st.Run, st.State)
	}

	// One at a time: queued, then running (the service has started it).
	code, p := e.admin.call(http.MethodPost, "/api/v1/admin/backup", nil, nil)
	if code != http.StatusConflict || p.Code != server.CodeBackupQueued || !strings.Contains(p.Error, "starts within a minute") {
		t.Errorf("asked again while queued: %d %+v", code, p)
	}
	started := time.Date(2026, 11, 14, 10, 7, 0, 0, time.UTC)
	writeStatus(started.Unix())
	code, p = e.admin.call(http.MethodPost, "/api/v1/admin/backup", nil, nil)
	if code != http.StatusConflict || p.Code != server.CodeBackupRunning || !strings.Contains(p.Error, "since 10:07") {
		t.Errorf("asked again while running: %d %+v", code, p)
	}
	if code, _ := e.admin.call(http.MethodGet, "/api/v1/admin/backup", nil, &st); code != http.StatusOK || st.Run != backup.RunRunning || !st.RunningSince.Equal(started) {
		t.Errorf("running: %d, run %q, since %v", code, st.Run, st.RunningSince)
	}

	// Done: the service deleted the request and wrote the result.
	if err := os.Remove(request); err != nil {
		t.Fatal(err)
	}
	writeStatus(0)
	if code, _ := e.admin.call(http.MethodPost, "/api/v1/admin/backup", nil, &st); code != http.StatusAccepted || st.Run != backup.RunQueued {
		t.Errorf("asked again when done: %d, run %q", code, st.Run)
	}

	// A service that hasn't started since it was installed: no folder to write in.
	if err := os.RemoveAll(requests); err != nil {
		t.Fatal(err)
	}
	if code, p := e.admin.call(http.MethodPost, "/api/v1/admin/backup", nil, nil); code != http.StatusServiceUnavailable || p.Code != server.CodeBackupNoService {
		t.Errorf("no backup service running: %d %+v", code, p)
	}
}
