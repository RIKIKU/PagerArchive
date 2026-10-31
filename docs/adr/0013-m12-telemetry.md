# 13. Milestone M12: telemetry

Date: 2026-10-30

## Status

Proposed. The owner answered the questions on 2026-10-31 ("The owner's answers", at the end); question 6, the A33 flake, stays open as separate work after M12. The owner's addition of the same day (radio health, "Radio stalled", stopped clients on the dashboard, a failed delivery's cause; A67) is in the last section.

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
| Tempo | `grafana/tempo:3.1.1` | traces, 7 days, in one process with no Kafka; TraceQL metrics for "by user ID" and "time in each step" read the stored traces |
| Loki | `grafana/loki:3.7.9` | logs over OTLP (trace and span IDs as structured metadata), 7 days |
| Prometheus | `prom/prometheus:v3.15.1` | metrics by remote write from Alloy, with exemplar storage, 30 days |
| Grafana | `grafana/grafana:13.2.5` | the only published port; admin password from `.env` (`.env.example` shipped), anonymous access and sign-up off |

- **Versions**: the newest stable release of each on 2026-10-30. Tempo was first 2.10.10 and moved to 3.1.1 at the owner's request ("Tempo 3.1.1", below).
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

Local runs are on cld01 (4 vCPU, 5 GB, a spinning disk) against PostGIS 18-3.6; CI on GitHub's `ubuntu-24.04` runners.

**Off means off** (R69, `go test -v ./internal/telemetry/`):

```
TestOffUnlessConfigured: R69: telemetry off: no span recorded, 0 connections to 127.0.0.1:4318
  OTEL_SDK_DISABLED=true: traces off; said: ""
  OTEL_TRACES_EXPORTER=none: traces off; said: "... msg=\"telemetry on\" traces=false logs=true metrics=false"
  OTEL_EXPORTER_OTLP_PROTOCOL=grpc: traces off; said: "... level=ERROR msg=\"telemetry: PagerArchive exports OTLP over HTTP only (http/protobuf); this signal is off\" signal=traces ..."
BenchmarkRequestOff/bare-4              3106730    378.0 ns/op    224 B/op    5 allocs/op
BenchmarkRequestOff/telemetry-off-4      947106   1060 ns/op     1040 B/op   17 allocs/op
```

With telemetry off a request costs about 0.7 µs more, most of it the route and status counting that `/metrics` needs anyway (R72); no tracer is installed on the database.

**The allowlist** (same package): a span with `url.full`, `page.text`, an email as `user.id` and an error quoting page text, an exception event, a span named by its path, a log line with page text and an email, and `owner.email` in `OTEL_RESOURCE_ATTRIBUTES` → exported: `["GET /api/v1/messages/{id}" "unnamed"]`, status code kept, `error.type` `other`, no `user.id`, the log record with `client`, `user` 7 and `error` `other`, `deployment.environment.name` kept, and none of the strings. `TestRequestSpans`: spans `GET /api/v1/messages/{id}`, `POST /api/v1/pages` (a browser's), `POST /api/v1/pages` (a client's), `GET unmatched`; only the client's continued the sent trace. `TestLogMessagesAreConstant`: 146 log calls, every message a constant.

**Dropping** (A64, R69): `TestDroppedOnceAMinute`: collector down, 2,000 spans in 610 ms, 1 line. `TestDropLogOnceAMinute` (a clock moved by hand): 60 failed exports over 5 minutes, 5 lines, the second saying `dropped=120`.

**A62** (`TestA62PageTrace`, 75 s):

```
A62: message 1's trace has 36 spans: client page, decode, buffer, send; API POST /api/v1/pages, ingest,
     ingest.redact, ingest.parse, ingest.store, ingest.dedup, ingest.locate, InsertMessage, live.publish,
     integration.deliver and the other queries
A62: client decode 25µs, buffer 204µs, send 50.3ms
A62: API POST /api/v1/pages 49.5ms, ingest 48.7ms, ingest.redact 29µs, ingest.parse 1.04ms, ingest.dedup 1.24ms,
     ingest.locate 1.67ms, ingest.store 32.2ms, InsertMessage 13.5ms, live.publish 3µs,
     integration.deliver 32.7ms retry, 19.9ms delivered
A62: client B's copy: trace 6865ad15…, outcome duplicate, message 1
A62: the held page buffered for 31.004s, over 6 send attempts
```

The API's request span's parent is the client's `send`; the delivery attempts are `1:retry` and `2:delivered` in the page's trace.

**A63** (`TestA63NothingPrivate`; the client is the real program with `PRINT_PAGES=true`):

```
A63: checked 256 spans, 17 log records and 30 metric points (134495 bytes) for 40 private strings: none found
A63: HTTP spans: [GET /api/ GET /api/v1/admin/templates GET /api/v1/messages GET /api/v1/messages/{id}
     GET /api/v1/stream POST /api/v1/integrations POST /api/v1/pages PUT /api/v1/admin/integration-hosts]
A63: 6 spans carry B's user ID 2, all with role user; the client's console printed 8 lines of page text, none exported
```

The 40 strings: the 8 pages' texts with and without their markers (16), 9 distinctive parts of them, the search term as typed, as a query and in a path, 4 redacted values, B's and the admin's cookies, the module's token, both client keys, the integration's secret URL path and auth header value, and `@example.com`.

**At the source** (`TestM12SourceSpans`, before the allowlist): `224 spans and 406 attributes as the code made them: allowed names and keys only, nothing private, no browser trace continued; 6 carry B's user ID`.

**A64** (`TestA64CollectorDown`, 100 pages at 10 a second, two users browsing, a stream open, a webhook):

```
A64 (off collector):     100 pages stored and delivered 10.028s after posting, slowest delivery 125ms after receipt; ... said 0 and 0 times
A64 (stopped collector): 100 pages stored and delivered 10.036s after posting, slowest delivery 142ms after receipt; in 11s the API said 1 time(s) and the client 1 time(s)
A64 (hung collector):    100 pages stored and delivered 10.036s after posting, slowest delivery 140ms after receipt; in 16s the API said 1 time(s) and the client 1 time(s)
```

**A66** (`TestA66ClientTelemetry`): `lines read 8, decoded 7, sent 5, buffered 2, failed sends 2 then 3, retries 2, last decoded 03:29:49, last sent 03:29:49` (one line wasn't a page; the last send was before the API stopped), then 7 sent once it was back.

**`/metrics`** (`TestM12Metrics`): R52's names unchanged; requests by route and status, the pool, live connections, Go and process metrics present; no query, ID or user; OpenMetrics ends `# EOF` and has exemplars, e.g. `pagerarchive_http_request_duration_seconds_bucket{le="0.005",route="GET /api/v1/messages",status="200"} 1 # {trace_id="7a6b5172…"} 0.001710629 1793331018.307`.

**A65** (`scripts/monitoring-checks.sh`):

| | cld01, run 2 | CI, run 41002463837 |
|---|---|---|
| "Client not receiving" fired for the moved client only | 18 s after the move | 45 s |
| it resolved after the client posted | 23 s | 24 s |
| "API unreachable" fired after the API stopped | 59 s | 60 s |
| it resolved after the API started | 30 s | 30 s |

**The stack's memory** (sum of each container's peak): cld01 run 1 idle 618 MiB, during A65 638 MiB, while viewing dashboards 1,334 MiB; run 2 idle 752, A65 832, dashboards 960; CI idle 398, A65 458. Per container in `docs/monitoring.md`.

**A15 with telemetry on** (`scripts/a15.sh -m 5 -w 1 -i 2 -r 30000 -t`, the CI short run, cld01): 105 pages posted, 20 viewers, 2,080 live events; the test collector received 2,190 spans and 111 log records. API peak 67.3 MiB under load after warm-up (limit 256), api + db 476.0 MiB (limit 1,024), idle CPU api + db 1.25 % of one core (limit 5). (That run's own summary line failed on a missing space in `a15.sh`, fixed in ebc30a1; the figures are its samples through the same calculation.) In CI (run 41003658801, ebc30a1), the same short run on both sides:

```
telemetry on:  test collector received 2190 spans, 111 log records and 0 metric points (975363 bytes as JSON)
               API peak: 92.2 MiB (limit 256) PASS   api + db peaks: 388.1 MiB (limit 1024) PASS   idle CPU api + db: 1.56 % PASS
telemetry off: API peak: 101.3 MiB (limit 256) PASS  api + db peaks: 393.2 MiB (limit 1024) PASS   idle CPU api + db: 0.51 % PASS
```

**Mutation proof** (`scripts/mutation-checks.sh` on b66d984, cld01, 2026-10-30: `mutation exit 0`, 53 mutations, every one failing its test; the nine M12 ones below, each with the first lines its test printed):

| Broken | Test | It fails with |
|---|---|---|
| the real path and query as a request span's name (`http.go`) | A63, `TestM12SourceSpans` | `span named "unnamed"`, `HTTP span named "unnamed", not a route` |
| SQL arguments on database spans (`store/trace.go`) | `TestM12SourceSpans` | `span "SessionUser" has attribute db.query.parameters, not on the allowlist` |
| an email address on a signed-in request's span (`auth.go`) | `TestM12SourceSpans` | `span "GET /api/v1/messages" carries an email address in user.role` |
| the allowlist removed (`filter.go`) and a `page.text` attribute added (`server.go`) | A63 | `exported telemetry contains page text … {"key":"page.text","value":{"string_value":"@@EX0000001 HSF1 SMOKE FROM ROOF …` |
| a browser request's `traceparent` continued (`http.go`) | `TestM12SourceSpans` | `span "GET /api/v1/messages" continues the trace a browser sent` |
| the span exporter synchronous (`WithSyncer`), with a hung collector | A64 | `0 of 100 pages delivered`, `0 of 100 pages stored`, `the client didn't exit` |
| `PRINT_PAGES` output sent to OTLP (`client.go` logs the text) | A63 | `exported telemetry contains page text … "body":{"string_value":"QDTraining Tuesday 7pm at the station"}` |
| the client not sending `traceparent` (`client.go`) | A62 | `the trace has no pagerarchive-client span "page"` (and `decode`, `buffer`) |
| the "client not receiving" rule's query broken (`rules.yaml`, a wrong metric name) | A65 on the stack | `no "Client not receiving" alert for mon-a-155816 within 2 minutes; got 0 notifications` |

Unmutated, the same run: `ok internal/acceptance 229.191s`, `--- PASS: TestA65ToldWhenAClientStops (190.36s)`, the print sheet's browser tests `6 passed`, Pester `Passed 372, failed 0`.

**The other suites on cld01** (snapshot of b66d984, 2026-10-30):

- `scripts/e2e.sh` (three browsers): `141 passed (15.9m)`, `API log lines checked: 695, page texts found: 0`, exit 0.
- `scripts/stack-checks.sh`: the module's checks passed with both vaults (`ok … 425.174s`, `ok … 419.147s`) and `API log lines checked: 565, page texts or email addresses found: 0`; the accounts part failed one check once: `TestA33WithinOneFlow`, `right reset code not refused in the same flow after 5 wrong ones` (the five wrong codes were each refused as invalid; the sixth, right one wasn't refused). It's the first failure of that check in any stack run kept on cld01 (M5 to M11, eight runs) and in CI on this branch (three runs). Rerun at once on the same snapshot (`PA_STACK_PART=accounts`): every check passed, A33's sixth code `ak-stage-access-denied Too many wrong attempts for this account`, exit 0. M12 changes nothing Authentik reads or runs (the lock counts Authentik's own failure records; the API only gained two empty `OTEL_*` variables), so I take it as a timing flake in Authentik's recording of failed codes on a loaded machine. Question 6.

**CI**: run 41003775728 (b66d984), every job passed, publishing after all of them (nothing published from a branch). The last commit's run is in the report.

## Tempo 3.1.1 (owner, 2026-10-31)

The first version of this branch used Tempo 2.10.10, on the grounds that Tempo 3 "changes its storage design". That was from memory, not checked. The owner asked for the check, not wanting to hold back a major release this early in the project.

**What changed in 3** (the 3.0 release notes and "Migrate to 3.0"): the ingester is gone, and writes go through a live store and block builder ("Project Rhythm"). In microservices mode those are joined by Kafka; **in monolithic mode, which is how this stack runs Tempo, everything runs in one process without Kafka**. For our config that means:

- the `ingester` and `compactor` sections are gone: 3.1.1 refuses the 2.10 file (`field ingester not found in type app.Config`). Retention moves to the backend scheduler, which plans it, and the backend worker, which carries it out. Each has its own `block_retention` (the effective config showed the scheduler's still at its default, 336 h, with only the worker's set), so `tempo.yaml` sets both to 168 h;
- the metrics-generator's `local_blocks` processor is gone: TraceQL metrics (the dashboards' "requests by user ID", "time in each step" and "slowest queries") are answered from the stored traces themselves;
- `tempo-cli migrate config --mode=monolithic` (3.1.1) stopped on our file (`field traces_storage not found in type generator.Config`), so the new `tempo.yaml` is written by hand, from the effective config 3.1.1 printed (`/status/config`);
- new defaults that show: searches leave out the newest 30 s (`query_end_cutoff`), and the idle backend worker logs `error calling scheduler … no jobs found` about once a minute. Both are in `docs/monitoring.md`'s troubleshooting;
- stored traces: nobody runs this stack yet, so there's no 2.10 data to carry over. (3.0 reads 2.x blocks, per the release notes; traces are kept a week anyway.)

**Checked** (cld01, `scripts/monitoring-checks.sh -s`, Tempo 3.1.1): A65 passed (`"Client not receiving" fired … 31s`, `resolved 24s`, `"API unreachable" fired 58s`, `resolved 29s`), and the screenshots, which open a page's trace and the three dashboards with their TraceQL metrics panels, were taken from it. Memory: the monitoring stack alone, idle for a minute, nothing else changed: Tempo 3.1.1 34 MiB against 2.10.10's 127 MiB (Alloy 58 and 48 MiB). In the full run Tempo peaked at 58 MiB idle, 94 MiB during A65 and 224 MiB while dashboards were open, as with 2.10.

## Trial on cld01 (2026-10-31)

The live copy on cld01 was upgraded to 117ac0a with telemetry on for its API and replay client. The example stack was started beside it by following `docs/monitoring.md` as written, which tested the docs. What the docs got wrong, now fixed:

- **`GRAFANA_ROOT_URL`** wasn't mentioned. With Grafana on a LAN address and the root URL left at `http://localhost:3000`, the alert that reached the webhook linked to `http://localhost:3000/alerting/grafana/pa-client-not-receiving/view`. Step 2 now says to set it with `GRAFANA_BIND`, and `.env.example` says why.
- **Where alerts go out of the box.** The docs said a default contact point, `grafana-default-email`, existed. Grafana 13.2.5 starts with none: its default policy sends to a receiver named `empty` that has no integration (`/api/alertmanager/grafana/config/api/v1/alerts`: `{"route":"empty","receivers":[{"name":"empty","types":[]}]}`). The docs now say alerts go nowhere until a contact point is added and made the default policy's, and they cover webhooks alongside Pushover and email.
- Smaller: the API's "telemetry on" line is JSON, not `key=value`; and `.env` holds the admin password and metrics token, so the docs now say `chmod 600 .env`.

Everything else worked as written:
- Five services running; only Grafana published. Grafana answered 151 s after `docker compose up -d`.
- The three dashboards in the PagerArchive folder, and `up{job="pagerarchive"} = 1`.
- After the API and client were pointed at `http://alloy:4318`, both logged `telemetry on`. A live page's trace in Tempo had 36 spans, from the client's decode, buffer and send, through the API's steps and queries, to two `integration.deliver` spans. Loki had the API's log lines (22 of 24 with a trace ID); Prometheus had the replay client's own metrics.

**Memory after an hour in use**, nobody viewing Grafana: 845 MiB for all five (`docs/monitoring.md`, "Memory and disk"). Alloy showed 278 MiB, like the 370 MiB seen once before, and the cause was found. `/bin/alloy` is a 543 MiB binary, and the kernel's cache of its pages is counted against the first container that reads them (cgroup `active_file` 227 MiB). Alloy's own memory (`anon`) was 48 MiB. The swing between runs is that cache, not a leak.

## Consequences

- An operator who runs the example stack can follow one page from the receiver's decoder to each delivery attempt, see where time goes, see which routes and which user numbers made the load, and is told when a receiver goes quiet or the API is unreachable, without any page text, query, secret or address leaving PagerArchive.
- With telemetry off nothing changes but the request middleware (about 0.7 µs a request, which `/metrics` needs anyway) and bigger binaries.
- A new attribute, span name or log key has to be added to the allowlist on purpose, and to Alloy's copy; until then it doesn't leave. `TestM12SourceSpans` makes the omission visible, and `TestAlloyAllowlistMatches` fails until Alloy's copy matches.
- Telemetry is best-effort: a collector that's down for a while loses that while's telemetry.

## Questions for the owner

1. **Clients on other machines and Alloy's OTLP port.** The brief says only Grafana's port is published. R74's client telemetry needs a receiver elsewhere on the LAN to reach Alloy. I left the port unpublished and added `compose.clients.yaml` to publish it on a LAN address when wanted. Anyone who can reach it can send telemetry (Alloy's receiver has no authentication here). Keep it opt-in like this, or publish it by default on the LAN, or add a bearer token on Alloy's receiver (`OTEL_EXPORTER_OTLP_HEADERS` on the clients)?
2. **The alert threshold lives in `rules.yaml`**, not `.env`, because Grafana doesn't fill settings into alert rules (§9). Fine, or would you rather `.env` drove it (Grafana's container would then write `rules.yaml` from a template at start)?
3. ~~Tempo 2.10 rather than 3.1?~~ Answered (owner, 2026-10-31): don't hold back a major release this early. Moved to 3.1.1 ("Tempo 3.1.1", below).
4. **Resource attributes**: only standard keys are kept from `OTEL_RESOURCE_ATTRIBUTES` (§3). A deployment wanting its own (`site=north`) would see them dropped. Keep it strict?
5. **Integration IDs in telemetry**: delivery spans and exported log lines don't carry the integration's ID (R70 names message, alias and job IDs; I treated integrations the same, as things users own). Troubleshooting one failing integration then means the app's own delivery list. Keep them out?
6. **A33 failed once on cld01** (Evidence) and passed on the immediate rerun and in every CI run. Shall I look into making that check (or the lock it tests) robust to slow recording of failed codes, as a separate piece of work?

## The owner's answers (2026-10-31)

1. **Alloy's OTLP port stays opt-in** (`compose.clients.yaml`), bound to the server's LAN address (`ALLOY_OTLP_BIND`). No token for now: the "Client not receiving" alert reads the API's `/metrics`, not anything sent to Alloy, so nothing sent to Alloy can hide a stopped client.
2. **The alert threshold stays in `rules.yaml`.**
3. **Tempo 3.1.1**, done in 8fd6d60 ("Tempo 3.1.1", above).
4. **`OTEL_RESOURCE_ATTRIBUTES` stays strict**: only the standard keys in `ResourceKeys` are kept.
5. **Integration IDs stay out of telemetry.** If finding a broken webhook turns out to be awkward, the owner will reopen it.
6. **Open.** The A33 flake is its own piece of work after M12. Nothing about it changes in M12.

## Follow-up from the review (2026-10-31)

- **A page's trace in the screenshots.** `trace.png` from 8fd6d60 showed Explore with the trace ID typed in and the query still loading, so it didn't show that a page's trace opens. `scripts/monitoring-screenshots.cjs` now checks, before it takes that screenshot, that the trace (from Tempo's API) holds the client's steps, the API's steps and at least one query, and then waits up to 90 s until the trace view has drawn every one of the trace's span names. If the view doesn't draw them, the script fails and takes no screenshot.
- **Summaries that agree with the result.** A63's summary said "none found", and `TestAllowlist`'s "nothing outside the allowlist", even when the test had just found a leak. Each now says what it found when the test fails: A63 gives `FOUND n:` and what was found, and `TestAllowlist` gives `OUTSIDE THE ALLOWLIST:` and the strings. `TestM12SourceSpans`, `TestRequestSpans` and `TestLogMessagesAreConstant` changed the same way.
- **Alloy's copy of the allowlist is checked.** `TestAlloyAllowlistMatches` reads `deploy/monitoring/alloy/config.alloy` and fails when a span, log, resource (all three statements) or metric key list differs from `SpanKeys`, `LogKeys`, `ResourceKeys` or `MetricKeys`. `mutation-checks.sh` adds a key to `SpanKeys` only and shows the test failing.
- **Dashboards, from looking at every screenshot.**
  - "Ingest log lines" showed every API line, including the test stack's `metrics: locked accounts unavailable` warnings. It now shows only lines that carry a trace ID, and is titled "Log lines from pages and requests".
  - "Recent pages" said to click a trace ID. Its table links span IDs, so it now says to click a span ID to open that page's trace.
  - "Radio running" was blank for a client that reads a decoder's output, because only a client that runs the radio itself reports it. Such a client now shows "not run by the client".

**Evidence** (cld01):

- `scripts/monitoring-checks.sh -s` from a fresh snapshot: exit 0. A65 passed (`fired … 39s`, `resolved 23s`, `"API unreachable" fired 59s`, `resolved 28s`). The screenshot script printed `trace c4a33db2b37dd7c7aaac37023ad82bdc: 24 spans: page, buffer, send, decode, POST /api/v1/pages, ingest, TouchClient, ingest.redact, ingest.parse, AliasesMatching, ingest.store, SQL BEGIN, ingest.dedup, LockDedupKey, FindDuplicate, InsertMessage, ingest.locate, StreetPoint, SetLocation, InsertReception, SearchMessages, MatchingIntegrations, SQL COMMIT, live.publish` and `the trace view drew all 24 span names 3.3 s after opening`. Every image in `docs/monitoring/` is from that run, and each was looked at.
- The same script against a running stack with `TRACE_WAIT_MS=1000`: `Error: the trace view didn't draw page, decode, buffer, send, … within 1 s; no screenshot taken`, exit status 1, and the `trace.png` already in the folder was removed.
- `TestAlloyAllowlistMatches`, unmutated: `config.alloy keeps the same keys as filter.go (SpanKeys 20, LogKeys 44, ResourceKeys 11, MetricKeys 1)`. Through `mutation-checks.sh`'s own `mutate`: `alloy_test.go:56: config.alloy's span list 1 lacks SpanKeys keys ["pagerarchive.reparse.batches"]` … `-> the test fails: the check is guarded`.
- With the allowlist taken out of `filter.go` and page text added to the ingest span, A63's summary: `checked 256 spans, 17 log records and 12 metric points (118731 bytes) for 40 private strings: FOUND 29: page text @@EX0000001 …`, then `the client's console printed 8 lines of page text, some exported (above)`. Unmutated: `… for 40 private strings: none found` and `none exported`. `TestAllowlist` with the allowlist removed: `OUTSIDE THE ALLOWLIST: ["SHED" "someone@example.com" "url.full" "page.text"] and the errors above`.

## Radio health, a stalled radio, stopped clients and why a delivery failed (owner, 2026-10-31)

From the owner's hands-on with the cld01 trial. Main 852dfa7 changed the spec for the first two (R74a; R76's dashboards and alert rules; A67, in M12); the third is a fault under R71 as written.

**1. A stopped client stays on the clients dashboard (R76).** "Each client's own view" used instant queries, which only see values from Prometheus' last 5 minutes, so a client dropped out of the table 5 minutes after it stopped: just when someone wants to look at it. Each column is now `last_over_time(…[$__range:1m])` over the dashboard's time range, so a client keeps its last values, and a new **Last report** column (`time() - last_over_time(timestamp(pagerarchive_client_pages_buffered)[$__range:1m])`: every client reports its buffer) says how long ago they came, orange after 5 minutes. The other client panels are time series, which keep a stopped client's history up to where it stopped, and "Clients, as the server sees them" comes from the API, which keeps every client's last-seen time; neither had the fault.

**2. Radio health (R74a).**
- **The audio passes through the client** (`internal/radio`). `rtl_fm`'s output used to be piped straight into `multimon-ng`; now the client reads it and writes it on. Each chunk is written to `multimon-ng` first and measured after: its bytes counted and its samples' sum of squares added, into atomics. Nothing on that path takes a lock, allocates or waits; the metrics side reads the atomics when the SDK collects (observable instruments), so a slow or hung exporter can't reach the audio. `TestRadioMeasuringNeverBlocks` passes a minute of audio through while the metrics exporter hangs, and checks it arrives within 5 s and byte for byte the same; `TestPassUnchanged` checks 5 MiB, cut mid-sample, arrive unchanged.
- **The numbers**: `pagerarchive.client.radio.audio` (bytes; its rate is audio per second, 44,100 at 22,050 samples a second), `.level` (RMS in dBFS since the last report, -120 floor, only when audio came), `.silent` (seconds since audio last came, or since the client started) and `.messages` with a `kind` attribute. With telemetry off, `rtl_fm` and `multimon-ng` are still connected through the client, and nothing is reported.
- **No squelch.** `rtl_fm` runs without `-l`, as before; `RTLFMArgs` says why in its comment, `docs/client.md` and `docs/monitoring.md` say so, and `TestNoSquelch` and A67 (the stand-in records its arguments) check it.
- **`rtl_fm`'s messages** go to the client's stderr as before, line by line as `rtl_fm` wrote them, and each is counted by kind. The kinds come from the source of rtl-sdr **2.0.2** as the client image installs it (Debian trixie `rtl-sdr`/`librtlsdr0` **2.0.2-2+b1**; source package 2.0.2-2, which carries no patches): `rtl_fm.c`, `convenience/convenience.c`, `librtlsdr.c` and the tuner drivers. Each message is matched whole (`internal/radio/health.go`):

  | Kind | rtl-sdr 2.0.2's messages | Printed by |
  |---|---|---|
  | `no_device` | `No supported devices found.`; `No matching devices found.` | `convenience.c` `verbose_device_search` |
  | `open_failed` | `Failed to open rtlsdr device #%d.` | `rtl_fm.c` main |
  | | `usb_open error %d`; `usb_claim_interface error %d`; `Kernel driver is active, or device is claimed by second instance of librtlsdr.`; `Detaching kernel driver failed!` (printed without a newline, so the next message follows on the same line) | `librtlsdr.c` `rtlsdr_open` |
  | `usb_transfer_failed` | `cb transfer status: %d, canceling...` | `librtlsdr.c` `_libusb_callback` |
  | | `Failed to submit transfer %i` | `librtlsdr.c` `rtlsdr_read_async` |
  | | `rtlsdr_read_reg failed with %d`; `rtlsdr_write_reg failed with %d`; `rtlsdr_demod_read_reg failed with %d`; `rtlsdr_demod_write_reg failed with %d` | `librtlsdr.c` (USB control transfers) |
  | | `r82xx_write: i2c wr failed=%d reg=%02x len=%d`; `r82xx_read: i2c wr failed=…` and `i2c rd failed=…` | `tuner_r82xx.c` (I2C over USB to the tuner) |
  | `tuner_not_locked` | `[R82XX] PLL not locked!`; `[R82XX] No valid PLL values for %u Hz!` | `tuner_r82xx.c` |
  | | `[E4K] PLL not locked for %u Hz!` | `tuner_e4k.c` |
  | | `[FC0012] no valid PLL combination found for %u Hz!`; the same for `[FC0013]` | `tuner_fc0012.c`, `tuner_fc0013.c` |
  | not counted | `Please fix the device permissions, e.g. by installing the udev rules file rtl-sdr.rules`; the kernel-driver message's two further lines; `Please increase your allowed usbfs buffer size with the following command:` and its `echo 0 > /sys/module/usbcore/parameters/usbfs_memory_mb`; blank lines | lines that only explain the message before them |
  | `other` | everything else, including the normal start-up lines (`Found 1 device(s):`, `Using device 0: …`, `Found Rafael Micro R820T tuner`, `Tuned to … Hz.`, `Output at 22050 Hz.` …: about 13 each start) and `WARNING: Failed to set …` | |

  As R74a says, anything else is "other", so "other" goes up by about 13 every time `rtl_fm` starts; the dashboard and docs say so. One thing the source shows: after `cb transfer status: …, canceling...`, `rtl_fm` 2.0.2 doesn't exit. Its main loop only ends on a signal (its `Library error %d, exiting...` can't be reached), so a dongle that drops off the bus leaves `rtl_fm` running with no audio: exactly the case "Radio stalled" is for.
- **Only the kind leaves.** The kind is the metric's only attribute; message text never goes into a metric, log record or span (R70). `kind` is added to `MetricKeys` and to Alloy's datapoint list (`TestAlloyAllowlistMatches`: `MetricKeys 2`). Every kind is reported from the start at 0, so a first error shows as an increase. `TestRadioExportsKindsOnly` and A67 check that no message text reaches the exporter or the stores.
- **A client reading stdin** registers none of these (`TestStdinClientHasNoRadioHealth`), and the dashboard shows "not run by the client" for them.
- **"Radio stalled"** (`rules.yaml`, uid `pa-radio-stalled`): `max by (client) (label_replace(pagerarchive_client_radio_silent_seconds, "client", "$1", "instance", "(.*)")) > 120`, the threshold a setting beside the other one. The client counts the silence itself, so the alert only follows clients that are still reporting: a switched-off client's last value (small) goes stale after 5 minutes and the rule has no data for it (`noDataState: OK`), which leaves it to "Client not receiving". A pipeline that isn't running gives no audio either, so it shows as silence too. The `client` label matches the other client alert's.
- **The clients dashboard** shows radio health in "Each client's own view" (Radio running, Audio, Audio level, Radio silent for, red over 120 s) and three new charts: audio per second, level, and `rtl_fm` messages by kind per hour.

**3. A failed delivery says why (R71).** `attemptResult` put the outcome (`retryable`, `refused`, `no_answer`) in `error.type`, and replaced it with `res.status` when there was one; MQTT failures have no status, so the owner's broken broker showed five attempts that each said only `retryable`. Now each way an attempt can fail sets a `cause` where it's decided (`netError` returns it beside the owner's text; MQTT's CONNACK codes and timeouts; HTTP statuses; building the request; unreadable secrets; too old), and the span gets it as **`error.type`**, through `telemetry.ErrorKind` as elsewhere (anything that isn't a kind would become `other`). The outcome stays useful (it says whether the page will be tried again) and moves to its own attribute, **`pagerarchive.delivery.failure`** (`retryable`, `refused`, `no_answer`), added to `SpanKeys` and Alloy's span list.
- Causes: `connection_refused`, `dns_lookup_failed`, `connection_reset`, `network_unreachable`, `timeout`, `tls_not_trusted`, `tls_not_tls`, `tls_handshake_failed`, `address_not_allowed`, `connection_failed`, `auth_refused` (MQTT bad user name or password, or not authorised), `mqtt_protocol_refused`, `mqtt_unavailable`, `no_answer`, `bad_request`, `secrets_unreadable`, `too_old`, and `HTTP <status>`.
- `TestFailureCauses` makes every kind fail every way it can (webhook, Discord, Telegram, Pushover and ntfy over HTTP: 13 ways each; MQTT: 11) against real listeners, and checks the span. It found two faults besides the missing cause: HTTPS to a plain-HTTP server came back as "connection failed" (net/http reports it as `server gave HTTP response to HTTPS client`, not a `tls.RecordHeaderError`), and an MQTT broker that accepts the connection but never answers it came back as "MQTT: couldn't connect" (paho wraps an i/o timeout). Both now say what happened, to the owner and on the span. A62 now checks that the exported retry span says `HTTP 503, retryable`.
- The page's-path dashboard has **"Failed delivery attempts, by kind and cause"** beside the deliveries chart: `{name="integration.deliver" && status=error} | count_over_time() by (span.pagerarchive.delivery.kind, span.error.type)`.

**A67** runs in `scripts/monitoring-checks.sh` after A65 (CI's monitoring job, now allowed 60 minutes). Its clients are the **client image** on the API's network, with **`internal/radio/standin`** mounted as `/usr/local/bin/rtl_fm`. The stand-in prints rtl_fm 2.0.2's start-up lines, then writes 22,050 Hz signed 16-bit audio in real time: Gaussian hiss (as FM without squelch hears) with a made-up POCSAG page from the example network at 1200 baud every 10 s, from its own encoder (`TestCodeword` checks its BCH(31,21) and parity against the sync and idle codewords). Files in its directory pause the audio (`rtl_fm` keeps running), write the USB-transfer message, or write any line once. It records its arguments, the pages it wrote and the bytes written, and writes a marker string into the hiss as samples, so the test can look for audio in the stores. multimon-ng 1.3.1 in the client image decoded every page from the first try.
