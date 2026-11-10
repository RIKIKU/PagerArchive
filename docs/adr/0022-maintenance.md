# ADR 0022: Maintenance (M18)

## Status

Status: Accepted (owner, 2026-11-09), with the review's changes below. The owner's brief for M18 of 2026-11-09: SPEC.md section 7.23 (R85-R85c), R86d and A79, on the branch `m18` from main at 2d277f9 (v0.17.2). To be released as 0.18.0. The API, client, search and import don't change, apart from three new backup metrics. **The PowerShell module doesn't change**, so its version stays.

## Context

Every merge to main is a release (12.1, ADR 0016), and CI fails a branch whose `VERSION` isn't higher than main's (`check-versions.sh`). A patch version may not change `compose.yaml` or the configuration archive (R84b). M18 adds three kinds of automatic update on top of that: base images daily (R85), libraries monthly (R85a) and third-party images weekly (R85b), each raising `VERSION` and passing every check (R85c). Four things in GitHub and in the existing rules decide how.

1. **`GITHUB_TOKEN` starts nothing.** A push or a merge made with a workflow's own token starts no other workflow. An update pushed that way would get no CI, and a merge made that way would never run main's CI, so nothing would be released.
2. **Update tools don't raise `VERSION`,** and two automatic pull requests open at once both want the same next version.
3. **Updates to `compose.yaml` can't be patches,** by `check-versions.sh`'s rule (R84b).
4. **"The owner is told"** without a new service.

## Decisions

### 1. Renovate, run in a workflow with a GitHub App's token

`.github/workflows/maintenance.yml` runs Renovate (`renovatebot/github-action`, Renovate 44.222.4 pinned) daily at 03:17 UTC and after every push to main. Its token comes from a GitHub App the owner creates (`actions/create-github-app-token`). The rules are in `.github/renovate.json5`.

- **Why an App's token:** pushes and pull requests made with it start workflows, so CI runs on each update's pull request; GitHub's auto-merge, turned on with it, makes a merge that starts main's CI and so the release. The token lasts an hour and reaches this repository only. A fine-grained personal token would also start workflows, but it acts as the owner, expires on a date someone has to remember, and makes every update look like the owner's own work.
- **Why Renovate run here, not Dependabot or Renovate's hosted app:** an update has to run a command (raise `VERSION`, decision 3). Dependabot runs none, and the hosted Renovate app doesn't run `postUpgradeTasks`. Renovate run here does, limited by `RENOVATE_ALLOWED_COMMANDS` to exactly `sh scripts/ci/bump-version.sh`. Renovate also covers every file in the brief: each `FROM` (with digests), both compose files, CI's service and container images, Go modules, npm and Actions.
- **Commits** go through GitHub's API (`platformCommit`), so they're the app's and verified.
- **Without the app's secrets** the workflow fails with a message saying which is missing, so a missing setup can't pass silently.

### 2. What opens, when, and what merges on its own

Each rule's `schedule` says on which day a pull request may open; the workflow runs daily, so that's the day it opens. A pull request that already exists is rebased on any day.

| R | What | When | Pull requests | Merges |
|---|---|---|---|---|
| R85 | Every `FROM` in `Dockerfile`, `Dockerfile.client`, `scripts/test-receiver/Dockerfile`, build stages included (`node`, `golang`, `distroless`, `debian`): a new digest or patch | Daily | One, "base images" | On its own |
| R85a | Go modules, npm packages, Actions (pinned by digest; a moved tag is a digest update) and Renovate's own version, each 3 days after its release; Playwright's image in CI with its npm package: patches | The 1st | One, "libraries patch" | On its own |
| R85a | The same, 3 days after release, base images, the runner images and Node (even, long-term support majors only): minor and major | The 1st | One, "libraries and base images minor and major" | Waits for the owner |
| R85b | Third-party images in `compose.yaml`, the monitoring stack's `compose.yaml`, `deploy/test/compose.yaml` and CI: a new minor or major | Mondays | One per image | Waits for the owner |
| R85b | Images named by an exact version (Alloy, Tempo, Prometheus, Mailpit; ADR 0019): a patch | Mondays | One for all four, apart from any new minor | On its own, as a minor release |

- **Every `FROM` is now pinned by digest** (`golang:1.27.2@sha256:8201...`), so a base image rebuilt under the same tag, often for a security fix, is a change Renovate sees. `distroless/static-debian13:nonroot` and `debian:trixie-slim` have no version in their tags, so only their digests move. `golang:1.27.2` is already behind 1.27.3: the first run opens that, in "base images".
- **PostGIS** (`18-3.6`) is read as PostgreSQL major, PostGIS minor and patch, and every update to it waits, as either part can need "Upgrade steps".
- **`separateMinorPatch`** is on, so a patch is never held back by a newer minor: Prometheus 3.15.0 with 3.15.1 and 3.16.0 out gets a pull request for each, and only the first merges on its own. Without it, Renovate offered only 3.16.0 (seen in the dry run). The group names are fixed by `group.branchTopic`, as Renovate otherwise splits a group into `patch-`, minor and `major-` branches.
- **Our own image** in `compose.yaml` is left to `bump-version.sh`.
- Each update's commit says which it is: "Merges on its own once every check passes" or "Waits for the owner's review". Waiting ones are assigned to the owner when they open.

### 3. `VERSION`: raised by the update, again on every rebase

`scripts/ci/bump-version.sh` runs on each update's files before Renovate commits them. It reads the base's `VERSION` and writes the next one, by `check-versions.sh`'s own rule:

- nothing a deployment downloads changed (`CONFIG_PATHS`): the next patch;
- `compose.yaml` or the archive changed: the next minor, with the API's image in `compose.yaml` renamed to it (decision 4).

Renovate rebases a pull request whose base has moved (`rebaseWhen: behind-base-branch`) by making it again from the new main, so the script runs again there. **Two at once:** both start as 0.18.1. The first merges and main is 0.18.1. The second can't merge while it's behind main (the ruleset, decision 5); the workflow, run by that push to main, rebases it, and it becomes 0.18.2 and runs CI again. Merged without the rebase, it would have failed main's release plan (0.18.1 isn't higher than 0.18.1), and `check-versions.sh` fails it first. If the owner pushes to an update's branch, Renovate stops rebasing it, and its `VERSION` is theirs to keep right.

`scripts/ci/bump-version-checks.sh`, in CI's versions job, does both cases and the two-at-once case in a throwaway clone.

### 4. Updates to `compose.yaml` are minor versions, the automatic ones included

R85b's pull requests change `compose.yaml` or the monitoring stack's, so they're minor versions: 0.19.0 for Authentik 2026.8, with `compose.yaml` naming `pagerarchive:0.19`. They wait for the owner, who may change the number. The exact-version images' patches (Alloy, Tempo, Prometheus) merge on their own and still change the monitoring stack's `compose.yaml`, so **they're automatic minor releases**: a deployment needs the new file for them, which a patch release mustn't need (R84b). Mailpit is only in `deploy/test/compose.yaml`, which no deployment downloads, so its patches are patches. This is a question for the owner below.

### 5. Merging: GitHub's auto-merge and one required check

`platformAutomerge`: Renovate turns on GitHub's auto-merge for the pull requests that merge on their own, and GitHub merges once the required check passes. CI gets a last job, **"All checks passed"**, which needs every check job and fails unless all of them succeeded ("versions" is skipped on main only). Main's ruleset requires that one check, with the branch up to date with main. So an automatic update merges only after every check (R85c), and never from behind main (decision 3). One required check, rather than each job's name, stays right when jobs are added.

CI on `renovate/**` branches runs on their pull requests only: the push of the same commit would run everything a second time.

### 6. The owner is told: the pull request is assigned to them

`.github/workflows/maintenance-failed.yml` runs when CI completes; on a failure on a `renovate/` branch's pull request, `scripts/ci/tell-owner.sh` assigns the pull request to the repository's owner, labels it "ci failed" and comments with the run's link. GitHub notifies the assignee by email and in the app; nothing new is installed. The pull request stays open: its required check failed, so GitHub won't merge it. A re-run or a newer update that passes every check may still merge it, which is what R85c asks. `scripts/ci/tell-owner-checks.sh`, in CI, checks the script against a stand-in for `gh`.

### 7. R86d: three numbers for each night, and a dashboard

The backup job already logged each night's dump time, check time and size; `backup.sh` now also writes the two times to the status file, and the API reports the last good night's three numbers:

- `pagerarchive_backup_last_good_bytes`
- `pagerarchive_backup_last_good_dump_seconds`
- `pagerarchive_backup_last_good_check_seconds`

The example monitoring stack's fourth dashboard, **PagerArchive: backups** (`pa-backups`), shows the time since the last good backup (green, orange after 26 hours, red after 36, when "Backup failing" fires), whether the last one is failing, and the three numbers over 30 days, so the check's growth with the archive shows. A80 checks the metrics against the files and the log line; the monitoring check's A80 part runs each panel's query as Grafana does, after a good backup and after a failed one.

## A79: what was run

The owner hasn't set up a test repository, so each clause was run as a **dry run**: Renovate 44.222.4 on cld01 with `dryRun: full` against this repository on GitHub, which reads and creates nothing. Its base was `m18`, and, for the two cases with no live example today (a digest-only change, and an exact version with a patch out), a branch `a79-dry-run` with `node`'s digest set to zeros and Prometheus and Alloy one patch back, pushed with `[skip ci]` and deleted afterwards. The rules were this file's with the schedules removed, so every pull request shows on one day; with the schedules, on Sunday 2026-11-08 UTC only "base images" was due and the others said "Skipping branch creation as not within schedule". Nothing was merged anywhere: the two pull requests that merge on their own can only be seen to merge once the app exists.

| A79 clause | Seen |
|---|---|
| A base image's digest changes | `renovate/base-images`: `node 24-slim@000000000000 -> 24-slim@01d566e8d94d (digest)` and `golang 1.27.2 -> 1.27.3 (patch)` in all three Dockerfiles; "Merges on its own"; `bump-version: 0.18.0 to 0.18.1, a patch` |
| A library patch and another major | `renovate/libraries-patch` (vite 8.3.6, svelte 5.57.3, Playwright 1.64.1 with its image...; merges on its own; 0.18.1) and `renovate/libraries-and-base-images-minor-and-major` (pgx 5.12, TypeScript 7, `actions/checkout` v7...; waits) |
| A patch-only pull request fails CI | `tell-owner-checks.sh`: assigned to the owner, labelled, commented; the required check (decision 5) keeps it open |
| A third-party image's new minor | `renovate/ghcr.io-goauthentik-server-2026.x`: `2026.5 -> 2026.8 (minor)`; waits; 0.19.0 with `compose.yaml` naming 0.19 |
| An exact version's new patch | `renovate/prom-prometheus-3.15.x`: `v3.15.0 -> v3.15.1 (patch)`, merges on its own (0.19.0, decision 4), beside `renovate/prom-prometheus-3.x` (3.16.0, waits) |
| Two at once | `bump-version-checks.sh`: the second fails `check-versions.sh` until rebased, then 0.17.4 (0.17.3 on main) passes |

What only the first real runs can show: that GitHub's auto-merge, turned on with the app's token, merges and then starts main's CI and the release. The golang 1.27.3 pull request is that run, and the release after it is the evidence.

## The owner's setup

1. **Create the app.** GitHub, Settings, Developer settings, GitHub Apps, New GitHub App. Name: "PagerArchive maintenance" (any name). Homepage URL: the repository. Webhook: untick Active. Repository permissions, as Renovate's docs list them: Contents, Pull requests, Issues, Workflows, Checks and Commit statuses: Read and write; Administration: Read-only (Renovate reads the ruleset before turning on auto-merge); Metadata is read-only already. Where can it be installed: Only on this account. Create it.
2. **Make its key.** On the app's page, note the Client ID, then Private keys, Generate a private key. A `.pem` file downloads.
3. **Install it** (Install App, on RIKIKU), Only select repositories: PagerArchive.
4. **Add the variable and the secret.** The repository's Settings, Secrets and variables, Actions: a variable `MAINTENANCE_APP_CLIENT_ID` with the Client ID (it replaced `MAINTENANCE_APP_ID`, the App ID, which `actions/create-github-app-token` deprecated; 2026-11-10), and a secret `MAINTENANCE_APP_PRIVATE_KEY` with the whole `.pem` file. Then delete the file.
5. **Allow auto-merge.** Settings, General, Pull Requests: tick "Allow auto-merge" (and "Automatically delete head branches", so merged updates' branches go).
6. **Protect main.** Settings, Rules, Rulesets, New branch ruleset: name "main", Active, Target: the default branch. Bypass list: Repository admin (so your own merges and pushes to main work as now). Rules: "Require status checks to pass", with "Require branches to be up to date before merging" ticked, and the check **All checks passed** (from GitHub Actions; it's listed once CI has run on m18). Leave the rest off. Create.
7. **Try it.** Actions, Maintenance, Run workflow, on main, `dry_run` set to `full`: the log lists the pull requests it would open. Then run it with `dry_run` `off`, or wait for 03:17 UTC: the golang 1.27.3 pull request opens, passes CI, merges, and v1.0.1 is released.

## The owner's review (2026-11-09)

Renovate is kept, and before the merge:

1. **3 days after release** (`minimumReleaseAge`) for Go modules, npm packages and Actions, not base images. Playwright is the exception: its image in CI has no release dates, so Renovate would hold it for good while the npm package moved, and CI fails when the two differ. Renovate's own image may have none either, so a missing date doesn't hold it (`timestamp-optional`).
2. **Actions pinned by digest**, with the tag beside it (`@<commit> # v5`); new ones are pinned by Renovate (`pinDigests`).
3. **The exact versions' patches in one weekly pull request**, still merging on its own as a minor release.
4. **Runner images and Node majors** in the monthly pull request that waits, Node's even (long-term support) majors only, in CI and the Dockerfile alike (`allowedVersions`).
5. **The clients dashboard's rate panels showed no data** at the client's default: the SDK's 60 s export left one sample in a 1-minute `$__rate_interval`. The client now sends its metrics every 15 s, the data source's step, when `OTEL_METRIC_EXPORT_INTERVAL` is unset (`telemetry.MetricInterval`); setting it still wins. A67 now starts its clients with no interval set, and checks that the audio, lines read and pages decoded and sent panels each show the client, as a browser asks for them.

**Seen** (the dry run as before, on a throwaway `[skip ci]` branch, deleted): `renovate/exact-version-images-patch` holds Mailpit 1.31.6, Alloy 1.21.3 and Prometheus 3.15.1 and merges on its own; Prometheus 3.16.0 waits apart. maplibre-gl was offered 6.16.0, not 6.17.0, released inside the 3 days. The waiting pull request has `node 24 -> 26`, `node 24-slim -> 26-slim` and `ubuntu 24.04 -> 26.04`. No odd Node major is out yet; the pattern takes 24.11.1, 26.0.0, 26-slim and 28-slim, and refuses 25.2.0, 27.0.0, 27-slim and 31-slim.
