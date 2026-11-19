package monitortest

import (
	"context"
	"net"
	"net/http"
	"os"
	"testing"
	"time"
)

// extensionAlert is when the first "Database extension update pending"
// alert since since, with this status, came for ext.
func (w *webhook) extensionAlert(since time.Time, status, ext string) (time.Time, bool) {
	w.mu.Lock()
	defer w.mu.Unlock()
	for _, n := range w.got {
		if n.at.Before(since) {
			continue
		}
		for _, a := range n.Alerts {
			if a.Labels["alertname"] == "Database extension update pending" && a.Status == status && a.Labels["extension"] == ext {
				return n.at, true
			}
		}
	}
	return time.Time{}, false
}

// A83's alert (R84c, ADR 0033), as A80 does for a backup: with the example
// stack scraping a switched API (its own role, not the superuser), current
// extensions give pagerarchive_db_extension_update_pending 0 and no alert.
// When postgis is registered at the release before the image's, as after a
// newer postgis/postgis image, and the API starts, it can't update it, and
// "Database extension update pending" fires for postgis within 2 minutes,
// and the Failures dashboard says UPDATE PENDING. When the operator runs
// ALTER EXTENSION postgis UPDATE as the superuser and the API restarts, it
// resolves.
func TestA83ToldWhenAnExtensionUpdateIsPending(t *testing.T) {
	s := newStack(t)
	hook := &webhook{}
	l, err := net.Listen("tcp", os.Getenv("PA_MON_WEBHOOK_ADDR"))
	if err != nil {
		t.Fatal(err)
	}
	go http.Serve(l, hook)
	defer l.Close()
	ctx := context.Background()
	exec := func(sql string, args ...any) {
		t.Helper()
		if _, err := s.st.Pool.Exec(ctx, sql, args...); err != nil {
			t.Fatalf("%s: %v", sql, err)
		}
	}
	t.Cleanup(func() { _, _ = s.st.Pool.Exec(context.Background(), `ALTER EXTENSION postgis UPDATE`) })
	restart := func() {
		t.Helper()
		s.sh(t, s.stopAPI)
		s.sh(t, s.startAPI)
	}
	square := func() (float64, bool) {
		for _, v := range s.panelValues(t, "pa-failures", "Database extensions")["A"] {
			return v, true
		}
		return 0, false
	}

	since := time.Now()
	if !waitFor(90*time.Second, func() bool {
		return s.query(t, `pagerarchive_db_extension_update_pending{job="pagerarchive", extension="postgis"} == 0`) == 1 &&
			s.query(t, `pagerarchive_db_extension_update_pending{job="pagerarchive", extension="pg_trgm"} == 0`) == 1
	}) {
		t.Fatal("Prometheus never had postgis and pg_trgm current: is Alloy scraping the API?")
	}
	if v, ok := square(); !ok || v != 0 {
		t.Errorf("failures dashboard: database extensions %v (shown %t), want 0, OK", v, ok)
	}
	t.Log("A83: current extensions scraped as 0; the Failures square says OK")

	var image, older string
	if err := s.st.Pool.QueryRow(ctx, `SELECT default_version FROM pg_available_extensions WHERE name = 'postgis'`).Scan(&image); err != nil {
		t.Fatal(err)
	}
	if err := s.st.Pool.QueryRow(ctx, `
SELECT source FROM pg_extension_update_paths('postgis')
WHERE target = $1 AND source <> target AND path IS NOT NULL AND source ~ '^\d+\.\d+\.\d+$'
ORDER BY string_to_array(source, '.')::int[] DESC LIMIT 1`, image).Scan(&older); err != nil {
		t.Fatalf("no older postgis to upgrade from: %v", err)
	}
	exec(`UPDATE pg_extension SET extversion = $1 WHERE extname = 'postgis'`, older)
	restart()
	if !waitFor(2*time.Minute, func() bool { _, ok := hook.extensionAlert(since, "firing", "postgis"); return ok }) {
		t.Fatalf("\"Database extension update pending\" didn't fire for postgis within 2 minutes of the API starting on %s under the image's %s", older, image)
	}
	at, _ := hook.extensionAlert(since, "firing", "postgis")
	t.Logf("A83: postgis registered at %s, the image's %s: the alert fired %s after the restart", older, image, at.Sub(since).Round(time.Second))
	if _, ok := hook.extensionAlert(since, "firing", "pg_trgm"); ok {
		t.Error("it fired for pg_trgm too, which is current")
	}
	if v, ok := square(); !ok || v != 1 {
		t.Errorf("failures dashboard: database extensions %v (shown %t) while postgis is behind, want 1, UPDATE PENDING", v, ok)
	} else {
		t.Log("A83: the Failures square says UPDATE PENDING")
	}

	// The operator's hand step, as the alert says: as the superuser.
	since = time.Now()
	exec(`ALTER EXTENSION postgis UPDATE`)
	restart()
	if !waitFor(3*time.Minute, func() bool { _, ok := hook.extensionAlert(since, "resolved", "postgis"); return ok }) {
		t.Fatal("\"Database extension update pending\" didn't resolve after ALTER EXTENSION postgis UPDATE and a restart")
	}
	t.Log("A83: it resolved after ALTER EXTENSION postgis UPDATE")
}
