# 13. Milestone M12: telemetry

Date: 2026-10-30

## Status

Proposed. Waiting for the owner (questions at the end).

## Context

M12 (SPEC.md section 7.16: R69-R76, checks A62-A66; main 4bdf572) adds telemetry for troubleshooting incidents: following a page from the receiver to the database, finding where time goes, tying high load to what users were doing, and being told when a client stops sending. The API and the receiving client export traces, logs and (the client) metrics over OTLP; the API's metrics stay on `/metrics`; and `deploy/` gets an example monitoring stack.

The owner's brief for the milestone asked, besides the requirements, for:

- the official OpenTelemetry Go SDK and OTLP exporters, with versions and licences here, and OTLP over HTTP or gRPC chosen with a reason;
- privacy "by design, not by remembering": an allowlist of attribute keys that every exported span, event, log record and metric label goes through, recorded here;
- a choice, with reasons, between child spans and span links for integration deliveries, and for how live-update streams and the PowerShell watch are recorded;
- the example stack's images pinned by version and digest, LAN-only, with retention, provisioned data sources, three dashboards, two alert rules and a webhook contact point for tests;
- docs for someone who knows Docker and PowerShell but not this stack;
- mutation proof for nine named breakages, and screenshots.

## Already decided elsewhere (cited, not re-decided)

- P1: no log line, error, or telemetry ever contains page text. Database errors are reduced to their SQLSTATE (`store.SafeError`, ADR 0002 §5).
- R14, ADR 0002 §1: each client authenticates with its own API key, which decides its client ID.
- R15, section 14: the client buffers pages in memory only, drops the oldest when full, and retries with backoff (1 s to 60 s).
- R13a: the client prints page text to its console only with `PRINT_PAGES=true`, and never to a file.
- R39, R52, ADR 0006 §11: `/metrics` is hand-written Prometheus text, guarded by `METRICS_TOKEN`, with clients' IDs (not names) as labels. Its names don't change (A32).
- R51a, ADR 0006 §3: integration deliveries run from the `deliveries` table, one integration's pages one at a time, and must be attempted within 5 s.
- R63b, ADR 0010: live updates and the PowerShell watch are one long `GET /api/v1/stream` each; pages are stored and announced in ID order under one lock.
- Section 8: the API stays within 256 MB with 20 viewers at 1,500 pages a day.

## Decisions

### 1. Libraries, versions and licences

| Module | Version | Licence | Why |
|---|---|---|---|
| `go.opentelemetry.io/otel`, `/trace`, `/metric`, `/log` (API) | v1.47.0 | Apache-2.0 | The official API |
| `go.opentelemetry.io/otel/sdk`, `/sdk/metric`, `/sdk/log` | v1.47.0 | Apache-2.0 | The official SDK: batching, bounded queues, sampling, resources |
| `go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp` | v1.47.0 | Apache-2.0 | OTLP/HTTP exporters (§2) |
| `.../otlpmetric/otlpmetrichttp` | v1.47.0 | Apache-2.0 | |
| `.../otlplog/otlploghttp` | v0.23.0 | Apache-2.0 | The logs exporter is still versioned 0.x |
| `go.opentelemetry.io/contrib/bridges/otelslog` | v0.21.0 | Apache-2.0 | The official bridge from `log/slog` to OTel logs (R73) |
| `go.opentelemetry.io/proto/otlp` | v1.11.1 | Apache-2.0 | The OTLP messages; the test collector decodes them |
| indirect: `google.golang.org/protobuf` v1.36.12 (BSD-3-Clause), `google.golang.org/grpc` v1.84.0 (Apache-2.0), `github.com/grpc-ecosystem/grpc-gateway/v2` v2.31.0 (BSD-3-Clause), `github.com/cenkalti/backoff/v5` v5.0.3 (MIT), `github.com/go-logr/logr` v1.4.4 and `stdr` v1.2.2 (Apache-2.0), `go.opentelemetry.io/auto/sdk` v1.2.1 (Apache-2.0) | | | All permissive, compatible with MIT |

No contrib instrumentation (`otelhttp`, an `otelpgx`): the request middleware (§4) and the database tracer (§4) are a few dozen lines each, written to record nothing but what §3 allows, so there's no library default to switch off.

Binaries grow: the server from 16.2 to 24.6 MB and the client from 7.1 to 16.2 MB (stripped, linux/amd64). Most of it is gRPC, which `go.opentelemetry.io/proto/otlp`'s collector packages pull in even though only HTTP is used. Memory is what the budget counts, and it stays within it (Evidence).

The example stack's images (§9) are separate programs, not linked: Grafana, Loki and Tempo are AGPL-3.0, Alloy and Prometheus Apache-2.0. They're used unmodified, as an example a deployment may run.

### 2. OTLP over HTTP, and never blocking

**HTTP (`http/protobuf`), not gRPC.** It's the protocol the OpenTelemetry spec recommends as the default; it goes through ordinary HTTP proxies and reverse proxies, which matters for a Raspberry Pi receiver at another site; it's one well-known port (4318); and the exporters don't need a gRPC client connection (gRPC is linked in anyway, §1, but never runs). `http/json` works too. `grpc` is refused: that signal stays off and the log says why.

**Off unless configured** (R69). A signal is on only when `OTEL_EXPORTER_OTLP_ENDPOINT` or its own `OTEL_EXPORTER_OTLP_<SIGNAL>_ENDPOINT` is set, `OTEL_SDK_DISABLED` isn't `true`, and `OTEL_<SIGNAL>_EXPORTER` isn't `none`. Otherwise no exporter is made: nothing is exported and nothing connects. Traces and logs are on for the API; traces, logs and metrics for the client (R75: the API's metrics are never pushed). A setting it can't use (`grpc`, an exporter other than `otlp`) is logged and leaves that signal off; telemetry never stops the API or a receiver.

**Standard settings.** The SDK reads the rest itself: `OTEL_SERVICE_NAME` and `OTEL_RESOURCE_ATTRIBUTES`, `OTEL_TRACES_SAMPLER` and its argument (every trace by default, `parentbased_always_on`), `OTEL_EXPORTER_OTLP_*` headers, timeout, compression and certificates, `OTEL_BSP_*`, `OTEL_BLRP_*` and `OTEL_METRIC_EXPORT_*`. Two deliberate exceptions: only W3C `traceparent` is read and sent (`OTEL_PROPAGATORS` is ignored), and baggage never is, because it could carry anything into a span (R70).

**Never blocking** (R69, A64):

- Spans and log records go through the SDK's batch processors: bounded queues (2,048 by default), exported in the background. A full queue drops, it never waits.
- **Exporter retries are off.** With them on, a hung collector held each batch for up to the batch timeout (30 s; measured: the first "dropping" line came 30 s in) while the queue behind it filled and dropped silently. Off, a batch the collector doesn't take within `OTEL_EXPORTER_OTLP_TIMEOUT` (10 s) is dropped at once and said so. A collector blip loses one batch; for troubleshooting telemetry that's the right trade.
- **At most one line a minute.** Export failures are counted, not returned to the SDK (which would print each one), and the program logs, to its own stdout only (never exported, so a dead collector can't loop): `telemetry: the collector isn't taking data; dropping telemetry (said at most once a minute)`, with the signal, how many items were dropped since the last line, and the error (the collector's address, which is the operator's own setting). The SDK's own error handler goes to the same line, and its internal logger is silenced.

### 3. The allowlist (R70)

Everything exported passes `internal/telemetry/filter.go`. It isn't a list of things to remove; it's the only things that may stay:

- **Span names** must be registered: the router's patterns with their method (`GET /api/v1/messages/{id}`, registered by `server.Handler` from the same table that builds the router), the sqlc query names (parsed from `queries.sql`, embedded) and `SQL <first word>` for the few statements outside sqlc, and the fixed step names below. Any other name is exported as `unnamed`.
- **Span attributes and link attributes**: only these keys, and only values of the listed kind (numbers must be numbers, booleans booleans; a "code" is reduced to its kind, below; an "ID" must look like a client ID):

  | Key | Kind | Set on |
  |---|---|---|
  | `http.request.method`, `http.route` | fixed | request spans (§4) |
  | `http.response.status_code` | number | request spans, the client's send |
  | `error.type` | code | any failed span |
  | `user.id` | number | a signed-in request (R70) |
  | `user.role` | code | a signed-in request |
  | `db.system.name`, `db.operation.name` | fixed | database spans |
  | `pagerarchive.client.id` | ID | the page post and ingest spans |
  | `pagerarchive.ingest.outcome` | code | ingest; the client's root span |
  | `pagerarchive.message.id` | number | **the ingest span only** (R71) |
  | `pagerarchive.send.attempt` | number | the client's send |
  | `pagerarchive.live.connections` | number | live.publish |
  | `pagerarchive.delivery.kind`, `.result` | code | integration.deliver |
  | `pagerarchive.delivery.attempt` | number | integration.deliver |
  | `pagerarchive.stream.caught_up` | number | the stream's span |
  | `pagerarchive.nightly.deleted`, `.redacted`, `pagerarchive.reparse.messages` | number | nightly, reparse |
  | `pagerarchive.buffer.pages` | number | (reserved for the client's buffer) |

- **Codes** pass only as a kind: a snake_case code (`clock_ahead`), a SQLSTATE, a Go type name (`*url.Error`), an HTTP status (`HTTP 503`) or one of a few fixed messages the code makes ("connection refused"); anything else becomes `other`, so an error's text can't carry a request's values.
- **Span events are dropped** (they're where libraries put exception messages) and **status descriptions are emptied**; the status code stays.
- **Log records** keep their message (every `slog` call's message is a string constant, which a test checks across the code base) and only these attribute keys: `client` (ID); `outcome`, `role`, `code`, `reason`, `error`, `kind`, `status`, `result` (codes; so `error` is always a kind); `message_id` (the ingest line only); `user`, `by`, `owner` (user numbers); the counts `users`, `count`, `deleted`, `redacted`, `rows`, `requests`, `requests_closed`, `integrations`, `aliases`, `entries`, `conflicts`, `streets`, `localities`, `addresses`, `seconds`, `heap_mib`, `uploaded_bytes`, `stored_bytes`, `buffered`, `dropped`, `days`, `agencies`; the flags `ok`, `dry_run`, `update`, `trusted`, `traces`, `logs`, `metrics`; and `op` and `version`, which are set from constants. Everything else on a log line (integration, alias, template, request and invitation IDs, URLs, an Authentik error's description) stays on stdout and isn't exported.
- **Metric data point attributes** (the client's): `reason` only.
- **Resource attributes**: `service.name`, `service.namespace`, `service.version`, `service.instance.id`, `deployment.environment.name`, `deployment.environment`, `host.name`, `host.arch` and `telemetry.sdk.*`. `OTEL_RESOURCE_ATTRIBUTES` works for these. (The SDK merges that variable into every provider's resource again, which would bring back other keys, so it's read once and hidden while the providers are made.)

The code doesn't lean on the list: a test (`TestM12SourceSpans`) reads the spans as the code makes them, before the allowlist, and fails on any key not on the list, any private value, or any unregistered name. So a careless attribute fails a test even though it would never have left.

**Alloy repeats the attribute lists** (`deploy/monitoring/alloy/config.alloy`) and drops every span event, as a second line of defence. The docs say it's the backup, not the rule.

### 4. Requests, the database and users (R70, R72)

- **Request spans**: one middleware around the router starts a server span for every request, always as a **new trace**, ignoring any `traceparent` (browsers and PowerShell, R70). After the router has run it names the span by `r.Pattern` with the method (`GET /api/v1/messages/{id}`; `GET /` for the web app; `GET unmatched` when nothing matched) and records method, route and status. 5xx sets an error status. Methods outside the standard ones are `_OTHER`.
- **Page posts continue the client's trace**: for `POST /api/v1/pages` the middleware starts nothing; the handler checks the client's key first, then starts the span as a child of the request's `traceparent` only if the key was valid (R14, R70), backdated to when the request arrived. An unauthenticated post gets a new trace. The key check's own database call isn't in the trace (there's no span yet when it runs).
- **Users**: `signedIn` puts `user.id` (the account's number) and `user.role` on the request's span, for browser sessions and PowerShell tokens alike. Nothing else about the person.
- **The database**: a `pgx` query tracer makes a client span per query, named by its sqlc name (`InsertMessage`), with `db.system.name` and `db.operation.name` only: never the SQL, its arguments or the error's text (its SQLSTATE is the `error.type`). It only makes spans inside traced work, so idle polling (the re-parse and delivery workers' timers) makes none. With telemetry off the tracer isn't installed at all.
- **Requests and load on `/metrics`** (R72), names new, R52's untouched:

  | Metric | Type | Labels |
  |---|---|---|
  | `pagerarchive_http_requests_total` | counter | `route`, `status` |
  | `pagerarchive_http_request_duration_seconds` | histogram (5 ms to 10 s) | `route`, `status` |
  | `pagerarchive_db_pool_connections` | gauge | `state`: `in_use`, `idle`, `constructing` |
  | `pagerarchive_db_pool_max_connections`, `..._acquires_total`, `..._waited_acquires_total`, `..._acquire_seconds_total` | gauge, counters | none |
  | `pagerarchive_live_connections` | gauge | none |
  | `go_goroutines`, `go_memstats_heap_alloc_bytes`, `go_memstats_heap_inuse_bytes`, `go_memstats_stack_inuse_bytes`, `go_memstats_sys_bytes`, `go_gc_cycles_total`, `go_gc_pause_seconds_total` | | none |
  | `process_cpu_seconds_total`, `process_resident_memory_bytes`, `process_start_time_seconds` | | none |

  Label values are the router's patterns and the status codes the handlers write, so they're bounded; no user ID is a label (by user ID is in traces, §8). The Go and process names are the conventional ones, so existing Grafana knowledge applies.
- **Exemplars**: when the scraper asks for OpenMetrics (Alloy does), `/metrics` answers in OpenMetrics, the same samples (counters' families lose `_total`, their samples keep it, so the series are unchanged), and each histogram bucket carries the trace ID of the latest sampled request that fell in it. A plain scrape gets the same text format as before (A32 unchanged).

### 5. A page's trace (R71)

One trace per page, from the receiver:

```
page (client)                                  ── decoded line to the server's answer
├─ decode                                      ── the multimon-ng line to a record
├─ buffer                                      ── waiting until the post that worked began (R15)
├─ send (attempt 1, failed …)                  ── one per attempt, with its status or "unreachable"
└─ send (attempt n)  ──traceparent──►
   POST /api/v1/pages (API)
   └─ ingest  outcome=stored|duplicate|ignored|refused, message.id, client.id
      ├─ ingest.redact                          ── control tokens, redaction, priority (R17, R18)
      ├─ ingest.parse  └─ AliasesMatching       ── alias, template, agency profile (R3)
      ├─ ingest.store                           ── the transaction
      │  ├─ ingest.dedup ├─ LockDedupKey └─ FindDuplicate (R19)
      │  ├─ InsertMessage
      │  ├─ ingest.locate └─ AddressPoint …     (R24-R26)
      │  ├─ InsertReception
      │  └─ MatchingIntegrations, InsertDelivery (queued with the page, ADR 0006 §3)
      ├─ live.publish  live.connections=n       ── the hub's fan-out (R32)
      └─ integration.deliver (attempt 1: retry) … integration.deliver (attempt 2: delivered)
```

- **Buffering** is the `buffer` span, from when the page was put in the buffer until the start of the post that worked, so the time the server was unreachable shows as buffering (A62: 31.0 s for 30 s down); the failed attempts are `send` spans beside it. A page dropped from a full buffer ends its trace with outcome `dropped`.
- **Refused** pages (invalid, wrong client, clock ahead) still get an ingest span with outcome `refused` and the reason as `error.type`.
- **Live update**: one `live.publish` span with the number of connections it reached. Not a span per viewer: that would tie users to the messages they were shown (R70).
- **Integration deliveries are child spans of the ingest span, not links.** R71 asks for one trace showing "each integration delivery with its retries"; a link would put each delivery in a trace of its own, and Tempo's trace view wouldn't show them with the page. OpenTelemetry allows children that start after their parent ends, which is what an asynchronous delivery is. The ingest span's context is kept in memory by message ID (at most 10,000, each for the 5-minute cut-off plus a minute: R51a), and each attempt, including retries minutes later, starts under it with its attempt number, destination kind and result (`delivered`, `retry`, `failed`, `too_old`, `cancelled`). Nothing is stored in the database. After an API restart a pending delivery's attempt starts a trace of its own. Delivery spans don't carry the integration's ID, owner or address.

### 6. Long-lived connections (R72)

A live-update stream (`GET /api/v1/stream`, the browser's list and R63b's watch) can stay open for hours. **Its span covers only setting it up**: signing in, subscribing, the catch-up (with the number of messages it sent) and the `ready` event; then the stream ends its own request span with status 200 and is counted in the request metrics with that set-up time. What the connection does afterwards isn't traced (no span per message sent to a viewer, for the same privacy reason as §5). How many are open is `pagerarchive_live_connections` on `/metrics`. Why not a span for the whole connection: an hours-long span is invisible in Tempo until it ends, distorts every duration chart, and says nothing a gauge doesn't.

### 7. Logs (R73)

- `slog` records go to stdout exactly as before and, when logs are on, also to OTLP through the official `otelslog` bridge, behind the allowlist (§3).
- **Trace and span IDs**: the bridge takes them from the call's context, so the log calls inside traced work now pass it (`InfoContext(ctx, ...)` in the server, ingest, integrations, nightly and re-parse code). The three error helpers in the server now take the request, and their line's message is the constant `request failed` with the operation as `op` (it was the operation itself, which the constant-message rule doesn't allow).
- **The console's page text never reaches OTLP**: `PRINT_PAGES` writes with `fmt.Fprintf` to stdout, never through `slog`, and A63 runs the real client with `PRINT_PAGES=true` and finds none of the text it printed in the collector.

### 8. Client telemetry (R74)

The client exports, under `service.name` `pagerarchive-client` and `service.instance.id` = its client ID:

| Metric (OTel name) | In Prometheus | Type |
|---|---|---|
| `pagerarchive.client.lines_read` | `pagerarchive_client_lines_read_total` | counter |
| `pagerarchive.client.pages_decoded` | `..._pages_decoded_total` | counter |
| `pagerarchive.client.pages_sent` | `..._pages_sent_total` | counter |
| `pagerarchive.client.send_failures` | `..._send_failures_total` | counter (failed attempts) |
| `pagerarchive.client.send_retries` | `..._send_retries_total` | counter (second and later attempts) |
| `pagerarchive.client.pages_dropped` | `..._pages_dropped_total{reason}` | counter: `buffer_full` (R15), `rejected`, `clock_ahead` |
| `pagerarchive.client.pages_buffered` | `..._pages_buffered` | gauge |
| `pagerarchive.client.last_decoded_timestamp` | `..._last_decoded_timestamp_seconds` | gauge (Unix time) |
| `pagerarchive.client.last_sent_timestamp` | `..._last_sent_timestamp_seconds` | gauge (Unix time) |
| `pagerarchive.client.radio_running` | `..._radio_running` | gauge 1/0, only when the client runs the radio (R13) |

Metrics are sent every `OTEL_METRIC_EXPORT_INTERVAL` (the standard 60 s; the docs suggest 15 s). A client that's switched off can't report: the server's last-seen metric raises that alarm (R74).

### 9. The example monitoring stack (R76)

`deploy/monitoring/compose.yaml`, a compose project of its own that joins PagerArchive's network (`PAGERARCHIVE_NETWORK`, default `pagerarchive_default`):

| Service | Image (pinned by digest in the file) | Role |
|---|---|---|
| Alloy | `grafana/alloy:v1.21.3` | OTLP receiver on 4318, scrapes `/metrics` every 15 s with the metrics token (OpenMetrics, for exemplars), the second-line allowlist, then to the stores |
| Tempo | `grafana/tempo:2.10.10` | traces, 7 days; TraceQL metrics (`local-blocks`) for "by user ID" and "time in each step" |
| Loki | `grafana/loki:3.7.9` | logs over OTLP (trace and span IDs as structured metadata), 7 days |
| Prometheus | `prom/prometheus:v3.15.1` | metrics by remote write from Alloy, with exemplar storage, 30 days |
| Grafana | `grafana/grafana:13.2.5` | the only published port; admin password from `.env` (`.env.example` shipped), anonymous access and sign-up off |

- **Versions**: the newest stable release of each on 2026-10-30, except Tempo: 2.10.10 rather than 3.1.1. Tempo 3 changes its storage architecture; the 2.10 line is still maintained (2.10.10 was released the same day as 3.1.1), and the config here is the well-known single-binary one. Moving to 3 is a separate step (Question 3).
- **LAN only**: Tempo, Loki and Prometheus are on an `internal` network with no way out. Grafana also joins an ordinary network, because Docker can't publish a port from an internal one. Nothing calls home: Grafana's usage reports, update checks, news and plugin downloads, Loki's and Tempo's usage reports, and Alloy's reporting are off.
- **Clients on other machines** need Alloy's OTLP port. It isn't published by default (the brief: only Grafana's port); `compose.clients.yaml` publishes it on `ALLOY_OTLP_BIND` for those who want client telemetry from other machines (Question 1).
- **Provisioned from files**: the three data sources, linked both ways (a Loki line's `trace_id` opens Tempo; a span's logs open Loki; a Prometheus exemplar opens Tempo); the three dashboards (`grafana/dashboards/*.json`: a page's path, requests and load, clients; read-only in Grafana); and the two alert rules (`rules.yaml`).
- **Alert rules**: "Client not receiving" is `time() - max by (client) (pagerarchive_client_last_seen_timestamp_seconds)` over a threshold, one alert per client, no-data treated as OK (while the API is down, the other rule speaks); "API unreachable" is `max(up{job="pagerarchive"}) < 1` for 30 s, no-data treated as alerting. Both are evaluated every 30 s and resolve when the condition clears. **The threshold setting** is `params: [1800]` in `rules.yaml`, not an `.env` value: Grafana doesn't fill environment variables into an alert rule's query or threshold (tried: neither `${…}` in the threshold nor in the PromQL was replaced). Question 2.
- **Contact points**: none is provisioned for deployments, so the default notification policy stays editable in Grafana; the docs show adding Pushover or email there, with credentials kept in Grafana. The test overlay (`test/compose.yaml`, `test/alerting-test.yaml`) provisions a webhook contact point and a fast policy for A65 only.

### 10. Tests and how A62-A66 are run

- **The test collector** (`internal/telemetry/telemetrytest`) is an in-process OTLP/HTTP receiver that keeps every request (protobuf or JSON, gzip or not) and can dump all of it as JSON. `scripts/test-collector` serves the same on a port, for A15.
- **A62-A64, A66 and the new metrics** (`internal/acceptance/m12_test.go`) run in CI's test job against PostGIS, with the API in the test process and the **real client program** (built by the test) fed decoder lines on stdin, both exporting to the test collector. A TCP proxy in front of the API makes it unreachable for the client alone.
- **A63** searches everything the collector received (span names, attributes, links, resources, metric data points and labels, log bodies and attributes) for every fixture page's text (whole, without its marker, and the distinctive parts), every redacted value (both phone forms, `AGE: 52`, `SEX: M`), the search term as typed and URL-encoded, both sessions' cookies, the module's token, both client keys, the integration's secret URL and auth header, and `@example.com`. It also checks that every span name is registered and every server span is a route, that B's spans carry B's number and role, that no span but ingest carries a message ID, and, with `PRINT_PAGES=true`, that the client did print the text and none of it was exported.
- **A64** runs three times: telemetry off (for comparison), a collector that refuses connections, and one that accepts and never answers. 100 pages at ten a second (600 times the hobby rate; one integration delivers its pages one after another, ADR 0006 §3, so a burst of 100 at once queues deliveries regardless of telemetry), while a user and an admin browse and a stream is open, with a webhook integration.
- **A65** (`internal/monitortest`, `scripts/monitoring-checks.sh`) runs against the example stack itself, with the API and PostGIS from the top-level compose file (no Authentik: A65 doesn't need it) and a webhook receiver in the test on the host. "The existing test clock" is the one A32 and `e2e.sh` use: the client's `last_seen_at` moved in the database. After "Client not receiving" fires and resolves, the API is stopped for "API unreachable", and started again to see that resolve too. The script samples the stack's memory, idle and during A65, and can take the docs' screenshots.
- **CI**: the new tests are in the test job (timeout raised from 20 to 30 minutes); a new `monitoring` job runs A65 (30 minutes); the budget job runs twice, telemetry off and on (`a15.sh -t`); every publishing job waits for `monitoring` too.

### 11. Small items

- `web/src/routes/sheet/+page.svelte`: the comment naming "the M12 redesign" says M13 (the redesign moved, 4bdf572). The ADRs keep their wording.
- `stack-lib.sh` takes extra compose files (`STACK_OVERLAYS`); `a15.sh -t` uses it.
- The hub's `Publish` returns how many connections it reached (for `live.publish`).

## Evidence

EVIDENCE

## Consequences

- An operator who runs the example stack can follow one page from the receiver's decoder to each delivery attempt, see where time goes, see which routes and which user numbers made the load, and is told when a receiver goes quiet or the API is unreachable, without any page text, query, secret or address leaving PagerArchive.
- With telemetry off nothing changes but the request middleware (about 0.7 µs a request, which `/metrics` needs anyway) and bigger binaries.
- A new attribute, span name or log key has to be added to the allowlist on purpose, and to Alloy's copy; until then it doesn't leave. `TestM12SourceSpans` makes the omission visible.
- Telemetry is best-effort: a collector that's down for a while loses that while's telemetry.

## Questions for the owner

1. **Clients on other machines and Alloy's OTLP port.** The brief says only Grafana's port is published. R74's client telemetry needs a receiver elsewhere on the LAN to reach Alloy. I left the port unpublished and added `compose.clients.yaml` to publish it on a LAN address when wanted. Anyone who can reach it can send telemetry (Alloy's receiver has no authentication here). Keep it opt-in like this, or publish it by default on the LAN, or add a bearer token on Alloy's receiver (`OTEL_EXPORTER_OTLP_HEADERS` on the clients)?
2. **The alert threshold lives in `rules.yaml`**, not `.env`, because Grafana doesn't fill settings into alert rules (§9). Fine, or would you rather `.env` drove it (Grafana's container would then write `rules.yaml` from a template at start)?
3. **Tempo 2.10 rather than 3.1** (§9). Keep 2.10 for now?
4. **Resource attributes**: only standard keys are kept from `OTEL_RESOURCE_ATTRIBUTES` (§3). A deployment wanting its own (`site=north`) would see them dropped. Keep it strict?
5. **Integration IDs in telemetry**: delivery spans and exported log lines don't carry the integration's ID (R70 names message, alias and job IDs; I treated integrations the same, as things users own). Troubleshooting one failing integration then means the app's own delivery list. Keep them out?
