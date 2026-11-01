# ADR 0016: Every merge to main is a release

## Status

Proposed (2026-11-01). The owner's brief: releases on merge, SPEC.md R63's "Guidelines and publishing" bullet and section 12.1 (owner, 2026-11-01), as a fix on its own branch, `releases-on-merge`, from main at 1f41c38. Revised after the owner's review of 21cdc17 (2026-11-02; main 7008d55, which changed section 12.1): see "Review (owner, 2026-11-02)".

## Context

Until now a release was a `v*` tag pushed by hand (v0.12.0 is the last). The tag's run published the images as `X.Y.Z` and `X.Y`, attached the client's binaries to a GitHub release, and published the PowerShell module with the tag's version, written into a copy of the manifest. So the manifest in the repo never changed: it still said `ModuleVersion = '0.9.0'`. Every push to main published images as `latest`, `main` and `sha-<commit>` but no version.

Section 12.1 makes every merge into main a release, with its version in a `VERSION` file, and R63 gives the module its own version, published only when a release changed the module.

## Decisions

### 1. VERSION, and what reports it

`VERSION` at the root says `0.12.1` on this branch: a fix raises the patch number (12.1), and v0.12.0 was the last release.

A Go package at the root of the module (`version.go`, package `pagerarchive`) embeds the file (`//go:embed VERSION`) as `pagerarchive.Version`. The server's and the client's `version` variables are set from it, so `pagerarchive version`, `pagerarchive-client --version`, the `version` field of their start-up log lines, and telemetry's `service.version` (both call `telemetry.Start` with `Version: version`) all report VERSION. Before, they reported whatever `-ldflags "-X main.version=…"` set: the commit SHA in CI's images, the tag's name in a release, and `dev` in any other build. The `VERSION` build argument and `-X` flag are gone from both Dockerfiles and from the workflow. A `go:embed` has to be in a package at or above the file, hence the root package; any `go build` or `go run` gets the version, with nothing to pass.

So a branch's build reports the version it will be released as, not the commit. The owner confirmed that's fine (2026-11-02).

`version_test.go` fails if VERSION isn't `MAJOR.MINOR.PATCH`. The `images` job checks `pagerarchive version` and `pagerarchive-client --version` in the built images against VERSION.

### 2. Versions are MAJOR.MINOR.PATCH

Both VERSION and the module's `ModuleVersion` are `MAJOR.MINOR.PATCH`: three numbers without leading zeros, and no pre-release or build part. Semantic versioning allows those, but every merge is a release, so nothing is ever a pre-release, and comparing three numbers is all the checks need. PowerShell's four-part versions are refused too, since the Gallery shows module versions as semantic versions.

### 3. The checks on a branch or pull request

A new job, `versions`, runs `scripts/ci/check-versions.sh origin/main` on every push to a branch other than main, every pull request and every manual run off main. It checks out the whole history (`fetch-depth: 0`, which brings the tags too) and fails, with a GitHub error annotation on the file and a line in the log, when:

- VERSION isn't `MAJOR.MINOR.PATCH`: "set it to MAJOR.MINOR.PATCH, higher than main's …";
- VERSION isn't higher than main's: "raise it (a milestone raises the minor number, a fix the patch number), so this merge is a new release";
- the branch changes `powershell/PagerArchive/` (anything that differs between the branch and its merge base with main) and the manifest's `ModuleVersion` isn't higher than main's: "raise ModuleVersion in powershell/PagerArchive/PagerArchive.psd1".

Main has no VERSION until this branch is merged, so main's version is then the newest `v*` tag merged into main (v0.12.0). The publish jobs need `versions`, so a branch that fails it publishes nothing, from a manual run included.

It's a separate, quick job (a minute) and not a step in another job, so its failure is the first thing a pull request shows.

### 4. The release, on main

On a push to main, a new job, `release`, needs every check (the same eight jobs the publish jobs needed before). It runs only on a push to main. Its first step, `scripts/ci/release-plan.sh <commit>`, decides and publishes nothing. **The previous release** is the newest `v*` tag in the commit's history (`git tag -l 'v*' --merged <commit>`, after `git fetch --tags`), other than one on this commit, which is a re-run's own tag. A tag anywhere else, such as one on another branch, doesn't count. Both checks below use it (owner, 2026-11-02):

- **Version and tag:** VERSION at the commit, tag `v<VERSION>`. If that tag exists on **another** commit, it fails: "Tag v… already exists, on …: … was released already, and a version is never reused. Nothing is published; raise VERSION in a new branch." Nothing has been published at that point: the tag, the release, the images and the module all come after it. If the tag is on **this** commit, it's a re-run of this release (below), and it goes on (the owner confirmed this reading, 2026-11-02).
- **Higher than every earlier release** (SPEC 12.1, owner 2026-11-02): it fails if VERSION isn't higher than the previous release's, by version: "VERSION is …, which isn't higher than the previous release, v…: every release is a new, higher version. Nothing is published; raise VERSION above … in a new branch." This doesn't depend on the branch check: nothing makes `versions` required before a merge, and none is added (owner, 2026-11-02).
- **The module:** "changed" if `git diff --quiet <previous release> <commit> -- powershell/PagerArchive/` finds a difference. This is git on the full checkout, not a third-party action. It compares with the previous release, not the push's previous commit: if a merge's run fails and isn't re-run, the next merge still sees the module version the failed one raised (see the review below). If the module changed, the Gallery is asked for that version (`GET https://www.powershellgallery.com/api/v2/Packages(Id='PagerArchive',Version='<v>')`: 200 is there, 404 isn't, anything else fails the job, since it can't tell). The plan is `publish` only if the module changed and its version isn't on the Gallery. Otherwise it logs `module unchanged: the Gallery keeps <version>`, with the reason in brackets, and the release notes say `PowerShell module: unchanged; the Gallery keeps <version>.`
- If there's no earlier release in the commit's history, the module counts as changed, and the Gallery check decides.

The job then builds the client's binaries for linux/amd64 and linux/arm64 with `SHA256SUMS` (as the old `release-binaries` job did, which this job replaces), tags the commit with an annotated tag, `PagerArchive v<VERSION>`, by `github-actions[bot]`, pushes the tag with the workflow's `GITHUB_TOKEN`, and creates the GitHub release with the binaries and notes listing the images' version and the module's line from the plan. The job has `contents: write`; the workflow's default stays `contents: read`.

Everything else follows the release job, in the same run:

- **Images:** `publish-server` and `publish-client-manifest` tag them `<VERSION>`, its `MAJOR.MINOR` (`0.12` for 0.12.1, the plan's `major_minor`), `latest`, `main` and `sha-<commit>` on a push to main, as section 12.1 lists them (owner, 2026-11-02). Hand-made tags had `X.Y` too. A manual run still publishes `sha-<commit>` only, on any branch: `latest` and `main` used to follow `{{is_default_branch}}`, which a manual run on main also matched; now they need a push to main.
- **The module:** `publish-powershell` runs only when the plan said `publish`. It checks the manifest with `Test-ModuleManifest`, checks that its version is the plan's, and publishes `powershell/PagerArchive` as it is, with the manifest's own version, not VERSION. If `PSGALLERY_API_KEY` isn't set, it fails, saying to set the secret and re-run the job, since the release notes already say the module is published (owner, 2026-11-02; the key exists).

The publish jobs need `release` and `versions`, which are skipped off main and on main respectively, so they use `if: !failure() && !cancelled()`: they run unless a job they need failed.

### 5. Re-running a failed publish

"Re-run failed jobs" re-runs the failed job and those after it, and the jobs that passed keep their outputs, so the release job's version and plan are the ones the run started with. Nothing reads VERSION again or makes a new version:

- an image job pushes the same tags again;
- `publish-powershell` asks the Gallery first (`Find-PSResource`) and only publishes if the version isn't there, so a publish that succeeded on the Gallery's side but failed in the job finishes cleanly;
- if `release` itself failed partway, it's re-run: the plan finds its own tag on this commit and goes on, the tag step skips the tag, and the release step uploads the binaries again (`--clobber`) to the release it finds.

"Re-run all jobs" does the same, since the tag is on the same commit.

### 6. Tag pushes start no run

GitHub doesn't start a workflow run for a push made with the workflow's own `GITHUB_TOKEN`, except `workflow_dispatch` and `repository_dispatch` (GitHub's "Triggering a workflow from a workflow"). The release's tag is pushed that way, so it can't start a second release. To make that hold for a tag pushed any other way too, the `push` trigger now has `branches: ["**"]`: with a branch filter and no tag filter, GitHub starts no run for a tag push (GitHub's "Workflow syntax", `on.push.<branches|tags>`: "If you define only tags/tags-ignore or only branches/branches-ignore, the workflow won't run for events affecting the undefined Git refs").

**What a `v*` tag push does now: nothing.** No run starts, nothing is built or published. Nothing is released by hand (12.1); a release that needs re-doing is re-run, not re-tagged.

The docs-only filter (ADR 0014) is unchanged, under the branch filter: both must match. ADR 0014's note that tag pushes ignore path filters, so releases always ran, no longer applies, since tags don't run.

### 7. The manifest says 0.12.0

The manifest said `ModuleVersion = '0.9.0'` because CI wrote the tag's version into a copy at publish time. This branch sets it to `0.12.0`, the version v0.12.0's release published. The module's files are the same at v0.12.0 (59f40ff) and at main (1f41c38): `git diff --quiet 59f40ff 1f41c38 -- powershell/` succeeds.

**On merge**, the change to the manifest counts as a module change, so the plan asks the Gallery for 0.12.0. With 0.12.0 on the Gallery, it's `unchanged`: the run logs `module unchanged: the Gallery keeps 0.12.0 (the module changed, but 0.12.0 is on the Gallery already)`, the notes say so, and `publish-powershell` is skipped. The merge publishes no module.

**0.12.0 on the Gallery.** When this branch started, the Gallery had only 0.10.0 (`FindPackagesById()`; `Packages(Id='PagerArchive',Version='0.12.0')` answered 404), because v0.12.0's tag run, 41290573137, was still running its checks. It finished at about 09:13 UTC with all 21 jobs passed, "Publish the PowerShell module to the PowerShell Gallery" among them. Afterwards the Gallery lists 0.10.0 and 0.12.0, and 0.12.0 answers 200. 0.9.0 was never on the Gallery (404). The plan for this branch's merge (the revised script, on 91ec415, after main 7008d55 was merged in):

```
$ sh scripts/ci/release-plan.sh HEAD
VERSION 0.12.1 is higher than the newest release, v0.12.0.
module unchanged: the Gallery keeps 0.12.0 (the module changed since v0.12.0, but 0.12.0 is on the Gallery already)
version=0.12.1
major_minor=0.12
tag=v0.12.1
module=unchanged
module_version=0.12.0
notes=PowerShell module: unchanged; the Gallery keeps 0.12.0.
```

Run before v0.12.0's publish, the first script (`release-plan.sh 1f41c38 7eeb2f5`) said `module: publishing PagerArchive 0.12.0 (the module changed and 0.12.0 isn't on the Gallery yet)`. Had that publish failed, this merge would have published 0.12.0 itself: the same files as v0.12.0's, so it would have caught up rather than published something new.

### 8. Small changes

- `docs/powershell-guidelines.md` §11 said the module is published "with the release's version". It now says the manifest's own version, published only when the release changed the module, and that a branch changing the module raises `ModuleVersion`.
- `CLAUDE.md` has one line: every merge to main is a release, so raise VERSION, and ModuleVersion when the module changes.
- The old `release-binaries` job is gone (into `release`). The binaries are still named `pagerarchive-client-v<VERSION>-linux-<arch>`, so docs/client.md's download lines are unchanged.

## Evidence

The first round, on 21cdc17. The plan's runs here are the first script's, which took the push's previous commit as well; the review below has the revised script's.

Nothing was published, tagged or released from this branch. The Gallery was only read (HTTP GETs).

**Versions reported** (cld01, an export of the working tree, go1.27.2):

```
$ go run ./cmd/pagerarchive version
pagerarchive 0.12.1
$ go run ./cmd/pagerarchive-client --version
pagerarchive-client 0.12.1
$ go test -count=1 . ./cmd/...
ok  	github.com/RIKIKU/PagerArchive	0.003s
ok  	github.com/RIKIKU/PagerArchive/cmd/pagerarchive	1.891s
```

`actionlint` 1.7.12 on the workflow: no findings.

**The branch checks failing, on CI**, each in a commit made for it and reverted after:

- 458d0ec set VERSION to 0.11.9. Run 41291640021, job "VERSION and the module's version are raised", failed:
  ```
  VERSION: 0.11.9; main's: 0.12.0
  FAIL: VERSION is 0.11.9, which isn't higher than main's 0.12.0: raise it (a milestone raises the minor number, a fix the patch number), so this merge is a new release.
  ##[error]VERSION is 0.11.9, which isn't higher than main's 0.12.0: …
  ```
  with the error annotation on `VERSION`. c0de3e3 reverted it. Locally, VERSION `0.12` gave `VERSION is '0.12', which isn't a semantic version: set it to MAJOR.MINOR.PATCH, higher than main's 0.12.0 …`.
- a0791d1 added a comment to `PagerArchive.psm1` and left `ModuleVersion` at main's 0.9.0. Run 41291870965, the same job, failed:
  ```
  VERSION: 0.12.1; main's: 0.12.0
  The module changed (powershell/PagerArchive/). ModuleVersion: 0.9.0; main's: 0.9.0
  FAIL: This branch changes powershell/PagerArchive/ but ModuleVersion is 0.9.0, which isn't higher than main's 0.9.0: raise ModuleVersion in powershell/PagerArchive/PagerArchive.psd1.
  ```
  with the annotation on the manifest. The next code commit reverts it.

Both runs were cancelled once the job had failed; the rest of each run was testing a proof commit.

**The plan, against made-up commits.** A scratch repository with `release-plan.sh`, `versions-lib.sh`, a VERSION, a two-line manifest and a `.psm1`:

```
* f8bc054 (gallery) C3: module changed, ModuleVersion 0.10.0 (already on the Gallery), VERSION 0.12.2
| * 07dc1ac (main) C2: server-only change, VERSION 0.12.3
| * 04c1885 C1: module changed, ModuleVersion raised to 0.12.1, VERSION 0.12.2
|/
* 9a4773f (tag: v0.12.1) C0: main before (VERSION 0.12.1, ModuleVersion 0.9.0)
```

```
=== push 9a4773f -> 04c1885 (expect publish)
module: publishing PagerArchive 0.12.1 (the module changed and 0.12.1 isn't on the Gallery yet)
version=0.12.2
tag=v0.12.2
module=publish
module_version=0.12.1
notes=PowerShell module: PagerArchive 0.12.1, published to the PowerShell Gallery by this release.
=== push 04c1885 -> 07dc1ac (expect unchanged)
module unchanged: the Gallery keeps 0.12.1 (this push didn't change powershell/PagerArchive/)
version=0.12.3
tag=v0.12.3
module=unchanged
module_version=0.12.1
notes=PowerShell module: unchanged; the Gallery keeps 0.12.1.
=== push 9a4773f -> f8bc054 (expect unchanged)
module unchanged: the Gallery keeps 0.10.0 (the module changed, but 0.10.0 is on the Gallery already)
version=0.12.2
tag=v0.12.2
module=unchanged
module_version=0.10.0
notes=PowerShell module: unchanged; the Gallery keeps 0.10.0.
```

All three exited 0. Then, with `v0.12.2` tagged on C1:

```
=== push 9a4773f -> f8bc054 (VERSION 0.12.2; tag v0.12.2 is on C1)
::error file=VERSION::Tag v0.12.2 already exists, on 04c1885…: 0.12.2 was released already, and a version is never reused. Nothing is published; raise VERSION in a new branch.
exit 1
=== re-run of C1's release (tag on the same commit)
Tag v0.12.2 is already on 04c1885…: a re-run, finishing the release.
module: publishing PagerArchive 0.12.1 (the module changed and 0.12.1 isn't on the Gallery yet)
=== before = 0000000000000000000000000000000000000000
No previous main commit to compare with ('0000…'): counting the module as changed.
module: publishing PagerArchive 0.12.1 (the module changed and 0.12.1 isn't on the Gallery yet)
```

The Gallery's own answers at the time: 0.10.0 200, 0.12.1 404 (`FindPackagesById()` listed only 0.10.0).

**A docs-only push to main starts no run.** 1f41c38, main's last commit (SPEC.md only), has no run and no check runs (`gh api "repos/RIKIKU/PagerArchive/actions/runs?head_sha=1f41c38…" --jq .total_count`: 0; its check runs: 0). That was under the old trigger. Under the new one (`branches: ["**"]` with the same paths), main is one of the branches, and the paths still have to match. Nothing can be pushed to main from this branch, so the new trigger was shown on the branch: 6cc4f9c, which added only this ADR's draft and the CLAUDE.md line, was pushed on its own at 09:10:59 UTC. 150 s later it had no run and no check runs (both `total_count` 0), and the branch's only runs were the two proofs above. The next push, which reverts a0791d1, is code and runs everything.

## Consequences

- Every branch that runs CI raises VERSION, even one that only changes tests or the workflow, because its merge is a release. A docs-only branch doesn't, and isn't released (ADR 0014).
- Two branches open at once both raise VERSION from main's. The second to merge then fails `versions` on its next push (its VERSION is no longer higher than main's) and raises it again. Its pull request shows the failure only after main moves and the branch is pushed or re-run; a pull request's check doesn't re-run by itself when main moves. A merge that skipped that fails in `release` (not higher than the previous release) before publishing anything.
- A merge whose release fails and isn't re-run isn't released, and its version is never used. The next release includes its changes: the module is compared with the previous release, and VERSION only has to be higher than it. The release notes don't list skipped versions: a version whose release failed was never released, and GitHub's releases list shows what exists (owner, 2026-11-02).

## The owner's answers (2026-11-02)

The first round's questions, answered in the review of 21cdc17:

1. **Builds that aren't releases report VERSION** (a branch's image, a manual run's, a local build): fine as it is.
2. **A re-run that finds its tag on this same commit carries on:** right; it's how a re-run finishes a release.
3. **Branch protection:** no required check and no branch protection. The release plan checks VERSION against the newest `v*` tag instead (SPEC 12.1, owner 2026-11-02).
4. **A missing Gallery key:** `publish-powershell` fails when the plan says publish and the key isn't set. The key exists now.

## Review (owner, 2026-11-02)

Main moved to 7008d55, which changed section 12.1 (VERSION higher than every earlier release's; the `MAJOR.MINOR` image tag; the module compared with the previous release). It was merged into the branch first (ac32c34), then 91ec415 made the four changes:

1. **The module is compared with the previous release, not the previous push.** The first script compared with `github.event.before`. If a merge's run failed and wasn't re-run, the next merge compared with the failed one, so the module version that one raised was never published. The reviewer's case: A raises `ModuleVersion` to 0.13.0, B raises only VERSION, and `release-plan.sh A B` printed `module unchanged: the Gallery keeps 0.13.0`, though 0.13.0 isn't on the Gallery. The plan now compares with the newest earlier `v*` tag merged into the commit, and counts the module as changed when there's no such tag, so the Gallery decides. Nothing else used `github.event.before`, so the script takes the commit only.
2. **VERSION must be higher than the newest earlier release**, or the plan fails before anything is published (section 4 above). At first that meant every `v*` tag; the second review limited it to the tags in the commit's history.
3. **Images are tagged `MAJOR.MINOR` too**: the plan outputs `major_minor` (`0.12` for 0.12.1), and both images' metadata steps list it.
4. **A missing `PSGALLERY_API_KEY` fails `publish-powershell`** when the plan says publish: `::error::The PSGALLERY_API_KEY secret isn't set, so PagerArchive <version> can't be published: set the secret (Settings, Secrets and variables, Actions), then re-run this job.`

### Evidence

Nothing was published, tagged or released. The Gallery was only read, and nothing was pushed to GHCR.

**The plan, in a scratch clone of the branch** (its remote removed, so nothing could be pushed), with made-up commits and tags on top of it:

```
6763588 (HEAD) E: VERSION 0.12.6
61da63f D: VERSION 0.12.5 (no check, its release failed: no tag)
8d82d75 (tag: v0.13.1) B: VERSION 0.13.1 only
4755c4b A: module changed, ModuleVersion 0.13.0, VERSION 0.13.0 (its run failed: no tag)
46a57de (tag: v0.12.1) R: this branch, as merged (VERSION 0.12.1, ModuleVersion 0.12.0)
```

The reviewer's A/B case, before v0.13.1 was tagged on B. 21cdc17's script, then the revised one:

```
$ old-release-plan.sh 4755c4b 8d82d75     # A B
module unchanged: the Gallery keeps 0.13.0 (this push didn't change powershell/PagerArchive/)
$ release-plan.sh 8d82d75                  # B
VERSION 0.13.1 is higher than the newest release, v0.12.1.
module: publishing PagerArchive 0.13.0 (the module changed since v0.12.1 and 0.13.0 isn't on the Gallery yet)
version=0.13.1
major_minor=0.13
tag=v0.13.1
module=publish
module_version=0.13.0
notes=PowerShell module: PagerArchive 0.13.0, published to the PowerShell Gallery by this release.
exit 0
```

The Gallery answered 404 for 0.13.0. With v0.13.1 tagged on B, a re-run of B's release: `Tag v0.13.1 is already on 8d82d75…: a re-run, finishing the release.`, then the same `VERSION 0.13.1 is higher than the newest release, v0.12.1.` and `module: publishing PagerArchive 0.13.0 …` (its own tag isn't an earlier release).

A VERSION lower than the newest tag but higher than the previous commit's (E's 0.12.6 against D's 0.12.5 and v0.13.1):

```
$ release-plan.sh 6763588
::error file=VERSION::VERSION is 0.12.6, which isn't higher than the newest release, v0.13.1: every release is a new, higher version. Nothing is published; raise VERSION above 0.13.1 in a new branch.
exit 1
```

21cdc17's script, given D and E, went on and planned a 0.12.6 release (`version=0.12.6`).

F, on E, with VERSION 0.13.2 and the module untouched since v0.13.1: `module unchanged: the Gallery keeps 0.13.0 (powershell/PagerArchive/ didn't change since v0.13.1)`. With every tag deleted from the clone: `No earlier release in this commit's history: counting the module as changed.`, then `module: publishing PagerArchive 0.13.0 (the module changed with no earlier release and 0.13.0 isn't on the Gallery yet)`.

**The images' metadata steps list `MAJOR.MINOR`.** On cld01, a bundle of 91ec415 with its tags (v0.10.0, v0.12.0) was cloned onto a branch named main. A workflow was generated from `ci.yml` with the release job's plan step, and the `meta` step and `env` of `publish-server` and `publish-client-manifest` copied verbatim. act 0.2.89 then ran it as a push to `refs/heads/main`. `docker/metadata-action` reads the repository from GitHub's API. It was pointed (`GITHUB_API_URL`) at a local server returning the public repository record, fetched without a token, and that server was stopped afterwards. Nothing logged in to GHCR or pushed. All three jobs succeeded:

```
[release] | version=0.12.1
[release] | major_minor=0.12
[publish-server] | ghcr.io/rikiku/pagerarchive:0.12.1
[publish-server] | ghcr.io/rikiku/pagerarchive:0.12
[publish-server] | ghcr.io/rikiku/pagerarchive:latest
[publish-server] | ghcr.io/rikiku/pagerarchive:main
[publish-server] | ghcr.io/rikiku/pagerarchive:sha-91ec415
[publish-client-manifest] | ghcr.io/rikiku/pagerarchive-client:0.12.1
[publish-client-manifest] | ghcr.io/rikiku/pagerarchive-client:0.12
[publish-client-manifest] | ghcr.io/rikiku/pagerarchive-client:latest
[publish-client-manifest] | ghcr.io/rikiku/pagerarchive-client:main
[publish-client-manifest] | ghcr.io/rikiku/pagerarchive-client:sha-91ec415
```

The same workflow as a push to `refs/heads/some-branch` listed only `ghcr.io/rikiku/pagerarchive:sha-91ec415`.

`actionlint` 1.7.12 on the revised workflow: no findings.

**CI on f6201fb** (the review's changes and this section; the last commit of that push), run 41321332131: success. All 19 check and build jobs passed, `VERSION and the module's version are raised` among them (`VERSION: 0.12.1; main's: 0.12.0`, `Versions: OK`), and the images reported `pagerarchive 0.12.1` and `pagerarchive-client 0.12.1`. The release, the client image's publishing and the module's were skipped, as off main, and the server image job printed `published: false`. Afterwards there was no `v0.12.1` tag on GitHub (`git ls-remote --tags origin`), `gh release view v0.12.1` answered `release not found`, and the Gallery answered 404 for 0.12.1.

## Second review (owner, 2026-11-02)

The owner's answers to the questions after the first review:

1. **Skipped versions aren't listed** in the release notes: a version whose release failed was never released, and the releases list shows what exists.
2. **Only tags in the commit's history count**, for the VERSION check too, so both checks use the same releases.

61cf63d makes the second change. `earlier_releases` lists only `git tag -l 'v*' --merged <commit>` (still without a tag on this commit), and the plan works out the previous release once and uses it for both checks. Its messages now say "the previous release" instead of "the newest release". The first answer needs no change.

### Evidence

A fresh scratch clone of f6201fb, its remote removed. A made-up v0.12.1 is on R. A made-up v0.20.0 is on S, a commit on another branch. M is on R's line with VERSION 0.13.0:

```
* 648b674 (HEAD -> releases-on-merge) M: on main's line, VERSION 0.13.0
| * e1f7598 (tag: v0.20.0, side) S: a side branch, tagged by hand
|/
* 9aeb6c0 (tag: v0.12.1) R: this branch, as merged (VERSION 0.12.1)
$ git tag -l "v*" --merged HEAD
v0.10.0 v0.12.0 v0.12.1
=== f6201fb's script (every v* tag):
::error file=VERSION::VERSION is 0.13.0, which isn't higher than the newest release, v0.20.0: every release is a new, higher version. Nothing is published; raise VERSION above 0.20.0 in a new branch.
exit 1
=== now (tags in the commit's history):
VERSION 0.13.0 is higher than the previous release, v0.12.1.
module unchanged: the Gallery keeps 0.12.0 (powershell/PagerArchive/ didn't change since v0.12.1)
version=0.13.0
major_minor=0.13
tag=v0.13.0
module=unchanged
module_version=0.12.0
notes=PowerShell module: unchanged; the Gallery keeps 0.12.0.
exit 0
```

The first review's cases still hold with the revised script, on the same clone:

- A raises ModuleVersion to 0.13.0 and VERSION to 0.13.1, and its run fails (no tag). B raises only VERSION, to 0.13.2. The plan says `VERSION 0.13.2 is higher than the previous release, v0.12.1.`, then `module: publishing PagerArchive 0.13.0 (the module changed since v0.12.1 and 0.13.0 isn't on the Gallery yet)`.
- With v0.13.2 tagged on B, a re-run of B's release: `Tag v0.13.2 is already on b016a92…: a re-run, finishing the release.`, and the same plan.
- D, after B, with VERSION 0.13.1: `::error file=VERSION::VERSION is 0.13.1, which isn't higher than the previous release, v0.13.2: …`, exit 1.

This branch's own merge, on 61cf63d: `VERSION 0.12.1 is higher than the previous release, v0.12.0.` and `module unchanged: the Gallery keeps 0.12.0 (the module changed since v0.12.0, but 0.12.0 is on the Gallery already)`. `actionlint` 1.7.12 on the workflow: no findings.