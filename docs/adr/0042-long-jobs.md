# ADR 0042: Long jobs are observable, restartable, resumable, pauseable and stoppable

## Status

Proposed (2026-11-18). The owner: "for any process, data migrations, reparsing, etc that can take more than 30 minutes, the process should be observable (in Grafana stack), restartable, resumable pauseable and stoppable." The owner approved the five definitions in SPEC.md R91. SPEC.md R91-R91b (section 7.29), with pointers from R23a, R23b, R83f and R84a, and a line in CLAUDE.md. Documentation only: no code changes here, and no `feature` label.

## Context

On 2026-11-17 and 18 a profile change on the production archive re-parsed about 3.2 million pages (R23a) at about 1,400 a minute, for about a day and a half. Until ADR 0041 (R23b) nothing showed how far it had got: the only log line came at the end. A restart part-way, such as a release, would have lost the run.

That isn't the only job of its kind. The archive grows by about 1,250 pages a day, and R33b and A75 size it at 10 years of that: 4,562,500 pages. At that size several of the app's background jobs and one of its commands take well over half an hour, and each was built with its own idea of progress, restarts and stopping.

## Decisions

### 1. One rule, R91, with the owner's 30 minutes and five properties

A **long job** is a process that could take more than 30 minutes on a production-sized archive, which SPEC.md defines as R33b's (4,562,500 pages). It must be:

- **Observable**: the example monitoring stack (R76) shows whether it's running, done of total, rate and estimated finish; it logs a start line (why, how much), progress lines at a steady interval and a finish line with its duration. Counts only (P1-P3).
- **Restartable**: an admin can run it again from the beginning on demand.
- **Resumable**: after a crash, an app restart or an upgrade, it carries on from where it got to, with progress kept in the database.
- **Pauseable**: an admin can pause it and carry on later without losing progress.
- **Stoppable**: an admin can cancel it, and each unit of work is done or not done, never half done.

R23b (ADR 0041) is the pattern for Observable: gauges for running, total and done, a counter for the rate, and started, progress and finished log lines.

### 2. Where it sits: a requirement, not a principle

The rule is an R item in its own section (7.29), dated like the other cross-cutting operational sections (telemetry 7.16, installing and upgrading 7.22, maintenance 7.23). It isn't a principle because P1-P3 are about what data is kept and shown, and they override everything else. This rule is about how work runs, and it must give way to them: its log lines and metrics are counts only, and a resumed or stopped job must leave no redacted detail behind (P1).

### 3. Migrations stay quick (R91a)

Migrations run when the API starts (R84a), and the API answers only once they've run, so automatic upgrades (R84f) wait on them, and a long one can't be watched, paused or resumed. So a migration makes only changes that are quick at that size: renames, and adding a column or an index that is quick to add. Anything that rewrites or backfills many rows is a background job under R91. That is how the derived localities (ADR 0034) and areas (ADR 0035) already work: their migrations make the tables, and the API fills them in the background.

### 4. Existing jobs are brought up to it when they're next worked on

None of the jobs below meets all of R91 today. The rule doesn't make that a bug in each one at once; R91b lists them with where they stand, and each meets the rule when it's next changed. New work is built to it from the start.

## The audit (2026-11-18)

From reading the code on main at `5634562`, and the speed checks the earlier ADRs recorded. Times are at 4,562,500 pages; where a check ran on another size, the time is scaled by the number of pages. "Part" means some of the property, as the notes say.

| Process | Est. time at that size | Observable | Restartable | Resumable | Pauseable | Stoppable |
|---|---|---|---|---|---|---|
| Re-parse of every page (R23a, `internal/reparse`), and its large-event rebuild (R87f) | About 54 hours at the owner's 1,400 a minute; the rebuild after isn't measured at this size | Yes (R23b, ADR 0041) | No | No: a restart drops the rest (1) | No | No (2) |
| Import (R83, `internal/importer`) | About 1.5 hours: 852 pages a second on the full A75 file (ADR 0018), then a vacuum | Part: progress on the command's output, not in Grafana | Yes: run the command again | Part: running the file again finishes it, by hand (R83c) | No | Part: only `docker compose restart api` stops it; whole batches |
| Retroactive redaction (P1, `internal/nightly`): every page read, each changed page re-parsed, then `VACUUM` | Not measured; the pass reads every page, and each changed page is re-parsed at the re-parse's rate (100,000 changed pages: about 70 min) | No: the nightly run's finish line and spans | No | No: starts again at the first page | No | No: half-done pages are possible (3) |
| Report counts' queue after an import or a full re-parse (R38a, `internal/reports`) | Every job (1,140,625): about 80 min at full speed, nearly 3 hours at the worker's half a connection (516,667 took 37 min at full speed, ADR 0034) | Part: the reports say they're incomplete; nothing in Grafana | No | Yes: the queue is in the database | No | No (each batch is whole) |
| Derived localities backfill (R24, `internal/geocode/backfill.go`), and its recount | About 15 min on the test box (10 min 31 s for 3.1 million, ADR 0034); 22 to 45 min by ADR 0034's estimate for the production host | Part: start, progress (percent) every 30 s and finish lines; no metrics, rate or estimated finish | No | Yes: its place is in `settings` | No | No (each batch is whole) |
| Areas backfill, per layer (R88a, `internal/areas/backfill.go`) | About 35 min on the test box (23 min 35 s for 3.1 million, ADR 0035); 45 to 90 min by ADR 0035's estimate for the production host | Part: as the localities; Administration says it's still running | No: only a changed file starts it | Yes: its place is in `settings` | No | No (each batch is whole) |
| Retention after the period is shortened (R45, R46) | Not measured; it can delete millions of pages, 1,000 at a time | No: the nightly run's finish line | No | Yes: the next night carries on | No | No (each batch is whole) |

Under 30 minutes at that size, so not long jobs: the migrations (the largest, 00013's search index built `CONCURRENTLY`, took 132 s on a spinning disk, ADR 0017; none `UPDATE`s many rows: 00022, 00024 and 00028 change only templates, aliases and integrations); the full report count (`Rebuild`: 461 s, ADR 0031, one transaction, so a restart starts it again); and the nightly backup and its check (31 s and 225 s, ADR 0020, on a fast disk). On the production host's slower disk the report count and the backup check could come near the line, so they're worth timing there.

**Notes:**

1. `ClaimReparseRequests` deletes the requests as it takes them, and on a failure `requeue` puts them back with the run's own context. A restart cancels that context, so they aren't put back, and a killed process puts nothing back. The start-up check (`CheckProfiles`) queues a full re-parse only when the profiles' hash differs from the stored one, and the hash was stored when the run was queued. So after a restart the rest of the run isn't done until another change queues one, and the pages it hadn't reached keep their old fields. ADR 0041 §5 says a restart "starts a run again from the beginning"; from the code it doesn't, and R23b now says so. This is from reading the code; no test was run.
2. Each page's parsed fields (`UpdateParsed`) and its location (`ingest.Locate`) are written by separate statements, not one transaction, so a stop between them leaves a page with new fields and its old location. The large events to rebuild are gathered in memory and lost with the run.
3. `redactAll` writes each changed page's new text, then re-parses that batch's pages. If it stops between the two, the next night's run finds the text already redacted, so it doesn't pick those pages again, and their parsed fields (description, address, extra fields) can still hold what the new rule removes. That's a gap in P1, not only in R91: it should be fixed first.

## Consequences

- An agent designing a job that could pass 30 minutes builds it to R91 from the start (CLAUDE.md says so), with R23b as the pattern for Observable.
- No long data rewrite goes in a migration (R91a).
- The jobs above stay as they are until they're next worked on; R91b says where each stands. Note 3 is the exception worth raising with the owner now, as it touches P1.
- Restartable, Pauseable and Stoppable need an admin control, in Administration and the PowerShell module, as backups have (R86a). How that's shared between jobs (one job table with state and progress, say) is for the first job built to the rule, in its own ADR.
