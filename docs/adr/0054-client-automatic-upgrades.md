# ADR 0054: Automatic upgrades for receiving clients (M30)

## Status

Status: Proposed. The owner's request of 2026-11-27, after a client was found two versions behind its server: "I think we should have an automated upgrade for this as well." SPEC.md R14b, R14c, R84n-R84p and A155-A157. It adds a feature and a migration, so the next release is a minor version. The PowerShell module doesn't change.

## Context

The server upgrades itself (R84d-R84m, ADR 0030), and a client named in the server's own `compose.override.yaml` moves with it (R84f). A receiving client on its own small host (a Raspberry Pi, a VM) is upgraded by hand, and falls behind. Nothing told anyone: the server didn't know which version a client ran, and the spec had no rule for which client versions a server supports.

## Decisions

### 1. The compatibility rule: never newer than the server (R14b)

There was no rule. The client's API (`POST /api/v1/pages`) has only gained fields since M1, so an older client works with a newer server of the same major version, and the safe rule is the one that needs nothing more: **a client runs its server's release or an earlier one in the same major version**, and what clients send only gains fields within a major version. A client is upgraded to the server's version, never past it, so a server upgrade that's rolled back (R84g) never leaves clients newer than it. "Behind" is a release older than the server's; a build of main isn't a release, on either side, so a development server never shows its clients as behind.

### 2. The server knows each client's version (R14c)

The client sends `User-Agent: pagerarchive-client/<version>` on every post, and its host's automatic upgrades say it in their report. The server stores the last version and **since when it's been behind** in `clients` (migration 00040), writing only when the version changes (an in-memory cache of what it last wrote), and works it out again at every start, when its own version may have changed.

- **Why in the server, not the client's telemetry:** the client's OTLP metrics need telemetry switched on, and the client that falls behind is the one nobody looks at. Every client posts pages, so the server always knows. The alert then works for every client, upgraded automatically or by hand.
- **Why "since" in the database:** "behind for more than a day" must survive the server's restarts, and the most common restart is the server's own upgrade, which is what makes clients behind. A Grafana `for: 24h` would reset at every gap in the series. The alert is `time() - behind_since > 86400`, as "Client not receiving" is `time() - last_seen`, and the monitoring check can move it back in the database to test it.
- Only `X.Y.Z` and `X.Y.Z-dev+<commit>` are kept (R84k): a User-Agent is anything a client sends, and it becomes a label.

### 3. A script on the client's host, like the server's

`client-auto-upgrade.sh` is attached to every release, with `client-compose.yaml` and `client-env.example`, and lives in the client's folder beside `compose.yaml`, `.env` and `key`. It's the server's script cut down: the same commands (`enable [--systemd|--no-schedule]`, `disable`, `run`, `status`, `verify`, `retry`, `clear`), the same hourly cron line at a minute chosen once, the same status file and JSON log line per step, the same verification (decision 3 of ADR 0030), the same version checks (R84k) and the same host-wide lock (R84m). It's a separate file, not the server's with a mode, so neither can break the other, and a client host gets 600 lines of shell, not the server's archive.

What differs, and why:

- **The target is the server's version.** The script asks the server (decision 5), then takes the newest full release on GitHub not newer than it, in the client's major version. Asking the server, rather than taking the newest release, is what keeps a client from going first.
- **`client-compose.yaml` names the exact version** (`pagerarchive-client:1.24.0`), where the server's `compose.yaml` names `MAJOR.MINOR`. A patch release reaches the server at its next pull, but a client pulling `1.24` could get `1.24.1` before its server has it. `stamp-version.sh` writes it, as it does the server's tag; the repository says `:main`.
- **Both architectures.** A Pi is arm64, so the pinned GitHub CLI and jq each have an amd64 and an arm64 checksum.
- **No backup of data, no restore.** The client has no database: its buffer is in memory (R15). The copies are its files; the rollback re-tags the image that was running, as the server's does, and pulls nothing.
- **The health check is "still running a minute later".** The client has no `/healthz` and may receive nothing for an hour on a quiet network. A client that can't start (its settings, its radio, a broken image) exits, and Compose restarts it over and over, so: the new version answers `--version` in its container, from the verified image, and a minute later it's the same container, running, not restarted. A90's page rule doesn't apply: a quiet network must never roll a client back either.
- **No Loki.** The script's log is on the client's host, where the example monitoring stack's Alloy isn't. What matters reaches Grafana through the server (decision 5); the log stays on the host for `status` and the owner.

### 4. Compose is the documented install

docs/client.md showed `docker run`. Automatic upgrades need a folder whose files they can replace and a project `docker compose` can pull and recreate, so the documented Docker install becomes Compose: the three release files, `.env`, and the key in its own file (mounted read-only, so the key is never in `.env`, a command line or the container's environment). `docker run` stays, as the way without automatic upgrades, as does the Pi binary.

### 5. The report: the server's version back, the status forward (R84p)

`POST /api/v1/client/auto-upgrade`, with the client's own key, carries the script's status file and the client's version; the answer is the server's version. The script sends it at each step of a run and when switched on or off, so the server's metrics show a run going now, as the server's own do (owner, 2026-11-20). The key goes in a header file only the script's user can read (`curl -H @file`): another user on the host can read any process's command line.

- **Why the server, not a status file the client container reads:** the server's metrics are what the dashboard already reads for clients, they work with the client's telemetry off, and the report is also how the script learns the server's version, so one request does both.
- The server keeps the last report as `clients.auto_upgrade`, reduced by `autoupgrade.Sanitize` to versions, known words, times and counts: the notes, failure reasons and history are the host's text and stay there. The metrics read it back with the same parser as the server's own status (`autoupgrade.Parse`), so "stale" (no check for 3 hours) and "failing" mean the same thing on both.
- Unknown fields are ignored (R14b), so a newer client's report works with an older server in the same major version. A server from before this answers 404, and the client isn't upgraded: it can't learn the server's version, so it waits.

### 6. One alert

**Client behind the server** fires per client after a day behind (a setting in its rule). There's no "client upgrade failed" alert, as there's no other alerting for the server's ("alert is a strong word"): a client whose upgrade failed stays behind, and the alert fires after a day, while the dashboard shows the failure at once.

### 7. Testing

- **A155**, in A90's run (the upgrade job): the previous release's client image from GHCR in the throwaway registry, this build's client files, a client on its own Compose project reading stdin (no dongle; the host's network to reach the API). N's client image is first published broken (it prints its version and exits), so its health check fails and it's rolled back; then republished working, retried and upgraded; then B, which the server rolled back, is out and the client stays on N. The server's PREV can't say its version, so the first check installs nothing.
- **A156**, in the acceptance tests: the User-Agent, the report, the sanitising and the metrics, with the server at made-up versions.
- **A157**, in the monitoring check: the dashboard and the alert, with since when moved back a day in the database.
- The root package's tests source the script for its version checks, its choice of release and the key's path to curl.

## Consequences

- A client on its own host follows its server within the hour once switched on, and never goes first: upgrade the server, and the clients follow.
- Every client says its version, so the Clients dashboard shows who's behind, and the alert says so after a day, automatic upgrades or not.
- A client installed with `docker run` or as the Pi binary isn't upgraded automatically; the docs say so. A systemd service for the Pi binary could get the same script later.
- The first release with this needs its server upgraded before its clients' automatic upgrades do anything.
