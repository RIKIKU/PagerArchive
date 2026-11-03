# ADR 0019: Installing and upgrading with no checkout (M17)

## Status

Proposed (2026-11-03). The owner's brief for M17 and decisions of 2026-11-03: SPEC.md section 7.22 (R84-R84b), A77, A78, on the branch `m17` from main at a82793a. Released as 0.15.0. The UI, the search, the import and the PowerShell module don't change.

## Context

Running PagerArchive needed a checkout: `compose.yaml` mounted repo files, and the upgrade began with `git pull`. M17 is the last item before the first production instance, whose host already runs a reverse proxy on an external Docker network named `proxy`.

The owner's decisions, which replaced the first plan of thin images and a setup container:

- Third-party services run their upstream images unchanged, pinned by version and digest. The project never publishes its own builds of them.
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

`scripts/ci/release-assets.sh` writes them, and A77, A78 and the monitoring check install from its output, so what's tested is what's attached. The release job fails if any of them isn't on the release after uploading.

**`env.example`, not `.env.example`.** GitHub renames an asset whose name starts with a dot.

**Flat names for monitoring.** A release's assets are one flat list, so the two compose files can't have the same name.

### 2. The file in the repo and the file attached are the same file

Not generated. The API's image is written in `compose.yaml` as `ghcr.io/rikiku/pagerarchive:0.15.0`, and no setting in `.env` can change it, so a stale setting can't quietly hold a deployment back after an upgrade.

- **Why:** there's no generating step that could break only at release time, what's tagged is byte for byte what's attached, and anyone can read a release's file in the repo.
- **Cost:** every release raises the version in `compose.yaml` as well as `VERSION`, and two branches can conflict on that line. `check-versions.sh`, `release-assets.sh` and a test each fail with the line to write. Since the third-party images are upstream's, it's one line.
- **Building for tests:** `compose.yaml` has no `build:`, because a deployment has nothing to build from. `deploy/test/compose.yaml` adds it for the test stacks.

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

### 10. The client

Installing the client uses no file from the repo: it's the published image, or the binaries attached to the release (`docs/client.md`). Nothing changes.

## Evidence

See the milestone's report for the commands and their output.

## Questions for the owner

1. **Upgrade steps in release notes.** R84a says a breaking release puts "Upgrade steps" at the top of its notes, but the notes are generated by the release job, and SPEC 12.1 says nothing is released by hand. Editing the notes after the release works today. Do you want a file in the branch that the job puts at the top?
2. **`latest` in the download links.** The docs use `releases/latest/download`, which always gets the newest release. Between the release job and the publish job finishing (a few minutes), `compose.yaml` can name an image that isn't on GHCR yet, and `docker compose pull` fails until it is. Nothing breaks, and running it again works. Is that acceptable, or should the assets be attached after publishing?
3. **Compose 2.24 or later.** The first production host needs it for the override's `!reset`. Is that fine for that host?
