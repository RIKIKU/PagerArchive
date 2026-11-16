package acceptance

import (
	"log/slog"
	"os"
	"testing"
	"time"

	"github.com/RIKIKU/PagerArchive/internal/areas"
	"github.com/RIKIKU/PagerArchive/internal/profile"
)

// How long working out stored pages' areas (ADR 0035) takes on an archive
// the size of a real one: 3,100,000 made-up pages, nine in ten placed at
// random over the areas' extent, against a real-size areas file
// (PAGERARCHIVE_AREAS_FILE, its names in PAGERARCHIVE_AREAS_PROPERTY; not in
// the repository). Run with PAGERARCHIVE_AREAS_SPEED=1; it takes a while.
func TestAreasBackfillSpeed(t *testing.T) {
	if os.Getenv("PAGERARCHIVE_AREAS_SPEED") != "1" {
		t.Skip("PAGERARCHIVE_AREAS_SPEED is not set")
	}
	e := setup(t)
	f, err := areas.ReadFile(os.Getenv("PAGERARCHIVE_AREAS_FILE"), os.Getenv("PAGERARCHIVE_AREAS_PROPERTY"))
	if err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	stats, err := areas.Load(e.ctx, e.st, f)
	if err != nil {
		t.Fatal(err)
	}
	mapped, err := areas.Prepare(e.ctx, e.st, &profile.Areas{Path: f.Path, Label: "Area"}, f)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("%d areas loaded in %s: %d pieces, outlines simplified at %g degrees: %d bytes, %d gzipped", stats.Areas, time.Since(start).Round(time.Millisecond), stats.Parts, stats.Tolerance, len(mapped.Map), len(mapped.MapGzip))

	const pages = 3_100_000
	start = time.Now()
	if _, err := e.st.Pool.Exec(e.ctx, `INSERT INTO messages (capcode, text, sent_at, dedup_key, priority, job_number, location, location_source)
		SELECT '0500001', 'made-up page ' || g, timestamptz '2016-01-01 00:00:00+00' + g * interval '100 seconds', sha256(int8send(g::bigint)),
		       'Emergency', 'EX' || lpad((g / 6)::text, 7, '0'),
		       CASE WHEN g % 10 <> 0 THEN ST_SetSRID(ST_MakePoint(x.min_x + random() * (x.max_x - x.min_x), x.min_y + random() * (x.max_y - x.min_y)), 4326)::geography END,
		       CASE WHEN g % 10 <> 0 THEN 'address' END
		FROM generate_series(1, $1::int) g,
		     (SELECT ST_XMin(e) AS min_x, ST_XMax(e) AS max_x, ST_YMin(e) AS min_y, ST_YMax(e) AS max_y FROM (SELECT ST_Extent(geom) AS e FROM areas) s) x`, pages); err != nil {
		t.Fatal(err)
	}
	if _, err := e.st.Pool.Exec(e.ctx, `ANALYZE messages`); err != nil {
		t.Fatal(err)
	}
	t.Logf("%d pages seeded in %s", pages, time.Since(start).Round(time.Second))

	start = time.Now()
	if err := areas.Backfill(e.ctx, e.st, slog.New(slog.NewTextHandler(os.Stderr, nil)), 30*time.Second); err != nil {
		t.Fatal(err)
	}
	took := time.Since(start)
	var in, located int64
	if err := e.st.Pool.QueryRow(e.ctx, `SELECT (SELECT count(*) FROM message_areas), (SELECT count(*) FROM messages WHERE location IS NOT NULL)`).Scan(&in, &located); err != nil {
		t.Fatal(err)
	}
	t.Logf("backfill: %d of %d located pages in an area, %d pages read, in %s (half of it waiting between batches)", in, located, pages, took.Round(time.Second))
	if in == 0 {
		t.Error("no page is in an area")
	}
}
