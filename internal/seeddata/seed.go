// Package seeddata fills a test database with A75's made-up pages, for the
// checks that need 10 years of them (TestA75SearchSpeed, scripts/a76.sh).
// Never for a deployment.
package seeddata

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// A75Pages is 10 years at 1,250 pages a day.
const A75Pages = 4_562_500

// The made-up pages for A75: 10 years at 1,250 a day, newest first, from
// Stations 1 to 4 in turn. Every page has extra fields; "TANKER9" is in one
// page in 10,000, only in an extra field (Station 4's).
//
// One page in every 4,228 is among the newest of all, 5 seconds apart: 1,079
// pages spread through the table, as the live pages an hour and a half's import
// stores among its own (ADR 0017). A search that reads the newest pages in order
// reads each from its own part of the disk, as a first run after a cold start
// does on an imported archive.
const a75Seed = `
INSERT INTO messages (capcode, text, priority, sent_at, dedup_key, paging_group, alias_id, extra)
SELECT a.pattern_key,
       CASE WHEN g % 4228 = 2000
            THEN '@@EX0000076 HSF1 LIVE ' || g || ' SMOKE @ 12 FICTION ST, MADEUPTON #STN' || (1 + g % 4)
            ELSE (ARRAY['@@','Hb','QD'])[1 + g % 3]
                   || (ARRAY['SMOKE FROM ROOF','CHECK HOSES TODAY','GRASS FIRE','STRUCTURE FIRE, SMOKE SHOWING','ROUTINE PUMP CHECK','MEET AT THE STATION'])[1 + (g * 7) % 6]
                   || ' ' || g || ' @ ' || (g % 900) || ' FICTION ST, MADEUPTON #STN' || (1 + g % 4)
       END,
       (ARRAY['Emergency','Non-emergency','Admin'])[1 + g % 3],
       CASE WHEN g % 4228 = 2000 THEN now() - (g / 4228) * interval '5 seconds' ELSE now() - g * interval '69.12 seconds' END,
       sha256(g::text::bytea), 'STN' || (1 + g % 4), a.id,
       jsonb_build_object('unit', 'PUMPER ' || (g % 20),
                          'crew', CASE WHEN g % 10000 = 7 THEN 'TANKER9' ELSE 'CREW ' || (g % 7) END)
FROM generate_series(1, $1::int) g
JOIN aliases a ON a.pattern_key = '010000' || (1 + g % 4)`

// Seed stores pages made-up pages, for aliases 0100001 to 0100004, with one
// INSERT rather than through the API. The search index, as the migrations made
// it, is built once afterwards: much faster than adding the pages to it one by
// one.
func Seed(ctx context.Context, pool *pgxpool.Pool, pages int) (seeded, built time.Duration, err error) {
	var def *string
	if err := pool.QueryRow(ctx, `SELECT pg_get_indexdef(to_regclass('messages_search_trgm_idx'))`).Scan(&def); err != nil {
		return 0, 0, err
	}
	if def != nil {
		if _, err := pool.Exec(ctx, `DROP INDEX messages_search_trgm_idx`); err != nil {
			return 0, 0, err
		}
	}
	start := time.Now()
	if _, err := pool.Exec(ctx, a75Seed, pages); err != nil {
		return 0, 0, err
	}
	seeded = time.Since(start)
	if def != nil {
		if _, err := pool.Exec(ctx, *def); err != nil {
			return 0, 0, err
		}
	}
	built = time.Since(start) - seeded
	if _, err := pool.Exec(ctx, `VACUUM ANALYZE messages`); err != nil {
		return 0, 0, err
	}
	return seeded, built, nil
}
