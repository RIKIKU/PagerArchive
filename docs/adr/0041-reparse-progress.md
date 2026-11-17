# ADR 0041: Re-parse progress in the metrics, the log and Grafana

## Status

Proposed (2026-11-18). The owner: "Let's add some more logging to the reparse so we can see the progress in Grafana next time and you don't have to work it out." SPEC.md R23b (with R72 and R76), with A122. On the branch `reparse-progress` from main; the pull request is labelled `feature`.

## Context

The re-parse worker (R23a, `internal/reparse`) claims every queued request, deleting it, walks the matching messages in batches of 500 by ID, and logged one line, "re-parsed messages", at the very end. A profile change re-parses the whole archive: on an archive of about 3 million pages that's most of a day. While it ran, nothing said how far it had got: the request table was empty, there was no log line and no metric, and the only way to tell was to guess from the database.

## Decisions

### 1. The worker keeps its progress in memory; the API's `/metrics` shows it

`reparse.Progress` (a field of the worker, so its zero value is idle) holds whether a run is going, its total, what it has done, when it started, and a count of every message re-parsed since the API started. The API serves it on R52's endpoint, which Alloy already scrapes (R75), so no new path reaches Prometheus:

| Metric | Type | What |
|---|---|---|
| `pagerarchive_reparse_running` | gauge | 1 while a run goes (until it has rebuilt the large events after, R87f) |
| `pagerarchive_reparse_run_messages` | gauge | the run's total, counted when it started; the last run's while idle, 0 before any |
| `pagerarchive_reparse_run_messages_done` | gauge | what the run has done; the last run's while idle |
| `pagerarchive_reparse_run_started_timestamp_seconds` | gauge | when the run (or the last one) started; absent before any |
| `pagerarchive_reparse_messages_total` | counter | every run's messages since the API started, so `rate()` gives pages a minute |

There are no labels: a capcode pattern isn't safe to export (P1-P3), and reasons are written by any tool. The worker and the API are one process, so nothing is stored for this: a restart loses it, as it loses the run.

Why the totals stay after a run: a scrape every 15 s can miss a short run, and "done equals to do, not running" says it finished, where a drop to 0 wouldn't.

### 2. The total is counted once, with the query the run uses

At the start of a run, `CountMessagesToReparse` counts `messages WHERE capcode LIKE pattern` for each pattern, the same condition `MessagesToReparse` walks, and adds them (overlapping patterns are walked, and counted, once each). One count over the archive, at most once per run, costs a few seconds against hours of re-parsing. Pages stored while it runs match too and are re-parsed by the same walk, so done can pass the total by a few; when the run finishes, its total becomes what it did, so the finished numbers agree. What's re-parsed, and in what order, is unchanged.

### 3. Log lines: started, progress every 30 s from a ticker, finished

- **"re-parse started"**: `reasons`, `requests`, `patterns`, `total`.
- **"re-parse progress"**, every 30 s (`Worker.ProgressEvery`): `done`, `total`, `percent`, `per_minute` and `eta_seconds` (left out until something is done). The rate is the average since the run started: over a run of hours it's steadier than the last interval's, and the dashboard has the last 5 minutes' rate beside it. It's logged from a ticker beside the walk, not from inside it, so a run that's stuck on a lock or a slow query still logs, with done not moving.
- **"re-parsed messages"** (unchanged message): `count`, `requests`, and now `seconds` and `per_minute`.

All are integers or codes, so they pass the OTLP log allowlist (R70, `internal/telemetry/filter.go`) and Alloy's copy of it, which both gain `reasons`, `patterns`, `total`, `done`, `percent`, `per_minute` and `eta_seconds`.

**Reasons** are logged only as codes from a fixed list: the reasons the code itself queues with (`profiles changed`, `retry`, the catalogue's alias and template reasons) become `profiles_changed` and so on; anything else, which any tool writing `reparse_requests` could fill with text, is `other`. A test (`TestKnownReasons`) reads the code's reasons from the source, so a new one can't be added without the list. Several reasons are one comma-separated value, so the allowlist gains a kind for it, a list of codes, each passed through the same check as a single code: the second line of defence keeps working if a line ever logs something else under that key.

### 4. The dashboard: a row on Requests and load

Re-parsing is background load on the API (R72), so it's a row at the bottom of **Requests and load** (`deploy/monitoring/grafana/dashboards/requests.json`), not a dashboard of its own: **Re-parse** (Idle or Running), **Re-parse progress** (%), **Re-parsed per minute** (the last 5 minutes), **Re-parse time left** (what's left at that rate), and a graph of each run's to do and done. The three numbers are filtered on running (`and on() (max(running) == 1)`), so while idle they have no value and show **Idle**, rather than the last run's 100 % or a stale rate. No alert: a re-parse is wanted work, and the graph shows a stuck one.

It's provisioned from the repository's file, like the others, so a release delivers it: the release's `pagerarchive-config.tar.gz` holds the file, an install upgrades the stack as docs/monitoring.md says (or automatic upgrades do, with `AUTO_UPGRADE_MONITORING_DIR`), and Grafana and Alloy are recreated because their `pagerarchive.config` labels changed. Files a deployment added beside the stack (its own dashboards, an override) aren't in the archive and are left alone.

### 5. Not done: resuming a run after a restart

A restart still starts a run again from the beginning: the claimed request is gone, and the start-up check queues a full re-parse again when the profiles changed. This ADR changes visibility only. The walk is already by ID, so resuming would be cheap to add: keep the claimed requests (marked, not deleted) with each pattern's last ID, written once a batch, and start from it; the progress would then start from the count up to that ID instead of 0.

### 6. Shown by A122

- `internal/acceptance/a122_test.go`: 3,000 made-up pages, the run held at the 1,500th by a row lock while `/metrics` and the log are read, then let go, then a full re-parse; the stdout and OTLP log lines, and no page text in either.
- `internal/monitortest/a122_test.go` (in `scripts/monitoring-checks.sh`): the same held run in the stack's own API, read through Prometheus, the dashboard's panels as Grafana runs them, and Loki; then Idle.
- Unit tests: `internal/reparse` (the rate, the time left and the reasons), `internal/telemetry` (`TestAllowlistCodes`, and the Alloy list matching).
