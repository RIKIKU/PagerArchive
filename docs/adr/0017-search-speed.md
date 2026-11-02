# ADR 0017: Search speed (M13a)

## Status

Proposed (2026-11-02). The owner's brief for M13a: SPEC.md R33b and A75 (owner, 2026-11-02), a fix after M13, on the branch `m13a` from main at abbebe7 (v0.13.0). Resized the same day to 10 years at 1,250 pages a day, 4,562,500 pages, the default retention and the archive being migrated (main e7aa98c, merged into `m13a`). Released as 0.13.1. The PowerShell module, the client, the UI and the search syntax don't change.

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

### 6. The migration

`CREATE INDEX CONCURRENTLY`, so reads and writes go on while it builds; goose runs the migration outside a transaction (`-- +goose NO TRANSACTION`). If it fails part-way, the invalid index is dropped when it runs again. It ends with `ANALYZE messages`: the planner knows how common a term is from statistics on the index's expression, and only ANALYZE gathers them, from a sample. The API runs migrations when it starts, so it serves once the index is built: about 2 minutes for 4,562,500 pages on a spinning disk, and a second or two for the live copy's 55,000 (see "Evidence").

### 7. P1

The index is derived from the text and extra fields alone. Retroactive redaction changes the text, and the re-parse that follows changes the extra fields, so the index changes in the same statements, in the same nightly run (`TestNightlyRedactionReachesSearch`). As with every index, PostgreSQL removes the old entries when it vacuums (P1: "followed by routine database vacuuming"). Nothing new is logged. The only new statements traced are `SET LOCAL` and the transaction around a search, which hold no page text or search terms, and A63 still passes.

### 8. A75 in CI

A job of its own, "Search speed (A75)" (`TestA75SearchSpeed`, run when `PAGERARCHIVE_A75=1`). On a fresh database with the migrations applied, it seeds 4,562,500 made-up pages with one `INSERT … SELECT … generate_series`, not the API. It drops the search index while seeding and builds it again afterwards, from the definition the migration gave it (`pg_get_indexdef`): building it once is faster than adding 4.5 million pages to it one at a time. It runs each of A75's five searches three times through the API, and fails if any run takes over 1 second. For each, it runs the query from before M13a directly as the reference, and fails if the pages listed differ. Two common searches are run the same way, for the record. The release jobs wait for this job.

The made-up pages: 10 years at 1,250 a day, from Stations 1 to 4 in turn, in six phrasings (the same as ADR 0015's timings), each with two extra fields. `TANKER9` is an extra field's value in one page in 10,000 (457 pages, all Station 4's), never in the text. A75's searches:

| A75 | Search |
|---|---|
| one term that matches nothing | `ZZNOTHING` |
| three terms that together match nothing | `hoses grass pump`, each in a sixth of the pages, never two in one page |
| `"Smoke showing" or:"grass fire"` | as written |
| a term found only in an extra field | `tanker9` |
| a term with `alias:"Station 1"` | `alias:"Station 1" 12345`: 35 pages |

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

The first run of each was within these too, apart from a cold first read: the slowest first run of an A75 search was 0.380 s. The last row isn't A75's. It's a term in a quarter of the pages, all of them other stations', with Station 1: see the questions below.

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
- The whole Go suite passes on cld01, A63 included.

### Mutations

Three more in `scripts/mutation-checks.sh`:

- **a term matches across two extra fields**: the per-field condition replaced with one over the values joined. `TestSearchFieldBoundary` fails.
- **the search index isn't built**: A75 fails on time.
- **no second try in time** (`SearchFirstTry` an hour): A75's `hoses grass pump` fails on time.

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

`TestA75SearchSpeed` with `PAGERARCHIVE_A75=1` against a fresh PostGIS 18-3.6 container on cld01, as CI runs it: 512 s in all.

```
m13a_test.go:170: seeded 4562500 pages in 5m56s, built the search index in 1m8s, all with VACUUM ANALYZE in 7m28s
m13a_test.go:177: messages with its indexes 2358 MB, of which the search index 364 MB; the database 2417 MB
m13a_test.go:239: one term that matches nothing                0 listed, 21ms 2ms 2ms (slowest 21ms; the query before M13a 19.371s)
m13a_test.go:239: three terms that together match nothing      0 listed, 384ms 376ms 375ms (slowest 384ms; the query before M13a 20.16s)
m13a_test.go:239: "Smoke showing" or:"grass fire"             50 listed, 6ms 5ms 5ms (slowest 6ms; the query before M13a 1ms)
m13a_test.go:239: a term found only in an extra field         50 listed, 12ms 9ms 8ms (slowest 12ms; the query before M13a 2.031s)
m13a_test.go:239: a term with alias:"Station 1"               35 listed, 8ms 6ms 7ms (slowest 8ms; the query before M13a 18.63s)
m13a_test.go:239: one term in a third of the pages            50 listed, 5ms 5ms 4ms (slowest 5ms; the query before M13a 2ms)
m13a_test.go:239: a common term with alias:"Station 1"        50 listed, 5ms 4ms 4ms (slowest 5ms; the query before M13a 14ms)
--- PASS: TestA75SearchSpeed (510.61s)
```

The search index built in 68 s here, with nothing else running (the migration's concurrent build, beside the probes, took 132 s). The job's limit is 40 minutes, 35 for the test: GitHub's runners have faster disks than cld01. Its disk use is the database, 2.4 GB, plus PostgreSQL's write-ahead log (at most `max_wal_size`, 1 GB). The job prints `df -h /` before and after. Loki's 90% guard (A67, ADR 0014) is in the monitoring job, on another runner, so this job's disk use can't reach it. The CI run on the pushed commit is in the report on this branch.

## Questions for the owner

1. **A common term that the other filters rule out.** `alias:"Station 1" STN2` takes 5.8 s. `STN2` is in a quarter of the pages, all of them Station 2's, so none are Station 1's. The index finds 1.1 million pages with the term, and each is then checked for the alias. The same would happen with a street only another station attends, searched within one station. A75 doesn't include it, and R33b's "a search" could. Making it fast would take the alias in the index too: a GIN index on the alias and the text together (`btree_gin`). Then `alias:` and `capcode:` would match by id rather than by name, so the names would be looked up first. That's about another 400 MB of index on 4,562,500 pages. Should M13a do that, should it be its own change, or is this case outside R33b?
2. **The API starts serving once the migration is done**: about 2 minutes for 3.1 million pages (the archive being migrated), going by the 132 s measured for 4,562,500 on cld01's spinning disk. Clients buffer their pages meanwhile. Is that acceptable for this upgrade, or should the index build in the background after the API starts (searches stay slow until it's done)?

## Consequences

- A search's first page comes within 1 second on 10 years of pages in every A75 case, and in milliseconds for most: about 0.4 s for terms that are each common but never together.
- The pages listed are the same as before for every search: the per-field condition is unchanged, and the index only finds candidates.
- One trigram index replaces another of the same size. The API's connections plan every query with its values. Searches run without JIT and, when the index can serve them, have a 250 ms first try.
- CI has one more job, A75's, which seeds 4,562,500 pages.
