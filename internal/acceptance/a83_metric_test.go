package acceptance

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/RIKIKU/PagerArchive/internal/server"
)

// pendingSample is the extension's pagerarchive_db_extension_update_pending
// value in a scrape, or "" if it has none.
func pendingSample(body, ext string) string {
	return sampleValue(body, `pagerarchive_db_extension_update_pending{extension="`+ext+`"}`)
}

// A83's alert (R84c, ADR 0033): a switched API can't update PostGIS, so
// /metrics says when an update is pending, for the example stack to alert
// on. Given the migrations' postgis and pg_trgm, when the extensions are
// checked in the background, as serve does, then the scrape has 0 for each;
// when postgis is registered at the release before the image's, as after a
// newer postgis/postgis image with the API still running, then within the
// interval it has 1 for postgis and 0 for pg_trgm; when ALTER EXTENSION
// postgis UPDATE runs, as the operator does, then within the interval it's 0
// again.
func TestA83UpdatePendingMetric(t *testing.T) {
	e := setup(t)
	exec := func(sql string, args ...any) {
		t.Helper()
		if _, err := e.st.Pool.Exec(e.ctx, sql, args...); err != nil {
			t.Fatalf("%s: %v", sql, err)
		}
	}
	t.Cleanup(func() { _, _ = e.st.Pool.Exec(context.Background(), `ALTER EXTENSION postgis UPDATE`) })

	body, _ := e.scrape()
	if pendingSample(body, "postgis") != "" {
		t.Errorf("before the first check, the scrape has the pending updates")
	}

	e.srv.Extensions = &server.ExtensionChecks{Store: e.st, Log: e.srv.Log, Every: 200 * time.Millisecond}
	ctx, cancel := context.WithCancel(e.ctx)
	done := make(chan struct{})
	go func() { e.srv.Extensions.Run(ctx); close(done) }()
	t.Cleanup(func() { cancel(); <-done })

	want := func(postgis, trgm string) bool {
		return eventually(2*time.Second, func() bool {
			b, _ := e.scrape()
			return pendingSample(b, "postgis") == postgis && pendingSample(b, "pg_trgm") == trgm
		})
	}
	if !want("0", "0") {
		b, _ := e.scrape()
		t.Fatalf("current extensions: postgis %q, pg_trgm %q, want 0 and 0", pendingSample(b, "postgis"), pendingSample(b, "pg_trgm"))
	}
	body, _ = e.scrape()
	if !strings.Contains(body, "# TYPE pagerarchive_db_extension_update_pending gauge") {
		t.Errorf("no gauge type for the pending updates")
	}
	t.Log("A83: current: postgis 0, pg_trgm 0")

	var image, older string
	if err := e.st.Pool.QueryRow(e.ctx, `SELECT default_version FROM pg_available_extensions WHERE name = 'postgis'`).Scan(&image); err != nil {
		t.Fatal(err)
	}
	if err := e.st.Pool.QueryRow(e.ctx, `
SELECT source FROM pg_extension_update_paths('postgis')
WHERE target = $1 AND source <> target AND path IS NOT NULL AND source ~ '^\d+\.\d+\.\d+$'
ORDER BY string_to_array(source, '.')::int[] DESC LIMIT 1`, image).Scan(&older); err != nil {
		t.Fatalf("no older postgis to upgrade from: %v", err)
	}
	exec(`UPDATE pg_extension SET extversion = $1 WHERE extname = 'postgis'`, older)
	if !want("1", "0") {
		b, _ := e.scrape()
		t.Fatalf("postgis at %s under the image's %s: postgis %q, pg_trgm %q, want 1 and 0", older, image, pendingSample(b, "postgis"), pendingSample(b, "pg_trgm"))
	}
	t.Logf("A83: postgis registered at %s, the image's %s: postgis 1, pg_trgm 0", older, image)

	exec(`ALTER EXTENSION postgis UPDATE`)
	if !want("0", "0") {
		b, _ := e.scrape()
		t.Fatalf("after ALTER EXTENSION postgis UPDATE: postgis %q, want 0", pendingSample(b, "postgis"))
	}
	t.Log("A83: after ALTER EXTENSION postgis UPDATE: postgis 0")
}
