# ADR 0036: Failures and Authentik in Grafana

## Status

Proposed (2026-11-17). The owner: "a failures dashboard would be good"; "Authentik dashboards and monitoring ... a possible gap in our observability right now". SPEC.md R89-R89b, with A115. On the branch `monitoring-failures-authentik` from main; the pull request is labelled `feature`.

## Context

The example monitoring stack (R76, ADR 0013) scrapes the API's `/metrics` with its token and receives the API's and clients' OTLP. Failures are spread over four dashboards, and Authentik, which every sign-in goes through, isn't watched at all.

Authentik 2026.8 (the version in `compose.yaml`) serves Prometheus metrics on port 9300 in both containers, the server and the worker, with no credentials: on the server, its Go front's and Django's (requests by view and status, latency by view, database connections and errors, flow plans by flow, cached flows and policies, workers connected, outposts); on the worker, its tasks (run, errors, retries, rejected, in progress, queued, duration, by task) and database. Port 9000, the one `compose.yaml` publishes at `AUTHENTIK_BIND` and the reverse proxy forwards, has none (`/metrics` and `/-/metrics/` say 404; checked on 2026.8.5). Authentik counts no sign-ins in its metrics: they're only in its event log (`authentik_events_event`, `login` and `login_failed`, whose context holds the username and the stage it failed at, and the row the client's IP).

## Decisions

### 1. Authentik's metrics over a network of their own

The monitoring stack makes a second network, `monitoring-authentik`, `internal: true`, with only Alloy on it from the stack. PagerArchive's `compose.override.yaml` (the same one that joins the API to `monitoring`) joins `authentik-server` and `authentik-worker` to it with aliases (`pagerarchive-authentik-server`, `pagerarchive-authentik-worker`), and Alloy scrapes port 9300 there (`PA_AUTHENTIK_SERVER_ADDRESS`, `PA_AUTHENTIK_WORKER_ADDRESS`).

Rejected:

- **Publish 9300 at a bind address, loopback by default** (as `API_BIND`). Alloy runs in a container, and a container can't reach the host's loopback, so the default would reach nothing; and any LAN address publishes unauthenticated metrics to the LAN. It stays a documented choice only for a stack on another host, firewalled to it.
- **Join Authentik to `monitoring`.** Every other app on that network (R76: it's shared) could then read the metrics, which have no credentials, and reach Authentik's port 9000 from inside.
- **A proxy that adds a token.** Another container and its config for what a network already does.

Only Alloy and the two Authentik containers are on the network, it has no way out, and nothing is published; the reverse proxy keeps forwarding only port 9000, which has no metrics (R84j's lesson from the security review's D8: the metrics must not be reachable through the proxy). `scripts/monitoring-checks.sh` checks who is on the network and that nothing publishes 9300, and A115 that port 9000 has no metrics. Joining an internal network as a second one leaves Authentik's way out (email) on its default network.

An address set to "" (`${VAR-default}`, so set-but-empty isn't replaced) drops that target (`discovery.relabel`), for a stack that watches no Authentik: no `up` series, so "Authentik down" has no data and stays quiet.

### 2. Only the series used, only known labels

`prometheus.relabel "authentik"` keeps a list of metric names (those the dashboard and alerts use, and `up`) and then a list of label names. Authentik's other series name policy bindings (`binding_target_name`, which is a user's or a group's name for a binding to one) and property mappings; the kept ones' labels are views (Django route names), flow slugs, task names, statuses and versions. It's the same rule as Alloy's OTLP allowlist: a second line, so a later Authentik that adds a label to a kept series loses it.

### 3. Sign-ins and Authentik's errors from its event log, through the locked-accounts role

`pagerarchive_authentik_events()` sits beside `pagerarchive_locked_accounts()` in `deploy/postgres/locked-accounts.sql`, `SECURITY DEFINER`, owned by `authentik`, executable only by `pagerarchive_metrics`, so every path that makes the first makes the second: the database's first start, `api-role.sh`, the API's `EnsureLocksRole` while it's the superuser, and a restore. It returns one row for each of a fixed list of kinds and reasons, with how many there were in the last 5 minutes: `login`; `login_failed` by the stage's model (`passwordstage` → `password`, `authenticatorvalidatestage` → `second_factor`, anything else `other`); `system_exception`, `system_task_exception`, `configuration_error`, `policy_exception`, `property_mapping_exception`. Every row every time, zero included, so a query always has a series. Only counts leave it.

The API exposes it as a gauge, `pagerarchive_authentik_events_5m{action, reason}`, cached for a minute as the lock count is (`authentik.Locks.Events`), and logs `metrics: Authentik's events unavailable` when it can't (cached too: at most once a minute).

A window, not a counter: Authentik deletes events as they expire, so a count of all of them would fall, which Prometheus reads as a reset and a spike. Five minutes is long enough to see a burst at a minute's cache and short enough to clear an alert soon after. The `created` index keeps it to a few milliseconds.

Rejected: counting sign-ins from Authentik's logs (not collected, section 5); from its flow metrics (`authentik_flows_plan_time` counts flows started, not how they ended; the executor answers 200 to a wrong password too).

### 4. Alerts

- **Authentik down**: `max by (instance) (up{job="authentik"}) < 1` for 1 minute; one alert per part. No data is OK (nothing scraped).
- **Failed sign-ins**: the 5-minute count above 10, a setting in `rules.yaml` as the others. The guessing protection locks each account after 5 (R44a), so 10 is more than one person mistyping twice.
- **Authentik tasks failing**: the 5-minute `system_task_exception` count above 0, or, if that count isn't there (a role without the function), the worker's `authentik_tasks_errors_total` increase over 5 minutes. Authentik writes one `system_task_exception` event per failed task (`authentik/tasks/middleware.py`), whereas a counter born with its first failure doesn't count that first one in `increase()`.

### 5. Authentik's logs aren't collected

Alloy would need Docker's socket or `/var/lib/docker/containers` (root on the host, and every container's logs), and Authentik's log lines carry usernames, email addresses and client IPs in their fields (P1, R70). Its event log, counted (section 3), and its admin interface (Events → Logs, System → Tasks) cover what the logs would.

### 6. The Failures dashboard

From what exists: the API's metrics (5xx by route, 4xx on `POST /api/v1/pages`, the clock-ahead outcome, clients not receiving, failed deliveries, the nightly run, backups, automatic upgrades, locked accounts, the Authentik counts), the clients' (failed sends, retries, dropped, buffered), Authentik's `up`, Loki (ERROR and WARN lines by message, `label_format message="{{ __line__ }}"`, and the automatic upgrades' failed steps) and Grafana's alert list. The API's log messages are constants (`internal/telemetry`'s test checks every slog call), so grouping by the line is safe; the client's are too. Geocoding has no counter of its own: it runs in the database, so its failure fails the whole ingest and shows as a 5xx on `POST /api/v1/pages`, and an address not found isn't a failure.

### 7. Titles

The two new dashboards are "Failures" (`pa-failures`) and "Authentik" (`pa-authentik`), with no "PagerArchive:" prefix (owner, 2026-11-17: the folder says that, and phones cut long titles).

## Consequences

- An install that already runs the stack gets "Authentik down" for both parts after upgrading the stack, until its override joins them (docs/monitoring.md, "An install that already runs the stack"), or until it sets both addresses to "".
- An install whose API has its own role (R84i) gets the sign-ins function only by running `api-role.sh` again; until then the sign-in panels say "Not counted" and the API logs it once a minute.
- The `backup` service's config label changes (it mounts `locked-accounts.sql`), so the release recreates it.
- The monitoring CI job starts Authentik and Mailpit too, a minute or two longer.
