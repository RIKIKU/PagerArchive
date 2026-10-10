# 2. Milestone M1 implementation decisions

Date: 2026-10-09

## Status

Accepted (2026-10-10, by the owner)

## Context

`SPEC.md` ("How to use", item 7) asks the implementer to record choices made where the spec is silent. This ADR collects those made in milestone M1 (client, ingest, redaction, deduplication, profiles and schemas, `validate-profile`, golden tests, CI). It builds on ADR 0001.

## Decisions

### 1. Client authentication (R14): owner decision, 2026-10-09

- Each client has its **own API key**, sent as `Authorization: Bearer <key>`. Keys are 32 random bytes (`pa_` + base64url). Only their SHA-256 is stored; a key is shown once, when it's issued.
- The key decides the client ID. A request whose `client_id` differs from the key's client is refused (403).
- Until the admin UI exists (M2), admins issue and revoke keys with `pagerarchive client add <id> [name]`, `client revoke <id>` and `client list`. A revoked key gets 401 at once.
- The client keeps retrying on 401 (as on 5xx, 408 and 429), so pages aren't lost while a key is being replaced. Other 4xx responses drop the page, so one bad page can't block the queue.

### 2. Profiles

- A JSON file is a **profile** when its top-level `kind` is `network` or `agency`. `validate-profile` and the server load every profile under a directory, so the layout is up to the deployment. Golden tables are the files named `golden-*.json`.
- Keys starting with `_` are comments, allowed in any object.
- An agency format without `priorities` applies to every priority, including none.
- A list field (`units`) must have a `split`. Extra fields are strings, or lists when they have a `split`. `split: ""` splits into single characters (e.g. service letters).
- Priority markers must be exactly 2 characters. This is checked in code rather than in the schema, so the error names the marker.
- **Address dataset format.** For M1 the dataset is a JSON file with `attribution` and `streets` (`street`, `locality`), the same shape as a golden street fixture (`schemas/address-dataset.schema.json`). Its `path` is relative to the network profile. M4 adds coordinates, and an importer for real datasets.
- Event-category codes ending in `*` match by prefix (`HSF*`).
- **Redaction `redact` group.** If a redaction pattern has a group named `redact`, only that group is replaced by `REDACTED`. RE2 has no lookaround, so this is how a rule keeps context, for example redacting a mid-text label up to, but not including, a trailing paging group. The must-not-match check applies the rule and fails if the example changes. (Needed by a private profile's patient-detail rules, owner decision 2026-10-09.)

### 3. Golden tables

- Agency rows list extra fields under `expect.extra`. Under ADR 0001 section 2.5, an extra field not listed there must be null.
- A row without `status` is confirmed. Golden tables are validated against `schemas/golden-table.schema.json`, so a misspelt key in `expect` is an error, not a silent pass.

### 4. Ingest

- **Control tokens** are the ASCII control names multimon-ng prints (`<NUL>` to `<US>`, `<DEL>`); `<LF>` and `<CR>` become a space. Other text in angle brackets is kept.
- **Deduplication** compares a copy's receive time with the message's sent time (its earliest copy so far), 10 s either side. A PostgreSQL advisory lock on the dedup key makes concurrent copies from two clients safe. Every copy's receive time is recorded in `receptions`, including repeats from the same client.
- **Jobs.** A job is the set of messages sharing a `job_number`. M1 has no separate jobs table.
- The **POCSAG function code and baud rate** are stored with the message (section 14).
- The server rejects receive times more than **60 s ahead of its own clock** (422 with `"code": "clock_ahead"`, logged without text). Section 14 puts this check on the client, but the client stamps its own receive times, so only the server can catch a client clock that's ahead (owner confirmed, 2026-10-10). The client prints a warning without page text ("this client's clock is more than 60 s ahead of the server's; check NTP") and drops the page. The server marks the client **clock ahead** (`clients.clock_ahead_since`), which admin client status shows (`pagerarchive client list` now, the admin UI from M2); the next accepted page clears it.
- Text is limited to 4096 bytes of UTF-8. A capcode is 1 to 7 digits and is zero-padded.

### 5. P1 in code

- Database errors are reduced to their SQLSTATE before they are logged or returned, because PostgreSQL messages can echo values. JSON decoding errors are never echoed, because they can quote the body.
- The acceptance tests capture the API's log and fail if it contains any page text they posted. The CI replay also searches the API and PostgreSQL container logs for every replayed page text.

### 6. Client

- The buffer holds 1,000 pages by default (`BUFFER_SIZE`). Retries back off from 1 s to 60 s.
- multimon-ng runs with `-f alpha` by default (`MULTIMON_MODE`). In its standard mode, function-0 pages print as numeric. Output is line-buffered with `stdbuf -oL`.
- The client container runs as root, so a mapped USB dongle is readable whatever the host's device permissions are.

### 7. CI

- CI builds the server image (amd64) and the client image natively on amd64 and arm64 runners, and replays the example golden lines through both images (`scripts/ci/client-replay.sh`). On the arm64 runner, the amd64 server and database run under emulation.
- Images aren't published to GHCR yet. Publishing comes with the compose file, when there is something to deploy (M2 to M4).

## Consequences

- Profile repositories can use any directory layout.
- Key rotation is "add a new client ID, then revoke the old one" until the admin UI adds rotation.
- The 60-second check sits on the server, not the client; a client with a fast clock shows as "clock ahead" to admins.
