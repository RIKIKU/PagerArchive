# 5. Milestone M4: location and map, reports, liveness, retention, resource budget

Date: 2026-10-13

## Status

Proposed (awaiting the owner)

## Context

Milestone M4 covers location and the map (R24–R26, R35), the capcode report (R38), client liveness (R39), retention and the nightly job (R45, R46, and P1's retroactive redaction), and the resource budget (section 8). Its checks are A11, A12, A13, A15 and section 11. Publishing images to GHCR moves here from M3 (ADR 0002, ADR 0004).

The owner set R26's distance before the build. The implementer then raised the remaining gaps, sorted as already decided (cited), needing the owner, or the implementer's choice (SPEC.md "How to use", item 7).

## Owner decisions (2026-10-13)

1. **R26: 1 km.** The map uses the address first and the grid reference second (R24, R25). `location_mismatch` stays admin-only (R35c).
2. **R26 for an address with no house number** (placed at its street's centroid): the distance is from the grid reference's point to the **nearest address point on that street in that locality**, not to the centroid, so a long road isn't flagged just for being long. Recorded in R26.
3. **GHCR tags.** Images are published only from a build whose checks all passed. `main` gives `latest`, `main` and `sha-<commit>`; a `vX.Y.Z` tag gives `X.Y.Z` and `X.Y`; a manual run on any branch gives `sha-<commit>` only. Other pushes build without publishing. (`latest` may move to releases only at v1.0.)

## Already decided elsewhere (cited, not re-decided)

- An address that is only a road and a locality is placed at the street's centroid and marked approximate (section 14). It is still the address, so it wins over a grid reference (R24, owner decision 1).
- A profile that doesn't extract the locality separately: the end of the address is matched against the dataset's localities (R24).
- The map shows the last 24 hours by default, follows the active filters, and has one point per located message (section 14). Ignored aliases are hidden from it by default, as from the list (R27).
- The map and reports are for every signed-in user. Flags and receiver status are for admins only (R44, R35c, R39).
- Reports count Emergency messages per capcode, with inclusive dates in `TZ`, and unmapped codes and parse failures counted under Other, one row per raw code. Heartbeats are excluded (R38, R35b, section 14).
- Retroactive redaction runs at the nightly run (P1, section 14, ADR 0003).
- Deleting a message also deletes its receptions, its location and its place in a job (section 14).
- Tiles are a self-hosted PMTiles extract read by MapLibre, with no tile server (ADR 0001).

## Implementer decisions

### 1. Address dataset

- The dataset gains coordinates. **JSON** (`streets`, plus `addresses` with `number, street, locality, lon, lat`) is for small datasets and street fixtures. **CSV** (`.csv` or `.csv.gz`, columns `number,street,locality,lon,lat`) is for real-size ones and is streamed, never held in memory whole. Converting a real source (a government address file) to this CSV belongs in the deployment's private profile repo, so this repo names no real dataset.
- The API **loads the dataset into PostGIS** (`address_points`, `street_points`, `localities`, migration 00008) when the file or the profile's `street_types` change, identified by a hash. The load runs in one transaction **before the API listens**, so nothing is placed against a half-loaded dataset. A real-size load takes minutes, but only after a change (clients buffer meanwhile, R15). The profile hash already includes the dataset, so a change also queues a full re-parse and re-placing (R23a).
- The street index used by `address_split` stays **in memory**, as ADR 0001 allowed below 50 MB. A15 measures it with a real-size dataset, and the API logs its heap at start-up.

### 2. Placing a message (R24–R26)

- **House numbers:** tried as written, then the street number of a unit (`2/12` → `12`), the first of a range (`12-14` → `12`), and without a letter suffix (`12A` → `12`). A number the dataset doesn't have falls back to the street, marked approximate.
- **Street centroid:** the dataset's own point for the street if it gives one (a row with no number). Otherwise it's the street's address point nearest the centre of all its address points in that locality, so the point lies on the street even when the street curves (a plain centroid can land in a paddock).
- **Grid references** (`mgrs-6-digit-in-locality-square`): the candidate 100 km squares are those the locality's extent overlaps, in every UTM zone it overlaps. The extent is the bounding box of the locality's points. A candidate whose point falls outside its own zone is dropped. If more than one remains, the one nearest the extent wins, then the one nearest its centre. A reference far from the locality still resolves within its square (R25), so a disagreement can be flagged. The point is the centre of the 100 m cell (section 14). The Norway and Svalbard zone exceptions aren't applied.
- **The locality for a grid reference** comes from the parsed address, then the `locality` field, then (only when no address was split) the end of the description. With no known locality, the grid reference isn't placed.
- **UTM is computed in Go** (Krüger series, tested against PROJ to 1 cm) rather than with PostGIS `ST_Transform`, as ADR 0001 suggested. This keeps placing in one testable package with no database round trip for the arithmetic.
- **R26 distances** use the great-circle distance (within 0.5 % at these scales). For street-only addresses, PostGIS measures from the grid point to the nearest of the street's address points (owner decision 2).
- Stored on the message: `location` (PostGIS geography), `location_source` (`address`, `street` or `grid`), and `location_mismatch`. `street` is shown to users as approximate.

### 3. Map (R35)

- **MapLibre GL 6** with **PMTiles**, styled by the Protomaps basemap layers (BSD-3). Fonts (Noto Sans, OFL; Latin ranges only, since the app is English only, R35a) and sprites (MIT) are bundled. A glyph range that isn't bundled gets an empty glyph set. MapLibre's worker is bundled as a same-origin module, so the Content-Security-Policy stays `'self'` with no `blob:` workers.
- The deployment supplies its extract (`MAP_TILES_FILE`; the docs show `pmtiles extract`). The credit line comes from the extract's metadata, and `MAP_ATTRIBUTION` overrides it. The dataset's attribution is always shown (R24).
- **Example map:** MADEUPTON and TESTVILLE sit in open ocean in the South Pacific (UTM zone 7S), at a neutral location (section 14). `scripts/example-map` generates the dataset and a tiny test extract (12 KB, built with tippecanoe) of the made-up streets, used by the browser tests and the live copy.
- Messages at the same point (copies to several groups) cluster. Clicking a cluster that can't be split further lists its messages. Street-only points are drawn as rings. New messages appear live through the same filtered stream as the list.
- One request returns at most **2,000 points**, the newest, and says when there were more.
- **Without WebGL** (turned off, or headless Firefox), the page says the map can't be shown and still lists the located messages. Every map page lists its located messages below the map, which is also the accessible alternative to the canvas.
- **Detail view** (R35c, owner 2026-10-15, A34): the same component in a single-message mode, with no clustering, centred on the message at zoom 16 and 16rem tall. On phones it comes after the text; from 48rem wide it sits beside the text and details. Without WebGL it says so, and the location stays described in the details. A message with no location gets no map.
- WebKit sometimes kept showing a frame from before the points were drawn (1 in 20 to 40 detail views in testing). MapLibre had drawn them, and one more frame showed them, so the map draws one extra frame when it first goes idle.

### 4. Reports (R38)

- A per-capcode page with a date range (default: the last 30 days), linked from the alias and capcode views. Categories with no messages are left out. Other lists its raw codes, with "no event type" for pages that weren't parsed.

### 5. Liveness (R39)

- **Not receiving** means 30 minutes since the client's last page of any kind (ignored capcodes included, R16), measured on the server's clock. A client never heard from counts from when it was added.
- Revoked clients aren't listed, since they can't send. The clients page refreshes every 30 s. Admins also get a header link ("1 receiver not receiving") checked every minute. The metric is M5 (R52).

### 6. Retention and the nightly job (R45, R46, P1)

- Retention is a setting in the database (default 3,650 days, 30 to 36,500). The confirmation shows the cut-off **as it will be at the next nightly run**, because that's when deletion happens. A run after midnight but before 03:00 still uses that day's date. The server checks that the confirmed date matches, so a stale confirmation is refused.
- The run is at `NIGHTLY_AT` (default 03:00) in `TZ`. **A run missed while the API was down is made up when it starts.** A PostgreSQL advisory lock stops two runs overlapping.
- Steps: (1) delete messages sent before the cut-off, in batches of 1,000; (2) if the redaction rules changed since they were last applied to every message (a hash of the rules is stored), redact every stored message again and re-parse and re-place the ones that changed, so the dedup hash, parsed fields, job and search index follow (P1); (3) `VACUUM (ANALYZE)` the message tables.
- Each run's result (time, cut-off, counts, error without page text) is stored and shown on the Retention page. M5's metrics will read it.
- Retention is entered in days, so "2 years" is 730 days. A12 is checked with a run on 13 October 2026, when no 29 February falls inside the period.

### 7. Resource budget (A15)

- `scripts/a15.sh` runs the compose stack (Authentik included) with a made-up dataset of real size (`scripts/a15/dataset.go`: 3,000 localities, about 200,000 streets, 3.8 million addresses), 20 live-update viewers, and pages generated from the example formats (`scripts/a15/load.go`). It samples `docker stats` every 10 s into a CSV and checks the limits as ADR 0001 set them. CI runs a short version (5 minutes at 20 times the rate, then 2 minutes idle). The full hour is run by hand, with evidence.

### 8. Images (GHCR)

- The server image (amd64) is built and pushed directly. The client image is built natively on amd64 and arm64 runners, pushed by digest, and joined into one multi-arch tag. Publishing jobs wait for every check, including A15's short run. The images are linked to the repository by their OCI labels.
- GHCR may create the packages as private on their first publish. If it does, the owner makes each one public once in the package settings (there's no API for this).

### 9. Styles

- `CLAUDE.md` gains the rule that styles use the tokens in `web/src/app.css`. The stray `#2e7d32` becomes `--ok-fg`, and the components' font sizes move into a type scale of tokens with the same values, so nothing changes on screen. Map colours are read from the same tokens at runtime.

## Consequences

### Positive

- Every message is placed or not for a stated reason (`address`, `street` or `grid`), and approximate places are visible as such.
- No third party is contacted at runtime. The only external fetch is the operator's one-off extract download.
- The dataset format is generic, and real-size data is streamed and loaded only on change.
- Admins see a quiet receiver without opening the clients page.

### Negative / risks

- A real-size dataset load blocks the API's start for minutes after a dataset change.
- Grid references can't be placed for pages whose locality isn't known.
- The map needs WebGL. Without it, the page falls back to the list.
- The bundled glyphs cover Latin text only. Other scripts in map labels (rare in English-language extracts) are left out.
- Headless Firefox has no WebGL, so in CI the browser tests check the map's fallback in Firefox and the drawn map in Chromium and WebKit.

## Verification

| Choice | Check |
|---|---|
| UTM and grid completion | `internal/geo` tests against PROJ reference values, zone boundaries, far side of a square |
| Placing, approximate streets, mismatch | `internal/acceptance/m4_test.go`: address first, street-only (approximate, on the street), unit/suffix fallback, grid fallback, unknown locality, R26 including the street-distance rule (mutation-checked) |
| A11, A12, A13 | Go acceptance tests through the API, with a controllable clock and a direct call to the nightly run |
| Retroactive redaction | A stored phone number is gone from every table after the run and the parsed fields follow; an unchanged rule set doesn't rescan |
| Map without a tile server | Range request answered `206`; browser test of the drawn map with no console or CSP errors (Chromium, WebKit) and the fallback (Firefox) |
| A34 | `web/tests/e2e.spec.ts`: marker, ring and no map at 375 px and 1280 px, read from the drawn pixels against the colour tokens (mutation-checked: a ring drawn as a marker, and the map above the text) |
| Section 11 | `scripts/stack-e2e.sh`: steps 2 and 3 on the compose stack |
| A15 | `scripts/a15.sh`: short run in CI, full hour by hand |
| GHCR | The publish jobs on `main` and on a manual run |
