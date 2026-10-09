# PagerArchive: Specification

**Status:** Draft 0.2 (2026-10-09), not yet approved for implementation
**Owner:** Kyle

## How to use this spec (implementing agent: read first)

1. Read this whole document and `CLAUDE.md` before doing anything.
2. **Principles (section 1) override every other section.** Where two principles conflict, the lower-numbered one wins.
3. **`OPEN` means undecided.** Don't guess. Stop and ask the owner. Work that doesn't depend on an OPEN item can continue.
4. **Before writing any code**, write `docs/adr/0001-technology-stack.md` recommending the stack against the constraints in section 8, and wait for the owner to approve it.
5. Golden tables (`profiles/**/golden-*.json`, in this repo and in any private profile repo) are **owned by the owner**. You may propose new rows, but never change an expected value to make a test pass.
6. "Done" means every acceptance check in section 9 passes, plus the end-to-end verification in section 11, with evidence (commands run and their output).
7. **Implementer discretion.** Where this spec is silent, choose the simplest option consistent with the principles, constraints and requirements, and record it in an ADR (`docs/adr/`). Ask the owner only when the choice changes what users see, what data is kept, privacy or security. **UI presentation details** (date and time formats, layouts, icon sets, the design of admin screens) are the implementer's choice within the requirements. The defaults in section 14 apply.
8. **ADR approval:** an ADR is approved when its status reads `Status: Accepted`, set by the owner or by Claude at the owner's explicit instruction, and committed.
9. **Repos:** this repo is `github.com/RIKIKU/PagerArchive`. The owner's private profile repo is checked out next to it at `../PagerArchive-profiles` on the build machine.
10. **Golden row status:** rows marked `"status": "proposed"` run in CI and are reported, but don't fail the build until the owner changes them to `"confirmed"`.

---

## 1. Principles

- **P1. Sensitive personal details are never stored.** Details matched by the network profile's redaction rules (for example patient age and gender, phone numbers, access codes) are removed before anything is stored, logged or shown. Retroactive redaction also covers derived data (dedup hashes, parsed fields, job links, search indexes), followed by routine database vacuuming. Database write-ahead logs and backups are out of scope. **No server-side log line, and no client error or diagnostic output, ever contains page text** (application, web server, reverse proxy, database or error logs). When a redaction rule is added or changed, it's applied to already-stored messages at the next nightly run. The one exception is the client's optional console output (R13a), which is never written to a file by the client.
- **P2. The full message is always shown.** Every page is stored and displayed in full, after the removals in P1. Parsed fields are extra metadata for search, filters, grouping, the map and reports. They never replace or hide the text. A parse failure, a missing alias or a wrong template can make a page harder to find, but never harder to read.
- **P3. The parser extracts; the display interprets.** Parsed fields hold the literal text from the page. Turning codes into labels is a separate display step driven by profiles.

## 2. Problem and users

Many emergency and organisational paging networks broadcast **POCSAG** pages that can be received with an inexpensive RTL-SDR dongle and decoded with `multimon-ng`. People who follow a network want to archive those pages, browse them live, search them, see them on a map, and look up what a given pager group has received.

PagerArchive is a self-hosted, resource-light archive for any POCSAG network. Network-specific knowledge (page formats, which capcodes to ignore, what must be redacted, how event types group into categories) lives in **configuration profiles**, not in code.

PagerArchive is for **people interested in what's happening on their network. It is not a tool for harassment.**

## 3. Glossary

| Term | Meaning |
|---|---|
| Page / message | One POCSAG transmission to one capcode |
| Capcode | The pager address a page was sent to, stored zero-padded to 7 digits (`0579064`) |
| Client / receiver | A machine with an RTL-SDR dongle that decodes pages and posts them to the server |
| Priority marker | The first 2 characters of a page's text, which some networks use to signal its priority (the receiving pager typically doesn't display them). Defined by the network profile |
| Priority | Emergency, Non-emergency, Admin, or `null`, derived from the priority marker |
| Network profile | One JSON file per deployment: rules that apply to every page on the network (section 6.3) |
| Agency profile | A JSON file describing one agency's page formats (section 6.4). Attached to alias templates |
| Alias | A human-readable name for a capcode or capcode pattern, linked to a template |
| Template | Shared alias settings: agency name, icon, colour, and optionally an agency profile |
| Job number | An identifier, extracted by a profile, that links pages about the same incident |
| Group | The pager group a copy was sent to, when the page names it (often a short code at the end) |
| Heartbeat | A periodic page the network sends to show it's alive. Its capcodes are listed in the network profile |
| Golden table | A JSON file of real-format raw lines and the exact fields they must parse to |

## 4. Scope

### In scope for v1
Receiving clients, ingest and redaction, profile-driven parsing, job grouping, map locations, aliases and templates, the browser UI with live updates and multi-criteria search, a per-group report, client liveness, invite-only accounts and retention.

### Out of scope
- **Incident state or resource tracking.** The app never infers or shows whether an incident is active, cancelled or finished, or where resources are. Pages carry too little information for that. Follow-up pages (cancellations, updates) are stored and grouped like any other page and never interpreted.
- **A native mobile app.** The browser UI must work well on phones instead.
- **Automatic client updates** (wanted later). v1 publishes multi-arch images and documents manual updates.
- **Writing an identity provider.**
- **Linking several email addresses to one person.** Accepted risk: one person may hold more than one account. Invite attribution and invite-tree deactivation (section 7.9) are the remedy.
- **Public geocoding services** (section 8).
- **Code plugins.** Network knowledge is configuration (JSON profiles), not code.
- **Building an alerting or notification system.** Integration with an existing one may come later.
- **Anti-harassment features beyond invite-only access** (for example an audit log of searches) in v1.

## 5. Architecture (decided)

```
RTL-SDR → rtl_fm → multimon-ng → [client] ──HTTPS──▶ [server API] ──▶ PostgreSQL + PostGIS
                                                    ▲        │    ▲
                                       Authentik ───┘        │    └── network + agency profiles (JSON, mounted read-only)
                                                             └── live updates ──▶ browser UI
```

Many clients post to one server. Each client records its own receive time. The server sits behind the deployment's **own reverse proxy** (which handles TLS) and is reachable by remote clients. The docs include a working example configuration for a common nginx-based proxy (e.g. linuxserver/swag), including whatever live updates need. Profiles are files mounted into the server container. They aren't stored in the database, so a deployment can keep them in its own (private) repository.

## 6. Profiles

### 6.1 Format
- **R1.** Profiles are **JSON** files, validated against JSON Schemas shipped in the repo. An invalid profile stops the server from starting, with an error naming the file, path and problem.
- **R2.** The repo ships a complete **example profile** for a made-up network (`profiles/example/`), with golden tables. It's the documentation and the default test fixture. The implementer writes the example network and agency profile JSON, the JSON Schemas and a small example address dataset; they must pass the owner's example golden tables.

### 6.2 How a page finds its rules
- **R3.** capcode → alias (section 7.5) → template → agency profile. The agency profile's **formats are tried in order**. The first format that matches parses the page.
- **R4.** If the capcode has no alias, its template has no agency profile, or no format matches, the page is still stored and shown in full (P2). No format matching → flagged `parse_failed`. No alias or no agency profile → **text only**, not flagged. There is no default agency profile. Network-level rules (priority, redaction, job number patterns) still apply to every page.

### 6.3 Network profile (one per deployment)
- **R5.** Defines:
  - **priority markers**: which 2-character prefixes mean Emergency, Non-emergency and Admin
  - **ignored capcodes**: capcodes or ranges whose pages are dropped and never stored (network housekeeping traffic)
  - **heartbeat capcodes** (kept and shown; used for liveness)
  - **redaction rules**: ordered patterns, each with a name, replaced by `REDACTED` (P1), plus **must-not-match examples** that must survive unchanged
  - **event categories**: a table mapping event-type codes to report categories, with "Other" for unmapped codes
  - **address dataset**: the local open-data file used for geocoding, and how grid references are interpreted (section 7.4)
  - **job number patterns** that apply to every agency (e.g. a prefix letter plus digits)
- **R6.** The network profile has its own golden table covering priority markers, ignored capcodes and every redaction rule, including the must-not-match examples.

### 6.4 Agency profile (attached to templates)
- **R7.** Defines an ordered list of **formats**. Each format has a name, optionally the **priorities it applies to**, a match rule and extraction rules for fields. A format may extract nothing ("text-only format", e.g. freeform admin pages), which counts as a match, not a failure. Formats run on the redacted text with the priority marker removed.
- **R8.** Standard fields that core features rely on: `job_number` (grouping), `event_type` (reports), `description`, `address`, `locality` and `grid_ref` (map), `units` (list), `group`. A format may also define **extra fields** with any name; they're stored, searchable and shown. Fields not present in a page are `null`. Values are literal (P3).
- **R9.** Optional **label tables** map codes to display labels (e.g. an event-type code to a readable name).
- **R10.** Each agency profile ships a **golden table**: raw lines and their exact expected fields, one row per format variant. Every row must pass in CI. The repo ships a `validate-profile` command that runs any profile's golden tables, so private profile repos can test themselves. `validate-profile` runs without a database and pairs agency tables with a named network profile.
- **R11.** `OPEN`: the extraction language. The ADR proposes one that can express ordered alternatives and optional segments. Regular expressions with named groups are the obvious candidate, but the hardest real format has unmarked boundaries between a description, an optional premises name and an address, so the ADR must show it can handle the private profile's golden rows. Those can't be copied into this repo: build sessions run on the owner's machine with the private profile repo checked out alongside (path given by the owner), and the ADR may describe its rows only in general terms.

## 7. Requirements

### 7.1 Clients
- **R12.** The client reads `multimon-ng` output lines on stdin, e.g. `POCSAG512: Address:  579064  Function: 0  Alpha:   <text>`, and posts each page to the server with: capcode (zero-padded to 7 digits), raw text, receive time (UTC) and client ID. POCSAG at 512, 1200 and 2400 baud. Numeric pages are stored as text. Tone-only pages (no text) are ignored. The client turns decoder output into an internal record (capcode, function code, baud rate, text, receive time) through a **decoder adapter**; v1 ships only the `multimon-ng` adapter, so other decoders can be added later without redesign.
- **R13.** Clients run as a Docker container with the USB dongle mapped through, or natively on a Raspberry Pi (arm64). The client container **runs the radio pipeline itself** (`rtl_fm` and `multimon-ng` in v1, configured by settings: frequency, gain, PPM, baud rates); reading lines from stdin stays supported. Clients must keep their clocks in sync with NTP.
- **R13a.** The client may print decoded page text to its console (stdout) for troubleshooting. This is **off by default** (setting `PRINT_PAGES=true`). The client never writes page text to a file. The docs warn that Docker's default logging driver stores container output on disk, and show how to limit or disable that.
- **R14.** `OPEN`: how clients authenticate to the server (suggestion: one API key per client, issued and revoked by an admin).
- **R15.** When the server is unreachable, the client buffers pages **in memory only** (never on disk, because client-side text isn't redacted) and retries with backoff, keeping the original receive times. The buffer is capped (implementer's choice), and its contents are lost if the client restarts.

### 7.2 Ingest (in this order, before storing)
- **R16.** Pages to the network profile's **ignored capcodes** are dropped. They still update the receiving client's last-seen time (no text is recorded).
- **R17.** Control tokens printed by `multimon-ng` (e.g. `<NUL>`) are removed.
- **R18.** The network profile's **redaction rules** are applied (P1).
- **R19.** **Deduplication:** the key is a hash of capcode + cleaned text. A page whose key was first stored no more than **10 seconds** earlier (by receive time, measured from the first copy) is the same transmission, whether it comes from another client or the same one (networks often repeat pages deliberately). It isn't stored again, but the extra client's receive time is recorded.
- **R20.** Every capcode's copy is stored as its own message, even when the text is otherwise identical.
- **R21.** A message's **sent time** is the earliest receive time among the clients that heard it.
- **R22.** The **raw text** (after R17–R18) is stored, including the priority marker. Priority comes from the marker (R5).

### 7.3 Job grouping
- **R23.** Messages with the same job number are grouped into one job, on a best-effort basis (one incident may have several job numbers; that's acceptable). **Any page containing a job number joins that job**, including follow-ups and admin pages that mention it. Pages without a job number are never grouped. If a format extracts a job number, that one is used; otherwise the first match of the network job pattern is written to `job_number`. Job numbers are grouped for all time (no time window).
- **R23a.** When an alias, template or profile changes, affected messages are **re-parsed (and re-geocoded) in the background**. Parsed fields are stored. Aliases are always shown as they are *now*, not as they were when a page arrived.

### 7.4 Location
- **R24.** The address (with its locality) is geocoded **locally** against the address dataset named in the network profile. If a profile doesn't extract `locality` separately, the geocoder matches the end of the address against the dataset's localities. The dataset's attribution is shown.
- **R25.** If the address can't be geocoded and the page has a grid reference, the grid reference is resolved as the network profile describes. For the case that motivated this: a 6-digit MGRS easting/northing without zone or 100 km square, completed from the zone and square containing the page's locality, giving a point accurate to 100 m.
- **R26.** If both exist and are more than `OPEN` (suggestion: 1 km) apart, the message is flagged `location_mismatch`.

### 7.5 Aliases and templates
- **R27.** An alias maps a capcode or capcode pattern to a name and a template. Patterns use `%` (any run of characters) and `_` (one character). Precedence: an exact capcode wins, then fewer wildcards; patterns starting with a wildcard come last. `OPEN`: should an alias be able to mark a capcode as ignored, and does that hide it or stop it being stored?
- **R28.** A **template** holds agency name, icon, colour and optionally an agency profile. Aliases are **linked** to their template: editing the template updates every alias that uses it.
- **R29.** Templates enable parsing but never gate storage or display (P2).
- **R30.** `OPEN`: importing aliases from a CSV file in v1?

### 7.6 Browser UI
- **R31.** The message list shows, for every message: the text **without the priority marker** (as a pager displays it), a priority badge, date and time, alias (or capcode), and group. On phone-width screens (375 px), date, time and alias stay visible with no horizontal scrolling.
- **R32.** **Live updates:** a new message appears in every open browser within **2 seconds** of a client posting it, with no refresh. Any mechanism is fine. A message that doesn't match a viewer's current filter isn't added to their list.
- **R33.** **Search** combines any of: text, capcode, alias, agency, group, priority, event category, job number, date range, and **"capcodes with no alias"**. The last one also lists each unknown capcode with its message count and last-seen time, so it can be identified and aliased. `OPEN`: confirm the list.
- **R34.** A **job view** shows all messages for a job number.
- **R35.** A **map** shows located messages, using **self-hosted map tiles** (e.g. an OpenStreetMap extract), so no third party sees what users view.
- **R35a.** **Times** are stored in UTC and shown in the deployment's time zone (setting `TZ`, e.g. `Australia/Melbourne`), with en-AU style dates. English only. Report days and the retention cut-off use the same time zone. (Showing times in each browser's own time zone may come later.)
- **R35b.** Heartbeat pages are shown with a **heartbeat badge** and are excluded from reports. They appear in the default message list.
- **R35c.** A **message detail view** shows all parsed and extra fields and every client's receive time. The `parse_failed` and `location_mismatch` flags are shown **to admins only**.
- **R36.** Supports current Chrome, Firefox, Brave and Safari on desktop and mobile; dark and light mode; layout adapts to screen orientation.
- **R37.** `OPEN`: **per-user integrations** (e.g. forwarding selected pages to a user's own notification service). In v1 or later?

### 7.7 Reports
- **R38.** A report shows, for a **capcode** and date range, the number of Emergency **messages** per **event category** (network profile, R5). Unmapped codes and parse failures are counted under "Other" (with the raw code shown where there is one).

### 7.8 Client liveness
- **R39.** A client is flagged **not receiving** after **30 minutes with no message of any kind**, heartbeats included. (Networks may only send heartbeats at certain hours, so missing heartbeats alone must not raise an alarm.) The flag is **shown on screen only**. Notifications are out of scope for v1. Client status is visible **to admins only** (client names may reveal where receivers are).

### 7.9 Accounts and access
- **R40.** Sign-in is handled by **Authentik** (self-hosted, in the same compose file) via **OIDC**. The app never handles passwords. Authentik runs on **its own subdomain**, shares the app's PostgreSQL container (its own database), and is configured automatically from **blueprints** shipped in the repo.
- **R41.** New users can only enrol with an **invitation** from an existing user **and** by verifying a code sent to an email address in one of the **allowed domains** (deployment setting `ALLOWED_EMAIL_DOMAINS`).
- **R42.** The app records **who invited whom**. Admins can deactivate any user, or a user **and everyone in their invite tree** (recursively).
- **R43.** A deactivated user can't be re-invited except by an admin.
- **R43a.** Invitations are created **in the app** (which records who invited whom, and creates them through Authentik's API) and are **single-use**, expiring after **7 days**. The inviter enters the invitee's email address, which must be in an allowed domain (admins included). The emailed link only works for that address.
- **R43b.** Deactivation takes effect **immediately**: the user is disabled in Authentik and their live sessions and connections are closed. Admins can reactivate users one at a time (reactivating doesn't reactivate their invite tree). The re-invite block (R43) goes by email address.
- **R43c.** Users can delete their own account. People they invited keep their accounts, credited to "deleted user".
- **R44.** `OPEN`: roles (suggestion: `user` and `admin`, where admin manages aliases, templates, users, clients and retention), and how the first admin is created.

### 7.10 Retention
- **R45.** Messages are kept for a configurable period, set **in days** in the admin UI (default **3,650 days**, minimum 30), then deleted completely (text, metadata and everything linked to them) by a nightly job. **Backups are out of scope.** The docs must say that deleted messages may survive in a deployment's own backups. The cut-off is a **calendar date** in the deployment time zone. The nightly job runs at **03:00** (configurable).
- **R46.** Shortening the period requires confirming the cut-off date ("All messages older than 9 October 2024 will be deleted at the next nightly run. Confirm?"). No row count is shown. Deletion happens at the next nightly run.

## 8. Constraints

| Constraint | Because |
|---|---|
| Docker Compose on a home Linux server; API and frontend may be separate containers | Simple installs and upgrades for self-hosters |
| All dependencies inside images | Install and maintenance stay easy |
| Linux only. **Server** images (API, frontend, database) for **amd64 only**; **client** images for **amd64 and arm64** | Servers are x86 PCs, so official amd64 images (e.g. PostGIS) can be used; receivers may be Raspberry Pis |
| **Resource budget:** the app's containers (API, frontend, PostgreSQL; Authentik excluded) ≤ **1 GB RAM** total in steady state; API ≤ **256 MB** with 20 viewers at 1,500 pages/day; idle CPU < 5% of one core | Target host: an old small-form-factor PC (e.g. 15 GB RAM, spinning HDD) shared with other self-hosted apps |
| **PostgreSQL + PostGIS**; **no separate message queue** | Best fit for map data and search; ~1 page/minute doesn't justify a queue |
| **Authentik** for identity | Invitations, email codes and domain rules out of the box |
| Profiles in **JSON**, not YAML | Not whitespace-sensitive; easy to auto-format and validate |
| Geocoding only against a **local** address dataset | Public geocoders are rate-limited, often restrict automated use, and would receive incident addresses |
| **No paid services** | Hobby-scale self-hosting |
| Language and frameworks: chosen in an ADR (see "How to use") against these constraints | Delegated by the owner |
| CI on **GitHub Actions**; multi-arch images published to **GHCR** | Free for public repos |
| Accessibility: **WCAG 2.1 AA** | Usable by everyone, including on phones in poor light |
| Runs behind an **external reverse proxy**; the compose file doesn't include one | Self-hosters already run one (the owner uses linuxserver/swag) |
| Licence: **MIT** | Open source |
| The repo names **no real agency, network or other project**; test data uses made-up formats and addresses | Generic tool; privacy (P1) |

## 9. Acceptance checks

All checks use the **example profile** and the **standard test fixture** (section 10) unless stated. "An empty database" means no messages; the fixture's aliases and template are present. Capcodes, addresses and phone numbers are made up.

- **A1. Every group's copy, one job.** **Given** an empty database, **when** a client posts `@@EX0000004 HSF1 SMOKE FROM SHED @ 3 FICTION ST, MADEUPTON #STN<n>` on capcodes `0100001`…`0100004` (with `<n>` = 1…4), **then** 4 messages are stored, each with its own group, **and** the job view shows 1 job with 4 messages.
- **A2. Deduplication across clients.** **Given** an empty database, **when** clients A and B post the same capcode and text 3 seconds apart, **then** 1 message is stored with receive times from both clients, and its sent time is client A's receive time. **When** the same text arrives 11 seconds after the first, **then** a second message is stored.
- **A3. Live update.** **Given** two users viewing the unfiltered message list, **when** a client posts example golden row 1 on capcode `0100001`, **then** within 2 seconds of the post it appears at the top of both lists without a refresh.
- **A4. Live update respects filters.** **Given** user A is filtered to group `STN2` and user B is unfiltered, **when** a client posts a page for group `STN1`, **then** within 2 seconds it appears for B, and after 5 seconds it still hasn't appeared for A.
- **A5. Unparseable page.** **Given** an empty database and capcode `0100001` aliased to a template with the example agency profile, **when** a client posts `@@GARBLED 123`, **then** 1 message is stored with priority Emergency, flagged `parse_failed`, and shown in full.
- **A6. Unknown capcode.** **Given** an empty database, **when** two pages arrive for capcode `0999999`, which has no alias, **then** both are stored and shown as text only (not flagged), **and** the "capcodes with no alias" search lists `0999999` with 2 messages.
- **A7. Redaction and must-not-match.** **Given** an empty database, **when** a client posts `QDCheck the shed Saturday. Text 0412 345 678 if available. EX0000001`, **then** the stored text is `QDCheck the shed Saturday. Text REDACTED if available. EX0000001`, **and** no row in the database contains `0412 345 678` or `0412345678`, **and** the message joins job `EX0000001` (R23).
- **A8. Sensitive details never stored.** **Given** an empty database, **when** `@@EX0000005 MED1 PERSON COLLAPSED AGE: 52 SEX: M @ 1 EXAMPLE ST, MADEUPTON #STN1` is posted, **then** the stored text contains `PERSON COLLAPSED` and `1 EXAMPLE ST, MADEUPTON`, **and** no row contains `AGE: 52` or `SEX: M`.
- **A9. Ignored capcode.** **Given** an empty database, **when** a page is posted on capcode `0000042` (ignored in the example network profile), **then** 0 messages are stored.
- **A10. Marker hidden in display.** **Given** example golden row 1 is stored, **when** it's shown in the message list, **then** the text starts with `EX0000001` (no `@@`), and an Emergency badge is shown.
- **A11. Capcode report.** **Given** capcode `0100001` has Emergency pages with event types `HSF1` on Oct 1 and Oct 3 and `GRS3` on Oct 5, an Admin page on Oct 4, and an `HSF1` page on Sep 28, **when** the report is run for `0100001`, Oct 1–7, **then** it shows Structure fire 2, Grass fire 1, total 3.
- **A12. Retention change.** **Given** 3 messages received 3 years, exactly 2 years and 1 year before the nightly run, **when** an admin shortens retention from 10 years to 2 years, confirms, and the nightly run happens, **then** 1 message is deleted (the 3-year-old one) and 2 are kept.
- **A13. Client liveness.** **Given** client A's last message (of any kind) was received 31 minutes ago and client B's 5 minutes ago, **when** an admin views client status, **then** A is shown as not receiving and B as receiving.
- **A14. Invalid profile.** **Given** an agency profile with a missing required key, **when** the server starts, **then** it exits with an error naming the file and the missing key.
- **A15. Resource budget.** **Given** the full stack running in Docker Compose with 20 connected viewers and fixtures replayed at 1,500 pages/day, **when** memory is measured after 1 hour, **then** the API uses ≤ 256 MB and the app's containers (excluding Authentik) ≤ 1 GB in total.

## 10. Example profile (made-up network)

`profiles/example/` describes an imaginary network. It exists to document profiles and drive tests. It isn't modelled on any real network.

**Network profile:** markers `@@` Emergency, `Hb` Non-emergency, `QD` Admin. Ignored capcodes `0000040`–`0000049`. Heartbeat capcode `0000050`. Redaction: Australian-style mobile numbers (`04XX XXX XXX`, with or without spaces) and `AGE: <n>` / `SEX: <letter>`. Job numbers: `EX` + 7 digits (also the must-not-match example for redaction). Categories: `HSF*` → Structure fire, `GRS*` → Grass fire, `MED*` → Medical, else Other.

**Agency profile "Example Fire":** formats, tried in order:
1. `emergency` (Emergency only): `<job> <type> <description> @ <address>, <locality> [GRID <6 digits>] [UNITS <unit> …] #<group>`
2. `follow-up` (Non-emergency only): `RE <job> <free text> #<group>`
3. `freeform` (Admin and Non-emergency only): anything (text-only format). An Emergency page that matches neither format 1 nor 2 is `parse_failed`.

**Standard test fixture** (loaded before every acceptance check): template **"Example Fire"** (agency "Example Fire Service", agency profile `example-fire`), and aliases `0100001` "Station 1", `0100002` "Station 2", `0100003` "Station 3" and `0100004` "Station 4", all using that template. Capcode `0999999` has no alias.

Golden rows (`profiles/example/golden-example-fire.json`):

| # | Raw (after `Alpha:`) | Expected (non-null fields) |
|---|---|---|
| 1 | `@@EX0000001 HSF1 SMOKE FROM ROOF @ 12 FICTION ST, MADEUPTON GRID 123456 UNITS T1 T2 #STN1` | job_number `EX0000001`, event_type `HSF1`, description `SMOKE FROM ROOF`, address `12 FICTION ST`, locality `MADEUPTON`, grid_ref `123456`, units `[T1, T2]`, group `STN1` |
| 2 | `@@EX0000002 GRS3 GRASS FIRE @ SAMPLE RD, MADEUPTON #STN2` | job_number `EX0000002`, event_type `GRS3`, description `GRASS FIRE`, address `SAMPLE RD`, locality `MADEUPTON`, group `STN2` |
| 3 | `HbRE EX0000001 CREWS RETURNING #STN1` | job_number `EX0000001`, group `STN1` (format `follow-up`) |
| 4 | `QDTraining Tuesday 7pm at the station` | none (format `freeform`) |

## 11. End-to-end verification

On a clean Linux host with Docker:
1. `docker compose up -d` starts the whole stack (including Authentik) from the documented `.env.example`, with the example profile mounted.
2. An admin is created, invites a test user, and the test user enrols with a code sent to an allowed test domain (via a local mail catcher in the test setup).
3. A client container is fed fixture lines through stdin (no radio needed). Within 2 seconds each page appears in the signed-in browser with correct parsed fields, grouping, and a map point for addresses in the example address dataset.
4. All acceptance checks A1–A15 and every golden row (network and agency) pass in CI.
5. Evidence is provided: commands run and their output.

## 12. Delivery plan

Build in milestones. Each one ends with its own acceptance checks passing, plus evidence. Open questions only need answers before the milestone that needs them.

| Milestone | Scope | Checks |
|---|---|---|
| **M0** | Stack ADR approved | n/a |
| **M1** | Client, ingest, redaction, dedup, profiles and schemas, `validate-profile`, golden tests, CI | Golden tables, A2, A5, A6 (storage), A7, A8, A9, A14 |
| **M2** | Browser UI, live updates, search, job and detail views | A1, A3, A4, A6 (search), A10 |
| **M3** | Accounts, invitations, deactivation (Authentik) | §11 step 2 |
| **M4** | Location and map, reports, liveness, retention, resource budget | A11, A12, A13, A15, §11 |

## 13. Open questions

R11 extraction language · R14 client authentication · R26 location-mismatch distance · R27 alias "ignore" · R30 alias import in v1? · R33 search criteria list · R37 per-user integrations in v1? · R44 roles and the first admin.

## 14. Accepted defaults

Settled by the owner after the second cold read (2026-10-09):
- Priority markers are case-sensitive and exactly 2 characters. With no known marker (priority `null`), the full text is shown unchanged.
- An Emergency page on a profiled capcode that matches no format, and any `null`-priority page that matches none, is `parse_failed`.
- Extracted values are trimmed, and empty matches become `null`. Extra fields are strings or lists of strings.
- Each redaction match is replaced by one `REDACTED`. Dedup runs on redacted text. A redaction rule that alters one of its own must-not-match examples stops the server at startup.
- If retroactive redaction removes the text a field came from, the message is re-parsed and may leave its job.
- The POCSAG function code and baud rate are stored but not used. The client accepts `Alpha:` and `Numeric:` lines, drops other lines silently, and trims only multimon's separator.
- Control tokens such as `<NUL>` are removed; `<LF>`/`<CR>` become a space.
- The dedup window is symmetric: a late copy whose receive time is within 10 s of the first copy, either side, is the same transmission, and sent time becomes the earliest.
- The client drops the oldest buffered pages when its buffer is full, and rejects receive times more than 60 s in the future (logged without page text). The client ID comes from configuration (see R14).
- Profiles are read at start-up. A change is detected by comparing file hashes, which then triggers re-parsing (R23a). A template pointing at a missing agency profile makes its pages text-only and is reported to admins.
- Re-parsing runs as soon as a change is detected. Retroactive redaction waits for the nightly run.
- The message list is sorted by sent time, newest first, with infinite scroll. Late buffered pages appear in their sorted position.
- Text search is case-insensitive substring search across the text and extra fields.
- The map shows the last 24 hours by default, follows the active filters, and has one point per located message. A tile server, if one is used, counts toward the resource budget.
- No house number: the street's centroid is used and marked approximate. Grid references resolve to the centre of the 100 m cell. Address matching normalises common abbreviations (ST/STREET) but isn't fuzzy.
- The example address dataset places MADEUPTON at a neutral location. No real map tiles are needed for tests.
- A template still used by aliases can't be deleted. Icons come from a bundled icon set.
- Unmapped report codes are listed one row per raw code inside "Other". Report date ranges are inclusive, in the deployment time zone. Reports are per capcode in v1.
- Jobs left with no messages, per-client receive records and geocode results are deleted along with their messages.
- Only client images are multi-arch; server images are amd64 only (owner correction, 2026-10-09). WCAG 2.1 AA is checked with automated accessibility tests plus a manual checklist.
- A12 and A13 use a controllable clock and a test-only trigger for the nightly job. The §11 "clean host" is a GitHub Actions Ubuntu runner, with a local mail catcher (e.g. Mailpit) and `example.test` as the allowed domain.
- For A15, "20 viewers" means 20 live-update connections, and the page stream is generated from the example formats. The test runs once by hand with evidence, plus a short version in CI.
