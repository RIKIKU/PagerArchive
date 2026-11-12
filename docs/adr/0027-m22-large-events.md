# ADR 0027: M22, large events

## Status

Status: Proposed (2026-11-13). The owner's brief for M22: large events (R87-R87e, A84-A86) and R5's deployment code pattern, on the branch `m22` from main. The PowerShell module doesn't change, so its version stays; `VERSION` and the compose image stay as they are (ADR 0024).

## Context

R87-R87e (owner, 2026-11-12) add **large events**: PagerArchive notices when something big is happening and groups the pages into an event. Emergency pages to aliased capcodes open one when they carry a **deployment code** (a new, optional pattern in the network profile, R5) or when one job's pages name 20 or more distinct units (a **big job**; admins set the 20). Pages within 12 hours of an event's latest page join it, with the other pages of their jobs, and 12 quiet hours end it (R87a). An event records its **levels** (deployment, several deployments, big job) with the time each was first met (R87b). A Large events page under Tools lists them and an event's view shows its pages (R87c); an integration may select large events at one level and fires once per event, with a summary and never page text (R87d); retention thins and deletes events with their pages (R87e).

Detection sits on the ingest path, which R51a holds to 5 seconds from a page to its deliveries, and it must be right when pages arrive late, out of order, or from several receivers.

## Decisions

### 1. The data model (`00018_large_events.sql`)

- `large_events`: `first_at` and `last_at` (the sent times of its earliest and latest pages: the window that joins pages to it, and its start and end in the app) and one column per level, `deployment_at`, `several_deployments_at` and `big_job_at`, NULL until met. A level, once set, only ever moves earlier (a page that arrives late) and is never unset: an integration fires when it's first set (section 4).
- `large_event_messages`: `(message_id, event_id, deployment_codes)`, with `message_id` the key, so **a page is in one event at most**. Both columns cascade, so retention takes a page out of its event (R87e) and a deleted event takes its rows. `deployment_codes` are the codes the profile's pattern found in that page, kept so an event's codes are counted without reading page text again.
- Every number the app shows (deployment codes, jobs, distinct units, pages) is **counted from the pages the event still holds** when it's asked for (`ListLargeEvents`, `LargeEventCodeTimes`, `LargeEventJobs`, `LargeEventUnits`), so it follows retention with nothing to keep in step. Events are about one a week; counting each one's few hundred rows at most is cheap.
- `integrations.large_events`: the level an integration selects, or NULL for a page integration. `deliveries.event_id`: the event a delivery is about (section 4).
- An upgrade adds the two tables and the two columns, nothing else (A77).

### 2. Where detection runs, and what it costs

- **In the ingest transaction, after the page is stored** (`ingest.Service.Events`, `events.Detector.Stored`), for a new page only: a duplicate copy (R19) never gets there, so several receivers count once. The event, its pages and the deliveries it fires commit with the page that made them, as the page's own deliveries do (ADR 0006 §3), and the delivery worker is woken as before, so R51a's 5 seconds hold.
- **Nothing is read for a page that can't count.** The detector first checks, from the values ingest already has, that the page is Emergency, to an aliased capcode and not a heartbeat (R87), and carries a deployment code or a job number. Any other page returns before any query.
- **A page that might** takes a transaction-level advisory lock (`LockLargeEvents`), so two pages can't both open an event or join one being merged; the API already stores one page at a time (ADR 0010 §3), so the lock is uncontended. Then: its job's counting pages, with the event each is in (`JobEventPages`, by `messages_job_idx`; a job is tens of pages), the threshold (one settings row), and the events within 12 hours of it (`LargeEventsNear`, by `large_events_last_idx`). A page that doesn't open or join an event stops there. One that does adds its rows and updates one event.
- **Measured** (`TestLargeEventsCost`, cld01, the API through HTTP with the database in Docker): 200 Emergency pages to aliased capcodes, each on its own job naming units, stored in 5.8 ms each with detection off and 6.96 ms with it on (the slowest 10.6 ms); 200 pages each joining one open event, 8.8 ms (the slowest 16.5 ms). A86's deliveries came 36 ms and 48 ms after their pages.
- **Imported pages aren't grouped** (R83): the import's own service has no detector, so R83f's 500 pages a second is untouched, and an imported page fires nothing anyway (R83b). See question 1.

### 3. How pages group (R87, R87a, R87b)

- **A page opens or joins an event** (it "would open an event", R87a) when it counts and carries a deployment code, or its job's counting pages have named the threshold's distinct units (`Crossing`: units are compared as written, P3; every appliance type). It joins **every event it's within 12 hours of**: from 12 hours before the event's first page (a page that arrived late) to 12 hours after its last. If there are none, it opens one; if there are several, they're merged into the oldest (below).
- **"And so do the other pages of its job"**: the job's counting pages that aren't in an event yet join with it, earlier ones included, so a big job's event holds the job's pages (A85). A job's pages already in another event stay there.
- **A page that doesn't open an event** joins one only if it's within 12 hours of an event that holds one of its job's pages, so a job's later pages follow it into its event and ordinary pages from elsewhere on a busy day don't.
- **Out of order and late.** Everything is in sent time, not arrival time. A late page earlier than an event's first page joins it and moves its start, and each level keeps its earliest time. A late page within 12 hours of two events **merges** them: the oldest keeps its ID, takes the other's pages and deliveries, and each level its earliest time. So the events that pages make don't depend on the order they arrive in (`TestLargeEventsOutOfOrder`: three orders, each page from two receivers, give what pages in order give).
- **Ending** (R87a): an event is ongoing until 12 hours after its last page (`events.Ongoing`); a page after that opens a new one (A84's 04:00). Nothing is written when an event ends: it's worked out when shown.
- **Levels** (R87b): deployment, when a page with a code joins (its sent time); several deployments, the first page of the event's second distinct code (`SeveralAt`, from `LargeEventCodeTimes`, which counts codes across merged events too); big job, when the job reached the threshold, or its first page in this event if later (a job that grew big in an earlier event and was heard again after 12 quiet hours opens a new event at big job).
- **The threshold** is the setting `large_event_units` (default 20, 2 to 1,000), in Administration → Large event settings, beside the other settings. A change judges new pages; events already recorded keep their levels (A85).

### 4. Large-event integrations (R87d)

- **Selection.** An integration selects capcodes and aliases, or large events at one level, never both (refused with the reason). Its content is always summary, whatever was asked for, and it has no filter. In the form, "What it sends" chooses between pages and large events; large events shows the three levels and hides the selection, filter and content.
- **Firing once.** When a page's transaction finds an event's level newly set (NULL before, among the event and any merged into it, and set now), `EnqueueEvent` queues one delivery for each active integration at that level, in the same transaction. Each level is set once per event, so each integration gets one delivery per event (A86: a third code fires nothing). A merge carries levels over without firing them again.
- **The same queue, timing and safety as pages** (R50-R51b). The delivery's row is keyed by the integration and **the page that took the event to the level**, with `event_id` beside it: the 5-minute cut-off and the "within 5 seconds" are counted from that page (R87d), the primary key is still "never twice" (R51b), retries, pausing after 3 failures, the address guard and the secrets are unchanged. A page that arrives already 5 minutes old and takes an event to a level is "skipped: too old", as a page is.
- **The payload** is built when sent, from the event as it is then (codes, jobs and units "so far"): `event_type` `large_event`, `id` and `sent_at` of the page, `url` the event's view, and `large_event`: `id`, `level`, `level_label`, `reached_at`, `started_at`, `deployment_codes`, `jobs`, `units` and `url`. **No page text, address, location, alias or fields**, for webhook, MQTT and the presets alike (`TestEventPayloadIsASummary`, and A86 checks the bodies against every posted text). The deployment codes and job numbers are R87d's own list. Presets show "Large event · Several deployments", then when it was reached and started, the codes, jobs and units, and the link. Home Assistant discovery declares `large_event` and `test` as the entity's event types. Send test sends a made-up event marked `"test": true`. Documented in `docs/integrations.md`.

### 5. Retention (R87e)

The nightly run, after retention's batches, deletes every event left with no pages (`DeleteEmptyLargeEvents`) and sets the start and end of the others from the pages they still hold (`RefreshLargeEventSpans`); their numbers are counted as they're shown. Levels and their times are kept: they're what the event met (`TestLargeEventRetention`).

### 6. The API and the browser

- `GET /api/v1/large-events` (newest first by start, 50 a page with a cursor) and `GET /api/v1/large-events/{id}` (with its codes and job numbers), for every signed-in user (A29's rule: 401 without a session). `GET /api/v1/messages?event=<id>` lists an event's pages as the message list does (the search's own query, `SearchMessages`, keeps a page with a `large_event_messages` row; unset, it is one more condition that holds, as the album's is in ADR 0026, and CI's A75 speed check runs on it), and the scrubber's counts and the live stream take `event` too. With no dates chosen, the scrubber in an event's view covers the event's own days, so an old event's pages are on it. `GET` and `PUT /api/v1/admin/large-events`: the threshold, admins only.
- **Tools → Large events** (`/large-events`): one card per event, newest first: its start and end (the last page's time) or "ongoing", its levels (each with when it was met, on hover), and its numbers. Cards, not a table, so a phone needs no sideways scroll.
- **An event's view** (`/large-events/{id}`, breadcrumbs `Tools / Large events / Large event, <start>`): its start and end or ongoing, its numbers, its levels with their times, its codes and its jobs (each a link to the job view), the line that several incidents may share an event and the app doesn't track an incident, then its pages with `MessageList`: full text (P2), current redaction and aliases (P1, R23a), ignored aliases' pages included, live updates as pages join, and the scrubber.
- **Administration → Large event settings**: the threshold. Named apart from Tools' Large events, so the menu has one entry per name.
- New icon (R77a), from Tabler as before: large events `flame`, on the menu entry, the settings entry and each card. No new tokens.

### 7. The profile (R5)

`deployment_code_pattern`, optional, in the network profile's schema: an RE2 pattern; every match in the text a pager displays (the marker removed) is a code, each kept once, in order; with a group named `code`, the group is the code. A pattern that doesn't compile or matches empty text stops the server, as other patterns do. The example network's is `\bDG[0-9]{4}\b` (SPEC.md section 10). Network golden rows may list `deployment_codes`, checked only when listed, so existing tables (and private profiles' tables) need nothing new. Three rows are **proposed** for the owner (a code; two codes and a repeat; 5 digits and a code run into a word); they pass. `docs/profiles.md` documents both.

## Tests

- Go, stack-free: `internal/events` (`Crossing`, A85's 19 and 20 units, `SeveralAt`, levels reached once and across a merge, `Ongoing` at 12 hours and A84's 03:01, which pages count, a page that can't count making no query at all, which pages join), `pipeline.TestDeploymentCodes`, the profile's two new errors, `TestEventPayloadIsASummary` (both contents, the presets' text, Send test), and the golden rows.
- Go, through the API and the database (`internal/acceptance/m22_test.go`): `TestA84LargeEventsOpenAndGroup`, `TestA85BigJobOpensAnEvent` (with the setting's range and admins only), `TestA86LargeEventIntegrationsFireOnce` (two users' webhooks, delivered within 5 seconds, the summary checked for every posted text and the address, a second receiver's copy, the owner's delivery list), `TestLargeEventIntegrationRules`, `TestLargeEventsOutOfOrder`, `TestLargeEventJobPagesJoin`, `TestLargeEventRetention`, `TestLargeEventsSignedIn` and `TestLargeEventsCost`.
- Browser (`web/tests/e2e.spec.ts`, Chromium, Firefox and WebKit): "A86 large events in the browser" (the form, the delivery at several deployments only, Tools → Large events, the event's view with its codes, pages, scrubber and breadcrumbs, a page joining live, 1,280 and 375 px with no sideways scroll, axe at both), "A85 large event settings in Administration", and the axe check of every view with `/large-events`, an event and the settings page added. A41, A68 and A70 list the new menu entries.

## Evidence

See the pull request for the commands and their output: `go test ./...` with the test database, `scripts/e2e.sh` in all three browsers, `npm run check`, `sqlc diff`, `gofmt -l`, `node scripts/check-tokens.mjs`, `node scripts/icons.mjs --check` and `validate-profile -v profiles/example`.

## Questions for the owner

1. **Imported pages and events.** Pages imported from the old archive (R83) don't form events, so the Large events page starts at the switch-over. Grouping them would make the past events visible too (no integration would fire: R83b) but adds work to every imported Emergency page, against R83f's 500 a second. Should imported pages form events, perhaps by a one-off command after the import?
2. **Re-parsing.** Events are decided when a page arrives. A capcode aliased later, a template's profile changed, or a deployment code pattern added doesn't regroup older pages (R23a re-parses them, but their events stay). Is that acceptable, or should a re-parse rebuild events?
3. **Ignored aliases.** A capcode whose alias is marked ignored (R27) still has an alias, so its Emergency pages count. Should ignored aliases' pages be left out?
4. **The end shown.** An event's end in the app is its last page's time; "ongoing" until 12 hours after it. (The other reading, the time it ended, is always the last page plus 12 hours.)
5. **A big job heard again.** A job that grew big in an event that ended, then has another page after 12 quiet hours, opens a new event at level big job, which fires big-job integrations again. Is a long-running job meant to be one event however long it runs?
