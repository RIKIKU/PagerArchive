# ADR 0044: Retroactive redaction: one unit, resumable, pauseable, stoppable and restartable

## Status

Proposed (2026-11-18). The owner: "queue it up, we can get this done today or tomorrow", for retroactive redaction (P1, in the nightly run, R45) brought up to the long-job rule, SPEC.md R91 (ADR 0042), with ADR 0042's note 3 closed first. SPEC.md R45a, with A124; R91b now says retroactive redaction meets the rule. On the branch `redaction-r91` from main; the pull request is labelled `feature`.

**ADR 0042's audit row for retroactive redaction, and its note 3: done.** Its decision is unchanged; this ADR is where the row and the note are closed.

## Context

ADR 0042's audit found retroactive redaction (`internal/nightly`, `redactAll`) observable only through the nightly run's finish line and spans, not restartable, starting again at the first page after a stop, not pauseable, and not stoppable without half-done pages, its note 3. The re-parse was brought up to R91 first (ADR 0043), and its design is the one to reuse: a run kept in the database, written in the same transaction as each unit of work; control functions that lock that row; an API under `/api/v1/admin/…`, a page in Administration and PowerShell commands; a state gauge beside R23b's metrics, a Grafana row and log lines.

## Decisions

### 1. A page's new text and its re-parse are one unit (note 3)

Each batch of 1,000 pages is one transaction: for each page the rules change, the page is locked (`FOR NO KEY UPDATE`, which receptions' foreign keys don't wait on), its new text and dedup hash are written, and it's re-parsed (`reparse.Worker.ReparseInTx`: parsed fields, job, location, areas and derived locality) in the same transaction; then the run's place, counts and the days to rebuild large events around are written, after locking the run's row. A crash, restart, upgrade, pause or stop rolls back only the batch in hand, text and fields together, and the run carries on after its last committed batch. A transaction was chosen over a durable "needs re-parse" mark because it leaves nothing to pick up: there's no state to reach between the two steps.

A page's parsed fields are also written only beside the text they were parsed from, whoever writes them: `UpdateParsed` now has `AND text = $16`, and a re-parse that finds 0 rows reads the page again and parses what it has.

A124 holds a batch with a trigger on one page's text update (an advisory lock), so it stops after that page's new text is written and before its re-parse, then ends the worker there. Run against a copy that writes the text outside the batch's transaction, as before, it fails; with one transaction it finds nothing half done at any point.

### 2. A run in the database, the re-parse's shape

Migration 00031 adds `redaction_runs` (one row at most, kept once finished) and `redaction_run_days`. A run has its state (`running`, `paused`, `stopped`, `finished`), a reason code (`rules_changed`, or `started_again` by an admin), the hash of the rules it applies, its total (stored pages, counted when it starts) and done (pages checked), how many pages it changed, the last page checked, whether the pages are done, where the imported source IDs' walk got to (it drops the IDs the rules match, ADR 0018, in batches the same way) and whether that's done, an epoch, and when it started, changed state and finished. The rules are saved as applied (`settings.redaction_hash`) in the same transaction as the run's finish, after the large events around the changed pages are rebuilt from its days (R87f). Migration 00031 only creates the two tables (R91a).

**Shared, and not.** The state, progress, rate, progress log line and refusal are the same for every long job, so they moved from `internal/reparse` into a new `internal/longjob` package, which both jobs use (the re-parse's names are aliases of it, so nothing that uses them changed). Each job keeps its own run table, as ADR 0043 reasoned: their units and places differ (capcode patterns for one, a page ID and a source ID for the other).

### 3. How it fits the nightly run

The nightly run still does it, in the same place (after retention, before the report counts and the vacuum), now through `redaction.Worker.Nightly`:

- No unfinished run, and the rules in use aren't the ones last applied to every page: it starts one (`rules_changed`), and does it.
- A running run (one cut short, say): it carries it on.
- A **paused** run stays paused: an admin's pause holds, across restarts and nightly runs, until they resume it. The nightly run logs that it left it.
- A **stopped** run is started again from the beginning. Stopping cancels this run, but the pages after where it stopped still hold what the rules remove, and P1 doesn't wait for a deliberate restart. An admin who wants it held pauses it instead.
- An unfinished run under other rules starts again from the beginning with the rules in use, in its state (a stopped one runs).

It returns when the run finishes or an admin pauses or stops it, and the nightly run goes on to the report counts and the vacuum.

Outside the nightly run, a background worker (`redaction.Worker.Run`, woken by `pagerarchive_redaction` and polling each minute) carries on a running run as soon as the API starts, without waiting for a night, and runs one an admin resumes or starts again; when it finishes one, it vacuums `messages` and `receptions`, as the nightly run would. It listens on a connection of its own, outside the pool: the pool has 4 connections on a small host, the re-parse's worker keeps one listening, and a second there left the nightly run and the API's start-up waiting for one. One walk runs at a time (a mutex in the worker). At start-up, `CheckRules` starts an unfinished run under other rules again from the beginning, in its state, as the re-parse's profile check does (ADR 0043 §3); a new run for changed rules still waits for the nightly run, as P1 says.

### 4. An admin's controls

As the re-parse's (ADR 0043 §5): `Pause`, `Resume`, `Stop` and `StartAgain` change the run in one transaction with its row locked, notify the worker, and interrupt its progress, so it stops after the page in hand and commits that batch with its place.

| Action | From | To | Refused |
|---|---|---|---|
| Pause | running | paused | otherwise, `redaction_not_running` |
| Resume | paused | running, from its place | stopped, `redaction_stopped`; running or idle, `redaction_not_paused` |
| Stop | running, paused | stopped; the large events around the pages it changed are rebuilt | stopped or idle, `redaction_not_running` |
| Start again | any state, and before any run | running, from the beginning, with the rules in use; epoch raised | never |

Start again is allowed before any run, unlike the re-parse's (`reparse_none`): checking every page against the rules in use is always meaningful.

`GET /api/v1/admin/redaction` (state, total, done, percent, pages changed, rate and time left while it runs, the times, the reason, and `pending`: the rules in use aren't yet applied to every page) and `POST /api/v1/admin/redaction/{pause,resume,stop,start}`, admins only; **Administration → Retroactive redaction** (phone and desktop, tokens only, a tag on the start page while it isn't idle); and `Get-`, `Suspend-`, `Resume-`, `Stop-` and `Start-PagerArchiveRedaction` in the PowerShell module, which share one private helper with the re-parse's commands (`Invoke-PaJobChange`). Each admin's change is logged with `by`, `state`, `done` and `total`.

### 5. Observable

`/metrics` gains `pagerarchive_redaction_running`, `_state` (0 idle, 1 running, 2 paused, 3 stopped), `_run_messages`, `_run_messages_done`, `_run_started_timestamp_seconds` and `_messages_total` (pages checked since the API started), with no labels. Requests and load gains a **Retroactive redaction** row under the re-parse's: its state, progress, checked per minute, time left and a graph of each run, with the same rules for when each shows. The log has "retroactive redaction started" (`reasons`, `total`), "retroactive redaction progress" every 30 s, "retroactive redaction carried on", "retroactive redaction finished" (`count`, `redacted`, `seconds`, `per_minute`), the start again for changed rules, the nightly run leaving a paused one, and the admin's changes. The traces have a `redaction` span with `pagerarchive.redaction.pages`. Counts and codes only (P1-P3): never a page's text, a rule's terms, a capcode, an address or a locality.

### 6. Not changed

What redaction does to a page, and what a re-parse does to one; the retention part of the nightly run (still its own row in R91b); and the nightly run's result (`redacted` is the pages this night's run changed).

## Shown by A124

- `internal/acceptance/a124_test.go`: over 3,000 made-up pages (every other one with a made-up number in its text and its parsed description) and 200 more, the nightly run's run held right after a page's new text is written and ended there: none half done at any point, it carries on from 1,000 when started again and finishes with the number in no row of any table, every changed page's re-parse kept once and only the batch in hand's tried twice; a pause holds across a restart and a nightly run; a stop is started again by the next nightly run; a rule added at start-up starts a paused run again at 0, still paused; the status, metrics and log follow, with no page text, number or capcode in the log.
- `internal/monitortest/a124_test.go` (in `scripts/monitoring-checks.sh`): the stack's own API killed with SIGKILL part way, none half done, back paused (written while it was down) at 1,000 of 3,000 in Prometheus and on the dashboard, and resumed it finishes; Loki has the carried-on and finished lines and no page text.
- `web/tests/e2e.spec.ts` "A124 retroactive redaction controls in Administration": the page and the start page's tag through running, paused, idle, stopped and started again, at 1,280 and 375 px, with axe.
- `powershell/tests/Module.Tests.ps1`: the five commands, `-WhatIf`, `-PassThru`, a refusal as an error with the API's code, and not being signed in.
- `internal/longjob`: the progress tests that were the re-parse's.
