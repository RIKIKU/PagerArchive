# ADR 0046: Speed checks report in the gates; a weekly benchmark fails a big slowdown

## Status

Proposed (2026-11-18). The owner's decision of 2026-11-18, on A76 "causing more harm than good": keep the correctness gating, report the timings without failing, and run a scheduled weekly benchmark that fails only on a big slowdown. SPEC.md A75, A76 and 12.1 ("What CI runs"). A CI design rule beside ADR 0025's retries, so its own ADR: ADR 0025 points here. Nothing a deployment runs changes, so `VERSION` and the compose image stay as they are (ADR 0024).

## Context

A76's CI check imports 200,000 pages in random order on top of A75's 4,562,500 while a client posts live pages, then runs A75's searches. It failed when any step was over its time limit, the import's 500 pages a second above all. GitHub's shared runners aren't the same machine twice, and the same commit's import ran at 451 pages a second on one and 838 on another. So the limit failed at random: Release run 44278003288 failed on it (451 a second), and v1.17.0 was never published, and it had failed releases before. Its correctness checks are sound, and they are the only check of search on a large archive.

The history, from `gh run list` and `gh run view --log` of every attempt of CI's last 300 runs and the Release workflow's last 60 (2026-11-03 to 2026-11-18):

- **A76 ("Importing messages")**: 266 attempts with a log, 20 failed. 15 of those were the import's rate, 451 to 494 pages a second, all since 2026-11-17 01:28 UTC, the release's among them. The other 5 weren't timings: a branch's own errors (a migration, 3 attempts), `TestA76FileChecks`' restart flake (ADR 0025's follow-up) and GitHub's artifact service. The rate has three bands since 2026-11-16, about 750 to 840, 600 to 640 and 450 to 495 pages a second, for the same commits: the runners, not the code. Every run in the lowest band failed. (In early November the rate was 935 to 2,050: the import has done more work a page since then, and the slowest runners came later.)
  - The live pages, whose limits are 2 seconds for the post's answer and the live update and 5 seconds for the integration's delivery: the slowest in any run was 654 ms.
  - The searches after the import, limit 1 second: 8,490 timed, the slowest 338 ms, the 99th percentile 304 ms.
  - The memory budget: the API's peak 17.0 to 20.7 MiB (limit 256), the API's and database's together at most 605.4 MiB (limit 1,024). It never failed.
- **A75 ("Search speed")**, since 2026-11-10 (198 runs, after ADR 0017's second cold start): 5 failed, 4 on a timing, all just over 1 second: A71's counts for a common term cold, 1.083 s and 1.21 s; A99's busiest and localities reports for 10 years cold, 1.05 s and 1.145 s. The searches' first runs after a cold start reached 1.844 s, but every one over 1 s was 0.534 s or less on its second cold start; warm, the slowest search was 472 ms, and the slowest warm report 928 ms.

## Decisions

### 1. A gate fails on correctness, and reports the times

Where A75 and A76 run as gates (CI on pull requests and main, and the Release workflow's "Checks and release"), they run as before and fail on exactly what they failed on that isn't a time:

- **A76**: the file checks (`scripts/a76-checks.sh`); the import failing; a live page lost, refused, or not in the open list and at the integration; the webhook, the open list or the deliveries getting more than the live pages (an imported page set one off, R83b); a search failing; page text in the logs; and the memory budget (decision 3).
- **A75**: the pages each search lists against the query before M13a, warm and after a cold start; A71's counts against the table, and counts left uncounted though they answered within `store.CountsTimeout` (they're uncounted only when counting took longer, a time); every report answering.

Each time is measured and reported, not enforced: `scripts/speed-report.sh` writes a table in the job's summary (step, seconds, the limit, and which search or what rate was the slowest), A76's memory peaks under it, and a warning annotation, `slow: <step> took <s> s, over its limit of <l> s (…)`, for each step over its limit, as the flaky passes are (ADR 0025). A slow step never fails a gate. The steps, each with its slowest:

| Step | Limit |
|---|---|
| A76: the import of 200,000 pages | 400 s (500 pages a second, R83f) |
| A76: a live page's post answered, its live update in an open list | 2 s (R32) |
| A76: a live page delivered to the integration | 5 s (R51a) |
| A76: A75's searches after the import, the slowest of 3 runs | 1 s (R33b) |
| A75: the searches, warm, and the first run after a cold start (the better of two cold starts when the first is over 1 second, ADR 0017) | 1 s (R33b) |
| A71: the counts, warm and cold | 1 s (A71) |
| A99: the reports, warm, and over 10 years cold | 1 s (R38a) |

The tests write the steps, tab-separated, and the script reads them: `speed.go` to `a76-timings.tsv` in A76's results (uploaded with them), `TestA75SearchSpeed` to `PAGERARCHIVE_SPEED_FILE`. The times are logged as before, and a step over its limit is also logged as `slow: …` where it happens.

### 2. A weekly benchmark fails a big slowdown

`.github/workflows/benchmark.yml` runs the same A75 test and A76 import (not A76's file checks, which time nothing) every Monday at 02:23 UTC, after the weekend's release, and by hand (`workflow_dispatch`, on any branch). With `PAGERARCHIVE_SPEED=benchmark`, `speed-report.sh` fails the job when a step took **more than twice its limit**: the import under 250 pages a second (over 800 s), a search, count or report over 2 s, a live page's answer over 4 s or its delivery over 10 s. A step over its limit but within twice it is a warning, as in a gate. The correctness checks and the memory budget fail it as they fail a gate, and so does a run that ended before its steps were timed.

Why twice: the worst step against its limit in the history above was 1.21 times it (A71's count), and the import's 1.11 (443 s); for one commit, the runners' rates differ 1.86 times. So twice the limit is past every slow runner seen so far, and a step gets there only when the code is much slower: the import's slowest runners (451 a second) would have to slow by 1.8 times. A tighter bound from the history (1.5 times, say) would leave 1.24 times over the worst run, which a new, slower runner could eat. The bound is one number in `speed-report.sh`.

The benchmark runs on GitHub's hosted runners (`ubuntu-24.04`), never self-hosted: the repository is public, and the release's attestations refuse self-hosted runners. It builds the API's image for A76's stack on the runner and publishes nothing, uses no secret, and its token only reads the repository (`contents: read`). Its concurrency group is its branch's, never cancelled.

**A failure is a failed scheduled run**, which GitHub emails to the owner. No issue is opened: that needs `issues: write`, and the benchmark is meant to read only. The CI retry workflow (ci-retry.yml) and the maintenance notice (maintenance-failed.yml) listen to CI's runs only, so they don't re-run it or tell anyone else.

### 3. The memory budget stays a gate

A76's API peak (at most 20.7 MiB of 256) and API and database peaks (at most 605.4 MiB of 1,024) have never failed and are far inside their limits on every runner, so they stay gating, in the gates and in the benchmark, and are shown under the timings table.

### 4. Runs beside another stack

`scripts/a76.sh` takes `API_BIND`, `PA_A76_DB_PORT`, `PA_A76_HOOK_PORT` and `PAGERARCHIVE_VERSION` (the image's tag), so it can run on a machine where another stack has 8080 (`scripts/stack-lib.sh`'s wait for the API follows `API_BIND`). CI sets none of them.

### 5. `scripts/speed-report.sh` has its own area

It's read by the search and import jobs only, so `scripts/ci/changes.sh` gives it the area `speed`, which runs those two (ADR 0025's table); `changes-checks.sh` checks it. `benchmark.yml`, under `.github/workflows/`, runs everything, as CI's own files do.

## Consequences

- A slow runner no longer fails a pull request, main or a release. A change that makes a step slower than its limit still shows, as a `slow:` warning on the run and in the job's summary: look into it, as a flaky pass.
- A change that makes a step more than twice as slow as its limit fails the next Monday's benchmark, up to a week after it merged, not on its own pull request. Anyone can run the benchmark on a branch by hand first.
- The limits in SPEC.md (R33b, R38a, R83f, R32, R51a, A71) are unchanged; what CI enforces of them is: their correctness always, their times at twice the limit weekly, and on every run as a warning.
- The benchmark costs about 40 minutes of runner time a week.

## Evidence

`scripts/speed-report.sh` on made-up timings (cld01): in a gate, a step over its limit and one over twice it are both warnings, and it exits 0; with `PAGERARCHIVE_SPEED=benchmark` the one over twice its limit is an error and it exits 1; with no timings it exits 0 in a gate and 1 in the benchmark.

`scripts/a76.sh -s 100000 -n 60000 -r` on cld01, with its own ports (`API_BIND=127.0.0.1:18476 PA_A76_DB_PORT=55476 PA_A76_HOOK_PORT=14376`) and image tag (`PAGERARCHIVE_VERSION=a76split`). The forced slow step: once the API answered, its container was cut to a fraction of a core (`docker update --cpus`), so the import, which runs in it, slowed.

In a gate (0.15 of a core), it warns and passes:

```
A76 speed: PASS
--- A76, importing messages (a gate: a step over its limit is a warning, never a failure)
  the import of 60000 pages                                       229.030 s  limit    120 s  slow: 262 pages a second, the limit 500 (R83f)
::warning title=Slow::slow: the import of 60000 pages took 229.030 s, over its limit of 120 s (262 pages a second, the limit 500 (R83f))
  a live page: post answered, the slowest                           0.097 s  limit      2 s  during the import (R32)
  a live page: live update in an open list, the slowest             0.119 s  limit      2 s  during the import (R32)
  a live page: delivered to the integration, the slowest            0.120 s  limit      5 s  during the import (R51a)
  A75's searches after the import, the slowest of 3 runs            0.112 s  limit      1 s  alias:"Station 1", a term never in its pages (R33b)
API peak:          27.5 MiB (limit 256)  PASS
api + db peaks:    335.4 MiB (limit 1024) PASS
A76 correctness, budget and timings (gate): PASS
a76.sh exit 0
```

The job's summary it wrote:

| Step | Seconds | Limit (s) | Notes |
|---|---:|---:|---|
| the import of 60000 pages | 229.030 | 120 | **slow**: 262 pages a second, the limit 500 (R83f) |
| a live page: post answered, the slowest | 0.097 | 2 | during the import (R32) |
| a live page: live update in an open list, the slowest | 0.119 | 2 | during the import (R32) |
| a live page: delivered to the integration, the slowest | 0.120 | 5 | during the import (R51a) |
| A75's searches after the import, the slowest of 3 runs | 0.112 | 1 | alias:"Station 1", a term never in its pages (R33b) |

In the benchmark (0.08 of a core), the same check fails, on the import alone:

```
A76 speed: PASS
--- A76, importing messages (the weekly benchmark: a step over 2 times its limit fails)
  the import of 60000 pages                                       424.866 s  limit    120 s  slow, over 2 times the limit: 141 pages a second, the limit 500 (R83f)
::error title=Slow::slow: the import of 60000 pages took 424.866 s, over its limit of 120 s (141 pages a second, the limit 500 (R83f)), over 2 times the limit
  ...
API peak:          25.9 MiB (limit 256)  PASS
api + db peaks:    323.9 MiB (limit 1024) PASS
--- A76, importing messages: a step took more than 2 times its limit
A76 correctness, budget and timings (benchmark): FAIL
a76.sh exit 1
```

`actionlint` (1.7.12, cld01) passes on every workflow, and `scripts/ci/changes-checks.sh` passes with `scripts/speed-report.sh: search import`. A75's test with `PAGERARCHIVE_SPEED_FILE` runs in this pull request's CI and in the benchmark run dispatched on the branch.
