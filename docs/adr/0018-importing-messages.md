# ADR 0018: Importing messages (M16)

## Status

Proposed (2026-11-03). The owner's brief for M16: SPEC.md section 7.21 (R83-R83f) and A76 (owner, 2026-11-02), on the branch `m16` from main at a2f7e70. Revised for the owner's decision of 2026-11-02 (main 5cb6da1, merged into `m16`): searches during an import aren't held to R33b. Released as 0.14.0. The UI, the search, the client and the PowerShell module don't change. It must fit M17: nothing from the repo at run time, no extra mounts, and a migration that runs by itself and only goes forward (R84, R84a).

## Context

The owner is moving about 3.1 million pages from an older archive, and the first production instance needs that (section 12). Converting the old data into this format happens privately, outside this repo. The brief asks for a server-only command that treats each page as a live page and doesn't tell anyone, with a report, at 500 pages a second while live pages keep arriving on time.

## Decisions

### 1. The command

`pagerarchive import-messages [--dry-run] <file>`, or `-` for standard input, so it runs as `docker compose exec -T api pagerarchive import-messages - < pages.jsonl`, with no file mounted (R84). It's a process of its own in the API's container, so its memory counts as the API's (see "Evidence"). Report on standard output, progress on standard error. Format and example: `docs/import.md`.

A field the format doesn't have refuses the line, as the API does, so a misspelt `source_id` can't silently cost the import its safety. Times are kept to the microsecond, as PostgreSQL keeps them, so that a second run compares equal. Limits are the API's: 1 to 7 digit capcodes, 1 to 4096 bytes of text, no more than 60 s in the future. A file says nothing about the radio, so function code and baud rate are stored as `NULL`.

A file with a byte order mark and CRLF line ends, as Windows tools make them, is read as it should be.

### 2. One path: `ingest.Service.Draft` and `Save`

`Ingest` is split at its joints, and the import calls the same pieces, so no rule has a second copy:

| Step | Live | Import |
|---|---|---|
| Redaction, cleaning, priority, ignored capcodes | `pipeline.Prepare` | `pipeline.Prepare` |
| Alias, profile, parsing, fields | `Service.Candidates`, `Service.Draft` | the same |
| Dedup (R19, R21), location, insert | `Service.Save`, in its own transaction | `Service.Save`, in the batch's |
| Who received it | a row in `receptions` | a row in `import_receptions` |
| Last-seen time, deliveries, live update | `TouchClient`, `OnStored`, `Hub.Publish` | none |

Job grouping needs nothing more: a job is its `job_number` column, for all time (R23), so it doesn't depend on order.

### 3. Lines out of time order are taken in time order

R19's rule runs from a page's first copy, so dedup needs the copies in time order. Refusing lines out of order is simpler, but an archive with several receivers rarely has its IDs in time order, and refused lines would make the result depend on the order all the same.

So the command works in two stages:

1. **Read and check.** Every line is checked, then redacted, and the result goes by `COPY` into a temporary table, with an index on the time. The unredacted text never leaves the command.
2. **Store.** Each batch reads the next 500 pages from the index, plus any others with the last one's time, ordered by time, then capcode, text and source ID. So even pages with one time are stored in one order, whatever the file's. Each batch of 500 is one transaction.

Why PostgreSQL holds them:

- **In the command's memory:** 4.6 million pages are about 700 MB, against the API's 256 MB budget. Holding only times and file offsets doesn't work for standard input, which can't be read twice.
- **In a temporary file in the container:** it would be page text in a container layer, and the image is meant to write nothing.
- **In PostgreSQL:** temporary tables take no write-ahead log, go with the connection however the command ends, and hold only redacted text (P1).

**No global sort.** The first version sorted the whole file into a second temporary table. On 4.5 million pages that wrote about 3 GB in a burst, and a live page's commit waited behind it: the first full run's slowest live page took 5.8 s, over R51a's 5, and 6 of the 7 live pages over 0.5 s were in or just after the sort. The index does the same job. It takes 4 s to build, and the staged pages take 718 MB rather than three times that, on 4,562,500 pages.

Each batch is two short queries, so no snapshot stays open for hours and vacuum can do its work throughout.

### 4. Quiet (R83b)

By construction: the command is another process, so it can't reach the API's live hub, its counters, or its `Integrations.Wake`.

- **Integrations.** `Importer.Run` clears `OnStored`, whatever service it's given. The queue is a table the API's worker reads, so a queued delivery would be sent.
- **Liveness.** `TouchClient` isn't called, and there's no client to call it for.
- **Metrics.** `pagerarchive_pages_received_total` counts in the API. `pagerarchive_messages_flagged` counts stored messages, so it does rise with imported parse failures: it's a count of what's stored, and no alert reads it. The example stack's alerts are about a client's last-seen time, a radio stalling and the API's reachability.
- **Live updates, the one that wasn't by construction.** A live connection's catch-up (ADR 0010 §3) read `messages` by ID, so a watch reconnecting during or after an import would have been sent every imported page: millions, in order. `MessageIDsAfter` and `LastMessageID` now read `receptions`, so they mean "messages clients sent". That's cheap however many pages were imported, from `receptions`'s primary key, where filtering `messages` would walk through all of them. It also closes a small race: the import's IDs interleave with live ones, and a catch-up's "covered up to" could otherwise pass a live page still being committed.

A page imported and later heard by a client is a duplicate, and duplicates aren't announced, as before.

### 5. "import" isn't a client

`receptions.client_id` must name a row in `clients`, and every use of `clients` means a receiver: the list, R39's flag, metrics, dashboards, API keys. A row called `import` would need an exception in each, now and later, and a real client might already have the name.

So a new table, `import_receptions (message_id, received_at, source_id)`, holds the copies read from files, and `clients` knows nothing of them. That's why it can't appear in the list, R39, metrics or dashboards. The two queries that read a message's receive times add its rows with an empty `client_id`, which no client can have (the column's `CHECK`), and the name `import`. `receptions()` shows it as `import` to everyone: it reveals nothing, and a user's "Receiver 1" stays the first real receiver. The detail view and the module show it with no change.

### 6. Safe to repeat (R83c)

- **By `source_id`**, when it's given: stored in `import_receptions`, with a unique index. If it's seen, the line is skipped, whatever its time and text, so a corrected export doesn't store a page twice.
- **Otherwise by dedup key and receive time**: the message with that key, with an import copy at that time and no ID. It reads the message's current key, so a retroactive redaction leaves nothing stale. A stored hash of its own would have been one more derived value for P1 to reach.
- **A run that stops** leaves whole batches. Each batch's copies are stored in the transaction that stores its pages, so running the file again skips exactly what's there and goes on. The pages are taken in the same order, so the result is the same as one full run.
- **Commits don't wait for the disk** (`synchronous_commit = off`, per batch). A batch lost in a crash is stored by the next run, so nothing is at risk, and it keeps the import's commits from queuing with a live page's.
- **One import at a time**, by a session advisory lock. A killed run's lock lasts until its backend notices, so a new run waits up to 30 s before saying `another import is running`.

The table's migration is `CREATE TABLE`, which is instant and touches nothing else (R84a). Copies go when their message does (R45, `ON DELETE CASCADE`).

**Stopping one.** Ctrl+C ends `docker compose exec`, and the import goes on inside the container: on cld01, an interrupted run went from 5,000 copies to 10,000 in the next 10 s, and a second run said `another import is running`. `docker compose restart api` stops it, and running the file again finished the job (19,500 and then 40,500 of 60,000). If it's still reading the file when interrupted, it carries on with what it has read, which a later run completes. `docs/import.md` says so.

**P1 and the source ID.** It's an ID, but it comes from outside, and the rules might match it (a long number can look like a phone number). A source ID a rule matches isn't stored: the page is, known by its key and time, and the report counts it. When the rules change, the nightly run drops any stored ID they now match, in the same step as its retroactive redaction. A refused line is named by its number, never its source ID.

### 7. Retention (R83d)

The cut-off is the nightly job's own (`Job.Cutoff`, a calendar date in the deployment's time zone), taken once when the command starts. A page before it is counted and goes no further. A nightly run during a long import deletes what it should, by its own later date.

### 8. Report and P1 (R83e)

A refused line is given as `line N: reason`, and the reason is one of a list of fixed texts. Decoder errors are never used: Go's JSON decoder quotes what it choked on (`invalid character 'S' looking for beginning of value`), and `time.Parse`'s error quotes the whole value. Database errors go through `store.SafeError`, as everywhere, so a failed statement reports its `SQLSTATE`.

The command exports no telemetry. It isn't a service, and what it did is in its report.

**Dry run.** The same code as a real run, but read only: `walk` is shared, and only what it does with a page differs. Pages earlier in the file aren't in the database, so a dry run keeps the first copies of the last 10 seconds. The pages come in time order, so that's all R19 can reach. A repeated source ID is taken out in one statement. `TestM16DryRun` compares a dry run's report with the real run's, field by field.

Refused lines don't fail the command, which exits 0: the report is what says so. It exits 1 only if it didn't finish.

### 9. Speed: what was measured, and no cap

**The trigram index isn't the bottleneck.** On one database of 4,562,500 pages on cld01's spinning disk, nothing else running, an import of 100,000 pages per row, in this order:

| Settings | Pages a second |
|---|---|
| Baseline: `fastupdate` on, 4 MB pending list (the defaults) | 862 |
| The same again | 902 |
| `fastupdate` off | 743 |
| 32 MB pending list | 887 |
| The defaults, with generic plans in the import's transactions | 1,118 |
| No search index at all | 930 |

The whole index costs about 5%, and its pending list does its job at its default. Nothing about it changes, and the question of dropping it during an import doesn't arise. No batch insert either: `Service.Save` stays one statement at a time, the same code as a live page.

**What did change: a page was written twice.** `Save` inserted the page, then placed it with an `UPDATE`, which writes a new version to every index, trigrams included, because `location` is indexed. It now finds the location first and sets it in the `INSERT`, for live pages too. On 200,000 pages on top of 4,562,500, that took the import from 686 to 830 pages a second.

**Generic plans were tried and taken out.** The table's 1,118 made them look like the next step: `Store.Open` plans every statement for its values (ADR 0017 §4), and `SET LOCAL plan_cache_mode = auto` in the import's transactions skipped that. But a table measured when seeded hides what they do to an empty one, which is the owner's case. A plan cached while the table is nearly empty is a sequential scan, so from an empty database the rate fell from 553 to about 300 pages a second as the table grew, until autovacuum's `ANALYZE` replaced the plan. Without them, the same 60,000 pages held 713 to 938 a second.

**No cap (owner, 2026-11-02).** At 1,120 pages a second, with generic plans, the import filled PostgreSQL's 1 GB of write-ahead log about every 70 s, and one search in 183 took 1.94 s during a checkpoint's fsync. A cap of 750 a second was being tried for that when the owner decided that searches during a one-off import don't matter (R83f), so it went, before it was ever committed. Neither of the things that still count needs one: live delivery and the budget hold at the import's full speed (see "Evidence"). If a live page ever does wait, the most it waits for is one batch's commit at `ingest.Save`'s lock, and only if it has the same capcode and text as a page in the batch.

### 10. After the import: nothing to catch up

The command ends with `VACUUM (ANALYZE) messages`, so when it returns, `messages` is as A75 tests it, right after its own `VACUUM ANALYZE`, with fresh statistics for the search index's expression (ADR 0017 §6). A75's searches are then timed straight away, with no wait for autovacuum, three times each as A75 does, in A76's speed check. A killed run doesn't get that far until the run that finishes it.

The vacuum is short: 5 s after 4,562,500 pages, as autovacuum had been through the table 7 minutes earlier.

The first full run, which ended with `ANALYZE` alone, failed this: three of the eight searches took over a second on their first run, one of them 17.4 s. The second, with the vacuum, took 459 ms at most. That's one run each, on otherwise different code, so it doesn't show the vacuum alone made the difference. And the cause is still there on a cold cache: see "Questions for the owner", question 1.

## What was weighed

- **Refusing lines out of order:** see decision 3.
- **A global sort of the file:** see decision 3.
- **A row in `clients` for `import`, or dropping `receptions`'s foreign key:** see decision 5.
- **A hash of `source_id`**, which can't hold a detail and needs no sweep. It would spread writes across the index at random, where an old archive's IDs arrive nearly in order. And the owner may want to read an ID when tracing a page back.
- **Refusing a line whose source ID a rule matches.** It'd lose a page (P2) over an ID the page doesn't need. Dropping the ID loses nothing: the page is still known by its key and time.
- **Filtering the catch-up with `NOT EXISTS (import_receptions)`:** it would walk every imported page, on every catch-up that spans an import.
- **Retrying a batch PostgreSQL ends to break a deadlock.** It would be untested code for something I couldn't make happen: the import never waits for a live page's lock while holding one it wants. Running the file again finishes the job.
- **A pace or a `--rate` option:** see decision 9.

## Evidence

On cld01 (4 vCPU, 11 GB, the database on a spinning disk), through `docker compose exec` on the compose file's API and PostGIS, with its settings (`shared_buffers=128MB`, `work_mem=4MB`). A live page every 5 s, with an integration selecting Station 1 and an open list. Memory by docker stats, A15's measure, in the API's container, so it includes the import.

### The full import: 4,562,500 pages on the spinning disk

`scripts/a76.sh -s 0 -n 4562500 -l 3000 -r` on 4c6602a: an empty database, the real-size address dataset (3,002 localities, 200,378 streets, 3.8 million addresses), the file in random order, the hardest read, and `NIGHTLY_AT` set to fire during it. It finished at 1h 29m.

```
[    68s] read 4562500 lines in 1m5s; indexing them by time
[    72s] indexed 4562500 pages in 4s, 718 MB on the database's disk until the import ends
[  5352s] vacuuming and analysing the messages
messages before 0, after 4563579; import receptions 4562500
messages with its indexes 3333 MB, of which the search index 524 MB; import_receptions with its indexes 714 MB; the database 4652 MB
--- rate
read 1m8s, indexed by 1m12s, finished at 1h29m17s
4562500 pages: 852 a second overall, 863 a second while storing
    0%- 10%:   880 a second
   10%- 20%:   886 a second
   20%- 30%:   869 a second
   30%- 40%:   862 a second
   40%- 50%:   873 a second
   50%- 60%:   876 a second
   60%- 70%:   855 a second
   70%- 80%:   829 a second
   80%- 90%:   844 a second
   90%-100%:   874 a second
--- live pages before the import
  11 live pages posted, 0 lost or refused
  post answered                      median    7ms, 99th percentile  262ms, slowest 262ms
  live update in an open list        median    8ms, 99th percentile  263ms, slowest 263ms
  delivered to the integration       median    9ms, 99th percentile  264ms, slowest 264ms
--- live pages during the import
  1068 live pages posted, 0 lost or refused
  post answered                      median   10ms, 99th percentile   49ms, slowest 1.278s
  live update in an open list        median   11ms, 99th percentile   50ms, slowest 1.279s
  delivered to the integration       median   12ms, 99th percentile   50ms, slowest 1.28s
  the integration's webhook got 1079 requests, the open list 1079 messages
--- A75's searches after the import, at 13:11:30 UTC (last ANALYZE 13:11:19, last autovacuum 13:04:06, changed since analysed 501)
  one term that matches nothing                    0 listed, 24ms 2ms 2ms
  three terms that together match nothing          0 listed, 131ms 119ms 116ms
  "Smoke showing" or:"grass fire"                 50 listed, 11ms 8ms 7ms
  a term found only in an extra field              0 listed, 2ms 2ms 2ms
  a term with alias:"Station 1"                   35 listed, 6ms 6ms 5ms
  alias:"Station 1", a term never in its pages     0 listed, 459ms 429ms 433ms
  the same with alias:sta                         50 listed, 5ms 5ms 7ms
  the same with "Station 1" picked                 0 listed, 437ms 433ms 447ms
A76 speed: PASS
peak memory api      114.7 MiB by docker stats,   111.1 MiB without file cache
peak memory db       435.7 MiB by docker stats,   121.9 MiB without file cache
API peak:          114.7 MiB (limit 256)  PASS
api + db peaks:    550.4 MiB (limit 1024) PASS
A76 speed and budget: PASS
```

The figures in the "before" block are 11 pages, so its 99th percentile is its slowest. Of the 1,068 during the import, 2 took over 0.5 s, both in its first 3 minutes (1.3 s and 0.5 s). The nightly run came and went at 23:19:02, with no live page over 0.5 s around it. All 4,562,500 pages were stored: this file holds no duplicates or refused lines, which A76's file checks cover. The API's memory peaked as the command loaded the address dataset, 85 s in, and its median through the run was 110 MiB.

From this, 3.1 million pages take about an hour.

### What the full runs showed along the way

| Run | Code | Rate | Slowest live delivery | Slowest A75 search after | |
|---|---|---|---|---|---|
| Full, in time order | 88792c1: global sort, `ANALYZE` at the end | 849 a second | 5.8 s (R51a's limit is 5) | 17.4 s | failed |
| 1,000,000 pages, random order | 4c6602a's import | 852 a second | 1.05 s | 43 ms | passed |
| Full, random order | 4c6602a | 852 a second | 1.28 s | 459 ms | passed |

### A76's speed in CI

On 4c6602a (run 41438521218), 200,000 pages in random order on top of 4,562,500, on GitHub's runner:

```
seeded 4562500 pages in 2m7s, built the search index in 46s, with VACUUM ANALYZE 3m1s
200000 pages: 971 a second overall, 986 a second while storing
--- live pages during the import
  post answered                      median    6ms, 99th percentile   33ms, slowest 33ms
  delivered to the integration       median    8ms, 99th percentile   35ms, slowest 35ms
--- A75's searches after the import, at 11:50:34 UTC (last ANALYZE 11:50:23, last autovacuum never, changed since analysed 1)
  three terms that together match nothing          0 listed, 406ms 400ms 400ms
  alias:"Station 1", a term never in its pages     0 listed, 452ms 439ms 449ms
  the same with "Station 1" picked                 0 listed, 445ms 452ms 445ms
api + db peaks:    397.1 MiB (limit 1024) PASS
```

The other five searches took 14 ms or less. The last CI run is in the report on this branch.

### A76's file checks

`scripts/a76-checks.sh`, on cld01, in the order A76 gives them, with the file shuffled:

```
dry run: map[duplicates that would be merged:20 lines read:10000 pages that would be stored:9972 parse failures:1980 refused lines:3 skipped by retention:5 ...]
the dry run's refused lines: map[4659:not valid JSON 4816:not valid JSON 5680:received_at must be RFC 3339 with its offset]
the import: map[duplicates merged:20 lines read:10000 pages stored:9972 parse failures:1980 refused lines:3 skipped by retention:5 ...]
the second run: map[already imported:9992 duplicates merged:0 lines read:10000 pages stored:0 refused lines:3 skipped by retention:5 ...]
a corrected export: map[already imported:100 lines read:100 pages stored:0 ...]
the file in time order: map[duplicates merged:20 lines read:10000 pages stored:9972 ...]
killed part-way with 2000 copies stored, of 9992
the run after the kill: map[already imported:2000 lines read:10000 pages stored:7992 ...]
telemetry exported: 64 spans, 9 log records, 0 metric points; API and database log 7932 bytes
--- PASS: TestA76FileChecks (50.40s)
API log lines checked: 18, page texts or email addresses found: 0
```

The test compares a hash of every imported message, in every column but its ID, across the first import, the second run, a corrected export, the file in time order, and the killed-and-rerun case. The kill is `docker compose kill -s SIGKILL api`, which takes the import with it. It also checks that nothing redacted is in a stored page or `import_receptions`, in the API's or database's log, in the command's output or in the exported telemetry, that the integration got only the live pages, that an open list and a watch's catch-up heard of no imported page, that the client's last-seen time didn't move, and that neither `client list` nor the metrics name `import`.

### Mutations

In `scripts/mutation-checks.sh`. The brief's four, each at API level and through the command, and three more for traps found on the way. Run alone on 4c6602a (the script's own `mutate`), abridged, ending `exit 0`:

```
=== mutation: import: imported pages queue integration deliveries
    m16_test.go:176: 4 deliveries queued, want the live page's 1
=== mutation: import: redaction is skipped
    m16_test.go:304: a redacted detail was stored: ...
=== mutation: import: source_id is ignored
    m16_test.go:484: a known source_id with new content: stored 1, already 0: want 0, 1
=== mutation: import: a refused line's text is logged
    m16_test.go:134: the report holds a refused line's text:
=== mutation: import: a refused line's reason is the decoder's error, which quotes the line
    m16_test.go:129: line 7: reported "line 7: parsing time ...
=== mutation: import: the file's order decides (no sort)
    m16_test.go:400: shuffled 0: a different result
=== mutation: import: a watch's catch-up sends imported pages
    m16_test.go:199: the catch-up after the import: {"count":3}
=== mutation: import, through the command: imported pages queue integration deliveries
    import_test.go:405: 9973 deliveries queued, want the live page's 1
=== mutation: import, through the command: redaction is skipped
    import_test.go:395: 1981 stored pages hold "0412 345 678"
=== mutation: import, through the command: source_id is ignored
    import_test.go:472: a corrected export: "pages stored" is 100 (reported: true), want 0
=== mutation: import, through the command: a refused line's text is logged
    import_test.go:389: the import's report holds "REFUSEDLINETEXT"
=== unmutated
ok  	github.com/RIKIKU/PagerArchive/internal/acceptance	23.070s
--- PASS: TestA76FileChecks (48.08s)
API log lines checked: 18, page texts or email addresses found: 0
```

### Tests

- `internal/stacktest/import_test.go`, `TestA76FileChecks`, A76's file checks through the command, in CI's job "Importing messages (A76)".
- `scripts/a76.sh`, A76's speed and A75's searches after it, in the same job.
- `internal/acceptance/m16_test.go`, in the main test job:
  - `TestM16RefusedLines`: 20 ways a line goes wrong, each by number and reason, none with its text.
  - `TestM16Quiet`: deliveries, an open list, a watch's catch-up, last-seen times, metrics.
  - `TestM16ReceivedByImport`: `import` to admins and users, and a client's copy of the same page.
  - `TestM16SamePathAsALivePage`: the same text imported and posted gives the same row in every column but the radio's.
  - `TestM16OrderOfTheFile`: shuffled three ways, the same result, the same order for same-time pages; R19 from the first copy.
  - `TestM16ManyPagesAtOneTime`: more pages with one time than a batch holds.
  - `TestM16DeduplicatesAgainstStoredPages`, `TestM16SafeToRepeat`, `TestM16StoppedPartWay`, `TestM16DryRun`, `TestM16Retention`, `TestM16SourceIDThatARuleMatches`, `TestM16NightlyRedactionReachesSourceIDs`, `TestM16FileFormat`, `TestM16EmptyFile`, `TestM16OneImportAtATime`.

## Questions for the owner

1. **The first search after a cold start can take far over a second, on imported data.** R33b has no exception for it, and I haven't changed it, because the brief rules out changes to the search. On the full run's database, with the database restarted and the OS's caches dropped before each search, three runs each, in seconds:

   | Search | Cold, the imported 4,563,579 | Cold, A75's seeded 4,562,500 |
   |---|---|---|
   | `ZZNOTHING` | 0.266, 0.002, 0.002 | 0.142, 0.002, 0.002 |
   | `hoses grass pump` | **4.755**, 0.117, 0.118 | 0.514, 0.447, 0.377 |
   | `"Smoke showing" or:"grass fire"` | **31.774**, 0.029, 0.007 | 0.055, 0.004, 0.004 |
   | `tanker9` | 0.082, 0.002, 0.002 | 0.133, 0.007, 0.007 |
   | `alias:"Station 1" 12345` | 0.168, 0.006, 0.006 | 0.060, 0.008, 0.007 |
   | `alias:"Station 1" STN2` | **2.027**, 0.429, 0.431 | **1.104**, 0.430, 0.460 |
   | `alias:sta STN2` | 0.238, 0.005, 0.005 | 0.058, 0.004, 0.005 |
   | `STN2`, "Station 1" picked | **1.111**, 0.438, 0.440 | **1.003**, 0.426, 0.454 |

   The 17.4 s in the first full run looks like the same thing. ADR 0017's first try has 250 ms. When a cold disk makes it miss that, a search that's common and would have taken 7 ms goes to the second try, which reads a third of the table's pages from that same cold disk. The database's log shows the first try's timeout just before the slow run. So after a restart or a reboot, the first person to run a common search can wait half a minute, and the next person doesn't. A busy disk may do it too: that's the likeliest reason for the first full run's 17.4 s. So the second full run's clean result doesn't show it's gone, and A76's check of A75's searches could fail on a slow disk for this reason. It hasn't failed on CI's runners so far.

   Would you like a fix as its own milestone? The likely one is small: when the first try times out, try it once more for a little longer, now that it has warmed the cache, before going to the second try. Otherwise this is a known limit until after going live. Either way, one search after the real import will warm what the others need.
2. **Text over 4,096 bytes, and pages with no text, are refused**, with the API's limits. If the older archive holds longer pages, they're named in the report and not stored, which sits uneasily with P2. Raising the limit for imports is one line, and the dry run will say whether it matters.
3. **A source ID a redaction rule matches isn't stored** (decision 6). Its page is, and repeats are still caught. Would you rather be told line by line?

## Consequences

- `pagerarchive import-messages` stores pages from an older archive, quietly, at about 850 a second on a spinning disk, while live pages keep arriving on time. 3.1 million pages take about an hour. Searches can be slow while it runs (R83f).
- Live pages are placed before they're stored, in one write. The live path has the same steps in the same order.
- A live connection's catch-up and its starting point read `receptions`, so they mean what clients sent.
- A message's receive times can include `import`, with an empty client ID in the API's queries. Anything new that reads them has to allow for that.
- CI has one more job, A76's, which also seeds 4,562,500 pages.
