# Profiles

Network knowledge lives in JSON profiles, validated against the schemas in [`schemas/`](../schemas). The example profile in [`profiles/example/`](../profiles/example) is a complete, made-up network. Start there.

A JSON file is a profile when its top-level `kind` is `network` or `agency`. Keys starting with `_` are comments. Patterns are regular expressions in [RE2 syntax](https://github.com/google/re2/wiki/Syntax), as used by Go: no backreferences or lookaround, and matching runs in linear time.

## Network profile (one per deployment)

| Key | Meaning |
|---|---|
| `priority_markers` | 2-character prefixes and their priority: `Emergency`, `Non-emergency` or `Admin`. Case-sensitive |
| `ignored_capcodes` | Capcodes or ranges (`"0000040-0000049"`) whose pages are dropped and never stored |
| `heartbeat_capcodes` | Capcodes or ranges of network heartbeats |
| `redaction_rules` | Ordered `{name, pattern, must_not_match}`. Each match becomes one `REDACTED`. If the pattern has a group named `redact`, only that group is replaced, so text around it (a preceding space, a trailing `[group]`) is kept; RE2 has no lookaround. A rule that changes one of its own `must_not_match` examples stops the server |
| `job_number_patterns` | Tried in order on every page whose format extracts no `job_number`. The first match is used |
| `deployment_code_pattern` | Optional. Finds a code in a page that opens a **large event** (R87, ADR 0027), such as one naming a pre-formed group of appliances sent together. Every match in the text a pager displays (the marker removed) is a code, each kept once; with a group named `code`, that group is the code. No roster of groups or members is kept, and the app never counts, lists or names these codes: people see an event's jobs (owner, 2026-11-13). Without a pattern, only a job with enough Emergency pages opens large events. The key keeps its name, so existing profiles keep working. The example network's is `"\bDG[0-9]{4}\b"` in JSON (`DG0801`) |
| `event_categories` | `{category, codes}` for reports. `HSF*` matches by prefix. Unmapped codes count as Other |
| `address_dataset` | `{path, attribution}`. `path` is relative to the profile file. The attribution is shown in the app (R24). See [Address dataset](#address-dataset) |
| `street_types` | Spelling to abbreviation, e.g. `"STREET": "ST"`, used to match streets |
| `grid_reference` | `{system}`: how grid references are read (R25). `mgrs-6-digit-in-locality-square`: 3 digits of easting then 3 of northing, in 100 m, with no zone or 100 km square. The app completes them from the UTM zone and 100 km square that the page's locality lies in, and uses the centre of the 100 m cell |
| `areas` | Optional. A list of layers, each `{id, path, label, name_property, attribution, outline}`: a GeoJSON file of named polygons a located page can fall in, such as districts, outlined on the map (R88). See [Areas](#areas) |

## Address dataset

The dataset places pages on the map (R24) and tells `address_split` where a description ends and an address begins. It's local: no geocoding service is ever contacted. It comes in two forms:

- **JSON**, for small datasets and street fixtures, validated by [`schemas/address-dataset.schema.json`](../schemas/address-dataset.schema.json): `streets` (`{street, locality}`, optionally with `lon` and `lat` to place the street itself) and `addresses` (`{number, street, locality, lon, lat}`). See [`profiles/example/example-addresses.json`](../profiles/example/example-addresses.json).
- **CSV**, for real-size datasets: a file ending `.csv` or `.csv.gz` (gzipped) with a header row and the columns `number,street,locality,lon,lat` in any order. Other columns are ignored. Coordinates are WGS84 decimal degrees. A row with an empty `number` places the street itself.

Convert your source data (a government address file, say) to this CSV with your own script or `ogr2ogr`, and keep the script with your profiles.

How a page is placed (ADR 0005):

1. Its address and locality are matched against the dataset, with street types normalised (`STREET` = `ST`). If the profile doesn't extract the locality separately, the address must end with a known locality.
2. With a house number, that address's point. `2/12`, `12-14` and `12A` fall back to `12`.
3. With no house number, or one the dataset doesn't have, the street's point, shown as **approximate**. That's the dataset's own point for the street if it gives one. Otherwise it's the street's address nearest the centre of all its addresses in that locality.
4. Otherwise, the grid reference, completed from the locality's 100 km square.

If the address and the grid reference both place a page and they're more than 1 km apart, it gets the admin-only `location_mismatch` flag (R26). For a street-only address, the distance is measured to the nearest address on that street.

The API loads the dataset into the database before it starts listening, but only when the file or `street_types` have changed. A dataset of several million addresses takes a few minutes. Messages are then placed again in the background.

## Areas

A network profile may name **layers** of **areas** (R88, ADR 0035): named polygons, such as the districts a network's agencies are organised in, and another agency's districts over them. A page placed on the map (R24, R25) is in the area of each layer its point falls in, which its job's view, its detail view and the map's list show as the layer's label and the name (`District 08`), and every map (the map page, a message's and a report's) outlines and labels each layer in its own style, with a checkbox for each in the map's "Map layers" panel to hide it, one choice for every map in that browser. A page with no location shows none; nor does a point in no area of a layer, on an area's edge, or in two of a layer's areas at once (for that layer).

```json
"areas": [
  {
    "id": "zones",
    "path": "example-areas.geojson",
    "label": "Zone",
    "name_property": "zone",
    "attribution": "Example zones, made up for PagerArchive",
    "outline": "solid"
  },
  {
    "id": "regions",
    "path": "example-regions.geojson",
    "label": "Region",
    "name_property": "region",
    "attribution": "Example regions, made up for PagerArchive",
    "outline": "dashed"
  }
]
```

- `id`: the layer's machine name, unique in the profile: lower-case letters, digits and hyphens, starting with a letter. What's stored for the layer is kept under it, so changing it is removing the layer and adding a new one.
- `path`: relative to the profile file. A GeoJSON `FeatureCollection` of `Polygon` and `MultiPolygon` features in WGS84 longitude and latitude (RFC 7946). A file whose `crs` names anything else is refused. Name it `.geojson`, so it isn't read as a profile.
- `label`: shown before each name (`Zone A`), and in the map's layers panel (`Zone outlines`). A short agency name works too: a label `Metro` shows `Metro Central`.
- `name_property`: the feature property that names each area: a string (trimmed), or a number as the file writes it, at most 40 characters. Features with the same name are one area (a district's land and its water, say).
- `attribution`: the data's credit, shown in the map's credits while the layer is shown. Many open datasets require one.
- `outline`: optional, `solid`, `dashed` or `dotted`: how the map draws the layer, in colours the look sets (light and dark). By default the first layer is solid, the second dashed and the third dotted.

The layers are shown in the order listed. Areas of one layer shouldn't overlap each other (a point in two is in neither); layers overlap freely.

The example's [`example-areas.geojson`](../profiles/example/example-areas.geojson) has two made-up zones either side of 11 FICTION ST, MADEUPTON, and [`example-regions.geojson`](../profiles/example/example-regions.geojson) two made-up regions over them. A real network's areas belong in its private profile repository, with their attribution.

The API loads each layer into the database before it starts listening, when its file or `name_property` has changed, and then works out every located page's area of that layer again in the background, a batch at a time (pages keep the area they had until then); the other layers aren't worked out again, so adding a layer costs only that layer. Changing the areas, their label, attribution or outline re-parses no page: the `areas` key is left out of what decides a re-parse. Removing a layer removes every page's area of it. The map gets each layer's outlines simplified to under 400 KB (the area a page is in is worked out from the full detail), gzipped, and the browser keeps them until they change. Administration's start page says which layers are loaded.

**A layer's file that's missing or broken doesn't stop the API** (R88d): it starts without that layer's changes, logs an error naming the layer and the problem, and Administration says so; a layer loaded before keeps showing the areas it had until the file is put right. `validate-profile` checks every layer's file as the server does, and fails on one that's missing or broken, so check a profile with it before you deploy it.

## Agency profile (attached to alias templates)

```json
{
  "kind": "agency",
  "name": "example-fire",
  "formats": [
    {
      "name": "emergency",
      "priorities": ["Emergency"],
      "match": "^(?P<job_number>EX[0-9]{7}) (?P<event_type>[A-Z]+[0-9]) (?P<description>.+?) @ ...",
      "fields": { "units": { "split": " " } }
    },
    { "name": "freeform", "priorities": ["Admin"], "match": "" }
  ]
}
```

- Formats are tried **in order** on the redacted text with the priority marker removed. The first one whose `priorities` include the page's priority and whose `match` matches parses the page. Without `priorities`, a format applies to every priority, including none.
- An empty `match` is a **text-only format**: it matches anything and extracts nothing.
- Each **named group** becomes a field. Standard fields: `job_number`, `event_type`, `description`, `address`, `locality`, `grid_ref`, `units` (a list of the appliances a page names, shown as **Appliances**), `call_sign` (the paged unit's call sign, often a short code at the end, shown as **Call sign**), `call_signs` (a list of the call signs of the units a page dispatches and the roles it names, apart from its appliances, shown as **Call signs**; usually filled by `into`, below). The app calls the `units` field "Appliances" wherever people see it (owner, 2026-11-13), but its name stays `units` in profiles, golden tables, schemas, the API and the database, so existing and private profiles keep working. `call_sign` was called `group` until 2026-11-17 (R8, ADR 0040): rename `(?P<group>…)` to `(?P<call_sign>…)`, and `group` to `call_sign` in `fields`, `labels`, `large_event_fields` and golden rows. Until you do, each is read as `call_sign` and `validate-profile` (and the server's log) prints a warning naming where it is, such as `warning: …/fire.json: at /formats/0/match: the call sign's field is now call_sign (ADR 0040): rename group to call_sign`; a format, `fields` or `labels` naming both is an error. Any other name is an extra field. Groups whose names start with `_` are intermediate and never stored. Values are trimmed; empty values are null.
- `fields.<group>.split`: a pattern that turns the value into a list. `""` splits into single characters.
- `fields.<group>.into` (optional, beside `split`): moves some of the list's tokens into another list (R7, ADR 0047). On many networks a page's list of appliances also names the call signs of the units dispatched and the roles people hold (a duty officer, say), which aren't appliances; `into` puts them in `call_signs`, so Appliances and the reports' appliance counts (R38f, R38h) hold only appliances:

  ```json
  "fields": {
    "units": {
      "split": " ",
      "into": { "field": "call_signs", "values_file": "example-call-signs.txt", "match": "DO[0-9]{2}" }
    }
  }
  ```

  - `field`: the list the tokens move to: `call_signs`, `units` (when another field is split), or an extra field's name.
  - `values`: known values, listed in the profile: `["STN1", "STN2"]`.
  - `values_file`: the same in a file beside the profile (relative to it): UTF-8 text, one value a line; each line is trimmed, and blank lines and lines starting with `#` are skipped. Name it `.txt`. A file suits a long list, or one several agency profiles share. A network's list of its call signs is its own data: keep it in the private profile repository with the profiles.
  - `match`: an RE2 pattern a **whole** token must match (it's anchored as `^(?:…)$`), for values that follow a shape, such as roles.
  - Give at least one of `values`, `values_file` and `match`; any of them together is fine.

  After the split, each token that is one of the values, or that `match` matches in full, moves to `field`; the others stay. Both lists keep the tokens' order, and a list left with none is null. Values are compared exactly as extracted (P3): with `STN2` listed, `STN2` moves, but `STN2T` (a known call sign with a suffix, an appliance) and `stn2` stay. The example agency's emergency format moves `STN1` to `STN4` (in `example-call-signs.txt`) and `DO` plus two digits, so `UNITS T1 STN2 STN2T DO01` gives Appliances `T1, STN2T` and Call signs `STN2, DO01`. Text search finds the moved tokens as before, as they're in the page's text; there's no search key for `call_signs`, and `callsign:` and integrations' call-sign filter stay the paged unit's `call_sign` (owner, 2026-11-19).

  Problems that stop the server and fail `validate-profile`: `into` without `split` beside it; no `field`; a `field` that is the field being split, a single-value standard field (`job_number`, `call_sign` and the like), not a field name, or a field the same format captures or writes; none of `values`, `values_file` and `match`; an empty `values`; a value that's empty, listed twice (in `values` and the file together, too), has a space at either end, or that `split` would split (it could never be one token); a `values_file` that's missing, unreadable, not UTF-8 or has no values; a `match` that doesn't compile; and any other key. A `large_event_fields` or `levels` rule naming the target fails as it does for any list field. Each names the file and the path, and a values file's problem the line, such as `…/fire.json: at /formats/0/fields/units/into/values_file: example-call-signs.txt line 6: value "STN2" is listed twice`.

  A field's options take `split` or `address_split`, not both.
- `fields.<group>.address_split: {"description": "...", "address": "..."}`: for formats where a description runs into an address with no delimiter. The address is the longest suffix that reads `[house number] <street> <locality>`, with the street listed in that locality in the address dataset. Everything before it is the description. If no street matches, the whole text is the description, the address is null, and the message gets the admin-only `address_unsplit` flag. See ADR 0001 section 2.
- `labels`: display labels per field, e.g. `{"event_type": {"HSF1": "House fire"}}`.
- `large_event_fields` (optional): field values that open a **large event** (R87). Some agencies dispatch with a level on each page, such as an alarm level, and a high enough level means something big. Each rule names one text field the formats extract and lists every value of it that opens an event:

  ```json
  "large_event_fields": [
    { "field": "alarm_level", "values": ["3", "4", "5", "6", "7", "8", "9"] }
  ]
  ```

  An Emergency page to an aliased capcode whose field holds a listed value opens or joins a large event exactly as a page with a `deployment_code_pattern` code does: grouped by time, told once to large-event integrations when the event opens. Any other value, no value, or another priority opens nothing. Values are compared exactly as extracted (P3): `"3"` isn't `"03"` or `"3 "`, and there's no ordering, so list each level that counts. The app never shows the level or says what opened an event. The example agency's emergency format has an optional `ALARM <digit>` segment (`alarm_level`), and its rule lists 3 to 9.

  Problems that stop the server and fail `validate-profile`: a field no format extracts (a named group not starting with `_`, or an `address_split` output), a list field (`units`, or any field a format splits), the same field in two rules, no values, an empty value or one listed twice, a value with a space at either end (extracted values are trimmed, so it could never match), and a rule without `field` or `values` or with any other key.

  The flag is worked out when a page is parsed and stored with it, so a change to the rule applies to older pages when they're re-parsed (R23a), which a changed profile does at start-up. The re-parse then rebuilds the large events around them (R87f): older pages at a listed value form events, and no integration is told of them.
- `levels` (optional): fields whose values are **levels in an order**, such as an alarm level, so an integration can send only pages at or above a level (R7, R49a, ADR 0045). Each rule names one text field the formats extract, what it's called, and its values **lowest first**, each with a label:

  ```json
  "levels": [
    {
      "field": "alarm_level",
      "label": "Alarm level",
      "values": [
        { "value": "1", "label": "First alarm" },
        { "value": "2", "label": "Second alarm" },
        { "value": "3", "label": "Third alarm" }
      ]
    }
  ]
  ```

  The integration form offers each one as "<agency>: <label>" (the agency is the profile's `description`, or its name) and then its levels by label. It's the only place a level list's labels are shown: pages, jobs, events and notifications don't (R87b), so a level's label isn't a `labels` entry. To name the values on pages and in payloads too, label them in `labels` as well, as any field's; the two are independent. Values are compared exactly as extracted (P3), and a value not listed (say `"10"`, or `"03"` for `"3"`) is no level: never at or above any, so a filter doesn't fire on it until you add it. It's separate from `large_event_fields`: a field can have either, or both. The example agency lists `alarm_level` from `1` "First alarm" to `9` "Ninth alarm".

  Problems that stop the server and fail `validate-profile`: a field no format extracts or a list field (as for `large_event_fields`), the same field in two rules, no values, a value listed twice or with a space at either end, a blank label, two levels with the same label, and a rule or value without its keys or with any other key.

**What re-parses** (R23a, ADR 0045). A changed agency profile re-parses every page at start-up, which takes hours on a large archive, so changing what doesn't change a page re-parses nothing: the top-level `labels`, `levels`, `description` and comments (`_…` keys) are left out of the profile's hash. Changing a format (its comments included), `large_event_fields` or the name re-parses. So does changing the values in a file a format's `into` reads (ADR 0047), as they change what pages say; its comments and blank lines don't count. Adding `into` to a profile re-parses its pages once, which fills their `call_signs`: until the re-parse reaches a page, it keeps its old `units` and no `call_signs`.

A page whose capcode has an agency profile but matches no format is stored in full and flagged `parse_failed` (admins only).

## Golden tables

Files named `golden-*.json` hold raw decoder lines and what they must produce. `profile` names the profile the table tests.

- **Network tables** check `priority`, `stored_text` (null: dropped) and `heartbeat`. `deployment_codes` (a list of the codes `deployment_code_pattern` finds, or null for none) is checked only when a row lists it.
- **Agency tables** check `capcode`, `priority`, every standard field (`call_signs` too, so a row that doesn't list it expects null), `extra` (an object of extra fields), and the flags `parse_failed` and `address_unsplit`. `format` is checked only when listed. `opens_large_event` (`true` or `false`) is checked only when listed: whether the page, as an Emergency page to an alias using this profile, opens a large event on its own, by a value `large_event_fields` lists or a code the network's `deployment_code_pattern` finds (a job's number of pages isn't a single page's). Network tables can't check it.
- A key a row doesn't list must be null, or false for flags.
- Rows with `"status": "proposed"` are reported but don't fail the run.

## validate-profile

Runs without a database:

```
pagerarchive validate-profile [-v] [--network <name>] [--streets <file>] <dir>
```

It validates every profile under `<dir>`, compiles every pattern, and runs every golden table under `<dir>`. Agency tables run with the network profile (`--network` when there's more than one). `--streets` replaces the network's address dataset with a **street fixture**, a file of the same shape listing the made-up streets in the golden rows. The exit status is 0 only if every confirmed row passes.

A private profile repository can test itself with the published image:

```
docker run --rm -v "$PWD:/profiles:ro" ghcr.io/rikiku/pagerarchive:latest validate-profile --streets /profiles/golden/fixture-streets.json /profiles
```

or with a Go toolchain:

```
go run github.com/RIKIKU/PagerArchive/cmd/pagerarchive@latest validate-profile --streets golden/fixture-streets.json .
```
