# ADR 0025: Faster, less flaky CI

## Status

Proposed (2026-11-12). The owner's brief of 2026-11-12: a pull request runs only the checks its files affect, failures that aren't the change's are retried (and still reported), A82's Firefox flake is fixed, security releases close together go out as one, and govulncheck reports the vulnerabilities the code can reach. On the branch `ci-speed` from main at 325daab. Builds on ADR 0014 (whose docs-only filter moves from the workflow's `paths:` into the changes job, keeping its intent) and ADR 0024 (the Release workflow, which runs every check before it releases); the security releases' wait is a dated section of ADR 0024, as it changes that ADR's decision 4. Nothing a deployment runs changes, and the PowerShell module doesn't change (only its tests' runner), so neither version moves.

A new ADR rather than a section of ADR 0014: that one is about A33 and A67, and its docs-only filter is one paragraph this replaces with the same rule; the path filters, the three kinds of retry and govulncheck are new decisions about how CI runs, and want one place.

## Context

- **Every pull request ran every check**, about 21 jobs and 25 to 45 minutes, whatever it changed. PR #10 changed one Grafana dashboard and ran all 14 check jobs; 2 of them (the root package's tests and the monitoring stack) use that file.
- **Most red runs lately weren't the change.** The runners' DNS failing for Ubuntu's mirror (`apt-get` exit 100, 4 jobs of PR #12's run); GHCR's `permission_denied: write_package` (v1.4.0); A80's backup racing CI (fixed in v1.6.0); A82's axe scan catching a tooltip part-way through its fade in Firefox (PR #13's run 43088290082); GitHub's artifact service answering "Failed to CreateArtifact" with an HTML page in 6 jobs of #14's run on main, so v1.7.3 was never released; the PowerShell Gallery answering 403 for 30 seconds (the 3 tries of `Save-TestModules.ps1`, 10 seconds apart, all failed; #15's Renovate run 43108950897); A15's idle CPU at 5.01% against a limit of 5 (#18's run on main, 43133795198). Each cost a manual re-run and about 25 minutes.
- **Two security releases 18 minutes apart** (v1.7.7 and v1.7.8, #18 and #19): each security merge started its own release.
- Renovate's OSV alerts (ADR 0024) name a module whether or not PagerArchive calls its vulnerable code.

## Decisions

### 1. A pull request runs only the checks its files affect

- **Job-level filters, not the workflow's `paths:`.** A workflow that doesn't start leaves the required "All checks passed" missing, and the pull request can't merge. So every run starts, and a first job, `changes` ("What the change affects"), works out which check jobs the changed files can affect; each check job has `if: needs.changes.outputs.<job> == 'true'`.
- **`scripts/ci/changes.sh`, not an action.** It lists the files between the merge commit's two parents (main, and main with the pull request merged; `--no-renames`, so a file moved out of an area counts for it too), gives each file one area and each area its jobs. It's about 200 lines of `sh` in the repository, so nothing third-party sees the token or needs pinning and review, and the rule that matters most here, "a file in no area runs everything", is one `*)` case. A paths-filter action matches files against named filters; a file left over by every filter needs a filter of its own that negates all the others, which is easy to get wrong when an area is added. `scripts/ci/changes-checks.sh`, in the `versions` job, checks the table with made-up file lists, that every tracked file has an area or is meant to run everything (a warning otherwise, so a new file or folder is noticed), and that `ci.yml`'s jobs and the script name the same jobs, each with its `if`.
- **The table**, from what each job builds, mounts and runs (its steps and scripts, the Dockerfiles, the compose files and the Go tests' file reads):

  | Change in | Runs |
  |---|---|
  | Go: any `*.go` (scripts' too), `go.mod`, `go.sum`, `cmd/`, `internal/` (migrations, queries), `schemas/`, `sqlc.yaml`, `VERSION` | everything but `powershell` |
  | `web/` (`web/static/**`'s Markdown included) | `web`, `stack-e2e`, `install`, `backups`, `images` |
  | `powershell/` | `powershell`, `stack` |
  | `compose.yaml`, `.env.example`, `deploy/authentik/`, `deploy/postgres/` | `test`, `stack`, `stack-e2e`, `import`, `upgrade`, `install`, `backups`, `budget`, `monitoring` |
  | `deploy/backup/` | `test`, `upgrade`, `install`, `backups`, `monitoring` |
  | `deploy/monitoring/` | `test`, `monitoring` |
  | `Dockerfile` | `images`, `stack`, `stack-e2e`, `import`, `upgrade`, `install`, `backups`, `budget`, `monitoring` |
  | `Dockerfile.client` | `images`, `stack-e2e`, `install`, `monitoring` |
  | `.dockerignore` | both of the above |
  | a job's own script (`scripts/e2e.sh`, `stack-checks.sh`, `stack-e2e.sh`, `a76*.sh`, `a77.sh`, `a78.sh`, `a80*.sh`, `a15.sh`, `monitoring-*.sh`) | that job |
  | documentation (`SPEC.md`, `docs/**`, other Markdown) | nothing |
  | scripts run only by hand (`mutation-checks.sh`, `oauth-probe.py`, `scripts/example-map/*.sh`), `.github/release.yml`, `.github/renovate.json5` | nothing |
  | `.github/workflows/`, `.github/actions/`, `scripts/ci/`; the shared fixtures `profiles/` (the golden tables), `testdata/`, `deploy/test/`, `deploy/example-map/`, `scripts/fixture.sql`, `scripts/stack-lib.sh`, `scripts/install-lib.sh`, `scripts/test-*/`; anything else | everything |

  `stamp` and `changes` always run, and `versions` unless only documentation changed. `govulncheck` (decision 5) runs with Go. On a pull request the publish jobs (which only build there) run when `images` does.
- **What changed from the draft table** (the notes of 2026-11-11):
  - **Go runs `web`, `stack-e2e` and `monitoring` too.** The browser tests run the real API built from this checkout (`scripts/e2e.sh`), and `stack-e2e` and `monitoring` build the server and client images. Only the PowerShell module's own tests don't use the Go code.
  - **`web/` runs `install` and `backups` too:** A78 and A80 drive the browser app (`tests/stack.spec.ts`, `tests/a80.spec.ts`).
  - **`powershell/` runs `stack` too:** two of its three parts run the module against the API and Authentik (A20, A21, A51-A59).
  - **`compose.yaml`, `deploy/authentik` and `deploy/postgres` run `test`, `import`, `backups`, `budget` and `monitoring` too.** The root package's tests read them (R84's files, R84a's config labels, which hash every mounted file), and every one of those jobs starts the stack.
  - **`deploy/backup` runs `test`, `install` and `monitoring` too:** the backup service is in every installed stack, and A80's alert is in the monitoring job.
  - **`deploy/monitoring` runs `test` and `monitoring`, not `install` or `upgrade`:** A77 and A78 never start the monitoring stack, and `deploy_test.go` and telemetry's `alloy_test.go` read its files.
  - **`profiles/` runs everything, not only `test`:** the server image bundles `profiles/example` as the default profile, and the browser tests, A15, A76, A78 and the images job's `validate-profile` use it.
  - **The Dockerfiles run every job that builds their image:** the draft had `budget` and `install` only; the stacks of `stack`, `stack-e2e`, `import`, `upgrade`, `backups` and `monitoring` build the server image too, and `stack-e2e`, `install` and `monitoring` the client's.
  - **Each job's own script runs only that job,** and the scripts run only by hand run nothing.
- **Main and the Release workflow run everything.** On anything but a pull request (a push to main, unless it changed only documentation; a manual run; the Release workflow's call) the changes job says every job runs. The `release` job needs every check, and its `if` has no status function, so a skipped check would skip the release: nothing is released that a check was skipped for. So a gap in the table can at worst let a pull request merge that then fails on main, before any release.
- **"All checks passed"** accepts a check that succeeded, or one that was skipped and that the `changes` job said to leave out. A skip it didn't ask for (because a job it needs failed), a failure or a cancel fails it. A notice lists the checks left out.
- **Documentation only** (owner, 2026-11-12): a pull request of documentation alone used to start no run (ADR 0014's workflow `paths:`), so the required "All checks passed" never reported and it couldn't merge (PR #25, `SPEC.md` only, was blocked so). The `paths:` filter is gone: every push and pull request starts a run, and when every changed file is documentation (or one no check reads), the changes job says no check runs, `versions` included (`versions=false`). Only `stamp` and `changes` run, and "All checks passed" reports success within a minute or two. ADR 0014's intent stands: no real work, and nothing published. A push to main gets the same: `changes.sh --all-unless-docs <before> HEAD` runs every check unless only documentation changed since the push's previous commit, in which case none (so no image is published for it, as before; `publish-client-manifest` now waits for the images' answer too). Documentation in a change with other files adds nothing.

### 2. Retries, none of which hides a failure

What passed only on a retry is reported, every time: a warning annotation, `flaky: <test or step> (…)`, and a line under "Passed only on a retry" in the job's summary. A test that fails every try still fails. No issues are opened.

1. **Network steps try again by themselves**, 3 tries, 15 seconds and then 45 apart:
   - `scripts/ci/retry.sh <command>`: `apt-get` (the web job's `psql`, and `npx playwright install --with-deps chromium` in `stack-e2e`, `install` and `backups`), `git fetch` of the tags, GitHub's API in the release jobs and the Release workflow's plan, the tag push, the release's create (which first looks for one an earlier try made), upload and Latest, `docker manifest inspect`, `docker buildx imagetools create`, and the CI retry workflow's `gh run rerun`.
   - **Image pulls:** `scripts/ci/pull-images.sh` pulls, through `retry.sh`, the images each job's builds and stacks use (the `FROM` lines of the Dockerfiles it builds, the `image:` lines of the compose files it starts, and `install-lib.sh`'s registry), so the build and `docker compose up` find them already there. The runner itself retries the service and job containers' pulls. A script's own images are covered too (2026-11-13): those pinned by digest, and those it names on a line of their own as `<NAME>_IMAGE=<image>` (`NGINX_IMAGE` in A78, `CURL_IMAGE` in the client replay), the install and images jobs passing those scripts; and A77 pulls the previous release's images (its `compose.yaml`'s, and ours from GHCR) the same way, once it knows which release that is.
   - **Actions:** `.github/actions/upload` wraps `actions/upload-artifact` (every upload in `ci.yml`), each try after the first with `overwrite: true`; `.github/actions/build-push` wraps `docker/build-push-action`, retried only when it publishes (a pull request's build-only failure is the change's, so it fails at once); the client's digests are downloaded with three `download-artifact` steps.
   - **PowerShell:** `scripts/ci/Invoke-Retried.ps1`, for `Install-PowerShell.ps1`'s downloads and `Save-TestModules.ps1`'s Gallery requests (which had 3 tries 10 seconds apart; the Gallery's 403s lasted longer than that).
   - npm (`npm ci`, which retries its own fetches), `setup-go`, `setup-node` and `actions/checkout` retry by themselves already, and Go modules come from setup-go's cache.
2. **A failed test runs once more in its job:**
   - **Playwright:** `retries: process.env.CI ? 1 : 0`. Playwright marks a test that passed on its retry "flaky"; `web/tests/flaky-reporter.ts` reports each, and sets the step's output `flaky`, so the web job keeps the report and the failed try's trace as it does for a failure.
   - **Go, the test job:** `gotestsum --rerun-fails=1 --rerun-fails-max-failures=5` (v1.13.0) runs `go test`, then once more only the tests that failed. More than 5 failures means something broke, and nothing is re-run. Its report lists each test run again with its runs and failures; each that passed is reported flaky.
   - **Pester:** `Invoke-Tests.ps1 -RetryFailed` runs up to 5 failed tests once more (Pester's FullName filter); a failed block or file means set-up broke, and nothing is retried.
   - **Not retried in the job:** A75 (35 minutes, and its job's limit is 40), and the Go tests that `stack`, `import`, `upgrade`, `install`, `backups`, `budget` and `monitoring` run inside their scripts: each runs against a stack the first try has changed, and `stack` holds A33's lock checks. Layer 3 covers them.
3. **A failed run is re-run once, as a last resort.** `.github/workflows/ci-retry.yml` runs on `workflow_run` (CI, completed) and, through `scripts/ci/rerun-decision.sh`, re-runs the failed jobs (`gh run rerun --failed`, with `actions: write`) when all of these hold:
   - it's the run's first attempt;
   - it's a pull request's run or a push to main's: not a manual run, and not the Release workflow's (CI runs inside the Release workflow's run there, so `workflow_run` for CI never sees it; a failed release is re-run by the owner);
   - the commit is still the branch's newest: a re-run of an older commit would join the pull request's concurrency group and cancel its newer run, and on main would publish an older commit as `main`;
   - nothing failed that only the change can fix: the jobs `versions`, `stamp`, `changes` and `govulncheck`, and the steps `gofmt`, `go vet`, sqlc's diff, the example profile's `validate-profile`, `svelte-check`, the style tokens (A73), the icons and Playwright's version;
   - something other than "All checks passed" failed;
   - no failed job's log has a security or privacy finding: a check's `SECURITY FAILURE` (A33's lock letting a guess through: `<file>_test.go:<line>: SECURITY FAILURE`, as a passing run logs the words too, as a unit test's expected text) or page text or an email address in the API's log (P1). Those are never retried away.

   It posts a notice naming the jobs it re-ran. The re-run's "All checks passed" compares the attempt before with this one and reports each job that failed then and passed now as flaky (with `actions: read`; the Release workflow grants that to the called CI too). A run the owner re-runs by hand is reported the same way.
- **Maintenance CI failed** (`maintenance-failed.yml`, R85c) asks `rerun-decision.sh` the same question and tells the owner only when the answer is no: after the retry's own failure (attempt 2), or when there's none. If the question can't be answered, it tells them anyway. If the retry decides to re-run but can't start it, the retry workflow tells the owner instead. GitHub runs both from main's copy only, so neither runs before this merges.

### 3. A82's Firefox flake: wait for the tooltip's fade

The tooltips under Help and Clear all fade in (`app.css`: opacity over 0.1 s after 0.25 s) on hover or focus. After Clear all and Back with the mouse, the pointer is still where Clear all was clicked, so its tooltip starts to fade in as the button comes back; once, in Firefox, the axe scan came while it was part-way, and the text at about half opacity failed contrast: `color-contrast: .clear > .tooltip[aria-hidden=true]`.

The tooltip isn't hidden at that moment: it's on its way to being shown, so it's text someone sees, and WCAG 1.4.3 applies (its exception is text no one sees; `aria-hidden` only hides it from assistive technology). So it isn't excluded from the scan. Instead:

- `axeOf` waits, before every scan, until no tooltip has a transition running (its delay included), so it never scans a fade, in any test;
- A82, with the mouse, after Back, hovers Clear all and waits for its tooltip's opacity to be 1, so the scan checks the tooltip as it's seen, every time, instead of at whatever point the pointer's hover had reached.

Nothing is excluded and the scan's rules are unchanged.

### 4. Security releases batch for 30 minutes

ADR 0024's section of 2026-11-12.

### 5. govulncheck: what the code reaches

A `govulncheck` job ("Known vulnerabilities the code calls (govulncheck)") runs `golang.org/x/vuln/cmd/govulncheck@v1.9.0 -format json ./...` with the Go of `go.mod`, when Go changed. `scripts/ci/govulncheck.sh` lists every finding in the job's summary at its most precise level (called, imported or only required), and fails only for a vulnerability the code **calls** that has a **fixed version**; one called with no fix yet is a warning. It's one of the checks "All checks passed" and the release need. Its network (the database, the module) is retried; a vulnerability published later can turn main's next run red with nothing changed, which is the point.

## Evidence

Nothing was merged, tagged, released or published. The throwaway branches and pull requests below were based on `ci-speed` (so each one's diff is only its own change), never merged, and closed and deleted afterwards.

**What runs.** Each throwaway pull request's run, from the jobs API (`gh api …/actions/runs/<id>/jobs`):

| Change | Run | Jobs that ran | Left out by `changes` | Runner minutes | To "All checks passed" |
|---|---|---|---|---|---|
| a browser test (`web/tests/e2e.spec.ts`), #22 | 43160285312 | `web`, `stack-e2e`, `install`, `backups`, `images` (2), the 3 publish builds, and `stamp`, `changes`, `versions` and the gate: 13 | `test`, `search`, `import`, `powershell`, `stack`, `upgrade`, `budget`, `monitoring`, `govulncheck` | 76 | 25 min |
| a Pester test (`powershell/tests/`), #23 | 43160289035 | `powershell` (4), `stack` (3), and the 4 that always run: 11 | everything else, the publish builds included | 40 | 21 min, up to 6 of them queued behind the other runs |
| a Go test package (`internal/zzdemo`), #24 | 43160690117 | everything but `powershell`: 21 | `powershell` | 200 | 36 min (failed on purpose, below) |
| this branch (`.github/`, `scripts/ci/`), #21 | 43160182166 | everything (`changes: 28 files changed; .github/actions/build-push/action.yml changed, which runs everything`) | none | 186 | 24 min (A76 failed, below) |
| a line in `docs/deploy.md`, #26 | 43171120071 | `stamp`, `changes`, the gate: 3 (`versions` too is left out) | every check | under 1 | 1 min, and the pull request was mergeable (`CLEAN`) |

Before, every pull request ran everything. The six pull request runs before this branch (42986822248, 43105174961, 43113284345, 43116586293, 43139437150, 43134000256) ran 25 or 26 jobs, 181-204 runner minutes, and reached "All checks passed" after 22-44 minutes; PR #10's (one Grafana dashboard) 25 jobs, 204 runner minutes, 34 minutes. A small change's wall time is now its slowest check's (the browser tests, 24 minutes, for a web change; `stack`, 11-13 minutes, for the module), and its runner time a fifth to two fifths of before, so less waits behind it when several runs start together, as these five did.

The gate's notice on #23: `Nothing these use changed (ADR 0025): test search import web stack-e2e upgrade install backups images budget monitoring govulncheck`. `changes-checks.sh` and `retry-checks.sh` passed in each run's `versions` job, and on cld01.

A push to main of documentation alone runs nothing (no push to main was made here): `changes-checks.sh` makes a throwaway repository with a commit of `SPEC.md` and `docs/x.md` (`--all-unless-docs`: every job false) and one with a dashboard as well (every job true).

**The Release workflow, dry run** (on `ci-speed`, run 43166175670): `Version 1.7.9 is higher than the previous release, v1.7.8.`, every check ran (the changes job said all), 31 jobs succeeded and the 2 that a dry run skips were skipped (the client's manifest and the module), and `dry run: would tag 855a9b0… v1.7.9, create the release "PagerArchive v1.7.9" (not yet the latest)`. Nothing was tagged or published. It also shows the called CI accepts the `actions: read` the Release workflow now grants.

**Layer 1** (a throwaway branch's own workflow, run 43160365407):

```
retry: made-up apt-get update failed with exit status 100 on try 1 of 3; trying again in 2 s
##[warning]flaky: made-up apt-get update (passed on try 2 of 3)
retry: always fails failed on every try (3), the last with exit status 7
##[error]Process completed with exit code 7.
##[notice]an annotation written to standard error
##[error]Failed to CreateArtifact: Received non-retryable error: Failed request: (409) Conflict: an artifact with this name already exists on the workflow run
upload-artifact failed on try 1 of 3; trying again in 15 s
##[warning]flaky: upload-artifact demo-1 (passed on try 2 of 3)
```

The upload's first try was made to fail by uploading one name twice; the retry, with `overwrite: true`, passed. The annotation on standard error shows `retry.sh` can report from a step whose output is captured. `pull-images.sh` pulled the client's base images in the same run.

**Layer 2:**

- **Playwright** (#22, a test that fails on its first try in each browser): `✘ 59 [chromium] › … zz throwaway: fails on its first try only`, then `✓ 60 … (retry #1)`, the same in Firefox and WebKit, then `##[warning]flaky: chromium › e2e.spec.ts › zz throwaway: fails on its first try only (failed 1 time(s), then passed on try 2)` and Firefox's and WebKit's. The step passed, and the report (`playwright-report-43160285312-1`, 44.7 MB, with the failed tries' traces) was uploaded because of the flaky tests.
- **Pester** (#23, a test that fails the first time in a job): in each of the 4 jobs, `Trying the failed test(s) once more: zz throwaway.fails the first time only`, `Tried again: passed 1, failed 0, …`, `##[warning]flaky: Pester: zz throwaway.fails the first time only (PowerShell 7.6.0 on Win32NT): failed, then passed when run again` (and 7.7.0, and Unix). By hand, with a test that fails every time as well: `Tried again: passed 1, failed 1`, exit 1.
- **Go** (#24: `TestFailsOnce` fails the first time in a job, `TestFailsInAttempt1` in a run's first attempt): gotestsum re-ran both (`=== FAIL: internal/zzdemo TestFailsInAttempt1 (re-run 1)`, `DONE 2 runs, 461 tests, 33 skipped, 3 failures`) and the job failed, as it should. Its flaky report didn't run in that attempt: GitHub runs bash with `-e` and `pipefail`, so the failed pipeline ended the script first. Fixed here (`|| status=$?`). In the re-run (attempt 2) the test job passed with `##[warning]flaky: go test github.com/RIKIKU/PagerArchive/internal/zzdemo.TestFailsOnce (failed, then passed when run again)`.

**Layer 3.** The CI retry workflow runs from main's copy only, so it can't run before this merges. Its decision (`rerun-decision.sh`) was run by hand against real runs, read-only, and its `gh run rerun --failed` by hand where it said yes:

```
43133795198 (#18 on main, A15's idle CPU 5.01%), as it is:  rerun=false  why=main is now at 325daab…, not 4711439…: a newer run has it
the same, as if its commit were still main's newest:         rerun=true   jobs=Resource budget, short run (A15), telemetry off
43094093703 (#14 on main, 6 uploads failed), likewise:       rerun=true   jobs=Backups (A80); Resource budget, short run (A15), telemetry off; Example monitoring stack (…); Section 11 steps 2 and 3 in a browser, on the compose stack; Importing messages (A76); Resource budget, short run (A15), telemetry on
43108950897 (renovate/base-images, the Gallery's 403s):      rerun=false  why=renovate/base-images is now at (gone), not …
43160182166 (#21, this branch, A76 failed, below):            rerun=true   jobs=Importing messages (A76)
43160690117 (#24, TestFailsInAttempt1):                       rerun=true   jobs=Tests, golden tables and acceptance checks
```

Run on real runs, it showed two faults, fixed before these: a job's name with commas in it was split in the list of jobs, and a deleted branch's 404 answer was taken for its newest commit.

- **#21's run, re-run** (`gh run rerun 43160182166 --failed`): attempt 2 passed, and its "All checks passed" said `flaky: job Importing messages (A76) (failed in attempt 1, passed in attempt 2)`.
- **#24's run, re-run** (`gh run rerun 43160690117 --failed`): attempt 2 passed, and "All checks passed" said `flaky: job Tests, golden tables and acceptance checks (failed in attempt 1, passed in attempt 2)`, beside `Nothing these use changed (ADR 0025): powershell`.
- `retry-checks.sh` covers the rest with a stand-in for `gh`: a second attempt, a manual run, a moved or deleted branch, the versions and govulncheck jobs, the gofmt and svelte-check steps, a gate that failed alone, and the security and privacy findings (`a33_test.go:255: SECURITY FAILURE: …` and `page text or an email address found in the API log` aren't re-run; a passing run's logged `failures ["SECURITY FAILURE: …"]` doesn't count).

**A flake seen on this branch.** #21's first attempt failed in A76's file checks, which this branch doesn't change: `import_test.go:495: the API didn't come back`, two minutes after the test's `docker compose kill -s SIGKILL api` and `start api`. It passed in the re-run. Recorded here to be looked into separately (the job keeps no API log for these checks).

**A82.**

- **The fault, made to happen:** a copy of A82 (on cld01, not committed) that, after Back, hovers Clear all and holds the fade with the Web Animations API, then scans as before. Held 280 ms in, the tooltip's opacity was 0.513 and the scan said exactly what CI's did, in Chromium and Firefox: `["color-contrast: .clear > .tooltip[aria-hidden=\"true\"]"]`; `axeOf`, once the fade could finish: `[]`. Held at 300 and 320 ms (opacity 0.80 and 0.94) the old scan passed: the window is narrow, which is why it failed only once.
- **Before:** the old test 30 times in Firefox on cld01 (10, then 20 with `stress-ng --cpu 8`): 29 passed, and one failed writing Playwright's trace under the load (`file data stream has unexpected number of bytes`), not on axe. It didn't fail on its own in that many tries.
- **After:** with `CI=true` (so with the retry) and `stress-ng --cpu 8`: Firefox `20 passed (17.2m)`, Chromium and WebKit `10 passed (6.8m)`, none flaky (no `retry #1`). `svelte-check found 0 errors and 0 warnings`; `check-tokens` clean.

**govulncheck** (v1.9.0, database of 2026-11-11, Go 1.27.4):

- In CI on this branch (#21): `called with a fix: 0; called, no fix: 0; imported only: 0; required only: 0`. Nothing today.
- On main before #18 and #19 (3f984ed, in Docker on cld01): 5 vulnerabilities the code calls in golang.org/x/net v0.59.0, fixed in v0.60.0 (GO-2026-6603, -6610, -6611, -6612, -6617, all HTTP/2), which would have failed the job, and 2 only required (GO-2026-6860, -6866, html, fixed in v0.62.0). Nothing for go-jose, which OSV had flagged.
- With cld01's own Go 1.27.2 it reports the standard library's GO-2026-6809 and GO-2026-6859 as called (fixed in 1.27.3 and 1.27.4). CI's Go, from `go.mod`'s `go 1.27`, is the newest patch, so it doesn't.

**Static checks.** `actionlint` 1.7.12 with shellcheck (`rhysd/actionlint:1.7.12`) on every workflow: no findings. `shellcheck` 0.11.0 on the new scripts: none. `go test .` (the stamp and R84 tests, with `changes` added to the jobs that don't stamp): ok. `gofmt -l`: nothing.

## Consequences

- A typical small pull request runs a fraction of the jobs. A Go change still runs nearly everything, as it should.
- The table must be kept up to date. A new file or folder with no area runs everything, which is safe but slow, and `changes-checks.sh` warns about it.
- A gap in the table can let a pull request merge that fails on main (main runs everything); a release runs everything first, so a gap never reaches a release.
- A flake no longer turns a run red by itself, but every one is a warning on the run: they're there to be fixed, not ignored.
- A security fix waits 30 minutes after its merge before the Release workflow starts.
- govulncheck can fail a run for a vulnerability published after the change was written.
