# ADR 0024: Merge freely, release deliberately

## Status

Proposed (2026-11-12). The owner's decision of 2026-11-12, "merge freely, release deliberately": a merge to main is no longer a release; a Release workflow cuts one every Saturday, on "Release now", and straight away for a security update. On the branch `release-process` from main at 59be943 (VERSION 1.7.5, whose release is the last one cut by the old process). Supersedes ADR 0016's decisions 1 to 4 and 6 where they say every merge is a release and a branch sets `VERSION`, and ADR 0022's decisions 3 and 4 (an update raises `VERSION`); the rest of both stands. The PowerShell module doesn't change, so its version stays.

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
- **A security update:** `.github/workflows/security-release.yml` runs when a pull request into main closes; if it merged and has the label `security`, it starts the Release workflow on main (`gh workflow run release.yml --ref main`, with `actions: write`). A dispatch made with `GITHUB_TOKEN` starts a run, GitHub's one exception with `repository_dispatch`. It's its own workflow because a release run that waits holds the release workflow's concurrency group, and a pending run there is replaced by the next one: a run for every closed pull request would replace a waiting release.
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

## This branch's own version

`VERSION` goes from 1.7.5 to `0.0.0-dev` and `compose.yaml`'s image from `:1.7` to `:main`, as the new `versions` check wants; nothing is raised by hand. Merging it publishes `main` and `sha-<commit>` only. The first release after it is cut by the Release workflow ("Release now", by the owner): with v1.7.5 the previous release it's **1.7.6**, a patch, as nothing a deployment downloads changes here other than our image's line.

## Evidence

(Below.)

## Consequences

- Library and base image updates merge as before but reach deployments once a week, or at once if they fix a known vulnerability.
- No version races: pull requests never set a version, so two open at once don't conflict.
- A tag's own tree isn't stamped; the release's images and attached files are.
- The schedule needs nothing from the owner, but GitHub turns off a public repository's scheduled workflows after 60 days without activity; any push turns them back on.
- A release on Saturday runs the whole suite again on a commit main's CI already ran (about an hour of runners, once a week).

## Questions for the owner

1. **OSV alerts on:** fine, or only GitHub's alerts (which would need turning on, and the app's "Dependabot alerts: read" permission)?
2. **Saturday 05:17 (daylight saving) or 04:17, your time:** fine, or another time?
