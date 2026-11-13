# ADR 0024: Merge freely, release deliberately

## Status

Proposed (2026-11-12). The owner's decision of 2026-11-12, "merge freely, release deliberately": a merge to main is no longer a release; a Release workflow cuts one every Saturday, on "Release now", and straight away for a security update. On the branch `release-process` from main at 59be943 (VERSION 1.7.5), with main merged in at 59e9069 (VERSION 1.7.6, #15: its release, v1.7.6, is the last one cut by the old process). Supersedes ADR 0016's decisions 1 to 4 and 6 where they say every merge is a release and a branch sets `VERSION`, and ADR 0022's decisions 3 and 4 (an update raises `VERSION`); the rest of both stands. The PowerShell module doesn't change, so its version stays. Amended the same day (ADR 0025): security updates that merge within 30 minutes of each other go out as one release (the section of that date).

## Context

Since ADR 0016 every merge to main has been a release, with its version in `VERSION`, raised by the branch (or by `bump-version.sh` for Renovate's updates, ADR 0022). Renovate's pull requests made the numbers climb fast (1.1 to 1.7 in two days), every merge ran about 21 jobs and then published, two pull requests open at once raced for the next number, and a release reached deployments for every library patch.

Main is protected by the "Main" ruleset: the required check "All checks passed", with the branch up to date (strict); no deletion; no force push. Only a repository admin bypasses it. No ruleset covers tags. So a workflow can't push a commit to main (a commit that raises `VERSION` would need the check on itself first, and main moves), but it can push a tag.

## Decisions

### 1. A release's version lives in its tag, not in the repository

The release workflow commits nothing. It tags the commit it checked, `v<version>`, and writes the version into the build instead:

- **`VERSION` says `0.0.0-dev`** in the repository, and **`compose.yaml` names our image `ghcr.io/rikiku/pagerarchive:main`**. Both are placeholders that are still true: a checkout's own build isn't a release, and main's `compose.yaml` runs main's image.
- **`scripts/ci/stamp-version.sh <version>`** writes a version into `VERSION` and its `MAJOR.MINOR` into `compose.yaml`'s image line, in the job's checkout, before anything is built or tested. Every CI job that checks out the code runs it straight after the checkout (a Go test, `TestEveryCIJobStampsTheVersion`, fails a job that doesn't), apart from `versions` (which checks the files are unstamped), the PowerShell jobs (the module has its own version) and `stamp` itself. So the images report the version, the attached `compose.yaml` names the release's `MAJOR.MINOR` as R84 wants, and the checks run on exactly what's published.
- **Why not a bot commit:** it needs a ruleset bypass for the workflow (or a GitHub App), and a race with main moving while the checks run (strict). Tagging the commit the checks ran needs neither. No ruleset or setting changes.
- **The cost:** a tag's tree says `0.0.0-dev`, so building from a tag by hand reports that, unless stamped first (`sh scripts/ci/stamp-version.sh 1.7.6`). The release's images and files are the stamped ones.

`version.go` still embeds `VERSION`; `version_test.go` accepts `0.0.0-dev`, `MAJOR.MINOR.PATCH` or `MAJOR.MINOR.PATCH-dev+<commit>`. `deploy_test.go`'s A79 and R84 checks want `:main` in the unstamped `compose.yaml` and the version's `MAJOR.MINOR` in a stamped one. A77, A78 and A80 take our image's tag from `compose.yaml` instead of working it out from `VERSION`.

### 2. A build that isn't a release reports `<next>-dev+<commit>`

A merge to main, a pull request and a manual run report, for example, `1.7.6-dev+3f2c1ab`: the version a release of that commit would get (decision 3), then `-dev+` and the short commit (`scripts/ci/release-version.sh --dev`). The `stamp` job works it out once per run, from the full history and tags, and every job stamps the same.

**Why this and not `<last release>+<commit>`:** semantic versioning orders `1.7.6-dev+…` before 1.7.6 and after 1.7.5, which is where the build is. `1.7.5+3f2c1ab` compares equal to 1.7.5 (build metadata doesn't count), so a main build could be taken for the release it came after. "-dev" says it in words too. Docker tags can't hold `+`, which doesn't matter: main's images are tagged `main` and `sha-<commit>`, never by version.

It shows wherever the version did: `pagerarchive version`, `pagerarchive-client --version`, the start-up log lines' `version`, `pagerarchive_build_info{version=…}` and telemetry's `service.version`. The `images` job checks both images report the stamp job's version.

### 3. What's released, and as which version

`scripts/ci/release-version.sh <commit>` decides, from the **previous release**: the newest `v*` tag in the commit's history, other than one on the commit itself (as ADR 0016).

- **Nothing to release** if nothing changed since it, or only documentation did: what CI's docs-only filter ignores (`SPEC.md`, `docs/**`, other Markdown, apart from `web/static/**`, built into the server). Also if a release on a later commit already has this one (a run that waited behind a newer release), or if this commit's own tag is the release GitHub marks Latest (released already).
- **The next minor version** if a file a deployment downloads changed (`CONFIG_PATHS`: `compose.yaml`, the monitoring stack's `compose.yaml` and the configuration archive's folders), as ADR 0016's rule had it: a patch release needs only pull and up (R84b). A change to our image's line in `compose.yaml` alone doesn't count (`git diff -I`), as a release stamps it: so the first release after this branch, whose `compose.yaml` went from `:1.7` to `:main`, is a patch.
- **The next patch version** otherwise.
- **This commit's own tag, not yet the Latest release:** a release that failed partway; the run finishes it as the same version (as "Re-run all jobs" did under ADR 0016).

There's no major version: a new major is the owner's decision, made by tagging by hand, which nothing does today.

`scripts/ci/release-version-checks.sh`, in the `versions` job, checks every case in a throwaway clone with made-up tags (below).

### 4. The Release workflow

`.github/workflows/release.yml`:

- **Saturday:** `cron: "17 18 * * 5"`, 18:17 UTC on Friday: 05:17 on Saturday in the owner's time zone with daylight saving (UTC+11) and 04:17 without (UTC+10). Both are before the owner's weekly upgrade at 09:00, with time for GitHub starting a scheduled run late (it does, by tens of minutes at busy times, which is also why it's off the hour) and for the checks (about an hour). Cron is UTC only, so the hour in the owner's time zone moves with daylight saving.
- **"Release now":** Actions, Release, Run workflow, on main. Off main it fails at once unless "Dry run" is ticked.
- **A security update:** `.github/workflows/security-release.yml` runs when a pull request into main closes; if it merged and has the label `security`, it starts the Release workflow on main (`gh workflow run release.yml --ref main`, with `actions: write`). A dispatch made with `GITHUB_TOKEN` starts a run, GitHub's one exception with `repository_dispatch`. It's its own workflow because a release run that waits holds the release workflow's concurrency group, and a pending run there is replaced by the next one: a run for every closed pull request would replace a waiting release. Since 2026-11-12 it starts the Release workflow 30 minutes after the last security merge (the section of that date below).
- **Every trigger releases main's newest commit** (`GITHUB_SHA`: the default branch's head for the schedule, the chosen branch's for a dispatch), under the rules of decision 3. "Release now" with nothing changed says "nothing to release" too.
- **One at a time:** `concurrency: {group: release, cancel-in-progress: false}`. A second run waits for the first; when it starts, a release on its commit or a later one means nothing to release. A third replaces a second still waiting (GitHub keeps one pending run), which releases the same or a newer commit.
- **The jobs:** `plan` (the version, from `release-version.sh`, with the Latest release from GitHub's API) and, if there's something to release, `checks`: `ci.yml` as a called workflow (`workflow_call`) with `release: <version>` and `dry_run`, `permissions: contents: write, packages: write` and `secrets: inherit`. So the release runs every check in `ci.yml`, on the commit it releases, and its release jobs are ci.yml's: the same `release`, `publish-server`, `publish-client`, `publish-client-manifest`, `attach-release-files` and `publish-powershell` jobs ADR 0016 and ADR 0019 describe, now run only when the `stamp` job's mode is `release` (or `dry-run`) instead of on a push to main. Their order and checks are as before: tag and GitHub release (not Latest), then images, then the 8 files attached (the client's two binaries, `SHA256SUMS` and R84's five), then Latest; the module if the plan says so; GitHub's generated notes, from the previous release (`--notes-start-tag`).
- **Dry run** (`dry_run`, any branch): everything but tag and publish. The version, every check, the client's binaries and checksums, the release's notes with GitHub's generated part (`POST /releases/generate-notes`, which creates nothing), the images built but not pushed (the login steps are skipped, `push: false`), the release's files made and listed, and the tags they'd get. The tag, release, upload, Latest, Gallery and GHCR steps are skipped.

### 5. What a run of CI publishes

The `stamp` job also gives the run's mode, which the publish jobs follow:

| Run | Mode | Images |
|---|---|---|
| The Release workflow's | `release` | `<version>`, `MAJOR.MINOR`, `latest` |
| Its dry run | `dry-run` | built, not pushed |
| A push to main | `main` | `main`, `sha-<commit>` |
| A manual run of CI, any branch | `sha` | `sha-<commit>` |
| A pull request | `none` | built, not pushed |

`latest` is now the latest release, not main's newest build, and a release doesn't move `main` or `sha-<commit>`: main's run already published that commit as a build of main, with its own `-dev` version. CI's concurrency: a pull request's runs still cancel each other; every other run (main's, a manual one, a release's) has a group of its own.

### 6. Pull requests don't touch VERSION

- **`versions` job** (now on every run, main's included, so "All checks passed" no longer has a skipped job to allow for): `check-versions.sh` fails a branch whose `VERSION` isn't `0.0.0-dev` or whose `compose.yaml` doesn't name `:main`, and says to put them back. `bump-version.sh` and `bump-version-checks.sh` are gone; `release-version-checks.sh` and `tell-owner-checks.sh` run here.
- **The module's version is still checked when a branch is**, because it protects something real: R63 publishes the module only if its `ModuleVersion` isn't on the Gallery yet, so a module change without a raise would never be published, silently. The check now compares with the **previous release**, not with main: if `powershell/PagerArchive/` changed since the previous release, `ModuleVersion` must be higher than that release's. Two branches changing the module before the same release share one raise; the second finds it raised already (under the old rule it would have had to raise again, and one version would never be published).
- **Renovate:** `postUpgradeTasks` and `RENOVATE_ALLOWED_COMMANDS` are gone: an update changes only what it updates.

### 7. Renovate's pull requests

- **Third-party images** (R85b, Mondays, waiting for the owner) are one pull request, `renovate/third-party-images`, instead of one per image, apart from **Authentik and PostGIS, which keep a pull request each** (`groupName: null` for them): they're the ones whose upgrades need the owner most. Every other rule is as it was, the exact versions' weekly patches included.
- **Vulnerability alerts:** `vulnerabilityAlerts: {addLabels: ["security"]}`, so a fix for a known vulnerability gets its own pull request at once, whatever the schedule (Renovate's default for them), labelled `security` as well as `maintenance`. Its merge starts a release (decision 4). `osvVulnerabilityAlerts: true` adds the OSV database (Go modules, npm packages) as a source, as GitHub's own alerts are off on this repository (`GET /repos/RIKIKU/PagerArchive/vulnerability-alerts` answers 404) and the app would also need the "Dependabot alerts" permission to read them.
- `rebaseWhen: behind-base-branch` stays: the ruleset still merges only a branch that's up to date.

### 8. A77 upgrades from the latest release

A77 now starts from the release GitHub marks Latest, straight from GitHub (`releases/latest`), instead of the newest tag in the branch's history not newer than it. On a pull request and on main these are the same release; it's now the rule rather than a consequence. It runs on every pull request, every build of main and every release, so a change that breaks the upgrade from what deployments run fails before a release is cut, not in it.

### 9. Small changes

- `SPEC.md`: 12.1 rewritten; R63, R85, R85a, R85b, R85c, A77 and A79 to match.
- `CLAUDE.md`'s rule "Every merge to main is a release" is now "Merge freely, release deliberately": leave `VERSION` and our image in `compose.yaml` alone; raise `ModuleVersion` once per release when the module changes.
- `docs/deploy.md`: the image tags table (`latest` is a release; `main` reports a `-dev` version), and when releases come out. `docs/client.md`: the same for the client's image. `docs/powershell-guidelines.md`: the module's raise is against the previous release.
- `release-plan.sh` takes the version as an argument (it used to read `VERSION`) and outputs `previous`, where the generated notes start.

## Security updates close together: one release (owner, 2026-11-12)

On 2026-11-12 two security pull requests (#18, go-jose; #19, golang.org/x/net) merged 18 minutes apart, and each started a release: v1.7.7 and v1.7.8, one with each fix. Two releases means two upgrades for a deployment, and the first went out without the second fix.

- **`security-release.yml` waits 30 minutes** after a security pull request merges (a `sleep 1800` step), then starts the Release workflow on main as before. The release takes main's newest commit when it starts, so it has every merge up to then.
- **A second security merge inside those 30 minutes starts the wait again:** the workflow's concurrency group, `security-release`, has `cancel-in-progress: true`, so the second merge's run cancels the first one's wait and waits 30 minutes itself. Both fixes go out in the one release it starts. A steady stream of merges less than 30 minutes apart would keep postponing it; Renovate opens its vulnerability pull requests together, so a burst is the case, not a stream.
- **Only a merged security pull request's run joins that group.** The workflow runs for every pull request that closes, and a run of any other one would otherwise cancel the wait: its group is its own (`security-release-not-<run id>`), worked out in the `concurrency` expression from the same condition as the job's `if`.
- **30 minutes** (owner, 2026-11-12; first 15, as briefed, which would not have joined #18 and #19, 18 minutes apart). It delays every security fix by that much, on top of the release's own checks (about an hour). Merges up to 30 minutes apart go out together, so the pair that prompted this, 18 minutes apart, would have made one release (answered below).
- `gh workflow run` is retried (`scripts/ci/retry.sh`, ADR 0025), so the job checks out the repository for it.

Not run end to end: it needs labelled pull requests merged into main. `actionlint` passes; the concurrency expression's two outcomes are GitHub's documented behaviour for a group name that differs per run.

## Attestations (owner, 2026-11-13: "we should be using attestations regardless")

A release attests what it publishes with GitHub artifact attestations (`actions/attest-build-provenance`, pinned by commit; SLSA build provenance signed through Sigstore): the `release` job the client's binaries and `SHA256SUMS`, `attach-release-files` R84's five files (before attaching them), and `publish-server` and `publish-client-manifest` the images by the digest their tags name (the client's multi-arch index), with `push-to-registry` so the attestation sits beside the image on GHCR. Anyone checks a download with `gh attestation verify <file> --repo RIKIKU/PagerArchive`, or `oci://ghcr.io/rikiku/pagerarchive:<version>` for an image (docs/deploy.md, docs/client.md). Only those four jobs ask for `id-token: write` and `attestations: write`, and release.yml's `checks` job grants them, as a called workflow's jobs get no more than their caller's. Only a real release attests: a dry run publishes nothing, and an attestation is published (to the repository's attestations and Sigstore's public log), so it skips those steps; main's and manual runs' images aren't attested. No storage records (`create-storage-record: false`): GitHub keeps those for organisations' repositories only. `TestReleaseAttestsWhatItPublishes` (deploy_test.go) checks the steps, their guards and the permissions.

## This branch's own version

`VERSION` goes from 1.7.6 to `0.0.0-dev` and `compose.yaml`'s image from `:1.7` to `:main`, as the new `versions` check wants; nothing is raised by hand. Merging it publishes `main` and `sha-<commit>` only. The first release after it is cut by the Release workflow ("Release now", by the owner): with v1.7.6 the previous release (#15's, being released by the old process as this is written) it's **1.7.7**, a patch, as nothing a deployment downloads changes here other than our image's line.

## Evidence

Nothing was tagged, released or published. Afterwards `git ls-remote --tags origin 'v1.7.6*'` listed nothing, `gh release view v1.7.6` said `release not found`, `docker manifest inspect ghcr.io/rikiku/pagerarchive:1.7.6` failed (1.7.5's succeeded), and the Latest release was still v1.7.5.

**The version scripts** (`release-version-checks.sh`, on cld01 and in CI's `versions` job), with made-up releases v9.4.1 (old style, `VERSION` and `compose.yaml` committed) and v9.4.2 in a throwaway clone:

```
--- the version leaves the repository (VERSION, our image :9.4 to :main) since v9.4.1: release=true version=9.4.2 (a patch: nothing a deployment downloads changed since v9.4.1)
--- nothing changed since v9.4.2: release=false version=9.4.3 (nothing to release: nothing changed since v9.4.2)
--- documentation only (docs/, SPEC.md, README.md, deploy/made-up.md): release=false version=9.4.3 (nothing to release: only documentation changed since v9.4.2)
--- web/static/LICENSE.md (built into the server): release=true version=9.4.3 (a patch: …)
--- code: release=true version=9.4.3 (a patch: nothing a deployment downloads changed since v9.4.2)
--- only our image's tag in compose.yaml: release=false version=9.4.3 (nothing to release: …)
--- our image's tag and code: release=true version=9.4.3 (a patch: …)
--- the monitoring stack's compose.yaml: release=true version=9.5.0 (a minor version: a file a deployment downloads changed since v9.4.2)
--- the configuration archive (deploy/authentik): release=true version=9.5.0 (a minor version: …)
--- a release on a later commit (v9.4.3) has it: release=false version=9.4.3 (nothing to release: v9.4.3, on a later commit, has everything this one has)
--- its own release, v9.4.3, is the Latest: release=false version=9.4.3 (nothing to release: v9.4.3 is on this commit, and released)
--- its own release, v9.4.3, isn't the Latest yet (v9.4.2): finished: release=true version=9.4.3 (v9.4.3 is on this commit, but the Latest release is v9.4.2: finishing v9.4.3)
--- --dev, code since v9.4.2: release=true version=9.4.3-dev+fa8bca4 (…)
--- --dev, a minor change since v9.4.2: release=true version=9.5.0-dev+d073288 (…)
--- --dev on a released commit (v9.4.3): release=true version=9.4.4-dev+f18cf24 (…)
--- stamp-version.sh: VERSION 9.4.3-dev+0e9ee09, compose.yaml :9.4
--- stamp-version.sh 0.0.0-dev: the repository's own files again
--- VERSION set by hand: FAIL: VERSION is '9.4.3': set it back to 0.0.0-dev. …
--- compose.yaml set by hand: FAIL: compose.yaml's API image is ghcr.io/rikiku/pagerarchive:9.4: set it back to ghcr.io/rikiku/pagerarchive:main. …
--- the module changed, not raised: FAIL: powershell/PagerArchive/ changed since v9.4.2, but ModuleVersion is 0.12.0, which isn't higher than v9.4.2's 0.12.0: …
--- the module changed, ModuleVersion 0.12.0 to 0.13.0: Versions: OK
--- a second module change before the release: Versions: OK
--- release-plan.sh, v9.4.3 on another commit: FAIL: Tag v9.4.3 already exists, on f18cf24…: 9.4.3 was released already, and a version is never reused. Nothing is published.
Version scripts: OK
```

`go test .` (the version, A79, R84 and stamp tests) passed on both the unstamped checkout and CI's stamped ones. `actionlint` 1.7.12 on all five workflows: no findings. `renovate-config-validator --strict` (Renovate 44.222.4): `Config validated successfully`.

**The dry run** (Release, Run workflow on `release-process`, `dry_run` ticked; run 43119692373, b1d2463, with v1.7.5 tagged but not yet the Latest release): success, every job. The plan:

```
Commit b1d246373916dde868cac2d309a67b9d101deaa0 (refs/heads/release-process); the Latest release is v1.7.4
version=1.7.6  major_minor=1.7  tag=v1.7.6  previous=v1.7.5
why=a patch: nothing a deployment downloads changed since v1.7.5
##[notice]Dry run: v1.7.6 would be released (…). Nothing is tagged or published.
```

then the stamp job `mode=dry-run publish=false`, and all 23 check jobs passed. Every job logged `stamp-version: VERSION 1.7.6, compose.yaml names ghcr.io/rikiku/pagerarchive:1.7`; the images reported `pagerarchive 1.7.6` and `pagerarchive-client 1.7.6`. A77: `upgrading this build, 1.7.6, from the latest release, v1.7.4`, `running: pagerarchive 1.7.4`, then `1.7.6`, then the patch `1.7.99`, `A77 passed`. The release job: `Version 1.7.6 is higher than the previous release, v1.7.5.`, `module unchanged: the Gallery keeps 0.12.0`, the two binaries and `SHA256SUMS`, the notes with GitHub's generated part (`**Full Changelog**: …/compare/v1.7.5...v1.7.6`, no pull requests yet as the branch isn't merged), and `dry run: would tag b1d2463… v1.7.6, create the release "PagerArchive v1.7.6" (not yet the latest)`; the tag and release steps were skipped. `publish-server`: `mode: dry-run; published: false` with `ghcr.io/rikiku/pagerarchive:1.7.6`, `:1.7`, `:latest`; the client's manifest job and the module's were skipped. `attach-release-files`: `would attach these to v1.7.6`, R84's five files, and `its compose.yaml: image: ghcr.io/rikiku/pagerarchive:1.7`.

**The guard off main:** a throwaway branch, `release-process-register`, with a `push` trigger added to `release.yml` (which also registered the workflow, so it could be dispatched before it's on main; the branch is deleted): run 43119663146 failed in its first step, `A release is made from main only, and this run is on refs/heads/release-process-register. Off main, tick "Dry run".`

**This pull request's CI** (run 43119773469, b1d2463): success, all 26 jobs that run on a pull request; the release jobs, the client's manifest and the module were skipped. The stamp job gave `version=1.7.6-dev+5fd2100 mode=none publish=false` for the pull request's merge commit, the images reported `pagerarchive 1.7.6-dev+5fd2100` and `pagerarchive-client 1.7.6-dev+5fd2100`, and `publish-server` said `mode: none; published: false` with `ghcr.io/rikiku/pagerarchive:sha-5fd2100` only.

**Renovate's grouping**, run locally on cld01 (`--platform=local`, a lookup only, nothing pushed) on a copy with the schedules removed and older versions of five images put in: Tempo 3.1.1, Loki 3.7, Prometheus 3.16.0 and Grafana 13.3 went in `renovate/third-party-images`; Authentik in `renovate/ghcr.io-goauthentik-server-2026.x`; PostGIS (three services) in `renovate/postgis-postgis-18.3.x`; the Tempo and Prometheus patches still in `renovate/exact-version-images-patch`; Mailpit (the test stack) still in the libraries' pull requests. With OSV on, the same run found two vulnerability branches today: `renovate/go-golang.org-x-net-vulnerability` and `renovate/go-github.com-go-jose-go-jose-v4-vulnerability`. A local run can't show labels; the `security` label is Renovate's documented `vulnerabilityAlerts` behaviour.

Not run: the security trigger end to end (it needs a labelled pull request merged into main), and a real release, which the owner starts after the merge.

## Consequences

- Library and base image updates merge as before but reach deployments once a week, or at once if they fix a known vulnerability.
- No version races: pull requests never set a version, so two open at once don't conflict.
- A tag's own tree isn't stamped; the release's images and attached files are.
- The schedule needs nothing from the owner, but GitHub turns off a public repository's scheduled workflows after 60 days without activity; any push turns them back on.
- A release on Saturday runs the whole suite again on a commit main's CI already ran (about an hour of runners, once a week).
- The first Renovate run after the merge opens two security pull requests (golang.org/x/net, go-jose; OSV), at once; each merge releases.
- A failed scheduled run is emailed to whoever last changed the cron line (GitHub's rule for scheduled workflows).

## Questions for the owner

1. **OSV alerts on:** fine, or only GitHub's alerts (which would need turning on, and the app's "Dependabot alerts: read" permission)?
2. **Saturday 05:17 (daylight saving) or 04:17, your time:** fine, or another time?
3. **Security releases' wait** (2026-11-12): 15 minutes, as briefed, joins security merges up to 15 minutes apart. #18 and #19 were 18 minutes apart, so they'd still have been two releases. 20 or 30 minutes instead? **Answered (owner, 2026-11-12): 30 minutes.**
