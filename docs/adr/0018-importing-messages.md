# ADR 0018: Importing messages (M16)

## Status

Proposed (2026-11-02). The owner's brief for M16: SPEC.md section 7.21 (R83-R83f) and A76 (owner, 2026-11-02), on the branch `m16` from main at a2f7e70. Released as 0.14.0. The UI, the search, the client and the PowerShell module don't change. It must fit M17: nothing from the repo at run time, no extra mounts, and a migration that runs by itself and only goes forward (R84, R84a).

## Context

The owner is moving about 3.1 million pages from an older archive, and the first production instance needs that (section 12). Converting the old data into this format happens privately, outside this repo. The brief asks for a server-only command that treats each page as a live page and doesn't tell anyone, with a report, at 500 pages a second while the app keeps working.

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

### 3. Lines out of time order are sorted

R19's rule runs from a page's first copy, so dedup needs the copies in time order. Refusing lines out of order is simpler, but an archive with several receivers rarely has its IDs in time order, and refused lines would make the result depend on the order all the same.

So the command works in two stages:

1. **Read and check.** Every line is checked, then redacted, and the result goes by `COPY` into a temporary table. The unredacted text never leaves the command.
2. **Store.** One statement writes a second temporary table in order: time, then capcode, text and source ID, so even pages with one time are stored in one order whatever the file's. Batches of 500 are read from it by number, and each is stored in one transaction.

Why PostgreSQL does the sort:

- **In the command's memory:** 4.6 million pages are about 700 MB, against the API's 256 MB budget. Holding only times and file offsets doesn't work for standard input, which can't be read twice.
- **In a temporary file in the container:** it would be page text in a container layer, and the image is meant to write nothing.
- **In PostgreSQL:** temporary tables take no write-ahead log, go with the connection however the command ends, and PostgreSQL's sort spills to disk within `work_mem`. They hold only redacted text (P1).

The second table is one statement and each batch one short query, so no snapshot stays open for hours and vacuum can do its work throughout. It costs disk while it runs: about three times the file. On 4.5 million pages, the first stage takes [READ] and the sort [SORT].

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

**P1 and the source ID.** It's an ID, but it comes from outside, and the rules might match it (a long number can look like a phone number). A source ID a rule matches isn't stored: the page is, known by its key and time, and the report counts it. When the rules change, the nightly run drops any stored ID they now match, in the same step as its retroactive redaction. Never in a report: a refused line is named by its number.

### 7. Retention (R83d)

The cut-off is the nightly job's own (`Job.Cutoff`, a calendar date in the deployment's time zone), taken once when the command starts. A page before it is counted and goes no further. A nightly run during a long import deletes what it should, by its own later date.

### 8. Report and P1 (R83e)

A refused line is given as `line N: reason`, and the reason is one of a list of fixed texts. Decoder errors are never used: Go's JSON decoder quotes what it choked on (`invalid character 'S' looking for beginning of value`), and `time.Parse`'s error quotes the whole value. Database errors go through `store.SafeError`, as everywhere, so a failed statement reports its `SQLSTATE`.

The command exports no telemetry. It isn't a service, and what it did is in its report.

**Dry run.** The same code as a real run, but read only: `walk` is shared, and only what it does with a page differs. Pages earlier in the file aren't in the database, so a dry run keeps the first copies of the last 10 seconds. The pages come in time order, so that's all R19 can reach. A repeated source ID is taken out in one statement. `TestM16DryRun` compares a dry run's report with the real run's, field by field.

Refused lines don't fail the command, which exits 0: the report is what says so. It exits 1 only if it didn't finish.

### 9. Speed: what was measured

Measured first, on one database of 4,562,500 pages on cld01's spinning disk, nothing else running, an import of 100,000 pages per row, in this order (the first import's code: see below for what it was compared with):

| Settings | Pages a second |
|---|---|
| Baseline: `fastupdate` on, 4 MB pending list (the defaults) | 862 |
| The same again | 902 |
| `fastupdate` off | 743 |
| 32 MB pending list | 887 |
| The defaults, with generic plans in the import's transactions | 1,118 |
| No search index at all | 930 |

So **the trigram index isn't the bottleneck**: the whole index costs about 5%, and its pending list does its job at its default. Nothing about it changes, and the question of dropping it during an import doesn't arise. No batch insert either: `Service.Save` stays one statement at a time, the same code as a live page.

Two things made the difference, and neither was the index:

- **A page was written twice.** `Save` inserted the page, then placed it with an `UPDATE`, which writes a new version to every index, trigrams included, because `location` is indexed. It now finds the location first and sets it in the `INSERT`, for live pages too. That took the import from 686 to 830 pages a second on the same run, with searches and live pages going (see "Evidence").
- **Planning every statement.** `Store.Open` sets `plan_cache_mode = force_custom_plan` for searches (ADR 0017 §4). The import's statements are lookups by key, so its transactions set it back with `SET LOCAL`: 1,118 against 880. The API's connections and searches aren't touched.

Batches are 500 pages, under a second. A live copy of a page in the batch waits for its commit, at `ingest.Save`'s lock, so it stays short, and that's about 2 ms of lock table per batch's page. Only a live page with the same capcode and text can wait at all.

The command ends with `ANALYZE messages`. An import can change what searches plan by (ADR 0017 §2) faster than autovacuum looks.

## What was weighed

- **Refusing lines out of order:** see decision 3.
- **A row in `clients` for `import`, or dropping `receptions`'s foreign key:** see decision 5.
- **A hash of `source_id`**, which can't hold a detail and needs no sweep. It would spread writes across the index at random, where an old archive's IDs arrive nearly in order. And the owner may want to read an ID when tracing a page back.
- **Refusing a line whose source ID a rule matches.** It'd lose a page (P2) over an ID the page doesn't need. Dropping the ID loses nothing: the page is still known by its key and time.
- **Filtering the catch-up with `NOT EXISTS (import_receptions)`:** it would walk every imported page, on every catch-up that spans an import.
- **Retrying a batch PostgreSQL ends to break a deadlock.** It would be untested code for something I couldn't make happen: the import never waits for a live page's lock while holding one it wants. Running the file again finishes the job.
- **Throttling, or a `--rate` option.** R83f's searches and live pages hold at full speed, so nothing asks for it.

## Evidence

On cld01 (4 vCPU, 11 GB, the database on a spinning disk), through `docker compose exec` on the compose file's API and PostGIS, with its settings (`shared_buffers=128MB`, `work_mem=4MB`). Live pages every 5 s, with an integration selecting Station 1 and an open list. A search as user B every second, A75's six in turn. Memory by docker stats, A15's measure.

### Before and after, 200,000 pages on 4,562,500 (`scripts/a76.sh`)

[COMPARE]

### The full import: 4,562,500 pages on the spinning disk

[FULL]

### A76's file checks

[FILE]

### Mutations

[MUTATIONS]

### Tests

- `internal/stacktest/import_test.go`, `TestA76FileChecks`, A76's file checks through the command, in CI's job "Importing messages (A76)".
- `internal/acceptance/m16_test.go`, in the main test job:
  - `TestM16RefusedLines`: 20 ways a line goes wrong, each by number and reason, none with its text.
  - `TestM16Quiet`: deliveries, an open list, a watch's catch-up, last-seen times, metrics.
  - `TestM16ReceivedByImport`: `import` to admins and users, and a client's copy of the same page.
  - `TestM16SamePathAsALivePage`: the same text imported and posted gives the same row in every column but the radio's.
  - `TestM16OrderOfTheFile`: shuffled three ways, the same result, the same order for same-time pages; R19 from the first copy.
  - `TestM16DeduplicatesAgainstStoredPages`, `TestM16SafeToRepeat`, `TestM16StoppedPartWay`, `TestM16DryRun`, `TestM16Retention`, `TestM16SourceIDThatARuleMatches`, `TestM16NightlyRedactionReachesSourceIDs`, `TestM16FileFormat`, `TestM16EmptyFile`, `TestM16OneImportAtATime`.

## Questions for the owner

None of these holds up the work. Each is a choice I made that you may want otherwise.

1. **Text over 4,096 bytes, and pages with no text, are refused**, with the API's limits. If the older archive holds longer pages, they're named in the report and not stored, which sits uneasily with P2. Raising the limit for imports is one line, and the dry run will say whether it matters.
2. **A source ID a redaction rule matches isn't stored** (decision 6). Its page is, and repeats are still caught. Would you rather be told line by line?
3. **The import stops only with `docker compose restart api`.** Ctrl+C ends `docker compose exec`, but the import goes on inside the container: Docker doesn't pass the signal on. [VERIFY] It's safe, because a second run says one is running, and `docs/import.md` says so. Stopping it any other way would need something more than a command, so I've left it.

## Consequences

- `pagerarchive import-messages` stores pages from an older archive, quietly, at well over 500 a second, while the app keeps working. 3.1 million pages take about an hour.
- Live pages are placed before they're stored, in one write. The live path has the same steps in the same order.
- A live connection's catch-up and its starting point read `receptions`, so they mean what clients sent.
- A message's receive times can include `import`, with an empty client ID in the API's queries. Anything new that reads them has to allow for that.
- CI has one more job, A76's, which also seeds 4,562,500 pages.
