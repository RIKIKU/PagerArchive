# ADR 0035: Areas: named polygons from the profile, a page's area kept beside it

## Status

Proposed (2026-11-16). The owner: on a large job they needed the job's district at a glance, to know which radio channel to listen to; "position-based district and map layer first". SPEC.md R5 and section 10 change, with R88-R88e (section 7.26), A111 and A112, and M25. On the branch `districts` from main; migration 00026; the pull request is labelled `feature`.

Revised (2026-11-17), before it was merged: the owner, having seen the demo, wants a second agency's fire districts shown too, so a profile names **layers** of areas (sections 10 and 11), and an areas file that doesn't load no longer stops the API (section 12). A114 is added. Migration 00026 hadn't been released, so it was changed rather than followed by another.

## Context

The owner's network publishes its districts as open data: 21 polygons, CC BY 4.0 with an attribution, 336,000 points, 8 MB of GeoJSON. Their names and outlines are a real network's, so they belong in the private profile repository; the public repository gets a generic feature any network can fill. A page placed on the map (R24, R25) has a point; the district is the polygon around it. A second agency publishes its own districts the same way (CC BY 4.0, 11 features, 10 districts, one of them land and water, 45,000 points, 1 MB), covering the metropolitan and regional cities; they overlap the first agency's, which is what they are: another set of districts over the same ground.

## Decisions

### 1. What they're called, and where they're named

**Areas**: a profile's named polygons. SPEC.md's glossary had no word for them; "district", "region" and "zone" are what particular networks call theirs, so each network says what one is called with a **label** ("District"), shown before each name ("District 08"). The network profile names them with an optional `areas` key: a list of layers (section 10), each with an id, the GeoJSON file's path, the label, the feature property naming each area, the attribution and its outline style. GeoJSON because the open data comes that way (a WFS answers with it directly) and everything reads it; RFC 7946's WGS84 longitude and latitude, as the address dataset's coordinates are. Each file is checked by the server and `validate-profile` alike (`areas.ReadFile`), with each problem's place as a JSON Pointer, as profile errors have (R1); what the server does with a problem is section 12.

An agency profile's extra field can be called `area` (the owner's network has a response area); the word is the code's and the spec's, and users see the label.

### 2. Changing them re-parses nothing

The network profile's hash decides when every page is re-parsed (R23a, section 14). Adding the `areas` key would change the file, so every page of an archive (3.1 million on the owner's) would be re-parsed and re-placed, and its large events rebuilt (R87f), for something that changes no parsed field. So the hash is taken with the top-level `areas` member cut out of the file's bytes (`profile.parseHash`): the member and the comma before it, or for a first member the comma after it, so the cut file is byte for byte the file as it was without the key, and its hash is the hash it had. Checked on the example, on the owner's private profile (the same hash with and without the key), and with the member first, in the middle and last (`TestParseHashLeavesOutAreas`). A key named `areas` deeper down is the profile's own and stays in.

### 3. A page's area: kept beside the page, not worked out when it's read

The area is kept in `message_areas (message_id REFERENCES messages ON DELETE CASCADE, layer, area, PRIMARY KEY (message_id, layer))`, a row for each layer the page is in an area of (section 10), as ADR 0034's derived locality is, rather than worked out from the point each time a page is shown.

- **Reads stay cheap.** Every list, job view, map and live update would otherwise test each page's point against the polygons: about 30 µs a page with the pieces of section 4, so 60 ms for the map's 2,000 points, on every request. Kept, it's one look-up by primary key for the page of results (`MessageAreas`), and a job's areas one join (`JobAreas`).
- **Search and reports can use it later.** The backlog has a search filter and a report by district. Finding the pages in one area by geometry would mean testing every page in the date range; with the area kept beside each page it's a join, as reports read derived localities.
- **Not a column of `messages`:** that holds what the page said (P3), and filling a column for every stored page rewrites every page and its 15 indexes (ADR 0034 §2). Not in the search query either: the query is tuned for R33b (ADR 0017), so the areas are fetched for the page of results afterwards, in one more query.

What it costs is keeping it right: working it out when a page is placed (section 5), for the pages stored before (section 6), and again when the areas change (section 7). It's derived, never a field: not searched, exported or sent to integrations (R88a).

### 4. Which area a point is in

`area_at(layer, geography)`, a SQL function: the area of that layer containing the point, or none for a point in none of the layer's areas, on an area's edge, or in two at once. On the edge is none because a point exactly on a border between two districts belongs to neither more than the other, and in two at once is none because overlapping areas within a layer are a mistake in the file, not an answer. Areas of different layers overlap by design.

Testing a point against a whole 40,000-point outline reads all of it, and PostGIS's prepared-geometry cache doesn't help when consecutive points fall in different areas. So each area is cut into pieces of at most 256 points (`ST_Subdivide`) in `area_parts (layer, name, geom)`, with a GiST index on the geometry: a point reads the few small pieces whose boxes hold it. A point inside a piece is inside its area; one only on a piece's own edge, where an area was cut, is checked against the whole area (`areas.geom`), which is rare. On cld01, 100,000 random points over the state: 2.9 s, so about 29 µs a point.

### 5. Worked out with the page's location

- Ingest (`ingest.Service.Save`): after the page is inserted, in its transaction, for a page that was placed (`SetMessageArea`, which reads the stored location and calls `area_at` for each layer loaded).
- A page placed again (`ingest.Locate`: a re-parse, a new address dataset, retroactive redaction): after its location is set, the row is written, changed or deleted. Unchanged rows aren't written.
- Retention (R45) deletes it with its page (`ON DELETE CASCADE`).

No report counts areas yet, so no triggers queue jobs (unlike ADR 0034).

### 6. Pages stored before: a background backfill, like ADR 0034's

`areas.Backfill`, started by the API after the areas are loaded, reads pages in id order, 2,000 at a time, and in one statement per batch works out each located page's area, inserts or corrects its row, and deletes the row of a page that has no location or is in no area; the batch and how far it got commit together. It does this a layer at a time, for each layer whose state isn't `done` (`settings` row `message_areas_backfill:<layer>`: the last id, then `done`), so a layer added or changed is worked out alone and the others aren't read again. So, as ADR 0034's:

- **resumable**: a restart carries on from the last batch committed;
- **idempotent**: run again, it changes nothing that's right and corrects anything wrong;
- **gentle**: between batches it waits as long as the batch took, at most half of one connection;
- **logged**: every 30 seconds, pages read, pages in an area, percent and seconds; counts only (P1).

Unlike ADR 0034's it overwrites rows (section 7 needs that), so a batch reads one snapshot (repeatable read): a page in it that a re-parse places again meanwhile, writing its own row, makes the batch fail with a serialization error, and the batch runs again rather than give that page the area it had. Locking the batch's pages instead (`FOR SHARE`) would write a lock into every stored page, which is the rewrite section 3 avoids. The migration only creates the tables and the function, so the upgrade itself takes no longer (A77).

### 7. Loaded when they change

The API reads each layer's file and loads its areas into the database before it listens, when the file or its name property has changed (`areas.Start` and `areas.Load`; `settings` row `areas_hash:<layer>`, the file's bytes, the property and a version of what Load makes). Loading a layer is one transaction: its outlines merged by name and made valid, the pieces, the map's outlines and label points (section 8), and its backfill's state set back to the first page; the other layers' rows aren't touched. A failed load leaves the previous areas. Rows already in `message_areas` stay until the backfill has worked them out again, so a page shows the area it had until then, rather than none; with no foreign key from `message_areas` to `areas`, an area renamed or removed doesn't take its pages' rows with it before then. A layer the profile no longer names is removed with every page's area of it (`areas.Clear`, from the layers the `settings` rows say are loaded); a profile with no `areas` key removes them all. The label, attribution and outline aren't in the hash: changing them changes what's shown, at the next start, and nothing else.

### 8. The map

The browser gets a GeoJSON FeatureCollection for each layer (`GET /api/v1/map/areas/{layer}`): the layer's edges as one line feature, and the point each area's label goes at, named. The edges are the outlines' boundaries merged (`ST_Union` of `ST_Boundary`), so an edge two areas share is sent and drawn once: drawn twice, from each side, a dashed line's dashes run in opposite directions and fill each other's gaps, and it looks solid. It also sends about half the points. One response a layer, rather than one for all, so a change of one layer's areas doesn't send the browser the others again. Outlines are simplified together (`ST_CoverageSimplify`), so neighbours keep sharing their edges with no gaps or overlaps between them, or one at a time (`ST_SimplifyPreserveTopology`) where the areas aren't a coverage. Each layer is simplified on its own: the tolerance is the smallest of 0.0001° doubling (about 10 m, 20 m, …) that brings its outlines under 400 KB of GeoJSON at 4 decimals (about 10 m), so two layers stay under the 1 MB the owner set for the map together. A shared budget was the other choice; it would make adding a layer simplify the others again, for no gain at two layers; the areas a page is in are always worked out from the full detail. For the owner's districts that's 0.0016° (about 180 m): 391 KB of outlines, sent as 264 KB of edges, 70 KB gzipped, from the file's 8 MB, and far under a pixel at the zooms the whole state fits in. The label goes at the centre of the largest circle inside the outline (`ST_MaximumInscribedCircle`), which stays inside a ragged or C-shaped area where a centroid wouldn't.

The API builds the response once at start-up and keeps it, gzipped too. Meta gives each layer's version (the start of its areas' hash), the page asks for `?v=<version>`, and that answer may be kept for a year (`immutable`): a change of areas is a change of version, so a browser asks again only then. Asked for without the version it must check again (`no-cache`, with an ETag).

MapLibre draws them on the map page only, a line layer and a label layer for each layer of areas, in the profile's order, added before the messages' layers, so they're under the points. Each layer is drawn in its outline style (section 10), whose colour, width, dashes and label colour are tokens (R82c). One "Map layers" control (owner, 2026-11-17: a button for each layer was too intrusive; the familiar layers control instead) shows and hides them: an icon button (a stack of sheets, `aria-expanded`, `aria-controls`) under the full-screen button, at the top, since at the foot of the map a phone's bottom bar can cover it. It opens a panel beside it, inside the map, with a checkbox for each layer ("Zone outlines", with a sample of its line); Esc, a click or focus outside, or the button again closes it. Each choice is kept in the browser's local storage, a per-viewer convenience. Room for more layers: the panel is no wider than the map leaves and scrolls when it's taller. The panel is a disclosure (a button and a group of checkboxes), not a menu or a dialog: nothing in it needs arrow keys, and the map behind stays usable. Each layer's attribution is its MapLibre source's, so MapLibre credits it only while the layer is shown (R35g).

The credits (R35g, owner, 2026-11-17) stay MapLibre's compact attribution, which gathers each shown source's credit, but its ⓘ is a real `<button>` ("Map credits", `aria-expanded`, `aria-controls`, the app's icon button) instead of MapLibre's `<summary>`, which isn't a button to assistive technology or to the tests, in the app's look (tokens) rather than MapLibre's white pill. They fold away when the viewer pans, zooms or clicks the map (MapLibre alone folds them on a drag only) or after five seconds, the two automatic ways the OpenStreetMap Foundation's Attribution Guidelines (adopted 2021-06-25) allow for an interactive map; the guidelines have no small-screen exception, so a phone starts with them open too. A drag of the map, not the map fitting itself to the messages, is the viewer's (MapLibre's `originalEvent`).

### 9. Where they're shown

- **A job's view**: the areas its pages are in, every layer's, as tags above its pages (`GET /api/v1/jobs/{job}/areas`), asked again as its pages arrive; each tag edged as its layer is outlined. A job's own page doesn't hold all its messages (the list pages them), so the server answers for every page of the job.
- **A message's detail view**: each layer's area, beside its location, under the layer's label.
- **The map's list** of located and selected messages: each one's area. (The map has no pop-ups: picking points lists them below.)
- Message rows don't show a page's location, so they don't show its area.
- Every message the API returns has `areas`, `[{layer, name}]` in the profile's order, or none when it's in none.

Administration's start page has a short section, a part for each layer: its label and outline, how many areas, the file's name, the names, the attribution, whether stored pages' areas are still being worked out, and why its file didn't load, if it didn't (`GET /api/v1/admin/areas`, admins only).

### 10. Several layers

A profile's `areas` is a list. Each layer has an `id`, a machine name unique in the profile, that keys its rows (`areas`, `area_parts` and `message_areas` all carry it), its `settings` rows, its outlines' URL and the browser's remembered choice; the label is what people see, and may change without touching anything stored. A page is in at most one area of each layer, so the second agency's districts sit beside the first's rather than competing with them.

- **The label is the pattern.** Each area is shown as its layer's label, a space and its name, so "District" makes "District 08" and a label of the second agency's short name makes, say, "Metro Central". The label lives in the profile, as before, so the code knows no agency's name. A pattern with a placeholder was the other choice; no layer needs anything but a prefix, so it can wait.
- **Features of one name are one area**, as before: the second agency's file has one district as two features, its land and its water, and they're merged.
- **The style is chosen in the profile, the look in the tokens.** `outline` is `solid`, `dashed` or `dotted`, by default the first layer solid, the second dashed, the third dotted. Each style is a set of tokens in each look (`--map-area-solid-line`, `-stroke`, `-dash`, `-label`, and so on): solid in the text colour, dashed in the accent, dotted in the muted colour, so two layers differ in colour and in line, in light and dark; where a look's accent is its text colour, the dashes still tell them apart. A profile can't name a colour (R82c).
- **Order** is the list's: the layers are drawn, listed, tagged and credited in it, the later drawn over the earlier.
- **Backfilled a layer at a time** (section 6): adding a layer works out that layer only, and changing one layer's file works out that layer only.

### 11. One request a layer, and how big

The second agency's districts fit their budget at the first tolerance tried, 0.0001° (about 10 m); with the first agency's the map gets 588 KB of GeoJSON for both, 139 KB gzipped, under the 1 MB the owner set (Evidence). The browser asks for each once per version and keeps it.

### 12. A file that doesn't load doesn't stop the API

The first version stopped the API for a broken file, as a broken profile does (R1). That made CI's A15 fail when its profiles folder left the new file out, and the same thing on an install would be worse: an automatic upgrade (R84d) whose profile's areas file went missing would leave the archive down, for a map decoration. So `areas.Start` reads, loads and prepares each layer on its own, and a layer whose file is missing, isn't valid, or fails to load:

- is logged at ERROR, naming the layer, the file and the problem (no page text: P1);
- keeps the areas it had in the database, if it was loaded before, and the API shows them as they were (the version is the start of the hash it was loaded with), until the file is put right; pages keep their areas, and new pages get theirs from those areas;
- shows nothing if it was never loaded;
- is shown in Administration as not loaded, with the problem, and whether the areas from before are shown.

The other layers load as ever. `validate-profile` still fails on such a file, so a profile repository's own checks catch it before an install does. R1 still holds for the profile itself: a layer with a missing key, two layers with one id, or an outline that isn't one of the three is a profile error, and the API doesn't start, as for any other profile mistake.

## Tests

- `TestA111Areas`: the example's two layers, zones (two, one a MultiPolygon, the edge between them through 11 FICTION ST) and regions over them (made up, overlapping both zones); pages in each, on the zones' edge, a street only, outside, unplaced; each layer's area on the API's views in order, each layer's outlines' response and its caching, a layer the profile doesn't name, Administration; a re-parse; moved zones loaded and worked out again without the profile's parse hash changing, and the regions not worked out again; retention; overlapping zones; one layer removed, then all.
- `TestA112AreasAfterUpgrade`: a database at migration 00025 with placed pages, upgraded with the zones: the backfill, its state, resuming, running again, the log holding no page text; then the regions added, worked out alone.
- `TestA114AreasThatDontLoad`: a broken file and a missing one: the API's start, the log, the areas from before kept and shown, a new layer with no file, Administration, the file put right.
- `TestParseHashLeavesOutAreas`, `TestAreasLayers`, `TestReadFileChecks`, `TestValidateProfileAreas`: the hash, the layers' ids and outlines, what a file may be, and `validate-profile` failing on a broken or missing file.
- The browser (web/tests/e2e.spec.ts, A111): the job's view, a message's view, the map's list, both layers' outlines, the "Map layers" control and its panel (closed at first; focus; Esc, a click outside and the control closing it; inside the map), one layer hidden alone, with its credit, and remembered, in full screen too, the credits folding away (R35g), no control without layers, light and dark, 1,280 and 375 px, axe; Administration.
- A15 (`scripts/a15.sh`) fails if the API logs that areas didn't load.
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

Both layers (2026-11-17), loaded by `areas.Start` into a throwaway PostGIS 18-3.6 on cld01 from the owner's private files, as the API loads them:

| Layer | Areas | Pieces | Tolerance | Map (GeoJSON) | Gzipped |
| --- | --- | --- | --- | --- | --- |
| The owner's districts | 21 | 3,146 | 0.0016° | 264,225 bytes | 70,462 bytes |
| The second agency's districts (11 features, 10 names) | 10 | 394 | 0.0001° | 323,693 bytes | 68,236 bytes |
| Both | | | | 587,918 bytes | 138,698 bytes |

Loading both took 5.5 s, the first layer nearly all of it. 14 of the 21 districts overlap one of the second agency's (8 of them by more than 1% of their area, the most by 28%); the second agency's cover about 2% of the first's ground. Each layer's backfill reads every stored page once, as above; the second's has a tenth of the pieces, so it should take no longer than the first's.
