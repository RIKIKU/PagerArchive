# ADR 0019: Installing and upgrading with no checkout (M17)

## Status

Status: Accepted (owner, 2026-11-03). The owner's brief for M17 and decisions of 2026-11-03: SPEC.md section 7.22 (R84-R84b), A77, A78, and A79's last sentence, on the branch `m17` with main at 19561c2. Released as 0.15.0. The UI, the search, the import and the PowerShell module don't change.

## Context

Running PagerArchive needed a checkout: `compose.yaml` mounted repo files, and the upgrade began with `git pull`. M17 is the last item before the first production instance, whose host already runs a reverse proxy on an external Docker network named `proxy`.

The owner's decisions, which replaced the first plan of thin images and a setup container:

- Third-party services run their upstream images unchanged. The project never publishes its own builds of them.
- `compose.yaml` names every image by its major.minor tag, with no patch number and no digest, so `docker compose pull` gets patches. This replaced pinning by version and digest.
- Their configuration is one archive attached to each release, unpacked next to `compose.yaml`.
- PagerArchive's own setup is in its image: the API creates what it needs on start, the metrics role included. Authentik's database and role are Authentik's setup, so they stay a first-start script in the archive.
- Before 1.0.0, a release may break the upgrade if its notes give "Upgrade steps".

## Decisions

### 1. What a release attaches

| Asset | From | What it is |
|---|---|---|
| `compose.yaml` | `compose.yaml` | PagerArchive's stack |
| `env.example` | `.env.example` | Its settings |
| `pagerarchive-config.tar.gz` | `deploy/authentik`, `deploy/postgres`, and `deploy/monitoring`'s config folders | Third-party configuration |
| `monitoring-compose.yaml` | `deploy/monitoring/compose.yaml` | The example monitoring stack |
| `monitoring-env.example` | `deploy/monitoring/.env.example` | Its settings |

`scripts/ci/release-assets.sh` writes them, and A77, A78 and the monitoring check install from its output, so what's tested is what's attached.

**Attached only once the images are on GHCR** (owner, 2026-11-03). The `attach-release-files` job runs after the publish jobs. With no sign-in, as anyone pulling would be, it checks both images are there, attaches the files, and fails if any is missing. The release job creates the release with `--latest=false`, and this job makes it the latest as its last step. Without that, the docs' `releases/latest/download` links would find no files for those few minutes. They keep getting the previous release's until this one is complete. A branch's A77 that starts during those minutes fails on the missing files, and a re-run passes.

**`env.example`, not `.env.example`.** GitHub renames an asset whose name starts with a dot.

**Flat names for monitoring.** A release's assets are one flat list, so the two compose files can't have the same name.

### 2. The file in the repo and the file attached are the same file

Not generated. The API's image is written in `compose.yaml` as `ghcr.io/rikiku/pagerarchive:0.15`, and no setting in `.env` can change it, so a stale setting can't quietly hold a deployment back after an upgrade.

- **Why:** there's no generating step that could break only at release time, what's tagged is byte for byte what's attached, and anyone can read a release's file in the repo.
- **Cost:** a release that raises the minor version raises `compose.yaml`'s tag too. `check-versions.sh`, `release-assets.sh` and a test each fail with the line to write.
- **Building for tests:** `compose.yaml` has no `build:`, because a deployment has nothing to build from. `deploy/test/compose.yaml` adds it for the test stacks.

### 2a. Image tags

Every image is named by its major.minor tag (R84), which `TestA79ImagesAreNamedByMajorMinor` checks: no patch number, no digest, and no `latest`.

| Image | Tag | What moves |
|---|---|---|
| `ghcr.io/rikiku/pagerarchive` | `0.15` | Our patch releases |
| `postgis/postgis` | `18-3.6` | Within PostgreSQL 18 and PostGIS 3.6 |
| `ghcr.io/goauthentik/server` | `2026.5` | Authentik's patches |
| `grafana/loki` | `3.7` | Loki's patches |
| `grafana/grafana` | `13.2` | Grafana's patches |
| `grafana/alloy` | `v1.21.3` | **Nothing: exact version** |
| `grafana/tempo` | `3.1.1` | **Nothing: exact version** |
| `prom/prometheus` | `v3.15.1` | **Nothing: exact version** |

**The three exceptions.** R84 asks for the nearest tag that moves only within a minor version. On 2026-11-03, Alloy, Tempo and Prometheus publish none: only exact versions, plus `latest`, and for Prometheus `v3`, which all cross minor versions. So they name an exact version, the test lists them, and they get patches only when a release changes `compose.yaml`. M18's checks (R85b) look for new minor versions, so patches of these three aren't covered yet: see "Questions for the owner".

**A patch release changes no deployment file.** R84b says it needs only pull and up, so `check-versions.sh` fails a branch whose version is a patch of main's if it changes `compose.yaml` or the archive's files. That has to be a new minor version. The owner didn't ask for this check; without it, the docs' promise could quietly stop being true.

**What moving tags cost.** CI and a deployment can run different patches of an upstream image, and CI's result is only for what was current at its run. A patch of Authentik could change a table `pagerarchive_locked_accounts()` reads, or something the blueprint uses, with no release of ours. The locked-accounts metric would be the first to show it. R85b's checks don't cover that.

### 3. A changed file takes effect through a label

R84a needs a changed third-party file to take effect on `docker compose up -d`. The brief suggested Compose `configs:`, so that was measured first, on Compose 5.6.0: a service using `configs: file:` and one using a plain bind mount, then each file replaced, then `docker compose up -d`.

```
before: one one
 Container pa-m17-exp-viaconfig-1 Running
 Container pa-m17-exp-viabind-1 Running
after: config=one recreated=no; bind=one recreated=no
[{"Type":"bind","Source":"/home/claude/m17-exp/cfg/a.txt","Destination":"/etc/a.txt",...}]
```

Compose doesn't notice either: a `file:` config is a bind mount, and the running container keeps the old file. So `configs:` wouldn't meet R84a.

Compose does notice a changed label. Each service with mounted configuration has a `pagerarchive.config` label in the compose file, holding a hash of those files. A release that changes a file changes the label, and `docker compose up -d` recreates only that service. `TestR84aChangedConfigRecreatesItsService` fails if a file changes without its label, and prints the value to write.

- **The other way considered:** a label holding the version is simpler, but restarts Authentik and all five monitoring services on every release, including ones whose files didn't change.
- **The database's script has no label.** It runs only on an empty volume, so a change means nothing to an existing database, and a label would restart PostgreSQL for no gain.
- **A file edited by hand** isn't covered: it needs `docker compose restart <service>`. These files aren't for editing, and an upgrade replaces them (`docs/monitoring.md` says so for the alert thresholds).

### 4. Files are mounted one at a time

Unpacking an archive doesn't delete what a later release dropped. With folders mounted, a dropped blueprint or dashboard would stay in use. With each file named, a leftover file does nothing. `TestR84MountsComeFromTheArchive` checks that each mount is a file, in the archive.

### 5. The API creates the metrics role; no setup container

`authentik.EnsureLocksRole` runs on every API start when `AUTHENTIK_METRICS_DB_PASSWORD` is set. It creates or updates the role and `pagerarchive_locked_accounts()` in Authentik's database. That replaces `deploy/postgres/20-pagerarchive-metrics.sh` and its hand step on upgrades, and a changed password now takes effect on restart.

- **A setup container isn't needed.** The first plan's `db-setup` existed to order Authentik's database before everything else. `db` isn't healthy until its first-start script has finished, and the API and Authentik both wait for healthy, so `10-authentik.sh` already gives that order. The function is PL/pgSQL, so Authentik's tables needn't exist yet.
- **A failure doesn't stop the API.** It costs one metric, and the API logs a warning.
- **`AUTHENTIK_METRICS_DATABASE_URL`** means the role is set up elsewhere, so the API leaves it alone.

### 6. The built-in example, and /dev/null

The example profile and map are in the server image, at `/usr/share/pagerarchive`. A Compose file can't leave a mount out when a setting is empty, so with `PROFILES_DIR` or `MAP_TILES_FILE` unset it mounts `/dev/null` there, and the API takes a character device to mean "use the built-in one". A mistyped path is still an error, never a quiet fallback.

With the example profile the API logs a warning on every start, and `import-messages` prints it too (R84, the owner's choice).

### 7. Site-specific changes

`compose.override.yaml` next to `compose.yaml`, which Compose reads by itself and an upgrade leaves alone. `docs/deploy.md`'s example joins `api` and `authentik-server` to the external `proxy` network, under aliases, because other stacks on a shared network can also have a service named `api`. It needs Compose 2.24 for `!reset`, to stop publishing their ports. A78 takes that example from the docs as written, and runs it behind a real nginx on that network.

`deploy/monitoring/compose.clients.yaml` goes too: publishing Alloy's port is now the same kind of override, documented in `docs/monitoring.md`.

### 8. Testing `docker compose pull` on a branch

Branch images aren't published, and a release's file names GHCR. The test's folder uses the file with one change, `ghcr.io/rikiku/` to a throwaway registry on the job's own machine (`scripts/install-lib.sh`). The image is pushed there and its local tag removed, so `docker compose pull` really does pull it. Nothing goes to GHCR. Compose runs from the folder with a clean environment, so `.env` is the only source of settings.

### 9. A77 on M17's own branch

A77 starts from the previous release's attached files, and v0.14.1 has none. So when the newest tag is 0.14.1 or older, the script makes a release from the checkout, in the throwaway registry: a lower `VERSION` (0.14.99), without the newest migration, with a changed blueprint and label. From v0.15.0 it downloads that release's assets and uses its published images. That path can only run once v0.15.0 exists, so **it isn't exercised until the first branch after this one**. A tag above 0.14.1 with missing assets fails the job rather than falling back.

The script also drops the metrics role before upgrading, so the API has to create it, on any previous release.

Its last step is a patch release: a newer image under the same tag in the throwaway registry, with nothing in the folder changed, then pull and up. It checks the API's reported version moved and the folder didn't change.

### 10. A failure in the PowerShell module's proxy test

On CI run 41574856977, `Watch.Tests.ps1`'s stand-in proxy received `CONNECT dc.services.visualstudio.com:443` before the test's own request. The test sets the proxy inside the `pwsh` running Pester, and that process's own telemetry goes through it. CI's `powershell` job now sets `POWERSHELL_TELEMETRY_OPTOUT=1`, in the job because PowerShell reads it when it starts, too early for the test to set. The module doesn't change.

### 11. The client

Installing the client uses no file from the repo: it's the published image, or the binaries attached to the release (`docs/client.md`). Nothing changes.

## Evidence

On cld01 (Docker 29.8.2, Compose 5.6.0), and in CI:

- **A77** (`sh scripts/a77.sh`), from a release made from the checkout, 0.14.99 without migration 14. The admin and the user signed in with their old passwords, and 5 pages, the alias, the template with its icon, the integration and both users were unchanged. Migrations reached 14, the metrics role was back, and a new page was stored, parsed (job EX0000077), found by search and delivered to the integration set up before the upgrade. The patch step moved the API to 0.15.99 and recreated only `api`.
- **A78** (`sh scripts/a78.sh --project=chromium`), both ways: section 11's steps in a browser, the folder unchanged, only our image and the named upstream ones running. Behind nginx on the external `proxy` network, no port was published and the database was out of the proxy's reach.
- **Compose 2.32.4**, the production host's version: A77 and A78 also pass with it, on cld01, through a copy of the scripts calling that binary for the folder's commands.
- **The monitoring stack** (`sh scripts/monitoring-checks.sh`), from a folder with no checkout: 3 dashboards, 3 alert rules and 3 data sources, then A65 and A67 passed. Memory on an earlier run: 691 MiB idle and 868 MiB during the checks, sum of each container's peak.
- **Mutations** (`PA_MUT_ONLY=... sh scripts/mutation-checks.sh`): the 17 M17 mutations each fail a test. The rest of the script, and its unmutated runs, weren't run for this milestone, because nothing they guard changed.

### The missed email

On CI run 41569199675, A78's second install failed: `no "code" email to owner@example.test`. Authentik's page said the token had been sent, and no mail reached the catcher within the test's 30 s. That run kept no worker log.

The owner asked for about 20 repeats. On cld01, 20 fresh installs behind the proxy, one straight after another, all passed, and no mail was lost. From Authentik queuing the first mail to its arrival, on the 19 iterations measured: usually 0.3 to 0.9 s, five between 2 and 3 s, and one 8.24 s, on an otherwise idle machine.

**Not explained.** The spread fits a busy worker on a loaded CI runner going past 30 s, but nothing shows that, or rules out a lost mail. The repeats didn't reproduce it, and cld01 is faster than a runner. So, as the owner said for an unclear cause, nothing was changed. The test's limit stays at 30 s, and `scripts/a78.sh` prints the mail received and the worker's log if it fails again.

## Questions for the owner

1. **Patches for Alloy, Tempo and Prometheus.** They have no tag that moves within a minor version, so they stay at an exact version until a release changes it, and M18's checks only look for new minor versions. Should M18's third-party check also open a pull request for their patches?

The owner's answers to the earlier questions (2026-11-03):

- **The missed email:** repeated about 20 times; see "Evidence".
- **"Upgrade steps" in release notes:** add a file for them when a release first needs one.
- **Assets and images:** assets attach only once the images are on GHCR (section 1).
- **Compose 2.24 or later:** fine; the production host has 2.32.4.
