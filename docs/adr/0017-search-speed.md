# ADR 0017: Search speed (M13a)

## Status

Proposed (2026-11-02). The owner's brief for M13a: SPEC.md R33b and A75 (owner, 2026-11-02), a fix after M13, on the branch `m13a` from main at abbebe7 (v0.13.0). Resized the same day to 10 years at 1,250 pages a day, 4,562,500 pages, the default retention and the archive being migrated (main e7aa98c, merged into `m13a`). Released as 0.13.1. The PowerShell module, the client, the UI and the search syntax don't change. Revised after the review of e9aad34 (2026-11-02): see "The owner's answers". Decision 5's order of tries was replaced for the first search after a cold start (m16a, 2026-11-03): see that section. A busy alias and short terms were added after production's first install (fix-search, 2026-11-08; 0.16.2): see "A busy alias and short terms". A map reference with a page number was added after production ran 0.16.2 (fix-search-2, 2026-11-08; 0.16.3): see "A map reference with a page number".

## Context

ADR 0015 found that a search whose terms are in few or no pages reads every page: about 5 s on 1,642,500 pages. The trigram index on the text (`messages_text_trgm_idx`, ADR 0001) wasn't used. Each term was matched in the text and in each extra field in one condition (`m.text ILIKE p OR EXISTS (… jsonb_each_text(m.extra) …)`), inside a subquery over the array of terms, and the planner can't use an index for that. On 4,562,500 pages such searches take 2 to 29 s (see "Evidence").

R33b asks for a search's first page within 1 second on 4,562,500 pages. That includes a search that finds little or nothing, several terms or `or:` terms, and a term found only in an extra field. It must stay within the resource budget (section 8) and list the same pages as before.

## Decisions

### 1. One index for the text and the extra fields

Migration 00013 adds `message_search_text(text, extra)`: the text, a unit separator (U+001F), and the extra fields' values as jsonb writes them, an array of JSON values without the keys:

```
HbCHECK HOSES TODAY 1 @ 1 FICTION ST, MADEUPTON #STN2\x1F["CREW 1", "PUMPER 1"]
```

It's a GIN trigram index on that expression, `messages_search_trgm_idx`, which replaces the text's own trigram index (nothing else used it). The expression is a plain one that PostgreSQL inlines, so no function is called per row.

### 2. The index finds candidates; each term is still matched field by field

The expression is a necessary condition, not the match itself. A term found in the text is found in it as written. A term found in an extra field's value is found in it too, unless the term holds a character that jsonb escapes when it writes the value: `"`, `\` or a control character. SearchMessages keeps the old condition, each term in the text or in one extra field, and adds the expression's conditions before it for the index to use. So the results are the same as before for every search, by construction. A term can't match across two fields, or across the text and a field, whatever characters it holds: that's the old per-field condition. The separator only keeps the index's candidates to one field where it can.

### 3. Which terms go to the index

The planner can use an index for `expr ILIKE pattern` and `expr ILIKE ANY(patterns)` but not for `ILIKE ALL(patterns)`. Every text term is required, though, and a search has up to 50. So the query takes up to four of them as separate conditions, `q_index1` to `q_index4`, which GIN combines in one scan. Go picks them (`search.indexTerms`):

- terms the index can find: a run of three letters or digits (a trigram), and none of the characters jsonb escapes;
- the longest such run first, as a longer term is usually rarer;
- at most four: one rare term is enough for the index to find few pages, and every term is matched in full anyway.

The `or:` terms go to the index as `ILIKE ANY` only if it can find every one of them (`q_any_index`). An unset condition is `NULL`, which the planner removes.

### 4. Each query is planned for its own values

pgx prepares each statement. After five runs, PostgreSQL may switch to a generic plan, made without the values. A generic plan can't use the index for a rare term, because it doesn't know the term, nor drop a condition that isn't set. So the API's connections set `plan_cache_mode = force_custom_plan`, and every query is planned with its values. That is how the planner keeps choosing well for a common term. It reads the newest pages in order until it has 50, as before, and uses the index when the terms are rare.

### 5. A second try for terms that are never together

The planner estimates how many pages hold all the terms by multiplying each term's share, as if the terms were independent. For terms that are each common but rarely or never together, that's far too many. For `hoses grass pump` it expected 63,306 pages where there are none. So it reads the newest pages in order, expecting 50 soon, and reads all 4,562,500 (9 s). No statistic PostgreSQL keeps can tell it otherwise. A bounded count from the index first would know, but costs up to 1.4 s for terms that are in every page, where the search itself takes 4 ms.

So `store.SearchMessages` gives a search the index can serve 250 ms (`SearchFirstTry`, as `statement_timeout`). If it isn't done by then, it runs again in a new transaction with ordered index reading off (`enable_indexscan = off`), and the index finds the pages. Searches that are fast now never reach the second try: every common search measured takes under 10 ms. `hoses grass pump` takes 0.38 s, the 250 ms first try and 0.13 s with the index.

Searches also run with `jit = off`. The second try's plan has a high estimated cost, so PostgreSQL compiled it with JIT, which took 459 ms of its 586 ms. A search returns at most 50 rows and never gains from JIT.

The second try also uses the `alias:` values (owner, 2026-11-02; see "The owner's answers"). Before it runs, `AliasIDsNamed` looks up the aliases the values name, and the query adds `m.alias_id = ANY(…)` for them. The names are still matched, so the pages listed are the same. The planner can then combine the alias's pages, from the index on `alias_id`, with the text index's pages. It also gets 32 MB of `work_mem`, for this query only. Each of the two sets is over a million pages for a common term and a common alias, and at the usual 4 MB PostgreSQL keeps such a set by heap page rather than by row. Every page holds rows of every station, so combining by page rules nothing out and it rechecked 3.1 million rows (2 s). By row, the combination is empty at once. The first try doesn't use the alias IDs: given them, the planner chose to combine the two sets for a common term too, and `alias:"Station 1" smoke` went from 5 ms to 3.4 s.

### 6. The migration

`CREATE INDEX CONCURRENTLY`, so reads and writes go on while it builds; goose runs the migration outside a transaction (`-- +goose NO TRANSACTION`). If it fails part-way, the invalid index is dropped when it runs again. It ends with `ANALYZE messages`: the planner knows how common a term is from statistics on the index's expression, and only ANALYZE gathers them, from a sample. The API runs migrations when it starts, so it serves once the index is built: about 2 minutes for 4,562,500 pages on a spinning disk, and a second or two for the live copy's 55,000 (see "Evidence").

### 7. P1

The index is derived from the text and extra fields alone. Retroactive redaction changes the text, and the re-parse that follows changes the extra fields, so the index changes in the same statements, in the same nightly run (`TestNightlyRedactionReachesSearch`). As with every index, PostgreSQL removes the old entries when it vacuums (P1: "followed by routine database vacuuming"). Nothing new is logged. The only new statements traced are `SET LOCAL` and the transaction around a search, which hold no page text or search terms, and A63 still passes.

### 8. A75 in CI

A job of its own, "Search speed (A75)" (`TestA75SearchSpeed`, run when `PAGERARCHIVE_A75=1`). On a fresh database with the migrations applied, it seeds 4,562,500 made-up pages with one `INSERT … SELECT … generate_series`, not the API. It drops the search index while seeding and builds it again afterwards, from the definition the migration gave it (`pg_get_indexdef`): building it once is faster than adding 4.5 million pages to it one at a time. It runs each of A75's six searches (and two variants of the sixth) three times through the API, and fails if any run takes over 1 second. For each, it runs the query from before M13a directly as the reference, and fails if the pages listed differ. Two common searches are run the same way, for the record. The release jobs wait for this job.

The made-up pages: 10 years at 1,250 a day, from Stations 1 to 4 in turn, in six phrasings (the same as ADR 0015's timings), each with two extra fields. `TANKER9` is an extra field's value in one page in 10,000 (457 pages, all Station 4's), never in the text. A75's searches:

| A75 | Search |
|---|---|
| one term that matches nothing | `ZZNOTHING` |
| three terms that together match nothing | `hoses grass pump`, each in a sixth of the pages, never two in one page |
| `"Smoke showing" or:"grass fire"` | as written |
| a term found only in an extra field | `tanker9` |
| a term with `alias:"Station 1"` | `alias:"Station 1" 12345`: 35 pages |
| the sixth (owner, 2026-11-02): a term in many pages but none of the alias's | `alias:"Station 1" STN2`, quoted; also partial, `alias:sta STN2`, and with "Station 1" picked from the suggestions (`alias_like`) |

## What was weighed

- **A stored column of the extra values with its own trigram index.** Adding it rewrites the table (as a generated column, under an exclusive lock), or updates all 4.5 million rows (a trigger, and a backfill that doubles the table until it's vacuumed). A trigger would also have to keep it in step for P1. And with two indexes, each term needs both, as an OR. The planner then builds a bitmap per term per index and combines them, and with `work_mem` at 4 MB the bitmap for a common term goes lossy. One expression index does without all of that.
- **A function that joins the values with `jsonb_each_text` and a separator.** It's what the brief suggested, and exact, but it needs a subquery, so PostgreSQL can't inline it. It cost 9.5 µs a row against 2.4 µs for the inlined expression and 4.6 µs for the old condition (50,000 pages, no index). Searches that read the newest pages in order evaluate it on every page they read, so common searches got slower: `SMOKE` went from 2 to 10 ms on 50,000 pages. It would also have needed a separator no term can hold, and there's none. Page text and search terms can both hold any character (the ingest API accepts any valid UTF-8), so the separator would have been one more exception. With the old per-field condition kept, the separator matters only to how many candidates the index finds.
- **Matching on the joined expression alone.** Faster to check, but the result would depend on jsonb's escaping and the separator, and wouldn't be the same as before for every search. The per-field condition costs little: it runs only on the pages the index found, or on the 50 or so a common search reads.
- **Full-text search (`tsvector`).** It matches whole words, but R33a's terms match within words (section 14: "a term can be part of a longer word").
- **Building the query per search, one condition per term.** sqlc's queries are fixed; four slots cover what the index needs.
- **Probing the index first** for terms the planner may misjudge: see decision 5. The second try costs nothing for a search that's fast.

## Evidence

On cld01 (4 vCPU, 11 GB, the database on a spinning disk), in a throwaway PostGIS 18-3.6 container with the compose file's settings (`shared_buffers=128MB`, `work_mem=4MB`). One database of 4,562,500 made-up pages, seeded as in A75 (481 s, with main's schema and its text index). The settings a first start stores were copied in first, so the API started no re-parse of every page. Main's API (abbebe7 with main's e7aa98c, schema version 12), then this branch's (migration 13 applied in between), each search once and then five times, through the API with a signed-in session.

The first try at seeding left the profile hash unset, so main's first start re-parsed every page in the background (R23a) while it was being timed. That run's numbers were thrown away and everything was seeded and timed again.

### Timings, before and after

Median of five, then the slowest of five, in seconds; "same" compares the pages listed (a hash of the ids, the same in every run):

| Search | main | this branch | slowest | listed | same |
|---|---|---|---|---|---|
| **A75** `ZZNOTHING` (one term, no match) | 20.277 | 0.003 | 0.003 | 0 | yes |
| **A75** `hoses grass pump` (never together) | 21.251 | 0.377 | 0.379 | 0 | yes |
| **A75** `"Smoke showing" or:"grass fire"` | 0.004 | 0.004 | 0.005 | 50 | yes |
| **A75** `tanker9` (an extra field only) | 2.252 | 0.007 | 0.007 | 50 | yes |
| **A75** `alias:"Station 1" 12345` | 20.942 | 0.007 | 0.007 | 35 | yes |
| `SMOKE` (a third of the pages) | 0.003 | 0.004 | 0.004 | 50 | yes |
| `alias:"Station 1" smoke` | 0.005 | 0.005 | 0.006 | 50 | yes |
| `"CHECK HOSES" ZZNOTHING truck` | 21.576 | 0.004 | 0.004 | 0 | yes |
| `"Check hoses" "Smoke" or:"grass fire"` | 0.008 | 0.005 | 0.005 | 50 | yes |
| `a b c d e f g h i j ZZNOTHING` | 28.651 | 0.003 | 0.003 | 0 | yes |
| `or:zznothing or:tanker9` | 2.987 | 0.008 | 0.008 | 50 | yes |
| `alias:"Station 1" STN2` (never together) | 16.657 | 5.834 | 6.329 | 0 | yes |

The first run of each was within these too, apart from a cold first read: the slowest first run of an A75 search was 0.380 s. The last row was the question the owner answered with A75's sixth search: a term in a quarter of the pages, all of them other stations', with Station 1. "The owner's answers" has its timings since.

### Plans

`auto_explain` (`log_analyze`, `log_buffers`) in the throwaway database only, each search once, abridged. A rare term, before (20.3 s) and after (0.16 ms):

```
main: Limit (actual time=20335.296..20335.299 rows=0)
  -> Incremental Sort
     -> Nested Loop Left Join
        -> Index Scan using messages_sent_idx on messages m (rows=0)
             Filter: ((NOT EXISTS(SubPlan 2)) OR EXISTS(SubPlan 4))
             Rows Removed by Filter: 4562500
             -> Function Scan on unnest term (loops=4562500)
                -> Function Scan on jsonb_each_text e (loops=4562500)

branch: Limit (actual time=0.161..0.162 rows=0)
  -> Sort
     -> Nested Loop Left Join
        -> Bitmap Heap Scan on messages m (rows=0)
             Recheck Cond: (message_search_text(text, extra) ~~* '%ZZNOTHING%'::text)
             Filter: (NOT EXISTS(SubPlan 2))
             -> Bitmap Index Scan on messages_search_trgm_idx (actual time=0.146..0.146 rows=0)
```

A common term, before (0.889 ms) and after (0.909 ms): the same plan, the newest pages in order, 156 read for 52 kept.

```
main: Limit (actual time=0.554..0.788 rows=51)
  -> Index Scan using messages_sent_idx on messages m (actual time=0.068..0.611 rows=52)
       Filter: ((NOT EXISTS(SubPlan 2)) OR EXISTS(SubPlan 4))
       Rows Removed by Filter: 104

branch: Limit (actual time=0.589..0.797 rows=51)
  -> Index Scan using messages_sent_idx on messages m (actual time=0.034..0.653 rows=52)
       Filter: ((message_search_text(text, extra) ~~* '%SMOKE%'::text) AND (NOT EXISTS(SubPlan 2)))
       Rows Removed by Filter: 104
```

`hoses grass pump`: the first try, before the second try existed, read every page (`Index Scan using messages_sent_idx … rows=63306` estimated, 9,074 ms, 0 found). The second try (131 ms):

```
Limit (actual time=131.258..131.261 rows=0)
  -> Bitmap Heap Scan on messages m (rows=0)
       Recheck Cond: ((message_search_text(text, extra) ~~* '%hoses%') AND (… '%grass%') AND (… '%pump%'))
       -> Bitmap Index Scan on messages_search_trgm_idx (actual time=131.244..131.244 rows=0)
```

### Index sizes, the migration and locking

On the 4,562,500 pages, from `pg_relation_size`:

| | before | after |
|---|---|---|
| `messages_text_trgm_idx` (the text) | 367 MB | dropped |
| `messages_search_trgm_idx` (the text and extra fields) | | 364 MB |
| `messages`, with every index | 2,362 MB | 2,358 MB |

The new index is no bigger than the old one: the extra fields' trigrams (`CREW`, `PUMPER`) are few and shared by many pages.

The migration on those pages took **132 s**, the API's `client add` from version 12 to 13, on cld01's spinning disk. A probe beside it wrote a page and read the newest 50 twice a second, and noted the locks held on `messages`. All 142 probes went through. The only lock seen during the build was `ShareUpdateExclusiveLock` (136 probes; none for 6), which blocks neither reads nor writes. Writes took 0.12 s at the median (most of it `docker exec`), but 0.5 to 1.8 s for about 20 seconds while the build competed for the disk. Reads took 0.09 s at the median and 0.79 s at most. The time grows with the pages: for the live copy's 55,000 it should be a second or two. The API doesn't serve until the migration is done.

### Memory

The database container used 214 MiB after the timings (`docker stats`), with `shared_buffers` at 128 MB as in the compose file. The API's memory doesn't change with the number of pages (a page of results is 50 rows). A15 in CI checks the budget: see "CI".

### Tests

- `TestSearchFieldBoundary`: a page whose extra fields are `KILO` and `WATT`, and one whose text holds the separator.
  - `kilowatt`, `wattkilo`, `tk` (too short for the index) and `or:tk or:zz` find nothing across the two fields.
  - `panelkilo` and `panelwatt` find nothing across the text and a field.
  - Terms holding the separator find nothing across fields, but `alpha\x1fbeta` finds the page holding it.
  - A typed `%` matches itself, and case is ignored.
- `TestNightlyRedactionReachesSearch`: a phone number stored, as if before its rule, in the text and in an extra field, found by search. After the nightly run, neither it nor part of it is found, and `CALL REDACTED` is.
- `TestIndexTerms` (unit): which terms go to the index, and when the `or:` terms do.
- `TestA75SearchSpeed`: A75, as above.
- `TestSearchSecondTryAliases`: the second try (`SecondTryOnly`, for tests) lists the same pages as the first. That holds for `alias:` typed partial and quoted, picked, two of them, both kinds together, a value naming no alias, and `or:` terms with an alias.
- The whole Go suite passes on cld01, A63 included.

### Mutations

Three more in `scripts/mutation-checks.sh`:

- **a term matches across two extra fields**: the per-field condition replaced with one over the values joined. `TestSearchFieldBoundary` fails.
- **the search index isn't built**: A75 fails on time.
- **no second try in time** (`SearchFirstTry` an hour): A75's `hoses grass pump` fails on time.
- **the second try looks up `alias:` values with case** (`LIKE` for `ILIKE` in `AliasIDsNamed`), added with the owner's answers. `TestSearchSecondTryAliases` fails:

```
m13a_test.go:306: q=STN2&alias=sta: first try "GRASS FIRE NEAR STN2 | SMOKE SEEN AT STN2", second try "", want "GRASS FIRE NEAR STN2 | SMOKE SEEN AT STN2"
m13a_test.go:306: q=smoke&alias=sta&alias_like=Station+2: first try "SMOKE SEEN AT STN2", second try "", want "SMOKE SEEN AT STN2"
--- FAIL: TestSearchSecondTryAliases (0.76s)
```

and passes unmutated (`ok … 0.866s`).

Run on cld01, the three alone (the script's own `mutate`), abridged:

```
=== mutation: search: a term matches across two extra fields (the values joined) (internal/store/db/queries.sql.go)
    m13a_test.go:56: "tk": "ALPHA\x1fBETA 50% OFF | CHECK THE PANEL", want ""
    m13a_test.go:56: "or:tk or:zz": "CHECK THE PANEL", want ""
    -> the test fails: the check is guarded
=== mutation: search: the search index isn't built (internal/store/migrations/00013_search_speed.sql)
    m13a_test.go:245: one term that matches nothing: 7.318s, over 1 second (R33b)
    m13a_test.go:245: three terms that together match nothing: 8.414s, over 1 second (R33b)
    m13a_test.go:245: a term found only in an extra field: 7.704s, over 1 second (R33b)
    m13a_test.go:245: a term with alias:"Station 1": 2.599s, over 1 second (R33b)
    -> the test fails: the check is guarded
=== mutation: search: no second try with the index in time (the first try runs for an hour) (internal/store/search.go)
    m13a_test.go:245: three terms that together match nothing: 8.972s, over 1 second (R33b)
    -> the test fails: the check is guarded
=== unmutated
ok  	github.com/RIKIKU/PagerArchive/internal/acceptance	1.001s
exit 0
```

### A75's job, timed on cld01

`TestA75SearchSpeed` with `PAGERARCHIVE_A75=1` against a fresh PostGIS 18-3.6 container on cld01, as CI runs it, with the sixth search: 541 s in all.

```
m13a_test.go:171: seeded 4562500 pages in 5m51s, built the search index in 1m9s, all with VACUUM ANALYZE in 7m24s
m13a_test.go:178: messages with its indexes 2358 MB, of which the search index 364 MB; the database 2376 MB
m13a_test.go:251: one term that matches nothing                0 listed, 5ms 2ms 2ms (slowest 5ms; the query before M13a 19.362s)
m13a_test.go:251: three terms that together match nothing      0 listed, 381ms 380ms 375ms (slowest 381ms; the query before M13a 19.059s)
m13a_test.go:251: "Smoke showing" or:"grass fire"             50 listed, 6ms 6ms 5ms (slowest 6ms; the query before M13a 1ms)
m13a_test.go:251: a term found only in an extra field         50 listed, 12ms 9ms 7ms (slowest 12ms; the query before M13a 1.942s)
m13a_test.go:251: a term with alias:"Station 1"               35 listed, 7ms 7ms 7ms (slowest 7ms; the query before M13a 18.414s)
m13a_test.go:251: alias:"Station 1", a term never in its pages   0 listed, 433ms 412ms 409ms (slowest 433ms; the query before M13a 16.594s)
m13a_test.go:251: the same with alias:sta                     50 listed, 5ms 5ms 4ms (slowest 5ms; the query before M13a 1ms)
m13a_test.go:251: the same with "Station 1" picked             0 listed, 436ms 411ms 415ms (slowest 436ms; the query before M13a 16.115s)
m13a_test.go:251: one term in a third of the pages            50 listed, 5ms 4ms 4ms (slowest 5ms; the query before M13a 1ms)
m13a_test.go:251: a common term with alias:"Station 1"        50 listed, 4ms 5ms 4ms (slowest 5ms; the query before M13a 14ms)
--- PASS: TestA75SearchSpeed (540.79s)
```

The search index built in 69 s here, with nothing else running (the migration's concurrent build, beside the probes, took 132 s). The job's limit is 40 minutes, 35 for the test: GitHub's runners have faster disks than cld01. Its disk use is the database, 2.4 GB, plus PostgreSQL's write-ahead log (at most `max_wal_size`, 1 GB). The job prints `df -h /` before and after. Loki's 90% guard (A67, ADR 0014) is in the monitoring job, on another runner, so this job's disk use can't reach it. On e9aad34 (run 41391763713), the job took 6 minutes on GitHub's runner. Seeding took 3 min 17 s and the index build 46 s; the slowest A75 search was 398 ms, and the disk went from 42% to 45% used. The CI run on the last commit pushed is in the report on this branch.

## The owner's answers (2026-11-02)

1. **A common term that the other filters rule out** (`alias:"Station 1" STN2`, 5.8 s). Try the cheap fix first: find the alias's pages by `alias_id` rather than by name. If that brings it under 1 second without a large new index, add it to A75 as a sixth search, with a partial alias and a quoted one, and a test. Otherwise record it as a known limit, to look at again after the real data is imported. It did: see decision 5 and below. No new index. A75's sixth search is `alias:"Station 1" STN2`, also run as `alias:sta STN2` and with "Station 1" picked.
2. **The API waiting for the migration before it serves** (about 1.5 minutes on 3.1 million pages, with clients buffering) is fine. `docs/deploy.md`'s upgrade notes say so.

What was tried, on the same 4,562,500 pages (median of five, seconds; the same pages in every run):

| Search | e9aad34 | alias IDs in both tries | alias IDs and 32 MB in the second try only |
|---|---|---|---|
| `alias:"Station 1" STN2` | 5.895 | 2.469 | 0.429 |
| "Station 1" picked, `STN2` | 5.811 | 2.366 | 0.441 |
| `alias:"Station 1" alias:"Station 3" STN2` | 11.381 | 4.502 | 0.609 |
| `alias:sta STN2` | 0.004 | 0.005 | 0.004 |
| `alias:"Station 1" smoke` | 0.005 | **3.433** | 0.004 |
| `alias:"Station 1"` (no text) | 0.003 | 0.003 | 0.003 |
| the other A75 searches | as in the table above | | within 1 ms of it |

With the alias IDs in both tries, the planner stopped reading in order for a common term with an alias (`alias:"Station 1" smoke`, 3.4 s). It also combined the alias's pages with the term's by heap page, as `work_mem` was too small to keep them by row, and so rechecked 3.1 million rows. Hence the second try only, with the memory. Its plan, now exact (178 ms in the database; no heap page read):

```
Limit (actual time=177.858..177.861 rows=0)
  -> Bitmap Heap Scan on messages m (actual time=177.806..177.807 rows=0)
       -> BitmapAnd (actual time=176.947..176.948 rows=0)
            -> Bitmap Index Scan on messages_alias_idx (actual time=44.289..44.289 rows=1140625)
                 Index Cond: ((alias_id = a.id) AND (alias_id = ANY ('{1}'::bigint[])))
            -> Bitmap Index Scan on messages_search_trgm_idx (actual time=127.939..127.939 rows=1140625)
                 Index Cond: (message_search_text(text, extra) ~~* '%STN2%'::text)
```

The 32 MB is for one query at a time, while it runs, and only for a search that has reached its second try. The database's steady state doesn't change, and A15 still passes (see the CI run in the report).

No questions are open from that review. "The first search after a cold start" below has its own.

## The first search after a cold start (m16a, 2026-11-03)

The owner's brief: A75 now counts the first run of each search, straight after a restart with both caches cold (owner, 2026-11-03). ADR 0018's question 1 measured a common search at 31.8 s cold and 29 ms on its second run, on imported pages. Released as 0.14.1. This replaces decision 5's order of tries. The UI, the client, the module and the search syntax don't change, and nor do the pages listed.

### The cause

The first try's 250 ms is a limit on the clock, used to spot a plan that's wrong. A cold disk also reaches it. A search that was nearly done then goes to a second try made for terms that are rarely together, which for common terms reads a third of the table. The guess in ADR 0018 was right. What's new is why it misses on imported pages and not on seeded ones.

The newest pages decide it. On M13a's seed they sit together at the start of the table, so a common search's first try reads 12 blocks. An import stores live pages among its own as they arrive, so the newest pages are spread through the table, one about every 4,228 pages, and each is on a block of its own. None of them holds `"Smoke showing"`, so a search reads all 1,079 before it finds a match. So this isn't about importing as such: any table with its newest pages spread like that does it. A table with them together doesn't, and that's why A75's old seed never showed it.

`EXPLAIN (ANALYZE, BUFFERS)` on cld01's spinning disk, each after its own restart with both caches cold, `alias:sta STN2` with 1,079 of the newest pages moved:

```
first try: Limit (actual time=189.170..260.287 rows=51)
             Buffers: shared hit=78 read=1088
             -> Index Scan using messages_sent_idx on messages m (actual time=30.297..259.027 rows=52)
                  Rows Removed by Filter: 1232
           Planning Time: 24.535 ms, Execution Time: 260.418 ms

second try: Limit (actual time=10232.076..10232.086 rows=51)
             Buffers: shared hit=489 read=143361
             -> Bitmap Heap Scan on messages m (actual time=199.937..8942.716 rows=1140625)
                  Heap Blocks: exact=142578
           Execution Time: 10234.240 ms
```

The first try needed 285 ms in all and was stopped at 250 ms. The second took 10.2 s. With the pages spread through the whole table, `"Smoke showing" or:"grass fire"` five times, each after a cold restart, took 17.275, 11.669, 11.518, 12.607 and 11.618 s on old code, with one first-try timeout in the database's log each time. The fix on the same table took 0.447, 0.477, 0.440, 0.435 and 0.551 s, with none. The first try alone, with nothing else running, takes 0.42 to 0.47 s through the API.

### The fix

The tries run in turn, on one connection:

1. **The first try**, as planned, for 100 ms (`SearchFirstTry`), or 500 ms with `or:` terms. That's an answer for every common search on a warm disk: all of them measured take under 15 ms.
2. **A bounded try.** The same query without its `ORDER BY`, with ordered and sequential reading off, stopping at 1,000 pages. If it finds fewer, those are all the pages that match, so it sorts them and that's the answer. Terms rarely together end here, having read almost nothing but the index. Common terms stop it soon after it has read the index.
3. **The first try again**, for 1 second, with what it read still in the caches. This is the brief's "retry the first try", but it only runs once the bounded try has shown the terms are common, so a search that's going to end at step 2 never pays for it.
4. **The second try**, as before, only if that didn't answer: many pages match, and few or none of them are new.

Why this is enough: on a cold disk the common search loses only what the bounded try costs, which is 125 heap blocks where the second try read 143,361. And warm searches are faster, as the terms rarely together wait 100 ms rather than 250 ms before the index finds them:

```
hoses grass pump, bounded, cold: Bitmap Heap Scan (rows=0), Buffers: read=716, Execution Time: 223 ms; no heap block read
alias:sta STN2, bounded, cold:   Bitmap Heap Scan (rows=1000), Heap Blocks: exact=125, Buffers: read=990, Execution Time: 219 ms
```

The sequential scan has to be off in the bounded try: with no order to keep, the planner would read the table from its start, expecting 1,000 pages soon, and for terms never together that's all of it.

`or:` terms get 500 ms because of two measured facts. The planner's estimate holds for them, as it doesn't multiply them together, so their first try is rarely wrong. And reading the index for them is the slowest bounded try (0.85 s cold, against 0.16 to 0.53 s), and a first try makes almost no progress beside it (below). With 500 ms the first try finishes before the bounded try starts.

**`alias:` values that name every alias aren't given to the planner.** `alias:sta` names all four stations, so looking up their IDs rules out nothing, but on some statistics the planner still combined them with the term's pages, reading all of the index on `alias_id` (1.1 s cold, 4.5 million entries) and taking `alias:sta STN2` to 1.74 s. The names are still matched, so the pages listed don't change.

### What was tried first

Each was measured, cold and warm, on the same table with the newest pages spread through it, in seconds (cold; warm median). "Old" is cf43ff3.

| | `"Smoke showing" or:"grass fire"` | `alias:sta STN2` | `hoses grass pump` | `alias:"Station 1" STN2` |
|---|---|---|---|---|
| Old | 14.617; 0.008 | 10.628; 0.005 | 0.577; 0.378 | 0.921; 0.437 |
| No second try at all (the first try's floor) | 0.462; 0.008 | 0.429; 0.005 | not useful | not useful |
| Second try beside the first from 250 ms, first to answer wins | 2.189; 0.007 | 1.460; 0.005 | 0.852; **0.500** | 0.964; **0.538** |
| The same from 100 ms | **5.592**; 0.010 | **6.987**; 0.006 | 0.596; 0.330 | 0.891; 0.392 |
| Bounded try beside the first from 100 ms, all terms | **1.301**; 0.008 | 0.699; 0.006 | 0.455; 0.228 | 0.767; 0.315 |
| The same from 250 ms | **1.432**; 0.007 | 0.667; 0.005 | 0.582; 0.383 | 0.808; 0.436 |
| **In turn** (this) | 0.447; 0.007 | 0.719; 0.005 | 0.546; 0.229 | 0.909; 0.284 |

The last row's first figure is the first of the five runs above. An earlier run with this code, on the same copy, took 14.180 s: see "What this doesn't show".

- **Trying the first try again straight away, for 250 ms**, wasn't built. It would add 250 ms to every search that ends at the second try (`hoses grass pump` 0.38 to 0.63 s warm) and to `alias:"Station 1" STN2` cold, already near 1 s.
- **Running the tries side by side.** It looked best, as nothing is given up, but on a cold disk one try reading the table starves the other. A time trace of the bounded try beside the first shows it: the bounded try started at 279 ms, finished at 1,172 ms, and the first try finished only at 1,280 ms, having needed 450 ms alone. It's worse on a table stored newest first, where ordered reading goes forward and the kernel's readahead makes it very fast: there `alias:"Station 1" STN2` went to 10.1 and 11.5 s, as the bounded try waited for the first to read the whole table. It would also have needed a second connection from a pool of 4, cancelling on the server, and a limit on how many searches did it. In turn needs none of that.
- **A lower `effective_io_concurrency` for the second try** made no useful difference (2.166 s against 2.064 s).
- **A warm-up (`pg_prewarm` with `autoprewarm`)**: the owner offered it if the cause wasn't cheap to fix directly. It was, so it wasn't built or measured. See "Questions for the owner".

### Timings, before and after

A75's own seed, now with the newest pages spread (below), through the API, on cld01's disk, as CI runs it. "Old behaviour" is this code with the mutation below, which puts the 250 ms limit and the second try back. Cold is the first run after a restart with both caches cold; warm is the three runs after it. In milliseconds unless shown:

| Search | Old behaviour, cold | Fix, cold (two runs) | Old behaviour, warm | Fix, warm (run 1) |
|---|---|---|---|---|
| **A75** `ZZNOTHING` | 220 | 756, 519 | 2 2 2 | 3 2 2 |
| **A75** `hoses grass pump` | 504 | 352, 668 | 381 375 380 | 248 242 250 |
| **A75** `"Smoke showing" or:"grass fire"` | **13,239** | 451, 423 | 15 9 9 | 10 12 13 |
| **A75** `tanker9` | 196 | 192, 134 | 6 7 7 | 8 7 7 |
| **A75** `alias:"Station 1" 12345` | 166 | 105, 209 | 7 6 6 | 7 7 7 |
| **A75** `alias:"Station 1" STN2` | 881 | 931, 952 | 427 465 435 | 276 282 288 |
| **A75** `alias:sta STN2` | **11,011** | 706, 683 | 15 7 7 | 6 6 6 |
| **A75** `STN2`, "Station 1" picked | 867 | 711, 714 | 429 433 432 | 272 276 280 |
| `SMOKE` | 119 | 109, 102 | 3 4 3 | 4 4 4 |
| `alias:"Station 1" smoke` | 114 | 122, **1,002** | 4 3 5 | 3 3 4 |

The same table as "Timings, before and after" further up, cf43ff3 against the fix, on the seed without spread pages (the same database for both, and the same pages listed in every row: a hash of the ids). Median of five warm, then the first run cold, in seconds. It was taken before the rule for values naming every alias, which only `alias:sta STN2` meets:

| Search | Warm, cf43ff3 | Warm, fix | Slowest, fix | Cold, cf43ff3 | Cold, fix |
|---|---|---|---|---|---|
| **A75** `ZZNOTHING` | 0.002 | 0.002 | 0.002 | 0.102 | 0.137 |
| **A75** `hoses grass pump` | 0.378 | **0.225** | 0.237 | 0.594 | 0.422 |
| **A75** `"Smoke showing" or:"grass fire"` | 0.004 | 0.004 | 0.005 | 0.094 | 0.113 |
| **A75** `tanker9` | 0.007 | 0.007 | 0.010 | 0.153 | 0.145 |
| **A75** `alias:"Station 1" 12345` | 0.006 | 0.006 | 0.010 | 0.110 | 0.114 |
| **A75** `alias:"Station 1" STN2` | 0.425 | **0.275** | 0.286 | 0.793 | 0.730 |
| **A75** `alias:sta STN2` | 0.004 | 0.004 | 0.004 | 0.092 | 0.092 |
| **A75** `STN2`, "Station 1" picked | 0.430 | **0.274** | 0.281 | 0.999 | 0.757 |
| `SMOKE` | 0.003 | 0.003 | 0.004 | 0.094 | 0.096 |
| `alias:"Station 1" smoke` | 0.005 | 0.005 | 0.005 | 0.085 | 0.097 |
| `"CHECK HOSES" ZZNOTHING truck` | 0.003 | 0.003 | 0.003 | 0.096 | 0.100 |
| `"Check hoses" "Smoke" or:"grass fire"` | 0.005 | 0.005 | 0.005 | 0.092 | 0.094 |
| `a b c d e f g h i j ZZNOTHING` | 0.002 | 0.003 | 0.003 | 0.095 | 0.082 |
| `or:zznothing or:tanker9` | 0.009 | 0.008 | 0.009 | 0.146 | 0.130 |

No warm search in A75, nor `SMOKE`, is slower. Some outside A75 are, and all stay under 1 second. Warm, median of five, milliseconds, on A75's seed, through the store:

| Search | cf43ff3 | Fix | |
|---|---|---|---|
| `alias:"Station 1" or:STN2 or:STN3` | 545 | **784** | `or:` terms rarely together wait 500 ms |
| `STN1 "456"` | 131 | **281** | a first try of over 100 ms: stopped, then run again |
| `"MEET AT" STN3 "456"` | 167 | **262** | a first try of over 100 ms: stopped, then the bounded try answers |
| `GRASS STN4 "4561"` | 126 | **233** | the same |
| `alias:"Station 1" alias:"Station 3" STN2` | 614 | 299 | faster |
| `alias:"Station 1" alias:"Station 2" alias:"Station 3" STN4` | 766 | 341 | faster |

`STN1 "4561"`, over 1,000 matches and few of them new, took 35 ms warm and 0.26 s cold on both. A search that reached step 4 would take over 1.1 s before it even started, but none of those measured warm does. A search still holds one connection at a time, as it did.

### What this doesn't show

- **The real import wasn't run again.** The owner asked for the seed instead (2026-11-03), as the cold start isn't only an import's. The seed reproduces the half-minute search, but it builds the search index once, where an import grows it. ADR 0018 measured `hoses grass pump` at 4.755 s and `alias:"Station 1" STN2` at 2.027 s cold on imported pages, and 0.514 and 1.104 s on seeded ones, which suggests that a grown index reads slower when cold. I haven't measured it, and this fix doesn't change it.
- **`drop_caches` doesn't empty everything.** cld01 is a virtual machine, and its host's cache can't be dropped from inside it. Reads of 155,046 blocks in 1.2 s show that much was served from there.
- **One run with the fix took 14.180 s.** It was `"Smoke showing" or:"grass fire"` after a cold restart, **with the fix**: the tries in turn, before the rule for values naming every alias, which doesn't apply to this search. That copy of the table hadn't been read for about ten minutes, while other copies were. Its next two runs took 0.085 and 0.007 s, and five more cold restarts gave 0.435 to 0.551 s with no timeouts. Why it took so long isn't known. The database's log for that moment wasn't kept, so which tries ran wasn't seen. The time matches the second try (11.5 to 17.3 s on old code), which this code reaches only after the first try has missed twice, so a disk slower than usual, perhaps from a read that missed the host's cache, is the likeliest reason. But it's a guess: the seeded copy, unread for a few minutes in the same timing run, showed nothing like it. So a real reboot of a real disk could still overrun the limits and reach the second try.
- **The margin on a slow disk is small.** `alias:"Station 1" STN2` cold took 865, 931, 952, 909 and 891 ms in A75 runs of this code on cld01, and once 1,081 ms, in a mutation run that doesn't change how it's answered. A75 can fail on noise on a disk like cld01's. CI's runner has a faster one (below). `alias:"Station 1" smoke`, not one of A75's, took 1,002 ms once, against 107 to 126 ms in other runs.

### A75 in CI

`TestA75SearchSpeed` now runs each search's first run straight after `a75ColdStart`: `docker stop`, `sync; echo 3 > /proc/sys/vm/drop_caches`, `docker start`, then waits for `pg_isready`, which makes no connection to the database. It empties the API's connection pool too, as a reboot does, so the first run pays for its connection. It fails if an A75 search takes over 1 second or lists other pages than the query before M13a, then does the three warm runs as before. `PAGERARCHIVE_A75_CONTAINER` names the container: `job.services.postgres.id` in CI.

Emptying the pool came from CI's first run (41544501188): the runner restarts the database in under a second, and pgx only checks a pooled connection that has been idle for over a second, so `hoses grass pump` got a dead one and a 500. cld01's restarts take longer. That's the test's restart, not a change in how the API behaves: an API that outlives a restart this quick can still fail one request, as it always could.

The seed now spreads 1,079 of the newest pages through the table, one in every 4,228, 5 seconds apart, as live pages during an hour and a half's import. It holds the same 4,562,500 pages. Without this, the old behaviour passes A75 on cld01's disk bar one search, and the check wouldn't be testing what it's for. A75's other runs and A76's speed check use the same seed.

A75 on b603e66, the same test on each, in milliseconds. CI is run 41547473824's job on GitHub's runner; cld01 is a fresh container on its spinning disk:

| Search | CI, cold | CI, warm | cld01, cold | cld01, warm |
|---|---|---|---|---|
| **A75** `ZZNOTHING` | 40 | 2 2 2 | 475 | 3 2 2 |
| **A75** `hoses grass pump` | 264 | 220 220 219 | 356 | 233 223 225 |
| **A75** `"Smoke showing" or:"grass fire"` | 182 | 8 8 8 | 519 | 9 8 8 |
| **A75** `tanker9` | 63 | 7 6 8 | 144 | 8 7 6 |
| **A75** `alias:"Station 1" 12345` | 46 | 6 7 5 | 125 | 6 6 5 |
| **A75** `alias:"Station 1" STN2` | 438 | 270 273 276 | 891 | 283 277 280 |
| **A75** `alias:sta STN2` | 362 | 6 6 5 | 751 | 7 7 7 |
| **A75** `STN2`, "Station 1" picked | 446 | 271 274 275 | 716 | 276 283 285 |
| `SMOKE` | 46 | 3 4 3 | 106 | 3 4 3 |
| `alias:"Station 1" smoke` | 45 | 3 4 3 | 107 | 4 3 4 |

On CI the job took 6 min 42 s, of which the test 331 s: seeding 2 min 58 s, the index 45 s, all with `VACUUM ANALYZE` 3 min 50 s. The disk went from 42% to 45% used. On cld01 the test took 612 s. A76's job on the same run, A75's searches warm after its import: 1 to 8 ms, `hoses grass pump` 186 to 193 ms, the two searches `alias:"Station 1" STN2` and "Station 1" picked 220 to 229 ms.

**CI may not catch a regression here; only the mutation on cld01 does.** Old behaviour fails A75 when a first try misses its limit, and CI's disk may be too fast for that: `"Smoke showing" or:"grass fire"` took 182 ms cold in all, under the old limit of 250 ms. So the old code could well pass A75 on CI's runner. That hasn't been tried there. It's the mutation in `scripts/mutation-checks.sh`, run by hand on cld01, that shows the check fails (13.2 and 11.0 s), and CI doesn't run that script. A75 in CI still fails if a search errors, lists other pages, or is over 1 second on the runner's disk. A change to the tries should be checked with that mutation on cld01.

- `TestSearchBoundedTry`: the bounded try, and all the tries in turn with a 1 ms first try, list what the first try lists, in the same order. That covers pages at one time, pages each at its own time, a smaller and a larger page, the next page by its cursor, over 1,000 pages, and terms never together. The pages are stored oldest first, so the order the index finds them in isn't the search's.

### Mutations

Five new in `scripts/mutation-checks.sh`, and one changed. Each A75 mutation seeds again, so they take about 10 minutes each. Run on cld01, the search's own mutations through the script's `mutate`, abridged:

```
=== mutation: search: no other try in time (the first try runs for an hour)
    three terms that together match nothing: the first run after a cold start took 11.603s, over 1 second (R33b)
    three terms that together match nothing: 9.924s, over 1 second (R33b)
    -> the test fails: the check is guarded
=== mutation: search: a first try is given up at 250 ms for the second try, as before
    "Smoke showing" or:"grass fire": the first run after a cold start took 13.239s, over 1 second (R33b)
    the same with alias:sta: the first run after a cold start took 11.011s, over 1 second (R33b)
    -> the test fails: the check is guarded
=== mutation: search: the bounded try keeps its order, so it reads every match
    the same with alias:sta: the first run after a cold start took 11.008s, over 1 second (R33b)
    -> the test fails: the check is guarded
=== mutation: search: alias: values that name every alias still go to the planner
    the same with alias:sta: the first run after a cold start took 1.958s, over 1 second (R33b)
    -> the test fails: the check is guarded
=== mutation: search: the bounded try's answer isn't in the search's order
    q=timepage&limit=3: the bounded try lists "...:61 62 63 ", the first try "...:120 119 118 "
    -> the test fails: the check is guarded
=== mutation: search: the bounded try's stopped short answer counts as all of them
    q=manypages: the bounded try lists "...:1120 1119 1118 ...
    -> the test fails: the check is guarded
=== unmutated
ok  	github.com/RIKIKU/PagerArchive/internal/acceptance	2.524s
```

The second is the brief's: the old behaviour back, and the cold-start check fails. With it, warm searches pass (`hoses grass pump` 381 ms), so it's the first run that catches it. The cold-start mutations also require that text in the failure, which caught the third in its first form: it broke the build, and failed for that. The last two first survived, against a test whose pages were stored newest first, so the test was strengthened.

And `TestA75SearchSpeed` unmutated on cld01, from a fresh database each time, as in the table: passed in 615 s and 610 s, and in 612 s on b603e66.

### Questions for the owner

Answered by the owner (2026-11-03).

1. **Do you want a warm-up as well, for a physically cold disk?** I hadn't chosen one, as the cause was cheap to fix directly and A75 passes without it. But the 14.18 s run could mean a real reboot of a spinning disk still reaches the second try, and reading 1,079 scattered blocks takes about 9 s at 8 ms a read, whatever the search does. That only arises while the newest pages are spread, so until enough new pages have arrived after an import. **Answer: no warm-up for now.** A75's wording stays as it is.
2. **Shall I run the full import against this code?** About 1.5 hours, then the cold table. It would settle whether a grown index keeps `hoses grass pump` over 1 second cold on imported pages ("What this doesn't show"), which the seed can't. If it does, the likely answer is rebuilding the search index at the import's end, which I haven't tried. **Answer: it waits for the rehearsal on the production host before go-live.**
3. **Are the warm costs above acceptable?** A first try of 100 to 250 ms is now stopped and run again, and `or:` terms rarely together wait 500 ms: up to about 150 ms and 240 ms more, all under 1 second. A longer `SearchFirstTry` would spare most of them but take from the cold margin of `alias:"Station 1" STN2`, so I kept 100 ms. A75 and `SMOKE` aren't affected. **Answer: they're accepted.**

## A busy alias and short terms (fix-search, 2026-11-08)

The owner's brief: on the first real install (3.1 million pages over 7 years), two searches took seconds. A common term (`FIRE`) with `alias:"..."` for an alias holding about 58,000 pages, 21,000 of them matching, took 4.1 to 4.2 s cold and 830 ms warm. A map reference typed without quotes, three short terms, took about 3 s even warm. A75 gained both (SPEC.md, owner 2026-11-08). The fix had to be a patch, 0.16.2: no change to `compose.yaml` or the configuration archive. The UI, the client, the module and the search syntax don't change, and nor do the pages listed.

### The made-up pages

A75's seed held neither case, so CI passed. It now has:

- **Station 5**, one page in 57: 80,000 pages, production's busiest alias's share (58,000 of 3.1 million) over 10 years. They take the six phrasings in turn, so 26,661 hold `FIRE`. The search is `alias:"Station 5" fire`.
- **2,000 units**, `Unit 0001` to `Unit 2000`, sharing one page in 10, about 230 each. Without them the busy alias passed on main (0.86 s cold, 27 ms warm). The cause wasn't the alias's pages but how many aliases there are. The planner reaches an alias's pages through the join on its name, and it guesses each alias holds 1/n of the pages, n being the number of aliases. With 5 aliases that's a fifth, so it reads the newest pages in order. With 2,005 it's 2,275 pages, so it starts from the alias and reads all 80,000 of its pages through `messages_alias_idx` to sort them, as on production. With 405 aliases the plan depended on the statistics' sample, and `alias:"Station 1" smoke` went that way instead. A real network has many aliases, so I chose 2,000. Production has 2,285 (owner, 2026-11-08).
- **A map reference** in one page in 9, all Emergency, at the end of the description (section 10): `M <page> <square>`, a page of 600 and a square of 16 columns by 30 rows, at random. The search is `m 23 b4`. The pages holding all three terms, 104 of them, are spread over the 10 years, the newest 19 days old: few, as production's 3 s warm says. `m` is in every page (`MADEUPTON`).

The units changed one older A75 search. `alias:sta` used to name every alias, which ADR 0017's rule kept from the planner. It now names five of 2,005 aliases, holding 90% of the pages, and failed on main: 12.5 s cold, 5.4 s warm (below).

### The causes

**One alias.** As above: through the join on the alias's name, the planner can't know which alias it is, so it can't use its statistics. It guesses 1/n of the pages, starts from the alias, and reads and sorts every one of its pages. On main, `alias:"Station 5" fire` took 9.6 s cold and 1.09 s warm. `alias:"Station 1" smoke`, an alias holding a quarter of the pages, took 11.1 s cold and 3.9 s warm.

**Several aliases holding most of the pages.** The same guess, five times over, so the first try starts from the aliases and reads 4 million pages. And the bounded try is given their IDs, so it may combine most of the index on `alias_id` with the text's pages (decision 5's "values that name every alias"). On main `alias:sta STN2` took 1.6 s cold with 405 aliases, and 12.5 s cold and 5.4 s warm with 2,005.

**Short terms.** A trigram index can't find a term of fewer than three letters or digits: it has no trigram. When no term has one, the search reads the newest pages in order, so a search matching few pages reads them all: 11.3 s cold and 10.9 s warm for `m 23 b4`.

### The fix

1. **One alias goes to the first try by its ID** (`only_alias_id`), from the lookup the bounded try already made, now made first. A new index, `messages_alias_sent_idx` on `(alias_id, sent_at DESC)`, lets the planner read that alias's pages newest first and stop at a page of results, using its statistics for that alias. Only the first try gets it (and a search with no index term, which has only the one try). The bounded and second tries are as they were, and still use `messages_alias_idx`. That index holds each alias once, with its pages, so it's a fifth of the new one's size (28 MB against 141 MB), and a bitmap read from it cold is faster. In a first version that dropped it, `alias:"Station 1" STN2` took 1.9 s cold. The alias view's `alias_id`, when it's one alias, is given the same way.
2. **Several aliases holding most of the pages**, by the planner's own estimate (`EXPLAIN` of `alias_id = ANY(…)` against `reltuples`, over half), are the other way round. The bounded and second tries aren't given them, which generalises "values that name every alias". The first try selects them by ID alone (`alias_names_by_id`), not by name. It looks them up in its own transaction, at `REPEATABLE READ`, so they are exactly the aliases the names match in the snapshot the search reads. The names then aren't matched in the query, and the planner can't start from them. The pages listed are the same by construction.
3. **Short terms go to a bigram index** (`messages_search_bigrams_idx`, migration 00015). It's a GIN index on `message_search_bigrams(text, extra)`: the distinct two-character substrings of `lower(message_search_text(text, extra))`. A term found in the text or one extra field, case aside, is found in the expression too (as in decision 2, unless it holds a character jsonb escapes). So every two-character substring of the lower-cased term is in the page's list. ILIKE compares through `lower()` in a UTF-8 database, so `lower()` here matches what it matches. The query adds `message_search_bigrams(…) @> search_terms_bigrams(q_short)`, a necessary condition. Each term is still matched field by field, so the pages listed are the same. `search_terms_bigrams` of a constant is worked out when the query is planned, so the planner sees the values and uses the expression's element statistics. `message_search_bigrams` has a high `COST`, so a plan reading pages in order checks it after every other condition. Only searches whose terms have no trigram at all use it (`search.shortTerms`): each term of two characters or more, without the characters jsonb escapes. A search with a trigram term is as it was. They take the same tries as a search the trigram index serves.

### What was weighed

- **Raising `shared_buffers`.** Ruled out by the owner: after a reboot the operating system's cache is cold too.
- **One index on the alias and the text together** (`btree_gin`). It finds the exact pages, 26,661 for `alias:"Station 5" fire`, but they all have to be read and sorted for the newest 50: about as many heap reads as today. Not built.
- **Giving the first try every alias's ID, as `ANY`.** Decision 5 measured it: the planner combined the alias's and the term's pages for a common term (`alias:"Station 1" smoke` 3.4 s). And with the names still matched, it can still start from the aliases. One ID as an equality, with the new index, reads in order. Several are given by ID only when they hold most of the pages, where combining makes no sense.
- **Giving the alias only to the first try's second run** (step 3, after the bounded try has shown the terms are common). It was measured, cold, five runs each, in milliseconds:

  | | main | this fix (first try) | step 3 only |
  |---|---|---|---|
  | `alias:"Station 1" STN2` | 595 to 662 | 665 to 736 | 596 to 627 |
  | `alias:"Station 5" fire` | 8,710 to 10,238 | 103 to 163 | 413 to 499 |
  | `alias:"Station 1" smoke` | about 10,900 | 67 to 76 | 822 to 905 |

  The first try, reading the alias's pages in order, takes about 70 ms more cold when the term is in none of them: it runs out its 100 ms either way, but the bounded try after it is slower. Step 3 only would leave that search as it was, but every common term with an alias would pay the 100 ms first try and a bounded try, warm as well as cold. **The owner chose the first try (2026-11-08).** This is the one search measured as slower, cold only (below).
- **Treating adjacent short terms as a phrase.** It lists other pages: a page with `M`, `23` and `B4` apart isn't found, and A75 asks for the same pages. Trying the phrase first doesn't help either, as the rest still need every page read.
- **Making the trigram index find short terms.** It can't. `%b4%` gives no trigram, and the trigrams that hold `b4` in a page (`xb4`, `b4x`) have any letter or digit, in any script, in place of `x`.
- **A trigram index on the text with every character doubled** (`b4` becomes `bb44`, which has trigrams). Also exact, but about twice the entries of a bigram list, and every candidate rechecked by rewriting its text. Not built.
- **Single characters** have no bigram either. A search of only one-character terms reads every page, as before. None is in A75, and a lone letter or digit is in almost every page anyway.

### Costs

On A75's 4,562,500 pages (cld01, from `pg_relation_size` in A75's log):

| | before | after |
|---|---|---|
| `messages_search_bigrams_idx` | | 370 MB |
| `messages_alias_sent_idx` | | 141 MB |
| `messages`, with every index | 2,373 MB | 2,884 MB (+22%) |

- **The migration.** On a copy of the seeded pages with main's schema (405 aliases, as it then was), the API's first start ran 00015 in **8 min 6 s** on cld01's spinning disk. Building the bigram index is most of it: seeding builds the two GIN indexes in 8 min 4 s, against 1 min 8 s for the trigram index alone. Production's 3.1 million pages should take about 5 to 6 minutes. Reads and writes go on during the build (`CONCURRENTLY`, as decision 6), but the API serves only once its migrations are done (decision 6). So during the upgrade it doesn't store pages. Clients hold them in memory and retry (R15), and an integration skips a page that reaches it more than 5 minutes late (R51a). See "Questions for the owner".
- **Importing (A76's 500 pages a second).** Each page stored adds its bigrams to the GIN index, through its pending list. See "A75 and A76 in CI" for the rate.
- **Memory.** No change in steady state. The index build uses `maintenance_work_mem` (PostgreSQL's default, 64 MB) while it runs.
- **Every search with `alias:` values** makes the lookup first (it was made before the bounded try), and with several aliases one `EXPLAIN` more, under 1 ms warm.

### Timings, before and after

A75 on cld01's spinning disk, `TestA75SearchSpeed` from a fresh database each time, with the seed above: main's code with this branch's seed and searches (30b67d9), against this branch (e04e89f). Cold is the first run after a restart with both caches cold; warm is the three runs after it. Milliseconds unless shown; over 1 second in bold:

| Search | Main, cold | Fix, cold | Main, warm | Fix, warm |
|---|---|---|---|---|
| **A75** `ZZNOTHING` | 601 | 340 | 4 3 3 | 3 3 2 |
| **A75** `hoses grass pump` | 420 | 374 | 226 226 225 | 231 226 227 |
| **A75** `"Smoke showing" or:"grass fire"` | 540 | 533 | 10 8 8 | 10 10 10 |
| **A75** `tanker9` | 202 | 230 | 8 7 9 | 9 7 7 |
| **A75** `alias:"Station 1" 12345` | 301 | 215 | 116 115 113 | 9 8 8 |
| **A75** `alias:"Station 1" STN2` | 660 | 760 | 283 280 283 | 279 284 285 |
| **A75** `alias:sta STN2` | **12,451** | 751 | **5,391** | 10 10 10 |
| **A75** `STN2`, "Station 1" picked | 652 | 784 | 294 287 279 | 274 278 280 |
| **A75** `alias:"Station 5" fire` (busy alias) | **9,578** | 704 | **1,090** | 6 6 6 |
| **A75** `m 23 b4` (map reference) | **11,286** | 244 | **10,874** | 8 6 7 |
| `SMOKE` | 127 | 138 | 4 4 4 | 3 3 4 |
| `alias:"Station 1" smoke` | **11,065** | 130 | **3,868** | 5 5 4 |
| `alias:"Unit 0001" smoke` (a small alias) | 141 | 220 | 7 5 6 | 6 5 5 |
| `st 12` (two short terms in most pages) | 132 | 161 | 4 3 3 | 9 9 8 |

The pages listed are the same as the query before M13a's in every row, cold and warm (A75 checks it). Single cold runs vary by 100 to 300 ms between runs on cld01: `ZZNOTHING` took 317 to 601, the busy alias 159 to 704 in four runs of the fix. The direct comparison is the table under "What was weighed", five cold runs each, through the store, on one database: there `alias:"Station 1" STN2` is the one search slower, by about 70 ms. Warm, `st 12` takes 9 ms against 4: two short terms in most pages now take the tries, and the first answers. Every A75 search's cold run on cld01 was under 1 second in the four runs of the fix but one, a version that gave the bounded try the alias too (1,007 ms for `alias:"Station 1" STN2`).

### A75 and A76 in CI

CI run 42440766086 on e04e89f, every job passed. A75's job on GitHub's runner, in milliseconds:

| Search | Cold | Warm |
|---|---|---|
| **A75** `ZZNOTHING` | 50 | 2 1 1 |
| **A75** `hoses grass pump` | 678 | 215 302 207 |
| **A75** `"Smoke showing" or:"grass fire"` | 384 | 7 5 6 |
| **A75** `tanker9` | 75 | 5 5 5 |
| **A75** `alias:"Station 1" 12345` | 57 | 6 5 5 |
| **A75** `alias:"Station 1" STN2` | 520 | 239 244 243 |
| **A75** `alias:sta STN2` | 482 | 7 6 6 |
| **A75** `STN2`, "Station 1" picked | 715 | 239 278 242 |
| **A75** `alias:"Station 5" fire` | 99 | 3 4 3 |
| **A75** `m 23 b4` | 208 | 5 5 4 |
| `SMOKE` | 55 | 3 2 3 |
| `alias:"Station 1" smoke` | 78 | 3 3 3 |
| `alias:"Unit 0001" smoke` | 85 | 4 4 3 |
| `st 12` | 196 | 6 6 6 |

Seeding took 3 min 7 s and the two GIN indexes 5 min 2 s: 8 min 23 s with `VACUUM ANALYZE`, within the job's 35 minutes. The tables with every index came to 2,884 MB.

A76's job: 200,000 pages imported in random order on top of the 4,562,500, with live pages, at **1,050 pages a second** (1,063 while storing), against 963 on main's last run (the runner varies by that much), so the bigram index's cost to an import doesn't show at A76's 500. Live posts answered in 4 ms at the median, 129 ms at most. A75's searches after it, warm: 1 to 9 ms, `hoses grass pump` 210 to 216 ms, the two never-together Station 1 searches 251 to 280 ms.

### Mutations

Five new in `scripts/mutation-checks.sh`, and three changed to follow the code (the first try's second run, and the two rules for aliases holding most of the pages, which replace "values that name every alias"):

- one alias isn't given to the first try (A75, the busy alias);
- short terms aren't given to the bigram index, and the bigram index isn't built (A75, the map reference);
- the bigrams keep their case (`TestSearchShortTerms`);
- a short term goes to the bigram index as its ILIKE pattern, `%` and all (`TestSearchShortTerms`);
- several aliases holding most of the pages still go to the bounded and second tries, and the first try matches them by name (A75, `alias:sta`).

`TestSearchShortTerms` lists the same pages through every try for short terms in the text or one extra field, in other case (non-ASCII too), with a typed `%` or `_`, and never across two fields. `TestSearchSecondTryAliases` covers alias: values that name most of the pages, through the first try's lookup by ID. The A75 mutations each seed again, about 18 minutes each on cld01. Their run there was still going when this was written. It finished on 2026-11-08 at 06:28 UTC, after 3 h 10 min, on the code merged as 0.16.2 (`PA_MUT_ONLY="^search: " sh scripts/mutation-checks.sh`): **15 of 15 guarded**, every one with "the test fails: the check is guarded". The A75 ones failed on the search they were written for.

### Questions for the owner

1. **How many aliases does production have?** The seed's 2,005 decide how the planner guesses (above). A count would show whether that's realistic. **Answer (owner, 2026-11-08): 2,285.** The seed's 2,005 is close enough to be realistic.
2. **Is a 5 to 6 minute pause on upgrade acceptable for a patch?** While 00015 builds the bigram index, the API doesn't serve. Pages are held by the clients (R15), but an integration skips any older than 5 minutes when they arrive (R51a). Letting the API serve while it builds would be a change to how migrations run, not made here. **Answer (owner, 2026-11-08): not this time.** No receiving client is registered with PagerArchive before cutover (the old system takes the live pages), so no page arrives during the migration and no integration can miss one. After cutover, a long migration would need planning.

## A map reference with a page number (fix-search-2, 2026-11-08)

The owner's brief: on production (3.1 million pages), with 0.16.2, every A75 search passed cold and warm except a real map reference typed without quotes. It took 3.35 s cold, 1.1 s on the first warm run and 0.8 s after that. That reference is three words: a letter (in 89% of the pages), a page number of three digits (86,626 pages, about 3%) and a square of two characters (26,468 pages), all three together in about 1 page in 1,000. The fix had to be a patch, 0.16.3: no change to `compose.yaml` or the configuration archive, and the same pages listed.

### The made-up pages

A75's `m 23 b4` has no term of three characters, so it took the bigram index and passed. The seed's map references now put 15% on page 345 and 6% in square C7, independently, as a network's own area is on a few map pages and its towns in a few squares. The rest are as before, at random. The search is `m 345 c7`. Counted on the seed:

| | pages | share | production |
|---|---|---|---|
| `m` | every page (`MADEUPTON`) | 100% | 89% |
| `345` | 109,575 | 2.4% | 2.8% |
| `c7` | 31,432 | 0.7% | 0.85% |
| all three | 4,927 | 0.11% | about 0.1% |

`345` is also in some page numbers and street numbers, so its pages aren't only map references, as on production. `m 23 b4` still finds 50 or more pages. `345 c7` is timed too, for the record. So are two searches mixing a longer term with a short one in every page: `smoke st` and `tanker9 st`. Main's code failed the new search, cold, on cld01 (2.81 s) and in CI (1.11 s, run 42464765459, on 889744d).

### The cause

As the brief says. `345` has a trigram, so `search.shortTerms` returned nothing and the bigram index wasn't used. The trigram index found 109,575 candidates for `345`. On cld01, cold: the first try ran out its 100 ms, the bounded try stopped at 1,000 matches, the first try again ran out its second, and the second try read and sorted all 109,575. Warm, on A75's seed, the first try read the newest pages in order and answered in 93 ms. Production's pages are older, so its first try reads further.

### The fix

1. **A search with a term too short for a trigram gives every term to the bigram index, and none to the trigram index.** `shortTerms` now returns every term of two characters or more without the characters jsonb escapes, longer ones too, whenever one of them has no trigram. `Params` then leaves `q_index1` to `q_index4` unset. Every bigram of a term is in each page that holds it (decision 2 and "A busy alias and short terms"), so the condition is still necessary and the pages listed are the same. `m 345 c7` gives the index `34`, `45` and `c7`, which find 5,090 candidates for 4,927 pages. The letter `m` has no bigram, so it narrows nothing. It is in 89% of production's pages anyway, and it's still matched field by field. A search with no term too short for a trigram is as it was.
2. **The bigram index gets only the three rarest bigrams** (`store.RarestBigrams`). They're ranked by the statistics ANALYZE keeps on the index's expression (`pg_stats.most_common_elems`). A bigram the statistics don't list is rarer than every one they do. Without statistics, before the first ANALYZE, it gets all of them. Each bigram goes to the query as a term of its own, as a bigram's only bigram is itself, so the query doesn't change. A bigram in every page (`st`, `er`) costs a posting list of every page to read and rules out nothing. `tanker9 st` has seven bigrams. Counting their candidates cold, all seven read 2,495 of the index's pages and took 350 ms, for 457 candidates. Three of the rarest (`r9`, `nk`, `ke`) read 423 and took 113 ms, for the same 457. The statistics pick `an`, `nk` and `r9`.

The tries are as they were. With the bigram statistics the planner expects few pages (277), so the first try sorts the index's candidates rather than reading in order. Warm it answers, in 32 to 42 ms. Cold it runs out its 100 ms, the bounded try finds 1,000, and the first try again finishes the same sort, in 0.6 to 0.7 s on cld01.

### What was weighed

- **Combining the two indexes, as the brief suggested** (short terms to the bigram index, `345` to the trigram index). It finds 4,927 candidates, the pages exactly. But the trigram index's matches are always rechecked, and a recheck works out the condition again for each candidate: its whole text's bigrams, about 130 µs a page. Counting the candidates took 1,276 ms warm, against 120 ms with the bigram index alone. `m 345 c7` took 1.9 to 2.6 s cold on cld01 and 0.52 s warm in the second try. The bigram index alone has nothing to recheck.
- **Sorting the candidates at once when they are few, instead of the first try again.** Once the bigram index serves the search, the first try is already that sort: the planner chooses it itself. Letting it run for 1 s from the start rather than 100 ms gained nothing on cld01, cold, five runs each: 602 to 1,031 ms against 609 to 1,013. The time goes on reading about 5,000 pages from a cold disk.
- **Every bigram to the index.** `tanker9 st` took 295 to 355 ms cold on cld01, against 97 to 150 on main and 103 to 108 with the rarest three.
- **More read-ahead** (`effective_io_concurrency` 64 or 256 for the search, against 16). Reading the 5,090 candidates cold went from 635 to 750 ms to 530 to 600 ms, but at 256 one run took 873. It's a setting more for a gain within the noise. Not made.
- **A faster bigram function.** Without `DISTINCT` it takes 62 µs a page, against 133. But changing it changes the index's expression, so the index must be built again: 14.5 minutes on production's upgrade to 0.16.2. And the recheck would still cost 0.3 s on 5,000 pages.
- **Working out the bigrams in Go.** Go's lower case isn't always PostgreSQL's, and a bigram that differs would leave out pages. PostgreSQL works them out (`search_terms_bigrams`).

### Costs

- **Disk, the migration and the upgrade:** none. No index, function or migration is added, and the database at A75's size is 2,884 MB as before. The upgrade to 0.16.3 on production's 3.1 million pages runs no migration: the API serves as soon as it has started, in seconds. 0.16.2's 14.5 minutes there were 00015 building the bigram index, which this uses as it is.
- **Importing (A76):** nothing changes for storing pages. CI's A76 job stored 200,000 pages at **1,382 a second** (1,402 while storing).
- **Memory:** none. The second try reads 5,090 candidates where main's read 109,575.
- **Each search with a short term** runs `RarestBigrams` first: a few ms cold, under 1 ms warm. `smoke st` took 7 to 8 ms warm in CI against 3 to 4 on main, and `st 12` 7 to 8 against 9 to 10.

### Timings, before and after

On cld01, cold, through `store.SearchMessages` on one database seeded as A75, main's code (889744d) and this fix (f4719ac) in turn, a restart and the page cache dropped before each run. Five runs each, in milliseconds. Another agent was building on cld01 at the same time, which explains the odd slow run (`smoke st` 416 on main, 522 on the fix):

| | main | fix |
|---|---|---|
| `m 345 c7` | 2,582 to 3,050 | 678 to 1,011 |
| `345 c7` | 2,656 to 3,087 | 654 to 893 |
| `m 23 b4` | 83 to 98 | 93 to 105 |
| `tanker9 st` | 97 to 150 | 103 to 108 |
| `smoke st` | 65 to 416 | 82 to 522 |
| `st 12` | 77 to 88 | 79 to 483 |

`TestA75SearchSpeed` on cld01 from a fresh database each time: main failed `m 345 c7`, cold 2,809 ms, warm 93 to 102. The fix passed every search, `m 345 c7` cold 943 ms and warm 38 to 42. That run shared cld01 with the other agent's end-to-end stack (load 3.4), and its cold runs of searches this fix doesn't touch were slower than main's run too (the busy alias 824 ms against 182). On cld01's spinning disk the new search's margin is small: it reads about 5,000 pages cold. CI, below, is the measure.

### A75 and A76 in CI

| Search | main (889744d) cold | warm | fix (f4719ac) cold | warm |
|---|---|---|---|---|
| **A75** `m 345 c7` | **1,107** | 95 94 95 | 331 | 32 32 31 |
| **A75** `m 23 b4` | 67 | 6 7 6 | 58 | 6 5 5 |
| `345 c7` | **1,103** | 94 95 95 | 327 | 29 28 28 |
| `tanker9 st` | 84 | 8 7 7 | 73 | 6 6 6 |
| `smoke st` | 78 | 3 4 3 | 64 | 8 7 8 |
| `st 12` | 69 | 9 10 9 | 62 | 8 7 7 |

Every other A75 search was within 1 second, cold and warm, in both runs, and the pages listed were the same as the query before M13a's in every row (A75 checks it). CI run 42467999903 on f4719ac: A75, A76 and every other job passed except the PowerShell module's, which failed downloading PowerShell (504 Gateway Time-out) and passed when run again.

### Mutations

Five new in `scripts/mutation-checks.sh`:

- with a term the trigram index finds, short terms aren't given to the bigram index (A75, `m 345 c7`);
- both indexes find candidates, so each one's bigrams are worked out again (A75, `m 345 c7`);
- only the terms too short for a trigram go to the bigram index (A75, `m 345 c7`);
- every bigram goes to the bigram index, the common ones too (`TestSearchRarestBigrams`);
- the commonest bigrams go to the bigram index (`TestSearchRarestBigrams`).

`TestSearchShortTerms` adds a longer term with short ones, in other case, across two extra fields and with a typed `_`. Its results are checked through every try. `TestIndexTerms` checks which terms go to which index.

### Questions for the owner

None.

## Consequences

- A search's first page comes within 1 second on 10 years of pages in every A75 case, including its first run after a cold start, on cld01's disk with a small margin (see "What this doesn't show"), and in milliseconds for most when warm. Terms that are each common but never together take about 0.25 s warm, and a common term never in the chosen alias's pages about 0.3 s.
- The pages listed are the same as before for every search: the per-field condition is unchanged, and the index only finds candidates.
- One trigram index replaces another of the same size. The API's connections plan every query with its values. Searches run without JIT and, when the index can serve them, try in turn: the first try for 100 ms (500 ms with `or:` terms), a bounded try, the first try again for 1 second, then a second try. The bounded and second tries look up the `alias:` values' aliases first, unless they name every alias, and have 32 MB of `work_mem` while they run.
- A first try of 100 to 250 ms now costs up to about 150 ms more, warm, as it's stopped and run again.
- CI has one more job, A75's, which seeds 4,562,500 pages, and now restarts its database and drops the runner's page cache before each search.
- The search query's `ORDER BY` line is matched as text to run it unordered (`searchOrderBy`). If sqlc's output changes, the bounded try would sort every match, and A75's cold check fails, by a mutation above.
- From 0.16.2 (fix-search): a search for one alias reads that alias's pages newest first in its first try (`messages_alias_sent_idx`). Several aliases holding most of the pages are selected by ID in the first try and aren't given to the bounded and second tries. Terms too short for a trigram are found through `messages_search_bigrams_idx`, when no term has a trigram (from 0.16.3, with or without one: see the next point). A search of only one-character terms still reads every page.
- The two indexes add about 511 MB at A75's size (22%), and the upgrade to 0.16.2 builds them before the API serves: about 8 minutes for 4,562,500 pages on a spinning disk.
- A single-alias search whose term is in none of the alias's pages is about 70 ms slower cold (0.62 to 0.70 s on cld01), and the same warm. The owner accepted it (2026-11-08).
- A75's seed has 2,005 aliases, and its searches include the busy alias and the map reference. Its seeding builds two GIN indexes, which takes about 7 minutes more.
- From 0.16.3 (fix-search-2): a search with a term too short for a trigram gives the bigram index the three rarest bigrams of all its terms of two characters or more, by the statistics on the index's expression, and gives the trigram index nothing. No index or migration was added. Such a search runs one more small query first, about 1 ms warm, and a common term with a short one takes about 8 ms warm against 4.
