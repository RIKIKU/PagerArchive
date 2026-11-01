# ADR 0016: Every merge to main is a release

## Status

Proposed (2026-11-01). The owner's brief: releases on merge, SPEC.md R63's "Guidelines and publishing" bullet and section 12.1 (owner, 2026-11-01), as a fix on its own branch, `releases-on-merge`, from main at 1f41c38.

## Context

Until now a release was a `v*` tag pushed by hand (v0.12.0 is the last). The tag's run published the images as `X.Y.Z` and `X.Y`, attached the client's binaries to a GitHub release, and published the PowerShell module with the tag's version, written into a copy of the manifest. So the manifest in the repo never changed: it still said `ModuleVersion = '0.9.0'`. Every push to main published images as `latest`, `main` and `sha-<commit>` but no version.

Section 12.1 makes every merge into main a release, with its version in a `VERSION` file, and R63 gives the module its own version, published only when a release changed the module.

## Decisions

### 1. VERSION, and what reports it

`VERSION` at the root says `0.12.1` on this branch: a fix raises the patch number (12.1), and v0.12.0 was the last release.

A Go package at the root of the module (`version.go`, package `pagerarchive`) embeds the file (`//go:embed VERSION`) as `pagerarchive.Version`. The server's and the client's `version` variables are set from it, so `pagerarchive version`, `pagerarchive-client --version`, the `version` field of their start-up log lines, and telemetry's `service.version` (both call `telemetry.Start` with `Version: version`) all report VERSION. Before, they reported whatever `-ldflags "-X main.version=…"` set: the commit SHA in CI's images, the tag's name in a release, and `dev` in any other build. The `VERSION` build argument and `-X` flag are gone from both Dockerfiles and from the workflow. A `go:embed` has to be in a package at or above the file, hence the root package; any `go build` or `go run` gets the version, with nothing to pass.

So a branch's build reports the version it will be released as, not the commit. Question 1 asks whether that's what the owner wants.

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

On a push to main, a new job, `release`, needs every check (the same eight jobs the publish jobs needed before). It runs only on a push to main. Its first step, `scripts/ci/release-plan.sh <github.event.before> <commit>`, decides and publishes nothing:

- **Version and tag:** VERSION at the commit, tag `v<VERSION>`. If that tag exists on **another** commit, it fails: "Tag v… already exists, on …: … was released already, and a version is never reused. Nothing is published; raise VERSION in a new branch." Nothing has been published at that point: the tag, the release, the images and the module all come after it. If the tag is on **this** commit, it's a re-run of this release (below), and it goes on.
- **The module:** "changed" if `git diff --quiet <before> <commit> -- powershell/PagerArchive/` finds a difference, `before` being the push's previous main commit (`github.event.before`). This is git on the full checkout, not a third-party action. If the module changed, the Gallery is asked for that version (`GET https://www.powershellgallery.com/api/v2/Packages(Id='PagerArchive',Version='<v>')`: 200 is there, 404 isn't, anything else fails the job, since it can't tell). The plan is `publish` only if the module changed and its version isn't on the Gallery. Otherwise it logs `module unchanged: the Gallery keeps <version>`, with the reason in brackets, and the release notes say `PowerShell module: unchanged; the Gallery keeps <version>.`
- If there's no previous main commit to compare with (`before` is all zeros, or isn't in the history after a force-push), the module counts as changed: the Gallery check still keeps a version from being published twice.

The job then builds the client's binaries for linux/amd64 and linux/arm64 with `SHA256SUMS` (as the old `release-binaries` job did, which this job replaces), tags the commit with an annotated tag, `PagerArchive v<VERSION>`, by `github-actions[bot]`, pushes the tag with the workflow's `GITHUB_TOKEN`, and creates the GitHub release with the binaries and notes listing the images' version and the module's line from the plan. The job has `contents: write`; the workflow's default stays `contents: read`.

Everything else follows the release job, in the same run:

- **Images:** `publish-server` and `publish-client-manifest` tag them `<VERSION>`, `latest`, `main` and `sha-<commit>` on a push to main. `X.Y` is gone: section 12.1 lists the four, and there are no tag runs left to make it. A manual run still publishes `sha-<commit>` only, on any branch: `latest` and `main` used to follow `{{is_default_branch}}`, which a manual run on main also matched; now they need a push to main.
- **The module:** `publish-powershell` runs only when the plan said `publish`. It checks the manifest with `Test-ModuleManifest`, checks that its version is the plan's, and publishes `powershell/PagerArchive` as it is, with the manifest's own version, not VERSION. A missing `PSGALLERY_API_KEY` still skips it, successfully, as before.

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

**0.12.0 on the Gallery.** When this branch started, the Gallery had only 0.10.0 (`FindPackagesById()`; `Packages(Id='PagerArchive',Version='0.12.0')` answered 404), because v0.12.0's tag run, 41290573137, was still running its checks. It finished at about 09:13 UTC with all 21 jobs passed, "Publish the PowerShell module to the PowerShell Gallery" among them. Afterwards the Gallery lists 0.10.0 and 0.12.0, and 0.12.0 answers 200. 0.9.0 was never on the Gallery (404). The plan for this branch's merge, run against main and this branch's implementation commit:

```
$ sh scripts/ci/release-plan.sh 1f41c38 7eeb2f5
module unchanged: the Gallery keeps 0.12.0 (the module changed, but 0.12.0 is on the Gallery already)
version=0.12.1
tag=v0.12.1
module=unchanged
module_version=0.12.0
notes=PowerShell module: unchanged; the Gallery keeps 0.12.0.
```

Run before v0.12.0's publish, the same command said `module: publishing PagerArchive 0.12.0 (the module changed and 0.12.0 isn't on the Gallery yet)`. Had that publish failed, this merge would have published 0.12.0 itself: the same files as v0.12.0's, so it would have caught up rather than published something new.

### 8. Small changes

- `docs/powershell-guidelines.md` §11 said the module is published "with the release's version". It now says the manifest's own version, published only when the release changed the module, and that a branch changing the module raises `ModuleVersion`.
- `CLAUDE.md` has one line: every merge to main is a release, so raise VERSION, and ModuleVersion when the module changes.
- The old `release-binaries` job is gone (into `release`). The binaries are still named `pagerarchive-client-v<VERSION>-linux-<arch>`, so docs/client.md's download lines are unchanged.

## Evidence

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
- Two branches open at once both raise VERSION from main's. The second to merge then fails `versions` on its next push (its VERSION is no longer higher than main's) and raises it again. Its pull request shows the failure only after main moves and the branch is pushed or re-run; a pull request's check doesn't re-run by itself when main moves. A merge that skipped that would fail in `release` (the tag exists on another commit) before publishing anything.
- The workflow's `github.event.before` is the previous main commit for a merge pushed to main. For a push of several commits at once it's still the commit before them all, so the module comparison covers the whole push.

## Questions for the owner

1. **Builds that aren't releases report VERSION.** A branch's or pull request's image, a manual run's `sha-<commit>` image, and a local `go build` all say `0.12.1` before 0.12.1 is released, and telemetry's `service.version` too. Is that right, or should a build that isn't from main say something else (for example `0.12.1-dev+<commit>`)? That needs the commit passed in at build time again.
2. **A re-run that finds its tag.** The plan fails if `v<VERSION>` is a tag on another commit, but goes on if it's on this commit, so that re-running the release job finishes the release. Is that the reading of "if the tag already exists, fail" the owner wants?
3. **Branch protection.** Nothing makes `versions` (or any check) required before merging (ADR 0014: no protection, no rulesets). A merge with a lower VERSION is still stopped at the release, but only if that version was tagged before; one that is lower but never released would be released. Should `versions` be a required check, or should the release job also check VERSION against the previous main commit's?
4. **The Gallery key.** With `PSGALLERY_API_KEY` missing, `publish-powershell` passes without publishing, as before, while the release notes already say the module was published. Should a missing key fail the job instead, now that releases are automatic?
