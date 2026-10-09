# 1. Technology stack and profile extraction language

Date: 2026-10-09

## Status

Proposed

## Context

`SPEC.md` (draft of 2026-10-09) fixes the architecture (section 5) and delegates the choice of languages and frameworks to this ADR (section 8). It also leaves **R11, the profile extraction language, `OPEN`** and asks this ADR to propose one and show that it handles the private profile's golden rows.

### Constraints that drive the choice (section 8)

| # | Constraint | What it means for the stack |
|---|---|---|
| C1 | App containers (API, frontend, PostgreSQL) ≤ **1 GB** RAM in steady state; API ≤ **256 MB** with 20 viewers at 1,500 pages/day; idle CPU < 5 % of one core | Favour a runtime with a small, flat memory footprint. Count every extra container (frontend server, tile server) against the budget |
| C2 | Docker Compose; all dependencies inside images | Few containers, no host-side installs |
| C3 | Linux, **amd64 and arm64** images; clients may run natively on a Raspberry Pi (arm64) | Cheap cross-compilation; a client that's easy to install without Docker |
| C4 | PostgreSQL + PostGIS, **no message queue** | Live updates and background jobs come from the API process and the database |
| C5 | Authentik via OIDC, sharing the app's PostgreSQL container | A mature OIDC client library. Authentik's database load counts against C1 |
| C6 | Profiles in JSON, validated by JSON Schema | A JSON Schema validator that supports a current draft |
| C7 | Local geocoding only; self-hosted map tiles | No third-party map or geocoding services at runtime |
| C8 | No paid services; CI on GitHub Actions; images on GHCR; MIT | Only open-source dependencies with permissive licences |
| C9 | WCAG 2.1 AA; works on phones (375 px) and current Chrome, Firefox, Brave and Safari | A frontend framework with good accessibility tooling |
| C10 | P1: no log line, error or diagnostic ever contains page text | Logging must be controllable in every component, including PostgreSQL |

Other requirements that affect the stack: live updates within 2 seconds (R32), ordered profile formats with optional segments (R7, R11), a `validate-profile` command that runs without a database (R10), background re-parsing (R23a) and nightly jobs (R45).

### The R11 problem

Formats run on redacted text with the priority marker removed (R7). Most of a format is easy to describe with regular expressions, because fields are separated by fixed keywords or punctuation. The example profile has `@`, `,`, `GRID`, `UNITS` and `#`.

The hard case is a format where **a free-text description runs straight into an address with nothing marking the boundary**. Illustration (made up):

```
BIN FIRE OLD MILL RD MADEUPTON
SMOKE SEEN 4 OLD MILL RD MADEUPTON
```

A regex has to choose one rule for where the description ends:

- **Shortest description** (lazy) puts `FIRE` into the address in the first line.
- **Longest description** (greedy) puts the house number `4` into the description in the second line.

No choice of quantifier gets both right, because the boundary depends on **which words form a street name**. That is vocabulary, not syntax. The private profile has exactly this shape (summarised below).

### What the golden tables contain (as of commit `a9e719a` of the private repo)

| Table | Rows | What makes it hard |
|---|---|---|
| `profiles/example/golden-example-network.json` | 7 | Priority markers, ignored and heartbeat capcodes, control tokens, three redaction rules with must-not-match cases |
| `profiles/example/golden-example-fire.json` | 4 | Ordered formats filtered by priority, optional segments (`GRID`, `UNITS`), a list field, a text-only format |
| Private network table | 7 | As the example network table. It also has a redaction rule that must remove a keyword followed by digits but leave a near-identical short phrase alone, and a rule that must leave job numbers, capcodes and grid references alone while removing phone numbers |
| Private agency table A | 1 | Description runs into the address with **no delimiter and no house number**. The address includes the locality and isn't split from it. Event type and a response code are joined in one token |
| Private agency table B | 2 | Same unmarked boundary, here with a house number. Optional segments (cross streets, a dash before the map reference, a delimited safety note, a radio channel). List fields. Trailing control tokens. In one row the description itself contains two address-like phrases, one of them with a house number and an unabbreviated street type, immediately before the real address |

All 21 rows are confirmed by the owner. Raw lines aren't reproduced here (CLAUDE.md, R11).

## Decision

### 1. Summary

| Part | Choice |
|---|---|
| **Profile extraction language (R11)** | **RE2-syntax regular expressions with named groups**, in ordered formats, plus declarative field post-processing: trim, `split` into lists, and **`address_split`, which anchors the description/address boundary on the street list from the network profile's address dataset** |
| **Server (API)** | **Go**: `net/http` standard library, `pgx` v5, SQL via `sqlc`, SQL migrations with `goose`, `coreos/go-oidc` + `x/oauth2`, `santhosh-tekuri/jsonschema` v6 (JSON Schema 2020-12), `log/slog` |
| **Live updates** | **Server-Sent Events** from an in-process hub in the API, filtered per connection on the server |
| **Frontend** | **Svelte 5 + SvelteKit (`adapter-static`, SPA mode) + TypeScript**, built to static files and **served by the API binary** (no separate frontend container) |
| **Map** | **MapLibre GL JS** with a self-hosted **PMTiles** vector extract, served by the API using HTTP range requests (no tile server) |
| **Database** | **PostgreSQL 18 + PostGIS 3.6** in a **project-built multi-arch image** (official `postgres` image + PGDG PostGIS packages), extensions `postgis` and `pg_trgm` |
| **Client** | **Go**: a single static binary that runs `rtl_fm` and `multimon-ng`, or reads stdin. Container based on `debian:trixie-slim` with the distro's `rtl-sdr` and `multimon-ng` packages. On a Pi: the same binary as a systemd service |
| **Tests** | Go `testing` (unit, golden tables, integration against a PostgreSQL service container), Playwright (end-to-end, A3/A4 timing, axe-core accessibility), Mailpit (section 11) |
| **CI/images** | GitHub Actions; Go cross-compiles both architectures without emulation; images built with `docker buildx`, tested on native `ubuntu-24.04-arm` runners; distroless `static` base for the API |

Compose services: `api`, `db`, `authentik-server`, `authentik-worker`. Only `api` and `db` count toward the budget, but `db` also carries Authentik's database load (C5).

### 2. Extraction language (resolves R11)

#### 2.1 Proposal

An agency profile format is:

```json
{
  "name": "emergency",
  "priorities": ["Emergency"],
  "match": "^(?P<job_number>EX\\d{7}) (?P<event_type>[A-Z]+\\d) (?P<description>.+?) @ (?P<address>.+?), (?P<locality>[A-Z][A-Z ]*?)(?: GRID (?P<grid_ref>\\d{6}))?(?: UNITS (?P<units>[^#]+?))? #(?P<group>\\S+)$",
  "fields": { "units": { "split": " " } }
}
```

- **`match`** is one regular expression in **RE2 syntax** (as implemented by Go's `regexp`): named groups, alternation, optional groups, lazy and greedy quantifiers. No backreferences and no lookaround. Each named group becomes the field with that name: a standard field (R8) or an extra field. The first format, in order, whose priority filter and `match` both succeed parses the page (R3). An empty `match` matches everything, which makes a text-only format (R7).
- **Groups whose names start with `_`** are intermediate. They feed post-processing and are never stored.
- **`fields.<name>.split`**: a regex that turns the captured text into a list. Elements are trimmed and empty ones dropped.
- **`fields.<name>.address_split`**: takes the captured span (description + optional premises + address) and writes two named fields. The **address** is the **longest suffix** of the span that reads `[house number] <street> <locality>`, where `<street> <locality>` is a street that the address dataset lists in that locality. Street types are normalised using an abbreviation table in the network profile (e.g. `STREET` = `ST`). Everything before that suffix is the **description**. If no suffix matches, an optional per-field **`fallback`** regex (with exactly two groups: description, address) is tried. Without one, the whole span becomes the description and the address is `null`.
- Every value is a literal substring of the page, trimmed, with empty values becoming `null` (P3, section 14). Redaction rules, job number patterns and must-not-match checks use the same dialect.
- A pattern that doesn't compile stops the server at start-up with the file and JSON path (R1, A14). `validate-profile` does the same.

The street list comes from the address dataset the network profile already names (R5) for geocoding (R24), so it adds no new data source. `validate-profile` reads it from a file, so it still runs without a database (R10). The API loads it into memory at start-up.

#### 2.2 Evidence

A throwaway prototype (Go 1.27.2, outside this repo, not committed) implements the pipeline from raw `multimon-ng` line to fields. It parses the line, drops ignored capcodes, removes control tokens, applies redaction and its must-not-match start-up check, derives priority and heartbeat, then runs ordered formats with the post-processing above. The prototype's draft profiles were written for the test and aren't proposed as the shipped profiles. For the private profile, a fixture street list holds only the made-up streets used in its golden rows, plus decoys (the same street name in other localities, and another made-up street that appears inside a description).

Each table was paired with its network profile, and every row checked every listed key. In agency tables, any produced field not listed in the row failed the test.

```
$ sh run.sh
go version go1.27.2 windows/amd64
example-network: 7 passed, 0 failed
example-fire: 4 passed, 0 failed
<private network table>: 7 passed, 0 failed
<private agency table A>: 1 passed, 0 failed
<private agency table B>: 2 passed, 0 failed
exit=0
```

(Per-row lines omitted here because they name private rows. All 21 rows printed `PASS`.)

Negative controls:

| Run | Result | Shows |
|---|---|---|
| Regex only, **lazy** description | Table A row fails: one description word lands in the address | Shortest-description regex isn't enough |
| Regex only, **greedy** description | Both table B rows fail: the house number lands in the description | Longest-description regex isn't enough |
| `address_split` with an empty street list and no `fallback` | All three hard rows: address `null`, whole span in the description | Graceful degradation: the format still matches, nothing is hidden (P2) |

So a lazy regex gets 20 of the 21 rows right and a greedy one 19, but no single regex rule gets all three unmarked-boundary rows right. The street-anchored split does. In table B, the longest-suffix rule also correctly ignores the address-like phrases inside the description: they're followed by more words, so they can't be a suffix.

#### 2.3 Alternatives considered

| Option | Verdict |
|---|---|
| **A. RE2 regex + street-anchored `address_split`** (chosen) | Passes all 21 rows. Regex is familiar to profile authors. RE2 runs in linear time, so a badly written profile pattern can't hang ingest. The only extension is one declarative field option |
| B. Regex only (RE2 or PCRE) | Fails the unmarked boundary (shown above). Lookaround (PCRE) doesn't help, because the missing information is vocabulary, not context |
| C. PEG grammar or parser-combinator DSL | Same ambiguity: a grammar can't know `FIRE` isn't part of a street name without a lexicon. Harder for profile authors to write and review |
| D. Template DSL (`<job> <type> <description> @ <address>`) | Readable for simple formats, but would need its own rules for optional segments and lists, and still can't resolve the boundary. It could later compile down to option A |
| E. Regex + a profile-supplied list of known descriptions | Descriptions are open-ended free text, so the list would never be complete. Street names already exist in the address dataset |

#### 2.4 Limits of the proposal

- **Premises names.** An optional premises name between the description and the address has no lexical anchor here. It stays in `description` (still fully visible, P2), and a `premises` extra field stays `null` unless the format has a delimiter for it. None of the current golden rows has one. A later extension could anchor premises on building names, if the address dataset provides them.
- **Streets missing from the dataset** (new estates, misspellings) fall back as described in 2.1. Such a page may have no geocoded address, but a grid reference can still place it (R25).
- **Golden tables need a street fixture.** A real address dataset won't contain the made-up streets in golden rows. `validate-profile` will take `--address-dataset <fixture>` to override the network profile's dataset. The example profile's own small address dataset (R2) will include its golden streets. For the private repo, see "Questions for the owner".

### 3. Server: Go

| Criterion | **Go** (chosen) | TypeScript on Node.js (Fastify) | Python (FastAPI + asyncpg) |
|---|---|---|---|
| Typical idle RSS for an API process | ~10–40 MB | ~50–90 MB | ~60–120 MB |
| 20 SSE connections | Goroutines, ~KB each | Event loop, cheap | Async, cheap, but higher base memory |
| amd64/arm64 builds | `GOARCH=arm64` cross-compile, static binary, no emulation | Multi-arch base images; native modules may need emulation | Multi-arch base images; wheels usually available |
| Image size | ~15–25 MB (distroless static) | ~150+ MB | ~150+ MB |
| Profile regex dialect | RE2 in the standard library, linear time | JS regex (backtracking); RE2 needs a native addon | `re` (backtracking); RE2 needs `google-re2` |
| OIDC / PostgreSQL / JSON Schema | go-oidc, pgx, jsonschema v6: mature | openid-client, pg, ajv: mature (ajv is the best of the three) | authlib, asyncpg, jsonschema: mature |
| Same language as the client | Yes (shared decoder adapter, types and test fixtures) | Possible, but a Node runtime on a Pi is heavy | Possible |
| Same language as the frontend | No | Yes | No |

The memory and image-size figures are typical published and experienced values, not measured for this project. A15 measures the chosen one. Go has the most headroom against C1, gives the client and server one language and one toolchain, and makes C3 trivial. The cost is a second language for the frontend (TypeScript), which every option except Node also has. C# (.NET) was also considered. Native AOT narrows the memory gap, but its library support and arm64 cross-compilation are less simple than Go's.

Notable server design points that follow from the choice:

- **Live updates (R32)**: SSE over `net/http`. The hub keeps each connection's filter and sends only matching messages (A4). Each event has an ID. If a connection's buffer fills, the server closes it, and the browser's `EventSource` reconnects with `Last-Event-ID` and backfills, so a slow viewer never silently misses a page. The reverse-proxy example sets `proxy_buffering off` for the stream path.
- **Background work without a queue (C4)**: goroutines driven by a jobs table and a PostgreSQL advisory lock, for re-parsing (R23a), the nightly retention and retroactive redaction run (R45, P1), and liveness (R39). If the API ever runs as several replicas, PostgreSQL `LISTEN/NOTIFY` can fan out live events. v1 runs one replica.
- **Search (R33)**: `pg_trgm` GIN indexes for case-insensitive substring search (section 14).
- **Geocoding (R24, R25)**: address tables loaded from the dataset into PostgreSQL. PostGIS `ST_Transform` handles UTM to WGS84. MGRS square completion is a small, unit-tested function.
- **`validate-profile` (R10)**: a subcommand of the same binary, also runnable via the published image (`docker run … validate-profile`), so a private profile repo can test itself without a Go toolchain.

### 4. Frontend: Svelte 5 + SvelteKit (static)

| Criterion | **Svelte 5 + SvelteKit static** (chosen) | React + Vite (+ React Router) | Server-rendered Go templates + htmx |
|---|---|---|---|
| Runtime shipped to phones | Small (compiled, no virtual DOM) | Larger runtime | Smallest |
| Live filtered list, infinite scroll, map, detail views | Natural (reactive stores) | Natural | Possible, but client-side state (filters + live stream + map) gets awkward |
| Accessibility tooling | Compile-time a11y warnings + axe-core | eslint-plugin-jsx-a11y + axe-core | axe-core |
| Extra container | None: static files embedded in the API binary | None (same approach) | None |
| Ecosystem size | Medium | Largest | Small |

Svelte gives the interactivity the UI needs (R31–R35c) with less code and a smaller bundle than React. The htmx approach is lighter still, but fights the combination of live updates, filters and a map. The built files are embedded in the API binary (`embed`), which saves a container and keeps the frontend on the same origin as the API (no CORS, and the session cookie just works). The spec allows separate containers but doesn't require them.

**Map (R35)**: MapLibre GL JS reads a PMTiles vector extract (with self-hosted glyphs and sprites). The API serves it with `http.ServeContent`, which handles range requests. There's no tile server process, so the map costs nothing against C1 beyond the OS page cache. Leaflet with raster tiles was the alternative. It needs either a tile-rendering server (heavy) or a large pre-rendered tile pyramid. The deployment supplies its own extract, and the docs explain how to make one.

### 5. Database image

The official `postgis/postgis` image publishes **amd64 only** (its README lists only amd64 as a supported architecture, checked 2026-10-09). The project therefore builds `ghcr.io/rikiku/pagerarchive-db` from the multi-arch official `postgres:18` image plus the PGDG PostGIS 3.6 package, the same recipe the upstream image uses. Settings sized for C1: `shared_buffers=128MB`, small `work_mem`, a `max_connections` cap, and Authentik's connection pool limited. For P1 (C10): `log_statement=none`, `log_min_error_statement=panic` and `log_error_verbosity=terse`, so a failed statement's text and its `DETAIL` line (which can echo values) are never logged.

### 6. Client: Go

| Criterion | **Go** (chosen) | Python | Rust |
|---|---|---|---|
| Native Pi install | Copy one static binary + systemd unit | Interpreter + virtualenv + dependencies | One static binary |
| Memory | ~10 MB | ~30–50 MB | ~5 MB |
| Shares code with the server | Yes (decoder adapter, record types, fixtures) | No | No |
| Build/CI cost | Fast cross-compile | None | Slow builds, cross-compilation toolchains needed |

The client runs `rtl_fm | multimon-ng` as child processes when configured with radio settings, otherwise it reads stdin (R12, R13). It keeps its buffer in memory only (R15) and never writes page text to a file (R13a).

## Consequences

### Positive

- The whole server side is one static Go binary in one small image (API, frontend, tiles, `validate-profile`). Only `api` and `db` count toward the budget.
- One backend language for client and server: shared decoder adapter, record types and golden fixtures.
- Profile authors write ordinary regular expressions. RE2's linear-time guarantee means a profile from a third party can't cause catastrophic backtracking.
- R11 is resolved with evidence against every confirmed golden row.

### Negative / risks

- **Two languages** (Go and TypeScript) for contributors.
- **Parsing now depends on the address dataset.** Changing the dataset can change `description`/`address` for pages that use `address_split`. The dataset file's hash joins the profile hashes that trigger re-parsing (section 14, R23a).
- **Golden tests that use `address_split` need a street fixture** next to the golden table.
- **Our own database image** to maintain and rebuild for PostgreSQL and PostGIS security releases (Dependabot + scheduled CI build).
- **Authentik shares the `db` container.** Since 2025.10 it keeps tasks, cache and sessions in PostgreSQL and opens noticeably more connections. Each PostgreSQL backend costs memory inside our 1 GB. This is the largest budget risk and is measured explicitly (below).
- RE2 has no lookaround. All 21 current rows were expressed without it. A future format that truly needs it would need a new field option, not a different engine.
- MapLibre is the heaviest frontend dependency, so it's loaded only on the map view.

### How each choice will be verified

| Choice | Verification | When |
|---|---|---|
| Extraction language | Golden-table tests in CI for `profiles/example/`. `validate-profile` run against the private repo on the build machine, with output as evidence. A unit test for `address_split` with the decoy cases above. A test that a non-compiling pattern stops start-up naming file and path (A14) | M1 |
| Go API within 256 MB and the stack within 1 GB | The **A15 harness**: full compose stack including Authentik, 20 SSE connections from a small load tool, pages generated from the example formats at 1,500/day. `docker stats` sampled every 10 s for 1 hour into a CSV (its memory figure is the cgroup's usage minus inactive file cache). Pass: the API's peak after a 10-minute warm-up is ≤ 256 MiB, and the sum of `api` + `db` peaks is ≤ 1 GiB. A shortened run (5 minutes, faster replay) in CI. The full run is done once by hand, with the CSV and summary as evidence | M4 (short run from M2) |
| Idle CPU < 5 % | Same harness with no pages and no viewers for 10 minutes: mean CPU of `api` + `db` from `docker stats` | M4 |
| Street index memory | Measured in A15 with a real-size dataset loaded. If it exceeds 50 MB, the index moves into a PostgreSQL table (same lookup interface, one indexed query per candidate suffix) | M4 |
| Authentik's load on `db` | A15 also records `db` memory and `pg_stat_activity` connection counts by database. Authentik's pool settings are tuned until `db` fits | M3–M4 |
| amd64 + arm64 | CI builds every image for both architectures and runs smoke tests on native amd64 and arm64 runners: API `/healthz`, `SELECT postgis_full_version()` on the DB image, client `--version` and a stdin replay | M1 |
| Live updates within 2 s | Playwright A3/A4 with two browser contexts, timing from the client's POST to the row appearing | M2 |
| WCAG 2.1 AA | axe-core in Playwright on every view, in light and dark mode, at 375 px and desktop widths, plus the manual checklist (section 14) | M2 onward |
| P1: no page text in logs | An end-to-end test captures all container logs during the acceptance suite and fails if any fixture text appears (`docker compose logs` searched for each fixture string). The PostgreSQL logging settings above are part of the image | M1 onward |
| Map without a tile server | A test that the API answers a `Range` request on the tile file with `206 Partial Content`, and the map view renders the example dataset's points over a tiny test extract | M4 |

Indicative numbers from a throwaway spike (Go 1.27.2 on Windows, not a container, so only a sanity check): a 250,000-entry street/locality index used about 25 MB of heap, and 20 connected SSE clients added under 2 MB. Process memory from the OS was about 40 MB in total.

### Questions for the owner (raised by this ADR)

1. **Street fixture in the private repo.** Its golden rows that use `address_split` need a small fixture listing the made-up streets they use. May the implementer propose that file there? (Golden rows themselves are untouched.)
2. **Network golden tables: unlisted keys.** The prototype treats a key that a network-table row doesn't list (e.g. `heartbeat`) as not checked, because one private heartbeat row doesn't list `heartbeat: true`. Should unlisted keys instead mean `null`/`false`, as they do in agency tables?
3. **Pages whose address can't be split** (no street match, no fallback): the address is `null`, and the format still counts as matched, so no `parse_failed` flag. Is that acceptable, or should admins see a flag?
