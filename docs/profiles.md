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
- Each **named group** becomes a field. Standard fields: `job_number`, `event_type`, `description`, `address`, `locality`, `grid_ref`, `units` (a list of the appliances a page names, shown as **Appliances**), `group`. The app calls the `units` field "Appliances" wherever people see it (owner, 2026-11-13), but its name stays `units` in profiles, golden tables, schemas, the API and the database, so existing and private profiles keep working. Any other name is an extra field. Groups whose names start with `_` are intermediate and never stored. Values are trimmed; empty values are null.
- `fields.<group>.split`: a pattern that turns the value into a list. `""` splits into single characters.
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

A page whose capcode has an agency profile but matches no format is stored in full and flagged `parse_failed` (admins only).

## Golden tables

Files named `golden-*.json` hold raw decoder lines and what they must produce. `profile` names the profile the table tests.

- **Network tables** check `priority`, `stored_text` (null: dropped) and `heartbeat`. `deployment_codes` (a list of the codes `deployment_code_pattern` finds, or null for none) is checked only when a row lists it.
- **Agency tables** check `capcode`, `priority`, every standard field, `extra` (an object of extra fields), and the flags `parse_failed` and `address_unsplit`. `format` is checked only when listed. `opens_large_event` (`true` or `false`) is checked only when listed: whether the page, as an Emergency page to an alias using this profile, opens a large event on its own, by a value `large_event_fields` lists or a code the network's `deployment_code_pattern` finds (a job's number of pages isn't a single page's). Network tables can't check it.
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
