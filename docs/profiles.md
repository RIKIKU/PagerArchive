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
- Each **named group** becomes a field. Standard fields: `job_number`, `event_type`, `description`, `address`, `locality`, `grid_ref`, `units` (a list), `group`. Any other name is an extra field. Groups whose names start with `_` are intermediate and never stored. Values are trimmed; empty values are null.
- `fields.<group>.split`: a pattern that turns the value into a list. `""` splits into single characters.
- `fields.<group>.address_split: {"description": "...", "address": "..."}`: for formats where a description runs into an address with no delimiter. The address is the longest suffix that reads `[house number] <street> <locality>`, with the street listed in that locality in the address dataset. Everything before it is the description. If no street matches, the whole text is the description, the address is null, and the message gets the admin-only `address_unsplit` flag. See ADR 0001 section 2.
- `labels`: display labels per field, e.g. `{"event_type": {"HSF1": "House fire"}}`.

A page whose capcode has an agency profile but matches no format is stored in full and flagged `parse_failed` (admins only).

## Golden tables

Files named `golden-*.json` hold raw decoder lines and what they must produce. `profile` names the profile the table tests.

- **Network tables** check `priority`, `stored_text` (null: dropped) and `heartbeat`.
- **Agency tables** check `capcode`, `priority`, every standard field, `extra` (an object of extra fields), and the flags `parse_failed` and `address_unsplit`. `format` is checked only when listed.
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
