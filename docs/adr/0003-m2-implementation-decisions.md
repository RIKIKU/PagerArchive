# 3. Milestone M2 implementation decisions

Date: 2026-10-10

## Status

Accepted (2026-10-11, by the owner, including the reload-on-reconnect change in section 5 and the receiver names in section 2)

## Context

Milestone M2 adds the browser UI, live updates, search, and the job and detail views (checks A1, A3, A4, A6 search, A10). The owner settled R27, R30 and R33 on 2026-10-10 (recorded in `SPEC.md`) and chose how M2 handles admins before sign-in exists. This ADR records those decisions and the implementer's choices where the spec is silent ("How to use", item 7).

## Decisions

### 1. Viewer role before sign-in (owner decision, 2026-10-10)

Roles and sign-in arrive in M3 (R40, R44). Until then the setting `DEV_VIEWER_ROLE` (`user`, the default, or `admin`) is every viewer's role. The API never sends admin-only data to a non-admin: the `parse_failed`, `address_unsplit` and `location_mismatch` flags (R35c), the flag filters (R33) and client status (R39, ADR 0002). The UI hides what the API doesn't send. With `admin`, the server logs a warning at start-up. M3 replaces the setting with Authentik roles.

### 2. Receive times in the detail view

R35c shows every client's receive time. R39 keeps client status to admins because client names can reveal where receivers are. So the detail view shows receive times to everyone, but names the clients only for admins; other viewers see "Receiver 1", "Receiver 2", and so on.

### 3. Aliases, templates and re-parsing (R23a, owner adjustment)

- Each message stores the alias that matched when it was last parsed (`alias_id`). Names, agencies and the ignored flag are read through the join, so a rename shows straight away without a re-parse.
- **Every** alias or template change, whether from the CLI, the import or (later) the admin UI, writes a re-parse request in the same transaction (`reparse_requests`, a capcode `LIKE` pattern) and sends `NOTIFY pagerarchive_reparse`. The API's worker wakes on the notification and also polls every minute, so a change made while the API is down is picked up at the next start. Requests that fail are queued again.
- At start-up the API hashes the network profile, every agency profile and the address dataset. If the hash differs from the one stored in `settings`, it queues a full re-parse (section 14).
- Re-parsing re-resolves the alias and re-runs the formats on the stored, redacted text. It never redacts: retroactive redaction stays a nightly job (M4).
- Until the M3 admin UI, aliases and templates are managed with `pagerarchive template …` and `pagerarchive alias …`.

### 3a. Alias import (R30, owner decisions 2026-10-10 and 2026-10-11)

- `pagerarchive alias import <file.csv> [--mapping M.json] [--dry-run]` reads a CSV with a header row and these columns, by name: `address` (capcode or pattern), `alias`, `agency`, `icon`, `color` (or `colour`), `ignore`. **Every other column is never read, stored, logged or printed**; an export's plugin settings column can hold credentials. Even so, the owner strips that column before import.
- One template per agency name, created only if missing (existing templates are left unchanged). Its icon and colour are the most common pair among that agency's aliases. The mapping file (`{"agency_profiles": {"<agency>": "<profile>"}}`, owned by the deployment) attaches agency profiles; unmapped agencies get a template with no profile (text only).
- An empty agency, or the literal `null` some exports write, means **no template**: the alias name is still used, and its pages are text only. Aliases may therefore have no template (migration 00004).
- Existing aliases with the same pattern are updated in place, so an import can be re-run. A capcode that appears twice keeps the later row.
- The whole import is one transaction, followed by one full re-parse request (R23a). The report gives counts and row numbers only, never alias data.

### 4. Search (R33)

- Text: case-insensitive substring of the stored text or any extra field's value (section 14). Alias and agency: substring of the name. Group: exact, case-insensitive. Capcode and job number: exact.
- Dates are calendar days in the deployment time zone, inclusive (section 14). The API also accepts RFC 3339 times.
- Category: the network profile's categories. "Other" means no mapped category, including pages without an event type.
- Pages to ignored aliases are left out unless "include ignored" is set (R27).
- Pages come 50 at a time, newest first by sent time, using keyset pagination on (sent time, id).

### 5. Live updates (R32)

- Each SSE connection runs the message list's own query, restricted to the new message, so its live updates follow exactly the same rules as its list (A4). At 20 viewers and about one page a minute, that's about 20 small indexed queries a minute.
- ADR 0001 planned `Last-Event-ID` backfill. Instead, the browser reloads the first page of its list whenever its stream reconnects. That's simpler and can't miss anything the list would show. A connection that falls 64 messages behind is closed and reconnects.
- Only new messages are pushed. A duplicate copy from another client doesn't push a row (it changes only the receive times).

### 6. Browser app

- SvelteKit 3 (the current major version) with Svelte 5, as a static single-page app embedded in the API binary (ADR 0001). SvelteKit 3 takes its configuration in `vite.config.ts` and imports `src/lib` as `#lib/…`.
- The search criteria live in the page URL, so a search can be shared and survives a reload.
- Late buffered pages are inserted at their sorted position (section 14). The list says when it's live and announces new messages to screen readers politely.
- Light and dark themes follow the system, with an override saved in the browser.
- The server sets a Content Security Policy that allows only its own origin.
- Templates have a colour, shown as a stripe beside the alias. The bundled icon set (section 14) comes with the template admin screens in M3.

### 7. Tests

- API-level Go tests for A1, A6 (search), A10, every search criterion, admin-only data, ignored aliases, re-parsing and filtered live updates.
- Browser tests (Playwright) on Chromium, Firefox and WebKit (R36) for A1, A3, A4, A10, R31 at 375 px and R35c, plus axe-core (WCAG 2.1 AA) on every view in light and dark at 375 px and 1280 px. `scripts/e2e.sh` runs them against a real API and a fresh PostGIS database, and fails if the API log contains page text (P1).

## Consequences

- Admin-only data can be tested now, and M3 changes where the role comes from but not what each role sees.
- A deployment that edits aliases from a script or the CLI gets the same re-parsing as the admin UI will.
- A viewer whose stream drops sees a short reload of the list instead of a backfill.
