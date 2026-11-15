# ADR 0032: Base images wait 3 days

## Status

Proposed (2026-11-15). The security review's finding D6 (the deploy review): R85's rule merged a base image's new digest or patch the day it appeared, with no wait, and the release and automatic upgrades (ADR 0024, ADR 0030) can carry it to production with no one looking. The owner, 2026-11-15: "hold for a few days is good". SPEC.md R85 changes; amends [ADR 0022](0022-maintenance.md). On the branch `base-image-hold` from main. Nothing in the server, client or module changes, so the pull request is labelled `maintenance` only.

## Context

The libraries wait 3 days after their release (`minimumReleaseAge`, R85a), so a broken or withdrawn release has time to be noticed. Base images didn't: R85's rule (`node`, `golang`, `gcr.io/distroless/static-debian13`, `debian:trixie-slim`; digest, pin digest and patch, daily, one group, merging on its own) had no wait.

Renovate's `minimumReleaseAge` needs a release date, and with its default `minimumReleaseAgeBehaviour` (`timestamp-required`) an update with no date waits for good; with `timestamp-optional` it doesn't wait at all. What Renovate 44.222.4 (the version `maintenance.yml` runs) has for each image, from its source (`lib/modules/datasource/docker/index.ts`, `lib/workers/repository/process/lookup/digest.ts`) and a run on cld01:

- **Docker Hub** is the only registry Renovate dates (`tag_last_pushed` from Docker Hub's tag list). A digest update is dated by its tag's last push. But Renovate reads the whole tag list, 100 a page, and Docker Hub refuses a client with no account past page 10 (403); on any error Renovate drops the dates and uses the registry's plain tag list. `node`, `golang` and `debian` each have more than 1,000 tags, so **none of them gets a date**. The run on cld01, with no account as in `maintenance.yml`: `GET https://hub.docker.com/v2/repositories/library/node/tags?ordering=last_updated&page=11&page_size=100 = statusCode=403`, then "digest update of node has no releaseTimestamp to age against" for `node`, `debian` and distroless alike.
- **gcr.io** gets no dates from Renovate at all. The image's own dates don't help: distroless is built reproducibly, so its `created` (and gcr.io's `timeCreatedMs`) is 1970-01-01. gcr.io's tag list does record when each manifest was uploaded (`timeUploadedMs`): `nonroot`'s current digest, `sha256:aafe9936...`, was uploaded 2026-11-12 21:16 UTC.
- Docker Hub's single-tag endpoint (`/v2/repositories/library/node/tags/24-slim`) answers with no account and gives the tag's `tag_last_pushed` and its current digest, the one our `FROM` lines pin.

## Decisions

### 1. Renovate holds every base image update; a script lets through the ones 3 days old

R85's rule in `.github/renovate.json5` gets `minimumReleaseAge: "3 days"` and keeps the default `timestamp-required`, so every base image update, having no date, waits. `scripts/ci/base-image-hold.sh`, run by `maintenance.yml` just before Renovate, asks each image's registry how old the tag's newest push is (Docker Hub's `tag_last_pushed`, gcr.io's `timeUploadedMs`), and for each one 3 days old writes a package rule for that image and tag with `minimumReleaseAgeBehaviour: "timestamp-optional"`, which lets it through. The workflow gives the rules to Renovate as `RENOVATE_PACKAGE_RULES`. A new patch (`golang` 1.27.4 to 1.27.5) is let through when the newest patch tag of the same form is 3 days old.

- **Why a tag's last push:** it's the newest digest's age. A digest can't be older than its tag's last push says, so it never goes early (Renovate's own note says the same of `tag_last_pushed`, issue 38659). A tag pushed again within the 3 days restarts the wait; distroless's `nonroot` has been uploaded every 1 to 6 weeks over the last year (twice within 3 days only once, 2026-10-20 to 10-23), so it wouldn't wait for long.
- **Why held unless let through, not let through unless held:** if the script can't run, a registry doesn't answer, or an image moves to a registry the script can't date, the update waits (and the run's log names it) rather than going out unseen.
- **Why the rules come from outside the file:** Renovate has no way to read a date from gcr.io's upload times, or from Docker Hub's one-tag endpoint. Global package rules come before the repository's, and the repository's base image rule doesn't set `minimumReleaseAgeBehaviour`, so the script's rule decides.
- **Why not a CI check on the pull request** that fails until the digest is 3 days old: it would fail CI, which tells the owner (R85c), and nothing would run it again when the time was up. Held in Renovate, the pull request simply opens 3 days later, with the rest of the group in it meanwhile (Renovate takes a held update out of the group's branch).
- **Why not a schedule:** a weekly schedule makes an update wait up to 7 days, but one released the day before still goes.

`scripts/ci/base-image-hold-checks.sh` checks the script in CI (the `versions` job) against a stand-in for `curl`: 4-day-old digests are let through and 2-day-old ones aren't, on Docker Hub and gcr.io; a patch 4 days old is, one a day old isn't; a registry that doesn't answer, or one it can't date, holds the image.

### 2. Vulnerability alert fixes don't wait

`vulnerabilityAlerts` now says `minimumReleaseAge: null` (it's also Renovate's default there). Renovate puts the alert's settings in `force` on the rule it makes for each alert (`lib/workers/repository/init/vulnerability.ts` for GitHub's alerts, `process/vulnerabilities.ts` for OSV's), which overrides every package rule, so an alert's fix never waits for its age, whatever the hold says. Neither source covers Docker images today (GitHub's alerts don't, and Renovate maps no Docker datasource to an OSV ecosystem), so a base image's security fix comes as an ordinary digest update and waits its 3 days, as the owner accepted ("slightly late is fine for normal ones").

## Seen

`renovate-config-validator --strict` (44.222.4, in the repository): "Config validated successfully". The checks: `base-image-hold.sh: OK`. actionlint on `maintenance.yml` and `ci.yml`: clean.

A dry run on cld01, Renovate 44.222.4 with `--platform=local` against a copy of this branch (no token, so nothing on GitHub is read or changed), with `node:24-slim`, `debian:trixie-slim` and distroless `nonroot` set to a digest of zeros in `Dockerfile` and `Dockerfile.client`, on 2026-11-15 at about 08:50 UTC:

- Without the script's rules: all three digest updates "pendingChecks": true ("digest update of node has no releaseTimestamp to age against", `timestamp-required`), so all are held.
- With them (`base-image-hold.sh`: "debian:trixie-slim (its digest): 10 days old, let through", "gcr.io/distroless/static-debian13:nonroot (its digest): 59 hours old, held until 3 days", "golang:1.27.4 (its digest): 3 days old", "node:24-slim (its digest): 3 days old"): `node` 24-slim to `dc26ae92c9fb` and `debian` trixie-slim to `a72b4433de1c` with no pending check, in `renovate/base-images`; distroless `nonroot` to `aafe993665bf` "pendingChecks": true, taken out of the branch ("Branch is not pending, removing pending upgrades").

The workflow's own dry run (Actions, Maintenance, `dry_run` `full`) reads the configuration from main, so it shows this only after the merge.
