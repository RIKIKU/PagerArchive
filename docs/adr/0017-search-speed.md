# ADR 0017: Search speed (M13a)

## Status

Proposed (2026-11-02). The owner's brief for M13a: SPEC.md R33b and A75 (owner, 2026-11-02), a fix after M13, on the branch `m13a` from main at abbebe7 (v0.13.0). Resized the same day to 10 years at 1,250 pages a day, 4,562,500 pages, the default retention and the archive being migrated (main e7aa98c, merged into `m13a`). Released as 0.13.1. The PowerShell module, the client, the UI and the search syntax don't change. Revised after the review of e9aad34 (2026-11-02): see "The owner's answers". Decision 5's order of tries was replaced for the first search after a cold start (m16a, 2026-11-03): see that section.

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

The last row's first figure is the first of the five runs above. The run before them, the first read of that copy after it stood idle, took 14.180 s: see "What this doesn't show".

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
- **`drop_caches` doesn't empty everything.** cld01 is a virtual machine, and its host's cache can't be dropped from inside it. Reads of 155,046 blocks in 1.2 s show that much was served from there. The one time a copy of the table was read after standing idle, its first run of `"Smoke showing" or:"grass fire"` took 14.180 s, then 0.085 s, and five runs after that took under 0.56 s. A real reboot of a real disk could therefore still overrun the limits and reach the second try. So this may not be the last word on a physically cold disk, which is question 1.
- **The margin on a slow disk is small.** `alias:"Station 1" STN2` cold took 865, 931, 952 and 909 ms in A75 runs of this code on cld01, and once 1,081 ms, in a mutation run that doesn't change how it's answered. A75 can fail on noise on a disk like cld01's. CI's runner has a faster one: see the report on this branch. `alias:"Station 1" smoke`, not one of A75's, took 1,002 ms once, against 110 to 126 ms in other runs.

### A75 in CI

`TestA75SearchSpeed` now runs each search's first run straight after `a75ColdStart`: `docker stop`, `sync; echo 3 > /proc/sys/vm/drop_caches`, `docker start`, then waits for `pg_isready`, which makes no connection to the database. It empties the API's connection pool too, as a reboot does, so the first run pays for its connection. It fails if an A75 search takes over 1 second or lists other pages than the query before M13a, then does the three warm runs as before. `PAGERARCHIVE_A75_CONTAINER` names the container: `job.services.postgres.id` in CI.

Emptying the pool came from CI's first run (41544501188): the runner restarts the database in under a second, and pgx only checks a pooled connection that has been idle for over a second, so `hoses grass pump` got a dead one and a 500. cld01's restarts take longer. That's the test's restart, not a change in how the API behaves: an API that outlives a restart this quick can still fail one request, as it always could.

The seed now spreads 1,079 of the newest pages through the table, one in every 4,228, 5 seconds apart, as live pages during an hour and a half's import. It holds the same 4,562,500 pages. Without this, the old behaviour passes A75 on a fast disk, and the check wouldn't be testing what it's for. A75's other runs and A76's speed check use the same seed.

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

And `TestA75SearchSpeed` unmutated on cld01, from a fresh database each time, as in the table: passed in 615 s and 610 s.

### Questions for the owner

1. **Do you want a warm-up as well, for a physically cold disk?** I haven't chosen one, as the cause was cheap to fix directly and A75 passes without it. But the idle copy's 14.18 s suggests a real reboot of a spinning disk could still reach the second try, and reading 1,079 scattered blocks takes about 9 s at 8 ms a read, whatever the search does. That only arises while the newest pages are spread, so until enough new pages have arrived after an import. If you'd like it, `pg_prewarm` with `autoprewarm` would need measuring as you described: how long it takes after a reboot on cld01, its memory against the budget, and searches while it runs. A75's wording would then change to match.
2. **Shall I run the full import against this code?** About 1.5 hours, then the cold table. It would settle whether a grown index keeps `hoses grass pump` over 1 second cold on imported pages ("What this doesn't show"), which the seed can't. If it does, the likely answer is rebuilding the search index at the import's end, which I haven't tried.
3. **Are the warm costs above acceptable?** A first try of 100 to 250 ms is now stopped and run again, and `or:` terms rarely together wait 500 ms: up to about 150 ms and 240 ms more, all under 1 second. A longer `SearchFirstTry` would spare most of them but take from the cold margin of `alias:"Station 1" STN2`, so I kept 100 ms. A75 and `SMOKE` aren't affected.

## Consequences

- A search's first page comes within 1 second on 10 years of pages in every A75 case, including its first run after a cold start, on cld01's disk with a small margin (see "What this doesn't show"), and in milliseconds for most when warm. Terms that are each common but never together take about 0.25 s warm, and a common term never in the chosen alias's pages about 0.3 s.
- The pages listed are the same as before for every search: the per-field condition is unchanged, and the index only finds candidates.
- One trigram index replaces another of the same size. The API's connections plan every query with its values. Searches run without JIT and, when the index can serve them, try in turn: the first try for 100 ms (500 ms with `or:` terms), a bounded try, the first try again for 1 second, then a second try. The bounded and second tries look up the `alias:` values' aliases first, unless they name every alias, and have 32 MB of `work_mem` while they run.
- A first try of 100 to 250 ms now costs up to about 150 ms more, warm, as it's stopped and run again.
- CI has one more job, A75's, which seeds 4,562,500 pages, and now restarts its database and drops the runner's page cache before each search.
- The search query's `ORDER BY` line is matched as text to run it unordered (`searchOrderBy`). If sqlc's output changes, the bounded try would sort every match, and A75's cold check fails, by a mutation above.
