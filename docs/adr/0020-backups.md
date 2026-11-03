# ADR 0020: Backups (M19)

## Status

Status: Accepted (owner, 2026-11-04). The owner's brief for M19 of 2026-11-03: SPEC.md section 7.24 (R86-R86c), A80 and R76's `monitoring` network, on the branch `m19` from main at 452e41e. To be released as 0.16.0. Search, the import and the client don't change. **The PowerShell module doesn't change**, so its version stays.

## Context

The first production host keeps each project's files under `/opt`, and a file backup tool copies folders to a NAS. So the backups are plain files in a mounted folder, and a finished file is safe to copy at any time. They run inside the stack, with nothing scheduled on the host and no image of our own build of a third-party app (R84).

## Decisions

### 1. Upstream's image and a script from the archive

Two services run `postgis/postgis:18-3.6` with `deploy/backup/backup.sh`, mounted from the release's archive. The API's image was the other choice.

- **Why:** `pg_dump` and `pg_restore` must suit the server, and this is the server's own image, by the same tag, so they move together. The API's image is distroless and static (ADR 0001), and adding PostgreSQL's client and server to it would make it larger for every install, for a job that runs once a night and needs a whole PostgreSQL for the check anyway.
- **Cost:** it's a shell script, tested by stack tests (A80) rather than unit tests. It gets a `pagerarchive.config` label like every mounted file (ADR 0019), so a release that changes it recreates the two services.

### 2. Two services, so the check really has no network

| | `backup` | `backup-check` |
|---|---|---|
| Does | Dumps, prunes, writes the status | Restores into a throwaway database and compares |
| Network | The stack's | `network_mode: none`: only `lo` |
| Secrets | `POSTGRES_PASSWORD` | None |
| Sees | The folder, the status volume | The folder, its own scratch volume |

They talk through the folder. `backup` renames `<night>.counts.tsv` last, `backup-check` notices it within 10 seconds, and it writes `<night>.check`, which `backup` waits for. A throwaway PostgreSQL listening only on a socket, in one container, would also have reached no network, but that container would still have had the database's password and a route to it. A test checks the mode and the missing secrets, and A80 checks the interfaces.

### 3. Counts and dump from one snapshot

Pages keep arriving during a dump, so comparing a restore with the live database would differ every night. An open transaction exports a snapshot, and the counts and `pg_dump --snapshot` both read it. So a difference means something, and it's named: `the check found the pagerarchive restore differs from the dump at public.goose_db_version 15`.

The comparison covers every ordinary table outside the system schemas. A database with no tables at all fails the backup, as a dump without Authentik's users isn't one.

The check does a full restore, indexes included, so it would catch a dump that can't be restored because of an index or a constraint. A data-only restore would be quicker, by an amount not measured: see "Questions for the owner".

### 4. The files

`<night>.pagerarchive.dump`, `<night>.authentik.dump`, `<night>.counts.tsv` and `<night>.check`, in one flat folder, by the night's local date. Each is written as `<name>.partial` and renamed. Leftover `.partial` files go at the next run.

**Owner and mode.** Mode 640, owned by `BACKUP_UID`:`BACKUP_GID` (default root). A dump holds Authentik's password hashes, so it isn't world-readable. The default folder is `./backups`, which is `/opt/<project>/backups` on the production host.

**Pruning follows a good check only, and goes by the name's date.** So a run of failures can't delete the last good backups, and a copied file's changed time doesn't matter. A night's four files go together.

### 5. The status file, on its own volume

`backup` writes `status.json` to the `backup-status` volume, which the API mounts read-only. The API reads it at each request, and it decides the state: `ok`, `waiting`, `failed` or `overdue`.

- **A file, not a table.** A80's failure is the database being stopped, when nothing can be written to it.
- **Not in the folder.** So the API never mounts the folder. A compromised API can't read password hashes from a dump.
- **A new install is `waiting`, not failed.** The first start records when it began. With no good backup 36 hours after that, it's overdue. If the status file never appears, 36 hours from the API's start does the same.
- **One state in two places.** `pagerarchive_backup_failing` comes from the same function as Administration, so the alert and the page can't disagree. That's why the 36 hours aren't a number in `rules.yaml`, unlike the other rules' thresholds. `pagerarchive_backup_last_good_timestamp_seconds` is there too. `/metrics` needs the database, so while it's down neither is scraped, and "API unreachable" fires if it stays that way. The failure then shows as soon as the database is back.

### 6. The schedule

`BACKUP_AT` (default `04:00`, in `TZ`), an hour after the nightly run's default 03:00, so a night's deletions are in that night's dump. It's checked once a minute against the time of day, not slept towards, so clock changes don't matter. A night missed while it was down is made up on the next start, a minute later, which leaves an upgrade's migrations to finish first. A new install doesn't back up until its first `BACKUP_AT`. A failed night isn't retried until the next: Administration has said so, and `docker compose exec backup pagerarchive-backup now` runs one.

The same command is A80's trigger. `--night` and `--at` are its clock.

### 7. Restore

`docker compose run --rm backup restore <night>`, before `up -d`. `run` starts `db` alone, which on a new volume creates Authentik's role. Then it recreates both databases from `template0`, restores each in one transaction, and compares the counts again.

It **refuses an install with tables in it**, rather than stopping the other containers or overwriting them, and a night that failed its check unless `--unchecked` is given. Refusing an install that's been started means an install that's run even once needs `docker compose down -v` first. That's a harder first step, chosen because the command can't then destroy an archive by mistake. Privileges aren't in the dumps: `pagerarchive_metrics` and its grant are the API's, on start (ADR 0019).

`.env` isn't in a backup, and has to be kept apart: `docs/deploy.md` says so, with `INTEGRATION_SECRETS_KEY` first.

### 8. P1: nothing from a dump in a log or message

`pg_dump`, `pg_restore`, `psql` and the throwaway server's output is all discarded, as an error from any of them can quote a row. Every message is fixed text. The one exception is the table and count that differ, reduced to letters, digits, `_` and `.`. A80 greps both services' logs for page text and addresses, after its failures.

### 9. The monitoring stack owns its network

`name: monitoring`, not external, not prefixed. PagerArchive joins with a documented `compose.override.yaml`, with the alias `pagerarchive`, which is now `PA_API_ADDRESS`'s default, as other stacks can have an `api` too. `PAGERARCHIVE_NETWORK` is gone.

**Only Alloy is on it.** The brief says the stack declares the network. Putting all five on it would let every app that joins reach Tempo, Loki, Prometheus and Grafana's back end directly, with no sign-in on the first three. So the stores stay on their own internal network, renamed `internal`, as before.

**`monitoring` isn't `internal: true`.** Alloy must be able to publish its OTLP port for clients on other machines, and Docker can't publish on an internal network. It was on PagerArchive's ordinary network before, so this takes nothing away.

**Order.** The stack must start before PagerArchive, once the override is there, or Compose stops with `network monitoring declared as external, but could not be found`. On a reboot Docker restarts both on its own. The docs say so, and give the one-off switch for a hand-made network. `monitoring-checks.sh` installs in that order, using the override from the docs as written, and stops at once if a `monitoring` network already exists, because the name is host-wide. So two runs can't share a machine.

**A patch release** still needs only pull and up: everything new is in this minor release (R84b), and `deploy/backup` joined the list `check-versions.sh` watches.

## What a backup costs

`scripts/a80-size.sh`, on cld01 (8 vCPU, a disk writing 2.9 GB/s), the full stack with 4,562,500 made-up pages, a 2,378 MB database:

| | Time | Peak memory it can't give back | Peak with file cache |
|---|---|---|---|
| Dumps (`backup`, `db`) | 31 s | `backup` 6 MiB, `db` 51 MiB + its `shared_buffers` | `db` 2,286 MiB |
| Check (`backup-check`) | 225 s (215 s in the check itself) | 175 MiB + 64 MiB `shared_buffers` | 3,224 MiB |
| Restore (`db`) | 235 s | 119 MiB + its `shared_buffers` | 3,929 MiB |

Files: PagerArchive's dump 299 MB, Authentik's 3 MB, 288 MiB in all, so about 4 GB for 14 nights. The check needs about the database's size again on Docker's disk while it runs.

**The budget** (section 8) is for the app's steady state, of API, frontend and PostgreSQL. A15 sums `api` and `db`, so these services aren't in its sum. Idle, they're two shell loops: 820 KiB and 760 KiB, at 0.00% CPU. During the nightly few minutes, what can't be given back is about 245 MiB on top of the app's. The file cache is given back when anything else wants it, as for any large read. There's no `mem_limit`: a limit would turn a tight night into a failed check and wasn't measured.

**Not measured: a spinning disk.** The production host's will be slower. The dumps read 2.4 GB, and the check writes that much and builds the indexes (fsync off). Expect minutes rather than seconds, and possibly much longer for the check. `docker compose logs backup` gives the real times each night, and the script gives the check 12 hours before calling it failed.

## Evidence

On cld01 (Docker 29.8.2, Compose 5.6.0), and in CI:

- **A80** (`sh scripts/a80.sh --project=chromium`), from a folder of the release's files only. The schedule fired with nothing but `BACKUP_AT`. During a held dump the folder had only `.partial`. A truncated dump and one with a table's rows missing each failed the check, and the last-good metric stayed put. 37 hours back in the clock showed as failing. 15 nights kept 14, and `BACKUP_KEEP_DAYS=2` kept 2. With the database stopped, the browser showed the failure in Administration, reached from the root URL through Authentik's sign-in. After `down -v`, the command from `docs/deploy.md` restored the night, and A77's checks found every page, alias, template, icon and integration, both users signed in, and a new page delivered.
- **The alert** (`PA_MON_RUN=TestA80 sh scripts/monitoring-checks.sh`): "Backup failing" fired 46 s after the database stopped, and resolved after the next good backup.
- **The network** (`sh scripts/monitoring-checks.sh`): A78's install, A65 and A67 pass through the docs' override, with only Alloy and the API on `monitoring`.
- **Mutations** (`PA_MUT_ONLY=... sh scripts/mutation-checks.sh`): the 4 M19 mutations each fail A80: a dump straight to its final name, the check skipped, old dumps not deleted, and the restore leaving out Authentik's database (the admin can't sign in). The rest of the script wasn't run, because nothing it guards changed.
- **Not tested: Compose 2.32.4**, the production host's version. These runs used 5.6.0, and CI uses the runner's. The new services use only long-standing Compose features, and `!reset`, the newest thing in the docs' overrides, was tested on 2.32.4 for ADR 0019.

## Questions for the owner

1. **The check's cost on the production disk.** It does a full restore each night. If it's too slow on the spinning disk, it could skip the indexes, at the cost of no longer proving them. Keep it full until there are real times?
2. **Who reads the files?** The default owner is root with mode 640. If the host's file backup tool doesn't run as root, set `BACKUP_GID` to its group. Which is it on the production host?
3. **The restore's first step.** It refuses an install that has any tables, so the steps are `down -v`, then restore, then `up -d`. Is that the right side to err on, given that `down -v` is itself destructive and the docs now tell people to run it?

### The owner's answers (2026-11-04)

1. The check stays a full restore, indexes included. Decide again if production's real times call for it.
2. Nothing reads the files yet on the production host. `BACKUP_GID` is set when its file backup tool is set up.
3. Restore refuses an install that has tables: `down -v` first is deliberate.
