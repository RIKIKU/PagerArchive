# ADR 0031: M24, more reports, and how their counts stay cheap

## Status

Proposed (2026-11-15). The owner's brief for M23 ("more reports"), the decided list in the backlog (owner, 2026-11-14): eight reports and what they share, nothing from its "Out" (network health belongs in Grafana). SPEC.md's milestone M23 was already Automatic upgrades (ADR 0030) when this was written, so this is **M24**, with R38a-R38i and A91-A99. On the branch `m23` from main at fc4b9ff. Migration 00023 adds the report tables; `VERSION` and the compose image stay as they are (ADR 0024), and the pull request is labelled `feature`.

## Context

R38a-R38i add eight reports beside R38's capcode report: busiest capcodes (with a trend against the previous period), busiest localities (with a heat layer on the map), when it's busy (hour by weekday), event type mix (and month by month), job size, long-running jobs, appliance activity and large events over time. They count **jobs**, not pages (owner, 2026-11-14): a job's repeats and copies count once. They have the same filters (agency, event type, priority, the range calendar from M21), a CSV and an image of the chart, desktop and phone layouts, tokens only, axe-clean, and they answer within R33b's budget on A75's data set, "using indexes/aggregates rather than scanning pages per request".

A75's data is 4,562,500 pages over 10 years. A job count over a range can't be read from the pages within a second on a spinning disk: a year is 456,000 pages, the whole archive 1.6 GB of table, and counting *distinct* jobs needs every one of them. ADR 0026 measured the same for the scrubber's page counts and chose not to keep a table of counts then (its "Question for the owner"); reports count jobs, which no index on the pages can give, so here the counts are kept.

## Decisions

### 1. What a job is, and its values (R38a)

A job is a job number's pages (R23), or a page with no job number on its own: its key is `j` and the job number, or `m` and the message's id. Heartbeats and pages to ignored aliases are in no job (as R87 leaves them out of large events); imported pages are (they're the archive's history). A job's values are fixed by its pages, so each job is counted **once**, on **one** day, which is what makes the counts add up across days:

- its time: its first page's sent time, and from it its day and hour in the deployment's time zone (R35a);
- its event type and locality: the first its pages give, in sent order (ties by id);
- its priority: the highest of its pages' (Emergency, Non-emergency, Admin);
- its agency: the template of its first page to an aliased capcode (0: none); a template's agency name is looked up when a report is asked for, so renaming an agency changes nothing stored;
- its pages, capcodes and appliances: its stored messages, the distinct capcodes they went to, the distinct appliances they name.

Busiest capcodes (R38b) and When it's busy for one alias (R38d) count jobs per alias or capcode a job paged: there the agency is the alias's own.

### 2. The tables (`00023_reports.sql`)

- `report_jobs`: one row per job, with the values above, by `day` (an index that also holds what R38g sorts and filters by) and by span (`(last_at - first_at) DESC`, partial, for R38g over long ranges).
- `report_job_whos`: one row per job and alias (`a` and its id) or capcode with no alias (`c` and the capcode), with the job's day, hour, event type and priority, by `(who, day)` (R38d's alias, covering) and by `day`.
- `report_days`: **jobs per day**, by kind and key: `t` every job (the totals shares are of), `h` the hour a job started, `l` its locality, `p`, `c` and `n` its numbers of pages, capcodes and appliances, `a` each appliance it names, `w` each alias or capcode it paged; each row also by template, event type and priority, so every filter is a column. Its primary key is the whole row but the count, with the count included, so a report reads it in an index-only scan, one range of days of one kind.

Most reports read `report_days` only: a sum over the range, grouped by key (busiest: `w`; localities: `l`; when it's busy: `h` by weekday and key; event types: `h` by month and event type; job size: `p`, `c` and `n` as histograms, whose percentiles Go works out by nearest rank; appliances: `a`). The total for shares is the `t` rows' sum. Long-running jobs read `report_jobs`: for a range of up to two years, the range's jobs from the day index (index-only) and sorted, and for longer ranges, the longest jobs overall from the span index until enough are in the range (most are). When it's busy for an alias reads `report_job_whos` by `(who, day)`, counting distinct jobs so aliases sharing a name count a job once. Large events over time reads `large_events` with `report_event_jobs`, each event's number of job numbers as R87c counts them, kept by triggers on `large_event_messages` (and on a page's job number changing) so a report over years of events doesn't read their pages.

### 3. Kept in step by a queue, never waited on

Triggers on `messages` (statement-level, with transition tables: insert, delete, and an update that changes a column a report reads) and on `aliases` (marked ignored or not, moved to another template) add the jobs they touch, old and new keys, to `report_dirty`. The queue only grows by plain inserts with an identity key, never `ON CONFLICT`: so storing a page never waits on the writer counting jobs, which deletes queue rows as it takes them. (A unique key on the job would make an insert of the same job wait for a batch's delete to commit, or for a whole rebuild.)

`reports.Service.CatchUp` takes up to 500 queued rows at a time, in one transaction under a transaction-level advisory lock (`pg_try_advisory_xact_lock`: a second writer skips instead of waiting): it deletes those jobs' rows, counts them again from their pages (`messages_job_idx`, or the message's id), and counts every day they were or now are on again from `report_jobs` and `report_job_whos`. A change is in the counts once a batch has taken it:

- the API's worker (`Run`) looks every 2 seconds, and between full batches waits as long as the batch took, so a large backlog (an import, a re-parse of every page) uses at most half of one connection;
- every report counts what's queued first, for up to 300 ms, so a report asked for just after a page arrives includes it (A99), and says how many changed jobs, if any, it couldn't count yet.

A re-parse (R23a), retroactive redaction (P1, whose re-parse changes the fields), retention (R45, a delete) and an import (R83) all reach the counts this way, with nothing of their own to call.

### 4. Built once, in the background

The first start with migration 00023 counts every job at once (`Rebuild`, one set-based statement over the pages, then one over the jobs, in one repeatable-read transaction), in the background: the API serves meanwhile, and reports say their numbers are incomplete until it's done (the settings row `reports_built` names the version and time zone they were built for). A change of `TZ` builds them again, as days and hours are the deployment's. The migration itself only creates the tables, so the upgrade (A77) takes no longer. A rebuild leaves the queue alone: what was queued before it is counted again after, which changes nothing.

### 5. P1 and privacy

The tables hold counts, keys and the parsed fields reports group by (event type, locality, appliances, job number): never text, description or address. They follow every change to the pages they're made from in the same way the pages' own fields do (section 3), so retroactive redaction and retention reach them within seconds. No log line holds a value: errors are logged by kind (`store.SafeError`). Responses hold counts, names and codes only (`TestA91`-`TestA98` check every report's JSON against the fixture's page texts, and the browser tests every CSV).

### 6. The browser

- `ReportPage.svelte`: the filters in a GET form whose values go into the address (so a report is a bookmark, and Back works), the range calendar (`DateRange`, M21), the report run as the page opens (the last 30 days, or 12 months for R38e and R38i), the line saying counts are still being made, and Download CSV, Download chart and Copy chart (where the browser allows images on the clipboard).
- Charts are SVG drawn at their box's width with the tokens (`BarChart`, `ColumnChart`): bars with their values written beside them, the previous period as a thinner bar under each (R38b), the middle half, median and largest as a range (R38f). The table beside the chart (below it under 1,280 px) holds every number; the chart is one image to assistive technology. When it's busy is a table whose cells are shaded in five steps that keep their text readable, and show their count.
- The chart image: the SVG is copied with each element's computed style written on it (so the image is in the look and theme on screen), loaded as a `data:` URL (the app's Content Security Policy allows `data:` images and refuses `blob:` ones), drawn onto a canvas at twice its size and saved as PNG; the heat grid's table is painted cell by cell.
- The heat layer is `MapView`'s new `heat` variant (R35's tiles): a MapLibre heatmap of localities at the middle of their extent in the address dataset, weighted by jobs, its colours from tokens, and a label with each name and number.
- New tokens (both looks): `--chart-*`, `--heat-0` to `--heat-4`, `--map-heat-*`. New icons from Tabler (R77a): `trending-up`, `map-pins`, `clock-hour-4`, `chart-pie`, `ruler-measure`, `hourglass`, `truck`, `chart-line`, and `download`, `copy`, `photo` for saving.

## What was weighed

- **Counting from the pages, with covering indexes.** Distinct jobs per key need every page of the range: a year of A75's pages is 456,000 index entries, 10 years 4.5 million, read cold from a spinning disk. Well over a second.
- **Materialized views refreshed on a timer.** A refresh recounts everything (minutes on 4.5 million pages), so the counts would be minutes stale, and the refresh's load comes every time, not only when pages change.
- **Counts per job number per day only** (no job table). A job spanning midnight would count on both days; assigning each job to its first page's day needs the job's first page, which is what `report_jobs` keeps.
- **Monthly totals as well as daily ones.** On A75's pages the daily rows answer 10 years within the budget (Evidence), so a second level isn't needed yet; it can be added without changing a report.
- **HyperLogLog sketches.** Approximate, and not in the PostGIS image without an extension.
- **A unique key on the queue** (section 3): makes a page wait on a batch.

## Tests

- Go (`internal/acceptance/m24_test.go`): A91-A98 on SPEC.md's report fixture, every report with and without filters, ranks and ties, an empty range, bad filters (400), signed out (401), no page text in any response; A99: the counts follow a late page, an alias marked ignored and its re-parse, and retention; a rebuild counts the same; no report table holds page text; the time zone (`TestReportsTimeZone`: a page on Sunday in UTC is Monday's in Melbourne, and a report counted for another time zone says it's incomplete).
- Speed (A99, in CI's A75 job, after A75's searches and A71's counts): every report for 30 days, a year, 10 years, and 10 years filtered, three times warm, and 10 years straight after a cold start, each within 1 second.
- Browser (`web/tests/e2e.spec.ts`, M24): each report at 1,280 and 375 px in Chromium, Firefox and WebKit: its table's rows, its chart, the breadcrumbs, no sideways scroll, the axe check, its CSV's lines and its PNG; the filters in the address and the alias picker.

## Evidence

On cld01 (4 vCPU, 11 GB, the database on a spinning disk), in a throwaway PostGIS 18-3.6 container with the test settings, `TestA75SearchSpeed` with A75's pages (`PAGERARCHIVE_A75=1`): 4,562,500 pages seeded in 4 min 37 s; every job counted once (`Rebuild`) in **461 s**, giving 1,140,625 jobs, 4,561,422 job-and-alias rows and 1,037,821 day rows, 1,428 MB with their indexes (A75's pages are a job of 4 capcodes every 4 pages, so the job-and-alias table is as long as the pages; a real archive's repeats make it shorter). A75's own searches and A71's counts passed as before. Each report through the API, three runs warm and, for 10 years, the first run straight after a restart with both caches cold (milliseconds):

| Report | 30 days | a year | 10 years, cold | 10 years | 10 years, filtered |
|---|---|---|---|---|---|
| Busiest capcodes | 15 | 45 | 876 | 83 | 95 |
| Busiest localities | 3 | 6 | 532 | 32 | 37 |
| When it's busy | 3 | 7 | 490 | 48 | 43 |
| When it's busy, Station 5 (80,000 pages) | 4 | 14 | 516 | 150 | 75 |
| Event type mix | 3 | 8 | 490 | 51 | 69 |
| Job size | 3 | 6 | 328 | 21 | 13 |
| Long-running jobs | 7 | 59 | 162 | 6 | 5 |
| Appliance activity | 3 | 5 | 361 | 33 | 26 |
| Large events over time (by week, 520 events) | 1 | 1 | 86 | 2 | 3 |

Warm is the slowest of the last two runs; the first run after another report can be slower while it reads its own rows (busiest for a year: 367 ms). "Filtered" is an agency, `HSF1` and Emergency. Before the changes that got here: busiest for 10 years read the `h` rows for its totals (1,085 ms cold; now `t`), long-running jobs for 30 days chose the span index and read most of 10 years' jobs (13.3 s; now the day index below two years), When it's busy for Station 5 read the table for each job key (4.8 s cold; now in the index) and large events counted each event's pages (4.7 s cold; now `report_event_jobs`).

On CI's runners A75's job took 9 minutes before (main, 2026-11-14), so its limit goes from 40 to 55 minutes for the counting, and A76's, whose seed counts the jobs too, from 70 to 85.

## Questions for the owner

1. **The milestone's name.** The brief called this M23, but SPEC.md's M23 is Automatic upgrades (merged). It's M24 here, with R38a-R38i and A91-A99. Rename if you'd rather.
2. **A page with no job number is a job of its own** (R38a). R23 never groups such pages, so each counts once, like a job; Admin pages and pages to agencies without job numbers count that way. The alternative is to leave them out of every report (a network whose agencies give no job numbers would then have empty reports). The priority filter leaves Admin pages out when wanted.
3. **What's left out**: heartbeats, and pages to ignored aliases (as large events leave them out); imported pages count.
4. **A job's agency** is that of its first page to an aliased capcode; Busiest capcodes uses each alias's own agency instead, so it lists only that agency's aliases. A job that pages two agencies counts for the first one's in the other reports.
5. **Shares in Busiest capcodes** are of the jobs the filters hold, so they can add up to more than 100%.
6. **Defaults**: the last 30 days (12 months for Event type mix and Large events over time); top 10 (Busiest), top 20 (Long-running); Event type mix's month table shows the five event types with the most jobs and the rest together.
