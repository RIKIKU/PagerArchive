# ADR 0042: Re-parse: resumable, pauseable, stoppable and restartable

## Status

Proposed (2026-11-18). The owner's rule: "for any process, data migrations, reparsing, etc that can take more than 30 minutes, the process should be observable (in Grafana stack), restartable, resumable pauseable and stoppable." Its terms, as the owner approved them: **restartable**, an admin can run it again from the beginning on demand; **resumable**, after a crash, app restart or upgrade it carries on from where it got to (progress kept in the database), not from the start; **pauseable**, an admin can pause it and later carry on without losing progress; **stoppable**, an admin can cancel it, and the data stays consistent (each unit of work done or not, never half done). The rule itself is being written into SPEC.md, CLAUDE.md and an ADR of its own on another branch; this ADR applies it to the re-parse. SPEC.md R23c, with A123. Observable is ADR 0041 (R23b, A122). On the branch `reparse-control` from main; the pull request is labelled `feature`.

## Context

What a restart part way through a re-parse did before this change, read from the code and shown by a throwaway test on main (3,000 made-up pages, the run held at the 1,500th by a lock, then the API's context ended as `serve` ends it on SIGTERM, then a new worker with the same profiles):

- **The profile hash is written at start-up, before the run.** `CheckProfiles` compares `settings.profile_hash` with the profiles in use and, when they differ, queues a `%` request ("profiles changed") and saves the new hash **in the same transaction**: the hash says the profiles are dealt with before a single page is re-parsed.
- **The requests are gone the moment a run starts.** `RunOnce` claims them with `DELETE FROM reparse_requests RETURNING …` (its own statement, committed at once) and keeps the run, its patterns and where it got to only in memory, walking `MessagesToReparse` by ID.
- **So a restart loses the run.** On SIGKILL or a crash nothing more runs. On SIGTERM the walk fails with `context canceled` and `requeue` tries to put the requests back with the same, already cancelled, context, which fails too ("re-queue re-parse" in the log). Started again with the same profiles, `CheckProfiles` finds the hash it saved and queues nothing, and there's no request left: the run is lost. The test: 1,499 pages re-parsed before the stop, 1,500 after (the held page's update went through once the lock was let go), `RunOnce` 0 after the restart, and **1,500 pages never re-parsed**. They keep their old parse until something else re-parses them: after a profile change, the next profile change.
- **A page could be half done.** Each page's re-parse is several statements outside any transaction (`UpdateParsed`, then the location, area and derived locality), so a stop between them leaves a page re-parsed but not placed again (the held page above).
- **The large events weren't rebuilt** around the pages a lost run did: they're rebuilt once, from a list kept in memory, at the end of the run (R87f).
- **Only a database error** part way (not a stop) put the requests back, as "retry", and that started the pattern again from the beginning.

ADR 0041 §5 sketched the fix: keep the claimed requests, write each pattern's last ID per batch, and start from it.

## Decisions

### 1. A run is kept in the database

Migration 00029 adds `reparse_runs` (one row at most: the latest run, kept once it's finished so its numbers can still be shown), `reparse_run_patterns` (each capcode pattern, the last message ID done, and whether it's done) and `reparse_run_days` (the UTC days of the pages done, each with its first and last sent time, for rebuilding the large events). A run has its state (`running`, `paused`, `stopped`, `finished`), its reasons as R23b's codes (never a request's text: P1-P3), how many requests it covers, its total and done, an epoch, and when it started, last changed state and finished.

Claiming the queued requests and creating the run from them is **one transaction**, so a crash can't lose them. The worker reads the run, not the requests: `RunOnce` carries on a running run, or starts one from the requests when the last has finished.

### 2. A batch and its place are one transaction

Each batch of 500 messages is re-parsed in one transaction, which also writes the pattern's last ID, adds to the run's done and records the batch's days, after locking the run's row. So a page is re-parsed and placed whole or not at all, and what the run says it has done, it has: a crash, restart or upgrade rolls back only the batch in hand, and the run carries on after its last committed batch. At most that batch's pages are re-parsed twice (A123 counts them: kept once each, with the rolled-back batch's tries beside them). A failed batch is rolled back and the worker tries again from the same place after 5 s; nothing is queued as "retry" any more (the code stays known, for requests an older version queued).

The large events are rebuilt from `reparse_run_days` when the run has done every pattern, then the days are forgotten, then the run is marked finished: a crash during the rebuild does it again, which gives the same events. The run is running until then, as R87f has it.

Cost: a batch's row locks are held until it commits, a few seconds on a large archive rather than one statement each. Live ingest only inserts pages and adds receptions (whose foreign key takes a key-share lock, which an update of the parsed fields doesn't conflict with), so it doesn't wait; retroactive redaction or a report count of the same job can wait for one batch.

### 3. Profile changes start an unfinished run again

`CheckProfiles` still queues a `%` request and saves the new hash in one transaction. In that transaction it now also looks at the run: one that hadn't finished (running, paused or stopped) parsed its pages with the old profiles, so it's put back to the beginning over every message (`%`), with the waiting requests taken in, its total counted again, its done 0 and its epoch raised, **in the state it was in**: an admin's pause or stop holds until the admin carries it on or starts it again. A run that had finished leaves the request for the next run, as before. With the hash unchanged, the run carries on (decision 2).

### 4. Changes made during a run wait

A request queued while a run is running, paused or stopped waits for the next run, as before this change (the worker never merged new requests into a run under way). The brief allowed either; waiting keeps a run's total and patterns fixed, and keeps what a re-parse does unchanged. The status shows how many wait. Start again takes them in (decision 5); a finished run lets the worker claim them at once.

### 5. An admin's controls

`reparse.Pause`, `Resume`, `Stop` and `StartAgain` change the run in one transaction with its row locked (so they wait for a batch committing at that moment), notify the worker, and set the API's in-memory progress, which the worker checks before each page: it stops after the page in hand, commits that batch with its place, and reads the run again.

| Action | From | To | Refused |
|---|---|---|---|
| Pause | running | paused | otherwise, `reparse_not_running` |
| Resume | paused | running, from its place | stopped, `reparse_stopped`; running or idle, `reparse_not_paused` |
| Stop | running, paused | stopped; the large events around the pages done are rebuilt | stopped or idle, `reparse_not_running` |
| Start again | any run, idle (the last run) included | running, from the beginning, with the waiting requests; epoch raised | no run yet, `reparse_none` |

A stopped run can't be resumed: stopped means cancelled, and its rest is done only by starting it again. The epoch keeps a batch that was under way when Start again committed from writing its old place over the new start (its pages are done, consistently; its place isn't written).

They're reached through the API, admins only: `GET /api/v1/admin/reparse` (the status: state, total, done, percent, the rate and time left while it runs, the times, reasons, requests, patterns as a count, and the requests waiting) and `POST /api/v1/admin/reparse/{pause,resume,stop,start}` (200 with the status, or 409 with the code). The browser's **Administration → Re-parse** page (phone and desktop; tokens only, R82c) shows the status, polling every 2 s while it runs, with the buttons the state allows; Stop and Start again ask first. Administration's start page tags the entry while the re-parse isn't idle. The command line is the PowerShell module, as R59 has every admin command line that isn't a server-only job: `Get-PagerArchiveReparse`, `Suspend-` (pause), `Resume-`, `Stop-` and `Start-PagerArchiveReparse`, each `SupportsShouldProcess` at `Medium`, `-PassThru` for the status, taking nothing from the pipeline (like the backup commands). `pagerarchive reparse …` on the server would need its own way to say which admin did it; the module signs in as one.

Each action is logged as other admin actions are: "re-parse paused", "re-parse resumed", "re-parse stopped", "re-parse started again", with `by` (the admin's user ID), `state`, `done` and `total`.

### 6. Observable

`/metrics` gains `pagerarchive_reparse_state` (0 idle, 1 running, 2 paused, 3 stopped; no labels). `pagerarchive_reparse_running` stays (1 only while running); done and total show a paused or stopped run's while it's held, and after a restart the last run's (read from the database) rather than 0. The rate is from when this process began or carried the run on, so a run carried on after a restart or a pause isn't averaged over the time it wasn't running.

On **Requests and load**, the **Re-parse** stat reads the state (falling back to `running` for an API from before this), mapped to Idle, Running, Paused and Stopped; **Re-parse progress** shows while the state isn't idle, so a held run shows how far it got; the rate and time left show only while it runs ("Not running" otherwise). The panels' descriptions and docs/monitoring.md say what each state means, and that a restart no longer starts a run again from 0.

New log lines: "re-parse carried on" (`state`, `reasons`, `done`, `total`) whenever the worker carries on a run it didn't start in that call (after a restart, a resume or Start again), and "re-parse starts again from the beginning: the profiles changed" (`state`, `reasons`, `total`). `state` joins the OTLP log allowlist (R70) and Alloy's copy, as a code. No alert: a paused or stopped re-parse is an admin's choice, and the dashboard shows it.

### 7. Not changed

What a re-parse does to a page (the parse, the alias it picks, where it's placed, its areas and derived locality), the order it goes in, R23b's start, progress and finished lines, and the nightly re-parse of pages retroactive redaction changed (`ReparseIDs`), which isn't a run and isn't paused or stopped (P1 doesn't wait).

## Shown by A123

- `internal/acceptance/a123_test.go`: over 3,000 made-up pages (and 200 on another capcode), with a trigger counting each page's kept re-parses and a sequence counting every try, the worker is ended part way (its context, as the process ending does; nothing more is written) and a new one started on the same database: it carries on from 1,000, every page kept once and only the batch in hand's tried twice; a pause holds across a restart; a stop leaves the rest as they were, a waiting request waits, and Start again runs everything from the beginning; changed profiles start an unfinished run again from the beginning (a paused one stays paused); the status, metrics and log follow.
- `internal/monitortest/a123_test.go` (in `scripts/monitoring-checks.sh`, which now passes `PA_MON_KILL_API`): the stack's own API is killed with SIGKILL part way, comes back paused (written while it was down) at 1,000 of 3,000 in Prometheus and on the dashboard, and resumed it does the remaining 2,000.
- `web/tests/e2e.spec.ts` "A123 re-parse controls in Administration": the page and the start page's tag through running, paused, idle, stopped and started again, at 1,280 and 375 px, with axe.
- `powershell/tests/Module.Tests.ps1`: the five commands, `-WhatIf`, `-PassThru`, a refusal as an error with the API's code, and not being signed in.
- Unit tests in `internal/reparse`: the state codes, the rate after carrying on, a rolled-back batch taken back, and an interrupt.
