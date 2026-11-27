package monitortest

import (
	"bytes"
	"context"
	"fmt"
	"net"
	"net/http"
	"os"
	"testing"
	"time"
)

// A155 (R14b, R14c, R84p; ADR 0053): with the example stack scraping
// PagerArchive, a client host's automatic upgrades' report (a run in its
// health check, for a target release) and the client's version reach
// Prometheus through the API's metrics, and the Clients dashboard shows them,
// with the server's own version; a client that's been behind the server's
// release for more than a day fires "Client behind the server" for that
// client, through the same contact point as every other alert, and it
// resolves when the client is no longer behind. Since when it's been behind
// is moved back in the database, as A65 moves a last-seen time; the API's
// own working out of it is A154, and the client host's script A153.
func TestA155ClientVersionsInGrafana(t *testing.T) {
	s := newStack(t)
	hook := &webhook{}
	l, err := net.Listen("tcp", os.Getenv("PA_MON_WEBHOOK_ADDR"))
	if err != nil {
		t.Fatal(err)
	}
	go http.Serve(l, hook)
	defer l.Close()
	ctx := context.Background()

	run := time.Now().Format("150405")
	behind, current := "mon-behind-"+run, "mon-current-"+run
	keys := map[string]string{}
	for _, id := range []string{behind, current} {
		key, err := s.st.AddClient(ctx, id, "A155 "+id)
		if err != nil {
			t.Fatal(err)
		}
		keys[id] = key
	}
	// One behind for 25 hours, at 0.0.1; the other on a version and behind
	// for only an hour.
	if _, err := s.st.Pool.Exec(ctx, `UPDATE clients SET version = '0.0.1', behind_since = now() - interval '25 hours' WHERE id = $1`, behind); err != nil {
		t.Fatal(err)
	}
	if _, err := s.st.Pool.Exec(ctx, `UPDATE clients SET version = '0.0.2', behind_since = now() - interval '1 hour' WHERE id = $1`, current); err != nil {
		t.Fatal(err)
	}
	// Its host's report: a run in its health check, upgrading to 0.0.3. No
	// client_version, so the API leaves the version set above as it is.
	started := time.Now().Add(-3 * time.Minute).Unix()
	body := fmt.Sprintf(`{"status":{"enabled":true,"state":"health-check","running_since":%d,"target_version":"0.0.3","last_check_at":%d,
		"last_check_result":"in-progress","failures":0,"rollbacks":0,"failed_versions":[]}}`, started, started)
	req, _ := http.NewRequest(http.MethodPost, s.api+"/api/v1/client/auto-upgrade", bytes.NewReader([]byte(body)))
	req.Header.Set("Authorization", "Bearer "+keys[behind])
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("the report: %d", resp.StatusCode)
	}
	since := time.Now()
	if !waitFor(90*time.Second, func() bool {
		return s.query(t, fmt.Sprintf(`pagerarchive_client_auto_upgrade_run{client="%s", step="health-check"} == 1`, behind)) == 1 &&
			s.query(t, fmt.Sprintf(`pagerarchive_client_behind_since_timestamp_seconds{client=~"%s|%s"}`, behind, current)) == 2
	}) {
		t.Fatal("Prometheus never had the client's version and its host's report")
	}

	// The dashboard.
	series := s.panelSeries(t, "pa-clients", "Client versions and automatic upgrades")
	byClient := func(ref, id string) (panelSeries, bool) {
		for _, ps := range series[ref] {
			if ps.labels["client"] == id {
				return ps, true
			}
		}
		return panelSeries{}, false
	}
	if v, ok := byClient("A", behind); !ok || v.labels["version"] != "0.0.1" {
		t.Errorf("dashboard: %s's version %+v (shown %t), want 0.0.1", behind, v, ok)
	}
	if v, ok := byClient("B", behind); !ok || v.value < 25*3600 || v.value > 25*3600+600 {
		t.Errorf("dashboard: %s behind for %v s (shown %t), want 25 hours", behind, v.value, ok)
	}
	if v, ok := byClient("C", behind); !ok || v.value != 1 {
		t.Errorf("dashboard: %s's automatic upgrades %v (shown %t), want 1, on", behind, v.value, ok)
	}
	if v, ok := byClient("D", behind); !ok || v.value < 3*60 || v.value > 10*60 {
		t.Errorf("dashboard: since %s's last check %v s (shown %t), want about 3 minutes", behind, v.value, ok)
	}
	if v, ok := byClient("E", behind); !ok || v.labels["result"] != "in-progress" {
		t.Errorf("dashboard: %s's last check %+v (shown %t), want in-progress", behind, v, ok)
	}
	if v, ok := byClient("F", behind); !ok || v.labels["step"] != "health-check" {
		t.Errorf("dashboard: %s running now %+v (shown %t), want health-check", behind, v, ok)
	}
	if v, ok := byClient("G", behind); !ok || v.value != 0 {
		t.Errorf("dashboard: %s's last upgrade %v (shown %t), want 0, OK", behind, v.value, ok)
	}
	if _, ok := byClient("C", current); ok {
		t.Errorf("dashboard: %s has automatic upgrades, with no report from its host", current)
	}
	server := s.panelSeries(t, "pa-clients", "Server version")
	if len(server["A"]) != 1 || server["A"][0].labels["version"] == "" {
		t.Errorf("dashboard: the server's version %+v, want one series with its version", server)
	}
	t.Logf("A155: the dashboard shows %s at 0.0.1, behind for 25 hours, its host's run in its health check; the server runs %s", behind, server["A"][0].labels["version"])

	// The alert: for the client behind for more than a day only.
	firedAt, fired := time.Time{}, false
	if !waitFor(2*time.Minute, func() bool {
		firedAt, fired = hook.find(since, "Client behind the server", "firing", behind)
		return fired
	}) {
		t.Fatalf(`no "Client behind the server" for %s within 2 minutes; got %d notifications`, behind, len(hook.got))
	}
	t.Logf(`A155: "Client behind the server" fired for %s %v after its version reached Prometheus`, behind, firedAt.Sub(since).Round(time.Second))
	time.Sleep(35 * time.Second)
	if _, wrong := hook.find(since, "Client behind the server", "firing", current); wrong {
		t.Errorf(`"Client behind the server" fired for %s, behind for only an hour`, current)
	}

	// No longer behind: it resolves.
	cleared := time.Now()
	if _, err := s.st.Pool.Exec(ctx, `UPDATE clients SET behind_since = NULL WHERE id = $1`, behind); err != nil {
		t.Fatal(err)
	}
	if !waitFor(3*time.Minute, func() bool { _, ok := hook.find(cleared, "Client behind the server", "resolved", behind); return ok }) {
		t.Fatalf(`"Client behind the server" didn't resolve for %s within 3 minutes of it catching up`, behind)
	}
	t.Logf(`A155: "Client behind the server" resolved for %s once it caught up`, behind)
	for _, id := range []string{behind, current} {
		_, _ = s.st.Pool.Exec(ctx, `UPDATE clients SET revoked_at = now(), behind_since = NULL WHERE id = $1`, id)
	}
}
