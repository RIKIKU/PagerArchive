# 6. Milestone M5: per-user integrations and metrics

Date: 2026-10-16

## Status

Proposed. Waiting on the owner's answers to the questions below and the owner's approval.

## Context

Milestone M5 covers per-user integrations (R47–R51c): webhook and MQTT destinations, the Discord, Telegram, Pushover and ntfy presets, Full or Summary content, Home Assistant MQTT discovery, the 5-minute cut-off, no double firing and Send test. It also covers the metrics endpoint (R52). Its checks are A30–A32 and A35–A39. The owner fleshed out M5 in the spec on 2026-10-16 (commit 02da9e8). Section 13 leaves R51's retry and pause thresholds to the implementer.

The owner asked for this ADR to record: the MQTT library, how secrets are encrypted and where the key lives, the retry and pause thresholds, the queue that survives a restart, and the payload JSON. Other rules from the owner for this milestone:

- Presets are tested against stand-in servers that check each service's documented format. CI never calls Discord, Telegram, Pushover or ntfy.
- Page text never appears in logs, metrics or integration error messages.

## Questions for the owner

Each question has a recommendation. The rest of this ADR assumes the recommendation, and marks where an answer would change it.

### Q1. Where the "accounts locked" metric comes from (R52, R44a)

The lock is decided inside Authentik by the `pagerarchive-not-locked` policy (ADR 0004 §9). It counts `login_failed` events, and the failure count on the account's email device for enrolment codes. The app can't see either today. Two ways to count locked accounts:

| | A. Read Authentik's tables, read only (recommended) | B. Authentik's API |
|---|---|---|
| What the app reads | Three tables in Authentik's own database (events, email devices, users), in the shared PostgreSQL container, with a new read-only role that can read only those three tables | Authentik's event log, through its API |
| Counts enrolment-code locks | Yes, the same rule as the policy | **No.** The API doesn't expose the email device's failure count (checked against 2026.5.5's OpenAPI schema: `EmailDevice` has only `pk`, `name`, `email` and `user`) |
| New access | `SELECT` on three tables | The app's service account gains read access to the **whole** event log, including every sign-in's IP address |
| Upgrade risk | The query depends on Authentik's table layout. The policy already depends on the same models, and ADR 0004 already says an Authentik upgrade re-runs `scripts/stack-checks.sh`. That script will check the metric too | Lower |

With A, the role is created by the database init script on a new install. An existing install creates it with one documented command.

### Q2. A request that was sent but got no answer (R51b)

R51b says a page is never sent to an integration again once it has been **delivered**. The hard case is a request that was fully sent but got no answer before the timeout (a webhook POST, or an MQTT publish with no acknowledgement). The endpoint may have acted on it (opened the gate) or may never have seen it.

- **Recommended: don't retry.** The page is recorded as "failed: no answer" and never sent again. A slow or dropped answer loses that page, but nothing fires twice.
- Alternative: retry until the cut-off. Nothing is lost, but an automation may fire twice. Receivers would have to drop repeats by page ID, which a Home Assistant automation can't easily do.

Either way, the request is retried when it **can't be sent** (connection refused, DNS failure, TLS handshake failure), and when the endpoint **answers** that it failed (`429`, or `5xx` except `501`). The timeout is 10 seconds.

### Q3. Who can resume an integration an admin paused (R47)

**Recommended:** only an admin can. The owner sees "Paused by an admin" and can still edit it, delete it, or press Send test. Otherwise an admin's pause would be undone with one click.

### Also worth a look (implementer decisions that change what users see)

- **The whole destination URL is a secret** (§4): after saving, owners see only the scheme and host (`https://ha.example.org/…`). Discord webhook URLs and Home Assistant webhook IDs carry their credentials in the URL itself.
- **Every payload, Summary included, has a link to the message in the app** (`url`, §6). Discord or Telegram then hold a link that only opens after signing in, but they do learn the app's address.
- **Pages that arrive already too old are recorded** as "skipped: too old" in the owner's delivery list, the same as pages that ran out of time (§3).
- **Two R33 criteria aren't offered as filters:** a date range (only new pages are sent) and "capcodes with no alias" (the selection already names capcodes, and R48 exists so that nobody forwards wide sets of pages by accident).

## Already decided elsewhere (cited, not re-decided)

- An integration is a selection, a filter and a destination, owned by one user. Others can't see it. Admins can see that it exists and pause it, but never see its secrets (R47).
- The selection is required: capcodes and/or aliases (R48). Only new pages are sent, after redaction. Admin-only fields go only to admins' integrations (R49).
- Destinations: webhook, MQTT, and presets for Discord, Telegram, Pushover and ntfy (R50). Full or Summary content; presets default to Summary, webhook and MQTT to Full (R50a). Home Assistant discovery for MQTT, one event entity per integration (R50b).
- Private, loopback and link-local addresses are refused unless an admin allows them. Secrets are encrypted. Failed deliveries retry with backoff, and an integration that keeps failing is paused (R51).
- Attempted within 5 s; retried until 5 minutes after the first receive time; then "skipped: too old". Pages already 5 minutes old on arrival are not sent (R51a). One delivery per page per integration, carrying the page's ID (R51b). Send test with `"test": true` (R51c).
- Metrics: Prometheus format, access-controlled, no page text (R52). Client status is admin-only (R39), so metrics carry client IDs, not names.
- No page text in any log line or error (P1, ADR 0001 §5).

## Implementer decisions

### 1. Data model (migration 00009)

| Table | Holds |
|---|---|
| `integrations` | `id`; `uid` (random, used in MQTT topics and Home Assistant IDs); `owner_id`; `name`; `kind` (`webhook`, `mqtt`, `discord`, `telegram`, `pushover`, `ntfy`); `content` (`full` or `summary`); `config` (JSON, non-secret settings); `secrets` (encrypted, §4); `filter` (JSON, §2); `status` (`active` or `paused`); `paused_by` (`failing`, `owner` or `admin`); `failed_pages` (undelivered pages in a row, §3); `last_error`, `last_error_at`, `last_delivered_at`; timestamps |
| `integration_capcodes` | (`integration_id`, `capcode`): exact 7-digit capcodes |
| `integration_aliases` | (`integration_id`, `alias_id`); deleting an alias removes it from selections (cascade) |
| `deliveries` | (`integration_id`, `message_id`) **primary key**; `status`; `attempts`; `next_attempt_at`; `last_error`; `delivered_at`; timestamps. No payload and no page text: the payload is built from the message when it's sent |

- Deleting a message (retention) deletes its deliveries (cascade). The nightly run also deletes delivery rows older than **30 days**, so the owner's view covers the last month.
- Deleting your account (R43c) deletes your integrations. Deactivation (R43b) stops them without deleting them (§3).

### 2. Selection and filter (R48, R49)

- **Selection:** exact capcodes (zero-padded, as everywhere) and aliases. An alias selects the messages that **resolved to it** (`messages.alias_id`), as the alias view does (R35e). So with "Station 1" on `0100001` and a pattern alias `0100%`, selecting the pattern doesn't pick up `0100001`. Pages to ignored aliases and heartbeat capcodes are sent if selected, because the selection is explicit (R27).
- **Filter**, all optional, all combined with AND:
  - event types: any of a list of **categories** (Other included) and/or **raw codes**. When either list is set, pages with no event type match only "Other" (as in reports, R38);
  - priorities: any of Emergency, Non-emergency, Admin, none;
  - text (case-insensitive substring of the text or an extra field, as search), group, agency, job number;
  - flags (`parse_failed`, `address_unsplit`, `location_mismatch`): **admins only**. They're ignored while the owner isn't an admin, as in search (A29).
- Matching is a pure Go function on the stored message row, unit-tested on its own. Categories come from the same function the display uses (`pipeline.Category`), so an integration and the list agree.

### 3. Delivery: the queue, retries, the cut-off and pausing (R51, R51a, R51b)

**The queue survives a restart** because it's the `deliveries` table, not memory. No separate queue (section 8).

- **Enqueue in the ingest transaction.** When a page is stored (not when a duplicate copy arrives), the same transaction finds the active integrations whose selection covers it, whose owner is active, and whose filter matches. It inserts one `pending` row per integration. So a stored page and its deliveries commit together, and a crash can't lose one without the other. The primary key (`integration_id`, `message_id`) makes a second row for the same page impossible, whatever the code path (R51b, A39).
- A page that arrives when its first receive time is already more than 5 minutes ago gets a row with status `too_old` and no attempt (R51a, A36). An integration created after a page arrived never sees that page (no backfill).
- **Sending.** A worker in the API wakes as soon as the ingest transaction commits, and on a timer for retries. Each integration's deliveries go one at a time, oldest page first, so a slow endpoint delays only its own pages. Different integrations send in parallel (up to 8 at once). An advisory lock keeps it to one worker if a second API ever runs. After a restart, pending rows are picked up again (and the cut-off still applies).
- **Timeouts:** 10 s per attempt (HTTP request; MQTT connect and acknowledgement).
- **Retry schedule** (R51): after 2, 4, 8, 16 and 32 seconds, then every 60 s, until the cut-off. That's 9 attempts at most. A `Retry-After` header or `retry_after` field (Discord, Telegram) is honoured when it's later than the schedule.
- **The cut-off** is the message's sent time (the earliest receive time, R21) plus 5 minutes, read when each attempt is due. So a late copy from another client that moves the sent time earlier moves the cut-off with it. No attempt starts after the cut-off. The row becomes `too_old`, shown as **"skipped: too old"** (A36).
- **Which failures are retried:** see Q2. A refusal that won't change (`400`, `401`, `403`, `404`, `405`, `410`, `413`, `422`, MQTT "bad credentials" or "not authorised") is recorded as `failed` at once.
- **Pausing** (R51, A31): `failed_pages` counts pages in a row that weren't delivered (`failed`, or `too_old` after at least one attempt). Pages that arrived too old don't count, because that's not the endpoint's fault. **Three in a row pause the integration** (`paused_by = failing`). A delivered page resets the count. With one endpoint outage of a few minutes (as in A36), one page is skipped and the integration carries on. An endpoint that stays broken is paused after its third page. The owner sees "Paused: kept failing", the last error and when it happened, and a Resume button.
- **Pausing, deleting or deactivating** cancels that integration's pending rows (`cancelled`). While paused, new pages aren't queued for it. Deactivating a user stops their integrations: they aren't matched, and their pending rows are cancelled at once. Reactivating starts them again, because they were never paused.
- **Delivery statuses:** `pending`, `delivered`, `failed`, `too_old` and `cancelled`. Each delivery keeps the HTTP status or MQTT result of its last attempt.

### 4. Secrets: what they are, how they're encrypted, where the key lives (R51)

- **Secret fields:** the whole destination URL (webhook, Discord), the custom auth header's value (webhook), the MQTT password, the Telegram bot token, the Pushover application token and user key, and the ntfy topic and access token (a topic on a public ntfy server works like a password). They're stored as **one encrypted blob per integration**.
- **Shown after saving:** the URL's scheme and host only (`https://ha.example.org/…`), kept in plain text for that purpose and for the address check. The header name, MQTT broker, username and topic, Telegram chat ID and ntfy server stay visible. The API never returns a secret; it says which ones are set. When editing, an empty secret field keeps the saved value.
- **Encryption:** AES-256-GCM (Go standard library), with a random 96-bit nonce for each encryption. The associated data is the integration's `uid`, so a blob copied onto another row doesn't decrypt. Stored as `version (1 byte) | key ID (4 bytes) | nonce | ciphertext + tag`.
- **The key lives outside the database:** `INTEGRATION_SECRETS_KEY` in `.env` (32 random bytes, base64; the docs give the `openssl` command), passed to the API like the other secrets. A database dump or backup alone doesn't reveal integration secrets.
- **Rotation:** set the new key, and the old one as `INTEGRATION_SECRETS_KEY_OLD`. The API re-encrypts every blob at start-up. The key ID says which key a blob needs, so a missing key gives a clear message ("re-enter this integration's secrets"), not a garbled error.
- **No key set:** the API still starts, logs a warning, and the Integrations page says integrations are off until the operator sets the key. That way existing deployments keep working after the upgrade.

### 5. Address safety (R51, A31)

- **Refused by default:** loopback, private (RFC 1918, IPv6 unique local), link-local, unspecified, multicast and broadcast addresses, the shared address space `100.64.0.0/10` (which Tailscale uses), and IPv4-mapped forms of these. Checked twice:
  - **when saving**: the host is resolved, and every address must be allowed. The message names the address and says an admin can allow it ("192.168.1.10 is a private address. Ask an admin to allow it.");
  - **when connecting**: a custom dialer resolves the host, checks each address again and connects to that checked address. So DNS can't be changed after saving to point at an internal host (DNS rebinding). TLS still verifies the original host name.
- **Redirects aren't followed.** A `3xx` answer counts as a failure. Proxy settings from the environment are ignored, so the check can't be routed around.
- **Admins' allow list** (Admin → Integrations): IP addresses, CIDR ranges (`192.168.1.0/24`) and host names (`homeassistant.lan`, matched exactly). An address is allowed if it's public, in an allowed range, or reached through an allowed host name. Kept in `settings`. Changing it doesn't re-check saved integrations; the connect-time check applies the current list.
- **Schemes:** webhooks `https` or `http` (the form says to use `http` only on your own network); MQTT `mqtt://` or `mqtts://` (TLS with the system's roots, or a CA certificate pasted in, for a home broker with its own CA).
- **Presets** only post to their own hosts: Discord webhook URLs must be `https://discord.com/api/webhooks/…` (or `discordapp.com`). Telegram and Pushover are fixed (`api.telegram.org`, `api.pushover.net`). The ntfy server defaults to `https://ntfy.sh` and can be changed for a self-hosted one (which then needs the allow list if it's private). The service base URLs are fields in the code, which only tests change, to point at stand-ins. They aren't settings, and there's no test switch in the shipped binary.

### 6. Payload JSON (R50, R50a)

The same JSON goes to webhooks and MQTT. Version 1, documented in `docs/integrations.md`:

```json
{
  "version": 1,
  "event_type": "page",
  "test": false,
  "id": 1234,
  "url": "https://pager.example.org/messages/1234",
  "integration": { "id": "k3x9q2m7vw4d", "name": "Station 1 fires" },
  "content": "full",
  "sent_at": "2026-10-16T03:04:05Z",
  "capcode": "0100001",
  "alias": "Station 1",
  "agency": "Example Fire Service",
  "priority": "Emergency",
  "heartbeat": false,
  "category": "Structure fire",
  "event_label": "House fire",
  "job_number": "EX0000001",
  "text": "EX0000001 HSF1 SMOKE FROM ROOF @ 12 FICTION ST, MADEUPTON GRID 123456 UNITS T1 T2 #STN1",
  "fields": {
    "event_type": "HSF1",
    "description": "SMOKE FROM ROOF",
    "address": "12 FICTION ST",
    "locality": "MADEUPTON",
    "grid_ref": "123456",
    "units": ["T1", "T2"],
    "group": "STN1"
  },
  "extra": {},
  "labels": { "event_type": "House fire" },
  "location": { "lon": -140.864937, "lat": -34.831359, "approximate": false, "source": "address" }
}
```

- `id` is the message's ID: the same for every copy of one page to one capcode (R51b), and never reused.
- `event_type` at the top level is the **kind of delivery**: `"page"` or `"test"`. The page's own event type code is `fields.event_type`. This is Home Assistant's MQTT event format, so Home Assistant reads the payload as it is: no template, with every other key becoming an attribute of the event (§7).
- `text` is the page **without its priority marker** (R50, as a pager shows it). Missing fields are `null`. Times are UTC, RFC 3339.
- **Admins' integrations** also get `"flags": {"parse_failed": …, "address_unsplit": …, "location_mismatch": …}`. The owner's role is read when each delivery is sent, so a demoted admin's next delivery has no flags (R49). Receive times and client names are never sent (R39).
- **Summary** has only `version`, `event_type`, `test`, `id`, `url`, `integration`, `content`, `sent_at`, `capcode`, `alias`, `priority`, `category`, `event_label`, `job_number` and `fields.event_type`. No text, address, location, extra fields, description, units, group or grid reference (R50a, A37).
- **Send test** (R51c) has `"event_type": "test"`, `"test": true`, `"id": null`, and made-up values that say they're a test ("PagerArchive test delivery"), with the integration's first capcode or alias.
- Webhook requests: `POST`, `Content-Type: application/json`, `User-Agent: PagerArchive/<version>`, and the custom auth header if set. Any `2xx` is success.

### 7. MQTT and Home Assistant (R50, R50b, A38)

- **Library:** `github.com/eclipse/paho.mqtt.golang` v1.5.1 (MQTT 3.1.1, licensed EPL-2.0 or EDL-1.0; the EDL is a BSD-style licence, compatible with MIT). It's the most widely used Go MQTT client, MQTT 3.1.1 works with every common broker including Home Assistant's, and `SetCustomOpenConnectionFn` lets the address check (§5) do the dialing. Tests use an in-process broker, `github.com/mochi-mqtt/server/v2` (MIT), as a **test-only** dependency (it isn't in the server binary). The live copy uses Mosquitto.
- **One connection per delivery:** connect, publish with QoS 1, wait for the acknowledgement, disconnect. Pages are rare, so this costs nothing that matters. It keeps no idle connections, and every connection goes through the address check. The client ID is `pagerarchive-<uid>`, with a clean session. The payload is §6's JSON, not retained.
- **Topic:** set by the owner. The default is `pagerarchive/<uid>`.
- **Home Assistant discovery** (optional, off by default; discovery prefix default `homeassistant`): when the integration is saved, the API publishes a **retained** single-component config to `<prefix>/event/pagerarchive/<uid>/config`:

  ```json
  {
    "name": "Station 1 fires",
    "unique_id": "pagerarchive_k3x9q2m7vw4d",
    "state_topic": "pagerarchive/k3x9q2m7vw4d",
    "event_types": ["page", "test"],
    "icon": "mdi:pager",
    "qos": 1,
    "device": {
      "identifiers": ["pagerarchive_<deployment id>"],
      "name": "PagerArchive",
      "manufacturer": "PagerArchive",
      "model": "PagerArchive server",
      "sw_version": "<version>",
      "configuration_url": "https://pager.example.org/integrations"
    },
    "origin": { "name": "PagerArchive", "sw_version": "<version>", "support_url": "https://github.com/RIKIKU/PagerArchive" }
  }
  ```

  The deployment ID is a random value made once and kept in `settings`. So every integration on one deployment shows up under one "PagerArchive" device, and two deployments on one broker don't collide. The save answers with what happened ("Home Assistant discovery published" or the broker's error). Turning discovery off, changing the topic or prefix, or deleting the integration publishes an empty retained config, which removes the entity. The config is retained, so Home Assistant picks it up again after a restart.
- **Each matching page** is published to the state topic. Home Assistant fires the event entity with `event_type: page`, and the rest of the payload becomes its attributes (`priority`, `alias`, `text`, …). `docs/integrations.md` has example Home Assistant automations for MQTT and for the webhook (R50b).

### 8. Presets (R50)

Each preset turns the same message into the service's documented request. Formatting is shared: a title like `Emergency · Station 1`, and a summary line (event type and label, job number, and the sent time in the deployment's time zone, en-AU style, R35a). With **Full** content, the text without its marker comes first. Every preset has the link to the message. Text is cut to the service's limit, with `…` at the end.

| Preset | Request (checked against the service's docs, 2026-10-16) | Config (secret in bold) |
|---|---|---|
| **Discord** | `POST` to the **webhook URL**, JSON `{"content": …, "embeds": [{title, description, url, timestamp, fields}], "allowed_mentions": {"parse": []}}`. Content ≤ 2,000 characters, embeds ≤ 10. Success: `204`. Mentions are turned off, so page text can't ping `@everyone` | **webhook URL** |
| **Telegram** | `POST https://api.telegram.org/bot<token>/sendMessage`, JSON `{"chat_id", "text", "link_preview_options": {"is_disabled": true}}`. Plain text (no `parse_mode`, so page text isn't parsed as markup), ≤ 4,096 characters. Success: `{"ok": true, …}` | **bot token**, chat ID |
| **Pushover** | `POST https://api.pushover.net/1/messages.json`, form-encoded (the format Pushover documents): `token`, `user`, `message` (≤ 1,024), `title` (≤ 250), `url` (≤ 512), `url_title`, `timestamp`, `priority` (with `retry=60` and `expire=3600` for priority 2). Success: `{"status": 1, …}` | **application token**, **user key**, priority |
| **ntfy** | `POST <server>/` JSON `{"topic", "message", "title", "priority" (1–5), "tags": ["rotating_light"] for Emergency, "click": <link>}`, optional `Authorization: Bearer <token>`. Message ≤ 4,096 bytes | server, **topic**, **access token**, priority |

- **Priority** (Pushover and ntfy): by default it follows the page (Emergency → high, others normal). It can be set to a fixed level, including Pushover's emergency (2), which repeats until it's acknowledged.
- **Stand-in servers** (`internal/integrations/standin`, test code only): one per service, built from its docs. Each checks method, path, content type, required fields, field types and lengths, and that mentions are off (Discord), and answers as the service does, including its error and rate-limit replies. The acceptance tests aim the presets at them (A37).
- **CI never calls the real services:** every integration test runs with a dialer that refuses any address outside `127.0.0.0/8`, so a mistake fails the test instead of reaching Discord. The stand-ins are the only endpoints used.

### 9. Errors without page text (P1, R51)

- Last errors are made by the app from fixed parts only: `HTTP 503 Service Unavailable`, `no answer within 10 s`, `connection refused`, `DNS lookup failed`, `TLS: certificate not trusted`, `MQTT: not authorised`, `address not allowed: 10.0.0.5 is a private address`.
- **Response bodies are never kept or logged** for real deliveries: an endpoint could echo the payload, which can hold page text.
- **Go's network errors quote the URL**, and a webhook URL can hold its credentials. So errors are classified (`*url.Error` unwrapped, `net.OpError`, TLS and DNS types) and never printed as they come.
- **Send test** shows the owner the status, the time taken and the first 1 KB of the answer's body. The test payload holds no page text. It's limited to one test every 5 seconds per integration.
- Log lines name the integration ID, delivery status and error class. A test checks that the server log, the API's integration responses and the metrics contain none of the posted page texts (as the M1–M4 checks do).

### 10. API and screens

- **API** (all signed in): `GET/POST /api/v1/integrations`, `GET/PUT/DELETE /api/v1/integrations/{id}`, `POST …/{id}/test`, `POST …/{id}/pause` and `…/resume`, and `GET …/{id}/deliveries` (the last 100: time, link to the page, status, attempts, last error). Another user's integration answers `404`, so it can't be told apart from one that doesn't exist (R47). `GET /api/v1/aliases` lists aliases for the selection picker.
- **Admin API:** `GET /api/v1/admin/integrations` returns owner, name, kind, status and last delivery. It has no destination, selection, filter or secrets. Admins can also pause and resume, and `GET/PUT /api/v1/admin/integration-hosts` manages the allow list.
- **Screens:** "Integrations" in the main navigation for everyone: a list with status, last delivery and last error, a form (selection, filter, destination, content, Home Assistant discovery for MQTT), Send test with the answer shown, and recent deliveries. "Admin → Integrations": everyone's integrations, with Pause, and the allow list. Styles use the tokens in `web/src/app.css`. The screens are covered by the axe checks at 375 px and desktop width, in both themes.

### 11. Metrics (R52, A32)

- **`GET /metrics`** on the API's port, in the Prometheus text format. It's written by hand: a few dozen lines, so no client library is needed.
- **Access:** `Authorization: Bearer <METRICS_TOKEN>` (Prometheus' `authorization` setting), compared in constant time. Without a token it's `401`. With `METRICS_TOKEN` unset the endpoint is off (`404`). The docs suggest scraping on the LAN and keeping `/metrics` out of the reverse proxy.
- **Metrics:**

  | Metric | Type | Labels |
  |---|---|---|
  | `pagerarchive_client_last_seen_timestamp_seconds` | gauge | `client` (the client's ID, not its name) |
  | `pagerarchive_client_receiving` (1 or 0, R39) | gauge | `client` |
  | `pagerarchive_client_clock_ahead` (1 or 0) | gauge | `client` |
  | `pagerarchive_pages_received_total` | counter | `outcome`: `stored`, `duplicate`, `ignored`, `clock_ahead` |
  | `pagerarchive_messages_flagged` (messages flagged now) | gauge | `flag`: `parse_failed`, `address_unsplit`, `location_mismatch` |
  | `pagerarchive_integration_deliveries_total` | counter | `kind`, `result`: `delivered`, `failed`, `too_old`, `cancelled` |
  | `pagerarchive_integrations` | gauge | `status`: `active`, `paused` |
  | `pagerarchive_accounts_locked` (R44a; Q1) | gauge | none |
  | `pagerarchive_nightly_last_run_timestamp_seconds`, `…_success` (1 or 0), `…_deleted`, `…_redacted` | gauge | none |
  | `pagerarchive_build_info` | gauge | `version` |

- Counters start at zero when the API starts (Prometheus' `rate()` handles that). Flag counts come from new partial indexes, so a scrape stays cheap on a large archive. The locked-accounts count is cached for 60 s. No metric has page text, alias names, integration names, users or destinations as a label.

### 12. Deployment and docs

- New settings in `.env.example` and `compose.yaml`: `INTEGRATION_SECRETS_KEY`, `INTEGRATION_SECRETS_KEY_OLD` (only while rotating), `METRICS_TOKEN`, and the read-only Authentik database role's password (Q1, option A).
- `docs/integrations.md`: what each destination needs, the payload (§6), Full and Summary, the cut-off and statuses, the allow list, and Home Assistant examples for MQTT and the webhook. `docs/deploy.md`: the key, metrics with an example Prometheus scrape config, and the upgrade step.
- **Live copy on cld01** (LAN only, example data): the M5 build, with a Mosquitto test broker and a small test receiver (`scripts/test-receiver`, not part of the server). The receiver accepts webhook POSTs, subscribes to the broker, and lists what arrived on a web page that refreshes itself. The seed script creates a webhook and an MQTT integration for the first admin, once they've set up their account.

## Verification

| Check | How |
|---|---|
| A30 | Go acceptance test through the API: a webhook integration on "Station 1", filtered to Structure fire, aimed at a test receiver. `HSF1` and `GRS3` pages for `0100001` and `HSF1` for `0100002` → exactly 1 request within 5 s, with the text without its marker. User B has none, and nothing else is sent |
| A31 | Saving `http://127.0.0.1:8080/` and `http://192.168.1.10/` is refused with a message naming the address. Allowed once an admin adds them. Then a receiver that answers `500`, three pages and a controlled clock → paused, last error `HTTP 500 …`, and no page text in the owner's view. Plus a DNS-rebinding unit test of the dialer |
| A32 | Two clients, one last heard 31 minutes ago → `receiving` 0 and 1. No page text in the scrape. `401` without the token, `404` with `METRICS_TOKEN` unset |
| A35 | Two webhooks on `0100001`, Emergency and Non-emergency, each with its own receiver → one request each, each for its own page |
| A36 | Receiver down for 6 minutes (controlled clock) while a page arrives, then back → no request for it, and its delivery is "skipped: too old". A page received 10 minutes earlier → no request |
| A37 | A Discord preset on its default content (Summary) and a Full webhook, both on `0100001`, with the Discord stand-in and a receiver → the Discord request passes the stand-in's format checks and has neither the text nor the address. The webhook request has the text |
| A38 | MQTT with discovery, in-process broker → after saving, the broker holds the retained config (device "PagerArchive", one event entity, `event_types` page and test). After a matching page, the state topic gets the event. Also checked: turning discovery off removes the entity |
| A39 | Two clients post the same page → exactly 1 request, with the page's `id`. A unique-key test inserts a second delivery row directly and it fails. Send test → a request with `"test": true`, and the owner sees the answer's status and body |
| Presets | Each stand-in rejects a wrong request (missing field, too long, wrong content type, mentions on). Each preset's request passes its stand-in, in Full and Summary |
| Secrets | Round trip; wrong key; blob moved to another row; rotation; the API never returns a secret; the database row doesn't contain the URL or password in plain text |
| P1 | Every integration test checks the server log, API responses (last error, deliveries) and the metrics for the posted page texts |
| Accounts locked | Added to `scripts/stack-checks.sh` after A33's locks: the metric counts the locked accounts, and goes back to 0 once the locks expire (with the shifted timestamps A33 already uses) |
| Screens | Playwright: create a webhook, Send test, see the answer and a delivery arrive at a local receiver, then pause and resume. Admin view without destinations. axe on every new screen |
| Mutation checks | At least two checks are shown failing with the code deliberately broken (for example, the cut-off removed for A36, and Summary sending the text for A37), then passing again |

## Consequences

### Positive

- Delivery is part of storing a page: a stored page always has its deliveries, and a page can't be delivered twice to one integration, which the database enforces.
- No page text is kept anywhere new: payloads are built when they're sent and never stored. Errors are built from fixed parts.
- Home Assistant reads the payload as it is, so the same JSON serves webhooks, Node-RED and Home Assistant.
- Integration secrets can't be read from a database dump without the key.
- Operators get receiver, ingest, delivery, lock and nightly-run health in their own monitoring.

### Negative / risks

- A request that was sent but never answered isn't retried (if Q2 is accepted), so that page can be lost.
- With Q1 option A, the metric depends on Authentik's table layout. An Authentik upgrade must re-run `scripts/stack-checks.sh`, as it already must for the guessing protection.
- Losing `INTEGRATION_SECRETS_KEY` means every owner re-enters their secrets.
- One MQTT connection per delivery adds a connection handshake (and a TLS one, with `mqtts`) to each page, typically well under a second.
- The allow list is all-or-nothing per host or range. Admins can't allow a private host for one user only.
