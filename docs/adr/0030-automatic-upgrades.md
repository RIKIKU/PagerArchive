# ADR 0030: Automatic upgrades (M23)

## Status

Status: Proposed. The owner's decisions of 2026-11-14 ("Automatic upgrades and the version in the menu") and 2026-11-15 (failures and diagnostics in Grafana too): SPEC.md R84d-R84h and A90, on the branch `auto-upgrade` from main. It adds a feature, so the next release is a minor version. The PowerShell module doesn't change.

## Context

A deployment installed with no checkout (R84, ADR 0019) upgrades by downloading a release's `compose.yaml` and archive, then running `docker compose pull && docker compose up -d` (R84a). The owner does this by hand every week, with a dump and copies of the files first and a hash check of the downloads. Automatic upgrades do the same on a schedule, and make a failure safe. The production host's account has no root, but it has Docker and a user crontab. The API must never run Docker (R86a).

## Decisions

### 1. A script on the host, from cron, shipped in the archive

`deploy/auto-upgrade/auto-upgrade.sh` (bash) is in the configuration archive, so every install has the version that matches it, and a release brings its successor. `enable` adds an hourly line, at a random minute, to the user's crontab. `enable --systemd` makes a systemd user timer instead, and `--no-schedule` leaves the scheduling to you.

- **Why cron by default:** it needs no root and no lingering. A systemd user timer stops when the user logs out unless an administrator enables lingering, which the production host's account can't do. Both run the same command, and the script writes its own log file either way, so journald isn't needed for Loki (decision 6).
- **Why not a container:** upgrading needs `docker compose` against the install's folder. A container would need the Docker socket, which is root on the host, and would be replaced by the upgrade it's running.
- **The running script can be replaced:** the upgrade unpacks a new archive over `deploy/`. Bash reads a script as it runs, so the whole script is one function, called on the last line: it's parsed before anything runs.

### 2. The newest full release, from the releases API

The list from `GET /repos/RIKIKU/PagerArchive/releases` is filtered to releases that aren't drafts or pre-releases, then sorted by version. GitHub's "latest" redirect, which A77 uses, would also skip drafts and pre-releases. The list makes the pre-release case testable (A90 gives it one that's higher), and works however "latest" was set. A release whose files aren't attached yet (it's still being published) isn't ready: nothing is installed, and the next hour tries again. It's not marked failed. The API needs no token for a public repository, and an hourly run that finds nothing new makes one request. `AUTO_UPGRADE_GITHUB_TOKEN` exists only for CI's shared runners.

What's skipped and left to the owner, with the reason in the status: a new major version; release notes starting with "Upgrade steps" (R84a); a release that failed here before; and a build that isn't a release (`-dev`).

### 3. Verification: a pinned GitHub CLI with offline bundles

The release workflow already attests the attached files and both images (12.1, ADR 0024), and docs/deploy.md tells people to check them with `gh attestation verify`. The script does the same, without installing anything on the host:

- It downloads GitHub CLI 2.103.3 (linux amd64, the server's only architecture) and jq 1.8.2 into `auto-upgrade/bin/`, each checked against the SHA-256 that its release publishes (pinned in the script). The CLI is unpacked from the checked archive on every run, so a changed binary is never used.
- `gh attestation verify` normally refuses to run without signing in. Given `--bundle`, it doesn't need to: the script fetches each subject's bundles from `GET /repos/RIKIKU/PagerArchive/attestations/sha256:<digest>`, which needs no token for a public repository. The CLI then verifies them against Sigstore's public trust root, fetched and updated through TUF, with `--repo RIKIKU/PagerArchive --source-ref refs/heads/main`. That is, the attestations must have been made by this repository's workflows running on main, where the Release workflow runs (the signer is `ci.yml`, called by `release.yml`).
- Images: the version's own tag is pulled, and the digest it resolves to is verified (`oci://…@sha256:…`). After `docker compose pull`, the image IDs Compose will run must be ones that were verified. A tag that moved in between, because a patch release came out, is verified too, or the upgrade is refused.
- **Cosign instead?** Cosign can verify the same Sigstore bundles, but it needs the certificate identity and the predicate spelled out by hand. The CLI does this for GitHub's attestations already, and it's what the docs and the owner use.
- Verification that can't run (the download fails, a checksum doesn't match, the API can't be reached) or that fails means nothing is installed. It isn't marked failed either: it's tried again the next hour, and every refusal is logged.

### 4. Backup: the backup service's own, kept aside

`docker compose exec backup pagerarchive-backup now` is the same checked backup as the nightly one, and its lock refuses while another backup is running, which is how "never during a backup" is kept. The night's four files are then hard-linked into `pre-upgrade/<set>/` in the backup folder, so a Back up now later that day can't replace the dump a rollback would restore. They cost no space while the night's own files are there. The install's files go to `auto-upgrade/files/<set>/`, with mode 700, because `.env` holds secrets. The last 3 of each are kept (`AUTO_UPGRADE_KEEP`). A `pg_dumpall`, as the owner's script does, wouldn't be checked, and couldn't be restored with R86b's command.

### 5. The health check, and the page rule

After `up -d`, the check needs these, within `AUTO_UPGRADE_START_TIMEOUT` (600 s):

- every service running;
- `/healthz` answering, on the published port, or else the container's address;
- `pagerarchive version` in the container giving the new version;
- the container running a verified image;
- the install's `AUTO_UPGRADE_CHECK_URLS`.

"Migrations applied" needs no check of its own: the API listens only after its migrations have run, and exits if one fails.

**A page stored after the restart.** A quiet network must never roll an upgrade back, so the check applies only when pages were clearly arriving. Before the upgrade, the script counts the pages stored in the 4 windows (2 hours by default) before it. If there were at least 20, 5 a window on average, a page must be stored (`messages.created_at`, by the database's clock) within one window of the restart. If pages arrived at random at that rate, a whole window with none would happen less than 1 % of the time, and a real network's heartbeats make it rarer still. With fewer than 20, the check is skipped and the status says so. Pages a receiver held while the API restarted count when they arrive, as they show the path works again. The window is a setting (`AUTO_UPGRADE_PAGE_WINDOW`, 30 minutes); the 4 windows and the 20 aren't, so the rule stays easy to explain.

### 6. Rollback and restore

- **Files and images:** the copies from decision 4 go back. Each container's image ID and name, recorded before the upgrade, are tagged again (`docker tag <id> <name>`), so `up -d` runs exactly what ran before. Nothing is pulled, which matters for a patch release, whose new image has the same major.minor tag.
- **When to restore:** the script fingerprints both databases before the upgrade and again at the rollback: PagerArchive's latest applied migration, the number of Authentik's migrations, and each database's PostGIS extension versions (R84c). If anything differs, or the fingerprint can't be read, the database is restored. If nothing changed, the old version starts on the same database, losing nothing.
- **How:** R86b's documented path. The night's files are put back from `pre-upgrade/`, and anything a later backup put there moves into the set's `after-upgrade/`. Then `docker compose down`, the old files back, the database's volume removed, the database started empty under the old `compose.yaml`, `docker compose run --rm backup restore <night>`, and `up -d`. The restore checks the row counts again. Pages stored between the upgrade and the rollback are lost. That's at most the health check's window, and the docs say so.
- **A rollback that fails** stops where it is, and the status names the step. A failed restore leaves the stack stopped, so the old API never creates fresh tables in an empty database. A run interrupted part way (the host went down) leaves its state in the status, and the next run refuses to do anything until an admin runs `clear`.
- The failed version goes into `failed_versions` and isn't tried again until `retry`.

### 7. Status, metrics, Grafana

The status is a JSON file in `auto-upgrade/status/`, written atomically with jq. The API mounts that folder read-only, as `${AUTO_UPGRADE_STATUS_DIR:-/dev/null}`, like `PROFILES_DIR`, so an install that hasn't switched it on mounts nothing, and the API says "off". Bind-mounting the folder rather than the file means a renamed status file is seen. `internal/autoupgrade` reads it. It passes on only versions, known words and fixed text, so nothing else in the file reaches a page or a metric's label. It works out the state an admin is told about:

| State | When |
|---|---|
| `off` | switched off, or no file |
| `failed` | the last upgrade failed (rolled back or not), until a later one succeeds or it's cleared |
| `stopped` | a run was interrupted part way |
| `stale` | no check for 3 hours |
| `ok` | none of those |

The metrics are `pagerarchive_auto_upgrade_*`: `enabled`, `last_check_timestamp_seconds`, `last_success_timestamp_seconds{from,to}`, `last_failure_timestamp_seconds{to,step}`, `failures_total`, `rollbacks_total`, `failing`, `state{state}` and `run{step}`.

**Logs to Loki.** The stack takes logs over OTLP today, but the script runs on the host, where it has nothing to send OTLP with. It appends one JSON line per step to `auto-upgrade/status/auto-upgrade.log` (moved aside at 1 MB). The example monitoring stack mounts the same folder read-only into Alloy (its own `.env`'s `AUTO_UPGRADE_STATUS_DIR`), and `loki.source.file` reads it. As with OTLP's allowlist, `loki.process` is the second line of defence: it drops a line that isn't the script's JSON, rebuilds each line from the known fields, and labels it `service_name="pagerarchive-auto-upgrade"`, `step`, `result` and `level`. This only works with the stack on the same host. On another host, the metrics, dashboard and alert still work through the API.

**Dashboard and alert.** The backups dashboard (`pa-backups`) becomes "backups and upgrades", gaining the time since the last check, the last upgrade's state, the last successful upgrade, failures and rollbacks, and a version history from `pagerarchive_build_info`. **Upgrade failed** fires on `pagerarchive_auto_upgrade_failing`, through the default notification policy like every other rule. There's no new channel and no other alerting.

### 8. Testing (A90)

A77's machinery is used again: the latest release is installed from its attached files, with images in a throwaway registry. `scripts/test-releases` stands in for GitHub's releases and attestations APIs and files, and `scripts/test-releases/gh` stands in for the CLI, accepting only digests the test says are attested, because a release built in CI has no real attestation. To make sure the real CLI is used too, A90's last step verifies the real latest release with it. A90 runs in the upgrade job after A77. Grafana's side is in the monitoring check (TestA90), with the status written as the script writes it. Administration's page is in the browser tests.

## Consequences

- The production install switches it on after a release brings the script (docs/deploy.md, "Automatic upgrades"). The owner's weekly manual upgrade becomes optional.
- A release that breaks the upgrade is now also caught on deployments: it's rolled back and marked failed, and Grafana alerts.
- The script is shell, so it's tested by A90's stack run rather than by unit tests.
