# ADR 0033: The API's own database role

## Status

Status: Proposed. The owner's decision of 2026-11-15 on the security review's finding D1 (with the API review's F4): SPEC.md R84i and A102, on the branch `sec-dbrole`. Scope, from the owner the same day: new installs get the role from the start; the one existing install is switched by hand with a script, not by an automatic upgrade; a release with the role must still run, unchanged, on an install that hasn't switched, so automatic upgrades can install it and roll back from it. The PowerShell module doesn't change.

## Context

Until now the API connected as `postgres`, the PostgreSQL superuser, to the server it shares with Authentik (`compose.yaml`, `DATABASE_URL`). It needed that for three things: its first migration creates PostGIS, which only a superuser can; it creates the locked-accounts metric's role and function in Authentik's database on every start (ADR 0006, `EnsureLocksRole`); and it updates PostGIS's extensions after a newer image (R84c). Nothing else it does needs more than owning its own tables.

So a second bug in the API, SQL injection or code execution in its container, would have had the whole data tier: Authentik's database (password hashes, the API token, OIDC secrets, the power to make anyone an Authentik superuser) and, through `COPY … TO PROGRAM`, a shell in the database container. It would also void boundaries the design relies on, such as the least-privilege Authentik API role in the blueprint.

## Decisions

### 1. One role, `pagerarchive`, that owns PagerArchive's objects

The API's role owns its tables, functions and types and runs its own migrations, so there's no separate migrator to keep in step: a release's migrations run as the role at start, as they did as the superuser. It has `USAGE, CREATE` on the public schema of its database (`postgres`, the database the API has always used, so nothing moves), `CONNECT, TEMPORARY` on that database, and nothing else: `NOSUPERUSER NOCREATEDB NOCREATEROLE NOREPLICATION NOBYPASSRLS`, no membership in any role (so not in `pg_execute_server_program`, `pg_read_server_files`, `pg_write_server_files` or `pg_read_all_data`), and no `CONNECT` on any other database. `CONNECT` is PUBLIC's by default, so it's taken from PUBLIC on every other database and given back to the roles that need it: `authentik` on its own, `pagerarchive_metrics` on Authentik's. Superusers need no grant.

`deploy/postgres/api-role.sql` says all of that, as plain SQL run by the superuser, safe to run again. It also creates `postgis` and `pg_trgm` if they're missing: PostGIS needs the superuser and `pg_trgm` needs `CREATE` on the database, which the role isn't given. The migration's `CREATE EXTENSION IF NOT EXISTS` is then a no-op the role may run. The same file is run by the database's first-start script, by the switch-over script and by a restore, and the API's tests run the embedded copy (`pagerarchive.APIRoleSQL`), so there is one definition.

Handing over existing objects is by catalogue, not `REASSIGN OWNED BY postgres` (which refuses the bootstrap superuser): every table, view, sequence (not one a column owns, which follows its table), function and type in `public` that isn't an extension's. PostGIS's `spatial_ref_sys` and functions stay the superuser's, which the role only reads and calls.

### 2. New installs: from `.env`, at the database's first start

`.env` gets `PAGERARCHIVE_DB_USER=pagerarchive` and `PAGERARCHIVE_DB_PASSWORD`, generated like the other secrets. `deploy/postgres/20-pagerarchive.sh` runs once, when the volume is new, after `10-authentik.sh`: it runs `api-role.sql` and sets the password, and, when `AUTHENTIK_METRICS_DB_PASSWORD` is set, `metrics-role.sql`, which makes the metric's role and `pagerarchive_locked_accounts()` (now `deploy/postgres/locked-accounts.sql`, embedded by the API too). The SQL files are mounted in a folder of their own under `/docker-entrypoint-initdb.d/`, which the image's start-up doesn't run, so like the first-start script they carry no config label and don't recreate `db` when they change. Passwords reach `psql` through the environment (`\getenv`), never a command line. A user set without a password, or another user name, fails the first start and says why, rather than leave an API that can't sign in.

### 3. `DATABASE_URL` falls back to the superuser while `.env` names no role

Compose can't say "this, if a variable is set, else that", so the URL takes each part with a default:

```yaml
DATABASE_URL: postgres://${PAGERARCHIVE_DB_USER:-postgres}:${PAGERARCHIVE_DB_PASSWORD:-${POSTGRES_PASSWORD}}@db:5432/postgres?sslmode=disable
```

With both settings, the API has the role and never sees the superuser's password. With neither, the install from before the role, it's exactly the old URL. That is what lets this release run on an install that hasn't switched: an automatic upgrade (ADR 0030) installs it with nothing to do, and its health check and rollback work as before. The API checks `rolsuper` after its migrations and, while it's the superuser, logs one WARN on every start naming the script, and keeps doing what only a superuser can (`EnsureLocksRole`, as before). As its own role it skips `EnsureLocksRole`, which it could no longer do.

Rolling back to a release from before the role (1.11.0 and earlier) works switched or not: that release's `compose.yaml` doesn't read the two settings, so its API connects as the superuser, which can use objects the role owns.

### 4. Existing installs: one script, run by hand

`deploy/postgres/api-role.sh`, in the archive, run on the host from the install's folder. It counts every table's rows; runs `api-role.sql` and sets the password in one transaction (`psql -1`, with a 10 s lock timeout, so a busy table makes it give up with nothing changed rather than hold the API up); makes the metric's role and function if `AUTHENTIK_METRICS_DB_PASSWORD` is set; signs in as the role over TCP, as the API will, and checks it isn't a superuser, can't connect to Authentik's database, can't `COPY … TO PROGRAM`, and can read and create tables; only then writes the two settings to `.env` (a new 64-character hex password, never printed) and recreates the API alone (`up -d --no-deps api`, so `db` and Authentik keep running); waits for the API to connect as the role, and counts the rows again. If the API doesn't come back as the role within 3 minutes, `.env` is put back and the API recreated as before. Until `.env` is written, the running API notices nothing: it's the superuser, and the objects it uses are simply owned by someone else.

Run again, it reuses the password in `.env`, so `.env` and the API's container don't change; it re-applies the rights, so it also repairs a switched install after something reset them. `undo` takes the two lines out of `.env` and recreates the API. The role and its ownership stay; a superuser needs neither.

Why not automatic, in an upgrade: the owner's decision, as there is one existing install. A hands-off switch would have to generate and store a secret from inside an upgrade, change `.env` under the operator, and work through every rollback path; one script, run once while watching, is less to get wrong.

`db`'s environment gains the two passwords (for its first-start script), so the first `up -d` after this release recreates `db`, and Compose restarts Authentik with it, as for a new PostGIS image. `api-role.sh` recreates only the API; `db` picks up the changed environment at the next upgrade.

### 5. What keeps the superuser, and why

- **The backup service.** It dumps both databases whole, and a restore drops and makes them again. It runs only the archive's script, in its own container, and the API can't reach it (R86a). A restore now also gives the roles their rights back, as `pg_dump --no-privileges` dumps hold no grants: if the dump's tables are owned by `pagerarchive` and the role doesn't exist (a new install restored without the old `.env`), it's made first, unable to sign in until `api-role.sh` gives it a password; after the restore `api-role.sql` and `metrics-role.sql` run again. So an automatic upgrade's rollback with a restore leaves a switched install switched.
- **Automatic upgrades** query the database as the superuser through `docker compose exec db`, on the host, as before.
- **PostGIS updates (R84c).** `ALTER EXTENSION postgis UPDATE` needs the superuser. A switched API logs the existing WARN with the fix after a newer image, and works on the older version until someone runs it. That's a hand step after a PostGIS image update, alerted; see Consequences.

## Consequences

- A flaw in the API reaches PagerArchive's own data and no further: not Authentik's database, not the server's files or programs.
- An unswitched install keeps working, with a WARN on every start, until its operator runs the script.
- The locked-accounts metric's role is made by the database's first start or by the script, not by the API, once the API has its role: setting or changing `AUTHENTIK_METRICS_DB_PASSWORD` later means running `api-role.sh`.
- After a PostGIS image update, a switched install needs `ALTER EXTENSION … UPDATE` run as the superuser (the WARN says so). Decided (owner, 2026-11-19: "option 1 plus the alert"): nothing privileged does it on its own, neither a one-shot service on `up` nor the backup service, so the superuser's use stays in the operator's hands. It stays a hand step, and it's alerted, since a WARN in the logs is easy to miss: `/metrics` has `pagerarchive_db_extension_update_pending{extension}` (PostGIS's extensions and `pg_trgm`), checked at start and every hour, and the example stack's **Database extension update pending** says what to run, `docker compose exec db psql -U postgres -c 'ALTER EXTENSION <name> UPDATE;'` (R84c, A83).
- Tests: A102 in the database tests (`TestA102APIRole`: an older install's database and a new one, rights checked directly, migrations as the role, rows kept, the SQL run twice), A77 (an install of the latest release upgraded, still the superuser with the WARN, then switched by the script, checked, and the script run again) and A80 (a new install and its restore both end with the role). The stack tests, A78 and the monitoring checks run new installs, so they run as the role too.
