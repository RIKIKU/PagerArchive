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
//
// One page in 57 is Station 5's instead, 80,000 in all: the share of
// production's busiest alias (58,000 of 3.1 million; owner, 2026-11-08). Its
// pages take the six phrasings in turn too, so a third of them hold "FIRE".
// One page in 10 is one of 2,000 units' (Unit 0001 to Unit 2000), about 230
// each, in its station's group: with 2,005 aliases the planner expects an alias
// to hold few pages, as on an archive with many (ADR 0017).
//
// One page in 9, all Emergency, has a map reference at the end of its
// description (section 10): a page of 600 and a square of 16 columns by 30
// rows, at random. A reference typed without quotes is three terms of one or
// two characters (ADR 0017).
const a75Seed = `
INSERT INTO messages (capcode, text, priority, sent_at, dedup_key, paging_group, alias_id, extra)
SELECT a.pattern_key,
       CASE WHEN g % 4228 = 2000
            THEN '@@EX0000076 HSF1 LIVE ' || g || ' SMOKE @ 12 FICTION ST, MADEUPTON #STN' || s.n
            ELSE (ARRAY['@@','Hb','QD'])[1 + g % 3]
                   || (ARRAY['SMOKE FROM ROOF','CHECK HOSES TODAY','GRASS FIRE','STRUCTURE FIRE, SMOKE SHOWING','ROUTINE PUMP CHECK','MEET AT THE STATION'])[1 + s.phrasing]
                   || ' ' || g
                   || CASE WHEN g % 9 = 0 THEN ' M ' || (1 + r.h % 600) || ' ' || chr(65 + ((r.h / 600) % 16)::int) || (1 + (r.h / 9600) % 30) ELSE '' END
                   || ' @ ' || (g % 900) || ' FICTION ST, MADEUPTON #STN' || s.n
       END,
       (ARRAY['Emergency','Non-emergency','Admin'])[1 + g % 3],
       CASE WHEN g % 4228 = 2000 THEN now() - (g / 4228) * interval '5 seconds' ELSE now() - g * interval '69.12 seconds' END,
       sha256(g::text::bytea), 'STN' || s.n, a.id,
       jsonb_build_object('unit', 'PUMPER ' || (g % 20),
                          'crew', CASE WHEN g % 10000 = 7 THEN 'TANKER9' ELSE 'CREW ' || (g % 7) END)
FROM generate_series(1, $1::int) g
CROSS JOIN LATERAL (SELECT CASE WHEN g % 57 = 0 AND g <= 57 * 80000 THEN 5 ELSE 1 + g % 4 END AS n,
                           CASE WHEN g % 57 = 0 AND g <= 57 * 80000 THEN (g / 57) % 6 ELSE (g * 7) % 6 END AS phrasing) s
CROSS JOIN LATERAL (SELECT (g::bigint * 2654435761) % 4294967296 AS h) r
JOIN aliases a ON a.pattern_key = CASE WHEN s.n = 5 THEN '0100005'
                                     WHEN g % 10 = 5 THEN '02' || lpad((1 + (g / 10) % 2000)::text, 5, '0')
                                     ELSE '010000' || s.n END`

// Station 5, the busy alias, and the 2,000 units, with Station 1's template.
const a75Alias = `
INSERT INTO aliases (pattern, name, template_id)
SELECT p.pattern, p.name, a.template_id
FROM aliases a,
     (SELECT '0100005' AS pattern, 'Station 5' AS name
      UNION ALL
      SELECT '02' || lpad(u::text, 5, '0'), 'Unit ' || lpad(u::text, 4, '0') FROM generate_series(1, 2000) u) p
WHERE a.pattern_key = '0100001'
ON CONFLICT DO NOTHING`

// The search indexes, which Seed builds after the pages (those the migrations
// made).
var searchIndexes = []string{"messages_search_trgm_idx", "messages_search_bigrams_idx"}

// Seed stores pages made-up pages, for aliases 0100001 to 0100005 and the
// units (adding those after 0100004 if they aren't there), with one INSERT
// rather than through the API. The search indexes, as the migrations made
// them, are built once afterwards: much faster than adding the pages to them
// one by one.
func Seed(ctx context.Context, pool *pgxpool.Pool, pages int) (seeded, built time.Duration, err error) {
	var defs []string
	for _, index := range searchIndexes {
		var def *string
		if err := pool.QueryRow(ctx, `SELECT pg_get_indexdef(to_regclass($1))`, index).Scan(&def); err != nil {
			return 0, 0, err
		}
		if def != nil {
			if _, err := pool.Exec(ctx, `DROP INDEX `+index); err != nil {
				return 0, 0, err
			}
			defs = append(defs, *def)
		}
	}
	start := time.Now()
	if _, err := pool.Exec(ctx, a75Alias); err != nil {
		return 0, 0, err
	}
	if _, err := pool.Exec(ctx, a75Seed, pages); err != nil {
		return 0, 0, err
	}
	seeded = time.Since(start)
	for _, def := range defs {
		if _, err := pool.Exec(ctx, def); err != nil {
			return 0, 0, err
		}
	}
	built = time.Since(start) - seeded
	if _, err := pool.Exec(ctx, `VACUUM ANALYZE messages`); err != nil {
		return 0, 0, err
	}
	return seeded, built, nil
}
