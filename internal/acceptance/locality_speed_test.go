package acceptance

import (
	"fmt"
	"log/slog"
	"os"
	"testing"
	"time"

	"github.com/RIKIKU/PagerArchive/internal/geocode"
	"github.com/RIKIKU/PagerArchive/internal/reports"
	"github.com/RIKIKU/PagerArchive/internal/streets"
)

// How long the derived localities' backfill (ADR 0033), with the report
// counts built again after it, takes on an archive the size of a real one:
// 3,100,000 made-up pages, about 6 to a job (516,667 jobs), none with a
// locality field, against a made-up street index of 3,000 localities with 50
// streets each. Run with PAGERARCHIVE_LOCALITY_SPEED=1; it takes a while.
func TestLocalityBackfillSpeed(t *testing.T) {
	if os.Getenv("PAGERARCHIVE_LOCALITY_SPEED") != "1" {
		t.Skip("PAGERARCHIVE_LOCALITY_SPEED is not set")
	}
	e := setup(t)
	const pages, localities, perLocality = 3_100_000, 3000, 50
	start := time.Now()
	// One page in ten names a locality the dataset doesn't have.
	if _, err := e.st.Pool.Exec(e.ctx, `INSERT INTO messages (capcode, text, sent_at, dedup_key, priority, job_number, address, description)
		SELECT '0500001', 'made-up page ' || g, timestamptz '2016-01-01 00:00:00+00' + g * interval '100 seconds', sha256(int8send(g::bigint)),
		       'Emergency', 'EX' || lpad((g / 6)::text, 7, '0'),
		       (g % 90 + 1) || ' STREET' || (g % $2) || ' RD ' || CASE WHEN g % 10 = 0 THEN 'NOWHERE' ELSE 'LOCALITY' || (g % $3) END,
		       'MADE UP JOB'
		FROM generate_series(1, $1::int) g`, pages, perLocality, localities); err != nil {
		t.Fatal(err)
	}
	if _, err := e.st.Pool.Exec(e.ctx, `ANALYZE messages`); err != nil {
		t.Fatal(err)
	}
	t.Logf("%d pages seeded in %s", pages, time.Since(start).Round(time.Second))

	rep := &reports.Service{Pool: e.st.Pool, Location: time.UTC}
	start = time.Now()
	if err := rep.Rebuild(e.ctx); err != nil {
		t.Fatal(err)
	}
	t.Logf("report counts built (as before the upgrade) in %s", time.Since(start).Round(time.Second))
	// The seeding's queue: counted long ago on a real archive.
	if _, err := e.st.Pool.Exec(e.ctx, `DELETE FROM report_dirty`); err != nil {
		t.Fatal(err)
	}

	var entries []streets.Entry
	for l := range localities {
		for s := range perLocality {
			entries = append(entries, streets.Entry{Street: fmt.Sprintf("STREET%d RD", s), Locality: fmt.Sprintf("LOCALITY%d", l)})
		}
	}
	g := &geocode.Geocoder{Index: streets.NewIndex(entries, map[string]string{"ROAD": "RD"})}
	start = time.Now()
	if err := g.BackfillLocalities(e.ctx, e.st, slog.New(slog.NewTextHandler(os.Stderr, nil)), 30*time.Second, rep.Rebuild); err != nil {
		t.Fatal(err)
	}
	backfill := time.Since(start)
	var derived, queued int64
	if err := e.st.Pool.QueryRow(e.ctx, `SELECT (SELECT count(*) FROM message_localities), (SELECT count(*) FROM report_dirty)`).Scan(&derived, &queued); err != nil {
		t.Fatal(err)
	}
	t.Logf("backfill: %d localities for %d pages in %s (half of it waiting between batches) and the counts built again, %d jobs queued", derived, pages, backfill.Round(time.Second), queued)
	if derived != pages*9/10 || queued != 0 {
		t.Errorf("%d derived localities and %d jobs queued, want %d and 0", derived, queued, pages*9/10)
	}
	var none, jobs int64
	if err := e.st.Pool.QueryRow(e.ctx, `SELECT count(*) FILTER (WHERE locality = ''), count(*) FROM report_jobs`).Scan(&none, &jobs); err != nil {
		t.Fatal(err)
	}
	t.Logf("%d jobs, %d with no locality", jobs, none)
}
