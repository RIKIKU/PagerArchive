# ADR 0035: Areas: named polygons from the profile, a page's area kept beside it

## Status

Proposed (2026-11-16). The owner: on a large job they needed the job's district at a glance, to know which radio channel to listen to; "position-based district and map layer first". SPEC.md R5 and section 10 change, with R88-R88e (section 7.26), A111 and A112, and M25. On the branch `districts` from main; migration 00026; the pull request is labelled `feature`.

## Context

The owner's network publishes its districts as open data: 21 polygons, CC BY 4.0 with an attribution, 336,000 points, 8 MB of GeoJSON. Their names and outlines are a real network's, so they belong in the private profile repository; the public repository gets a generic feature any network can fill. A page placed on the map (R24, R25) has a point; the district is the polygon around it.

## Decisions

### 1. What they're called, and where they're named

**Areas**: a profile's named polygons. SPEC.md's glossary had no word for them; "district", "region" and "zone" are what particular networks call theirs, so each network says what one is called with a **label** ("District"), shown before each name ("District 08"). The network profile names them with an optional `areas` key: the GeoJSON file's path, the label, the feature property naming each area, and the attribution. GeoJSON because the open data comes that way (a WFS answers with it directly) and everything reads it; RFC 7946's WGS84 longitude and latitude, as the address dataset's coordinates are. The file is checked when the profile is, by the server and `validate-profile` alike (`areas.ReadFile`), with each problem's place as a JSON Pointer, as profile errors have (R1).

An agency profile's extra field can be called `area` (the owner's network has a response area); the word is the code's and the spec's, and users see the label.

### 2. Changing them re-parses nothing

The network profile's hash decides when every page is re-parsed (R23a, section 14). Adding the `areas` key would change the file, so every page of an archive (3.1 million on the owner's) would be re-parsed and re-placed, and its large events rebuilt (R87f), for something that changes no parsed field. So the hash is taken with the top-level `areas` member cut out of the file's bytes (`profile.parseHash`): the member and the comma before it, or for a first member the comma after it, so the cut file is byte for byte the file as it was without the key, and its hash is the hash it had. Checked on the example, on the owner's private profile (the same hash with and without the key), and with the member first, in the middle and last (`TestParseHashLeavesOutAreas`). A key named `areas` deeper down is the profile's own and stays in.

### 3. A page's area: kept beside the page, not worked out when it's read

The area is kept in `message_areas (message_id PRIMARY KEY REFERENCES messages ON DELETE CASCADE, area)`, as ADR 0034's derived locality is, rather than worked out from the point each time a page is shown.

- **Reads stay cheap.** Every list, job view, map and live update would otherwise test each page's point against the polygons: about 30 µs a page with the pieces of section 4, so 60 ms for the map's 2,000 points, on every request. Kept, it's one look-up by primary key for the page of results (`MessageAreas`), and a job's areas one join (`JobAreas`).
- **Search and reports can use it later.** The backlog has a search filter and a report by district. Finding the pages in one area by geometry would mean testing every page in the date range; with the area kept beside each page it's a join, as reports read derived localities.
- **Not a column of `messages`:** that holds what the page said (P3), and filling a column for every stored page rewrites every page and its 15 indexes (ADR 0034 §2). Not in the search query either: the query is tuned for R33b (ADR 0017), so the areas are fetched for the page of results afterwards, in one more query.

What it costs is keeping it right: working it out when a page is placed (section 5), for the pages stored before (section 6), and again when the areas change (section 7). It's derived, never a field: not searched, exported or sent to integrations (R88a).

### 4. Which area a point is in

`area_at(geography)`, a SQL function: the area containing the point, or none for a point in no area, on an area's edge, or in two at once. On the edge is none because a point exactly on a border between two districts belongs to neither more than the other, and in two at once is none because overlapping areas are a mistake in the file, not an answer.

Testing a point against a whole 40,000-point outline reads all of it, and PostGIS's prepared-geometry cache doesn't help when consecutive points fall in different areas. So each area is cut into pieces of at most 256 points (`ST_Subdivide`) in `area_parts`, with a GiST index: a point reads the few small pieces whose boxes hold it. A point inside a piece is inside its area; one only on a piece's own edge, where an area was cut, is checked against the whole area (`areas.geom`), which is rare. On cld01, 100,000 random points over the state: 2.9 s, so about 29 µs a point.

### 5. Worked out with the page's location

- Ingest (`ingest.Service.Save`): after the page is inserted, in its transaction, for a page that was placed (`SetMessageArea`, which reads the stored location and calls `area_at`).
- A page placed again (`ingest.Locate`: a re-parse, a new address dataset, retroactive redaction): after its location is set, the row is written, changed or deleted. Unchanged rows aren't written.
- Retention (R45) deletes it with its page (`ON DELETE CASCADE`).

No report counts areas yet, so no triggers queue jobs (unlike ADR 0034).

### 6. Pages stored before: a background backfill, like ADR 0034's

`areas.Backfill`, started by the API after the areas are loaded, reads pages in id order, 2,000 at a time, and in one statement per batch works out each located page's area, inserts or corrects its row, and deletes the row of a page that has no location or is in no area; the batch and how far it got (`settings` row `message_areas_backfill`: the last id, then `done`) commit together. So, as ADR 0034's:

- **resumable**: a restart carries on from the last batch committed;
- **idempotent**: run again, it changes nothing that's right and corrects anything wrong;
- **gentle**: between batches it waits as long as the batch took, at most half of one connection;
- **logged**: every 30 seconds, pages read, pages in an area, percent and seconds; counts only (P1).

Unlike ADR 0034's it overwrites rows (section 7 needs that), so a batch reads one snapshot (repeatable read): a page in it that a re-parse places again meanwhile, writing its own row, makes the batch fail with a serialization error, and the batch runs again rather than give that page the area it had. Locking the batch's pages instead (`FOR SHARE`) would write a lock into every stored page, which is the rewrite section 3 avoids. The migration only creates the tables and the function, so the upgrade itself takes no longer (A77).

### 7. Loaded when they change

The API reads the file when it reads the profile, so a broken file stops it before anything else (R1), and loads the areas into the database before it listens, when the file or its name property has changed (`areas.Load`; `settings` row `areas_hash`, the file's bytes, the property and a version of what Load makes). Loading is one transaction: the outlines merged by name and made valid, the pieces, the map's outlines and label points (section 8), and the backfill's state set back to the first page. A failed load leaves the previous areas. Rows already in `message_areas` stay until the backfill has worked them out again, so a page shows the area it had until then, rather than none; with no foreign key from `message_areas` to `areas`, an area renamed or removed doesn't take its pages' rows with it before then. A profile with no `areas` key removes the areas and every page's area (`areas.Clear`). The label and attribution aren't in the hash: changing them changes what's shown, at the next start, and nothing else.

### 8. The map

The browser gets one GeoJSON FeatureCollection (`GET /api/v1/map/areas`): each area's outline and the point its label goes at, named. Outlines are simplified together (`ST_CoverageSimplify`), so neighbours keep sharing their edges with no gaps or overlaps between them, or one at a time (`ST_SimplifyPreserveTopology`) where the areas aren't a coverage. The tolerance is the smallest of 0.0001° doubling (about 10 m, 20 m, …) that brings the outlines under 400 KB of GeoJSON at 4 decimals (about 10 m); the areas a page is in are always worked out from the full detail. For the owner's districts that's 0.0016° (about 180 m): 391 KB, 99 KB gzipped, from the file's 8 MB, and far under a pixel at the zooms the whole state fits in. The label goes at the centre of the largest circle inside the outline (`ST_MaximumInscribedCircle`), which stays inside a ragged or C-shaped area where a centroid wouldn't.

The API builds the response once at start-up and keeps it, gzipped too. Meta gives the outlines' version (the start of the areas' hash), the page asks for `?v=<version>`, and that answer may be kept for a year (`immutable`): a change of areas is a change of version, so a browser asks again only then. Asked for without the version it must check again (`no-cache`, with an ETag).

MapLibre draws them on the map page only, as a line layer and a label layer added before the messages' layers, so they're under the points; their colours, width and type come from tokens (R82c), `--fg` on the map in light and dark. A button on the map ("Zone outlines", `aria-pressed`) hides and shows them; the choice is kept in the browser's local storage, a per-viewer convenience. It sits under the full-screen button, at the top: at the foot of the map, a phone's bottom bar can cover it. The attribution joins the address data's in the map's credits.

### 9. Where they're shown

- **A job's view**: the areas its pages are in, as tags above its pages (`GET /api/v1/jobs/{job}/areas`), asked again as its pages arrive. A job's own page doesn't hold all its messages (the list pages them), so the server answers for every page of the job.
- **A message's detail view**: its area, beside its location.
- **The map's list** of located and selected messages: each one's area. (The map has no pop-ups: picking points lists them below.)
- Message rows don't show a page's location, so they don't show its area.
- Every message the API returns has `area` (a name, or null).

Administration's start page has a short section: how many areas, the label, the file's name, the names, the attribution, and whether stored pages' areas are still being worked out (`GET /api/v1/admin/areas`, admins only).

## Tests

- `TestA111Areas`: the example's zones (two, one a MultiPolygon, the edge between them through 11 FICTION ST); pages in each, on the edge, a street only, outside, unplaced; the API's views, the outlines' response and its caching, Administration; a re-parse; moved zones loaded and worked out again without the profile's parse hash changing; retention; removed areas.
- `TestA112AreasAfterUpgrade`: a database at migration 00025 with placed pages, upgraded: the backfill, its state, resuming, running again, the log holding no page text.
- `TestParseHashLeavesOutAreas`, `TestReadFileChecks`: the hash, and what a file may be.
- The browser (web/tests/e2e.spec.ts, A111): the job's view, a message's view, the map's list, outlines and button, light and dark, 1,280 and 375 px, axe; Administration.
- `TestAreasBackfillSpeed` (`PAGERARCHIVE_AREAS_SPEED=1`, with a real-size file, by hand): the Evidence below.

## Evidence

On cld01 (8 vCPU, 11 GB, the database on a spinning disk), in a throwaway PostGIS 18-3.6 container with the test settings, `TestAreasBackfillSpeed` with the owner's district file (21 districts, 336,435 points, 8 MB; kept in the private profile repository):

| Step | Time |
| --- | --- |
| Loading the areas: merged, cut into 3,146 pieces, outlines simplified at 0.0016° to 390,955 bytes (98,851 gzipped) | 4.3 s |
| Seeding 3,100,000 made-up pages, nine in ten placed at random over the districts' extent | 7 min 27 s |
| The backfill: 3,100,000 pages read, 2,790,000 placed, 1,375,028 of them in a district, half of it waiting between batches | 23 min 35 s |

One batch of 2,000 pages, its pages cached, takes 87 ms in the database (`EXPLAIN ANALYZE`), 62 ms of it `area_at`; the rest of the backfill's time is reading pages from disk and committing. On the production host, a smaller box, the estimate is 30 to 60 minutes in the background after the upgrade (ADR 0034's backfill took 10 min 31 s on cld01 and was estimated at 15 to 30 there), during which pages already stored show no district; new pages have theirs at once. Changing the areas later costs the same again.

The owner's network profile, with the `areas` key and without it, has the same parse hash (`45a6c707…`), so the upgrade with the districts re-parses nothing. `validate-profile` on the private profiles with this build: 21 areas, every golden table as before.
