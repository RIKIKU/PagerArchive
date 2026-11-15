# ADR 0034: A page's derived locality, kept beside it

## Status

Proposed (2026-11-15). The owner's report: on the production network, Busiest localities (R38c) shows only "No locality". Every stored page's `locality` is null and every counted job's locality is empty, because the network's profile has no `locality` field: its locality is at the end of the address, or of the description. The geocoder found that locality (R24, R25) but didn't keep it. SPEC.md R24, R38a and R38c change, with A102 and A103. On the branch `locality` from main; migration 00025; the pull request is labelled `bug`.

## Context

`ingest.FieldsOf` fills `messages.locality` from the profile's `locality` field only. Without one, the geocoder matches the end of the address against the dataset's localities (`streets.Index.ParseAddress`), or, for a grid reference with no address split, the end of the description (`LocalityAtEnd`, ADR 0005), and uses the locality for the address lookup or to complete the grid reference. It never returned it. Reports (ADR 0031) count a job's locality from `messages.locality`, so on such a network every job is "No locality".

## Decisions

### 1. What the derived locality is

`geocode.Result.Locality`: the locality `Locate` settles on, in the same order as the grid reference uses (`Geocoder.locality`): the parsed address's locality, else the `locality` field (normalised), else the end of the description when no address was split from it. It's kept only when the address dataset has that locality (`streets.Index.HasLocality`), so it's always a name the map can place (R38c's heat layer). `Geocoder.Locality` works it out from the street index alone, with no database lookups, for the backfill (section 4).

The order is the grid reference's on purpose: the derived locality is the one the page was placed by. An address whose street isn't in the dataset but whose last words are a dataset locality gets none (the address lookup fails, and there's no description fallback when an address was split). That is the grid reference's behaviour too; changing both is a separate question.

### 2. Where it's kept: a table beside the pages, not `messages.locality`

`message_localities (message_id PRIMARY KEY REFERENCES messages ON DELETE CASCADE, locality)`, one row per page whose profile gives no `locality` field and whose derived locality the dataset has. A profile's own field always wins: such a page has no row.

Why not `messages.locality` itself: it holds what the page's text said (P3). Search, the message view, the API's fields, integrations' payloads (R49), exports, the job view and the browser's filters all read it as a parsed field; a derived value there would pretend to be text the page held, and a re-parse would have to tell the two apart. Nothing but reports needs it.

Why not a column on `messages`: filling it for 3.1 million stored pages is an `UPDATE` of every row. `messages` has 15 indexes, three of them GIN indexes on the text; a row that doesn't fit on its page again (most of an archive written in order) is a new tuple in every index, which is the cost ingest avoids by placing a page before it's stored. An insert into a two-column table costs one small index entry, and the backfill never rewrites a page.

Reports read it with a left join: `COALESCE(m.locality, ml.locality)` in `basePages` (`internal/reports/build.go`), for the rebuild and the catch-up alike. Nothing else reads it.

### 3. Written with the page's location

- Ingest (`ingest.Service.Save`): after the page is inserted, in the same transaction, when its fields have no locality and the geocoder found one.
- Re-parse and retroactive redaction (`ingest.Locate`): written, changed or deleted with the page's location, so a new dataset (which re-parses every page, section 14) or a profile gaining a `locality` field changes it. Unchanged rows aren't written (`ON CONFLICT … WHERE … IS DISTINCT FROM`).
- Retention (R45) deletes it with its page (`ON DELETE CASCADE`).

Triggers on `message_localities` (statement-level, like ADR 0031 §3's on `messages`) queue the page's job in `report_dirty` when its derived locality is added, changed or removed and the page has no locality field. A row deleted with its page needs nothing: the page's own delete queued its job.

### 4. Pages stored before: a background backfill, once

`geocode.Geocoder.BackfillLocalities`, started by the API after the dataset is loaded, reads the pages with no locality field in id order, 2,000 at a time, works out each one's derived locality from the street index, and inserts the rows and how far it got (`settings` row `message_localities_backfill`: the last id, then `recount`, then `done`) in one transaction per batch. So:

- **resumable**: a restart carries on from the last batch committed;
- **idempotent**: `ON CONFLICT DO NOTHING`, and only for a page that still has no locality field, so a row ingest or a re-parse wrote meanwhile is kept; run again from the start, it changes nothing (A103);
- **gentle**: between batches it waits as long as the batch took, so it takes at most half of one connection, as the reports' catch-up does;
- **logged**: every 30 seconds, pages read, localities found, percent and seconds; counts only (P1).

The migration only creates the table and triggers, so the upgrade itself takes no longer (A77).

It needs no rights beyond the API's own role (R84i, ADR 0033), which owns the app's tables: it reads `messages`, inserts into `message_localities` and writes its `settings` row, never updates a page, and `SET LOCAL pagerarchive.backfill` is a custom setting any role may set. The rebuild after it is the reports' own (ADR 0031 §4).

Re-parse (R23a) was weighed instead: queueing a full re-parse on the upgrade would fill the table with nothing new, but it re-parses and re-places every page (two updates of `messages` each, the cost section 2 avoids), isn't resumable (a restart starts again) and logs only at the end.

### 5. The report counts: built again once, after it

The backfill's transactions set `pagerarchive.backfill`, and migration 00025's triggers queue nothing for them. When every page is done the state becomes `recount`, the reports are built again (`reports.Service.Rebuild`, ADR 0031 §4: one set-based count, readers see the old counts until it commits), and then the state is `done`; a restart between the two only rebuilds. Pages stored, re-parsed or deleted meanwhile queue their jobs as always, from their own transactions.

The first version let the backfill's rows queue their jobs and the reports' worker count them again (ADR 0031 §3). On the made-up archive below the backfill queued every job, about 517,000, and a queue of 516,667 jobs took 37 min 9 s to count at full speed, so over an hour at the worker's half a connection, while a rebuild of the same jobs took 1 min 43 s. Counting job by job is right for a trickle of changes, not for nearly every job at once. `builtVersion` doesn't change: a new install, or one whose profile has a `locality` field, rebuilds once too, which costs about two minutes at this size and nothing on a small archive.

## Tests

- `TestA102DerivedLocalities`: a made-up agency profile with no `locality` field; pages whose address ends in a dataset locality (with a house number, and a street only), one with no address whose description ends in one, and one whose locality the dataset doesn't have; the localities report, its map points, the message view's `locality` still null, a page with a field keeping no row, a re-parse, retention.
- `TestA103LocalitiesBackfilled`: a database at v1.11.1's migration 00024 with such pages and the report counts built, upgraded: the backfill, its state, nothing queued, the counts after its rebuild, a restart before the rebuild, running again, the log holding no page text.
- `TestLocalityBackfillSpeed` (`PAGERARCHIVE_LOCALITY_SPEED=1`, by hand): the Evidence below.

## Evidence

On cld01 (4 vCPU, 11 GB, the database on a spinning disk), in a throwaway PostGIS 18-3.6 container with the test settings, `TestLocalityBackfillSpeed`: 3,100,000 made-up pages (the size of the production archive the owner reported), 6 to a job (516,667 jobs), none with a locality field, one in ten naming a locality the dataset doesn't have, against a street index of 3,000 localities with 50 streets each:

| Step | Time |
| --- | --- |
| Seeding the pages | 8 min 0 s |
| The report counts built as before the upgrade (`Rebuild`) | 1 min 7 s |
| The backfill: 3,100,000 pages read, 2,790,000 localities kept, half of it waiting between batches | 10 min 31 s |
| The report counts built again after it | 1 min 43 s |

Afterwards all 516,667 jobs had a locality, and the backfill had queued no jobs (the 516,667 in the queue were the seeding's own; the test now clears them first). On the production host, a small box, the estimate is 15 to 30 minutes in the background after the upgrade: the backfill is mostly reading pages in id order and the rebuild is one statement, both bounded by the disk. Until the rebuild commits, reports count the localities they had before (all "No locality" there).

The first version's run on the same data (with the queue left to count job by job) is the 37 min 9 s in section 5.
