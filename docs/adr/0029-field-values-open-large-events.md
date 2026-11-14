# ADR 0029: Field values open large events

## Status

Status: Proposed (2026-11-14). The owner's decision of 2026-11-14: some agencies dispatch with an alarm-level system, each page carrying a level, and a high enough level means something big, so an agency profile may say which values of one of its fields open a large event (R7, R87, A88). `VERSION` and the compose image stay as they are (ADR 0024); migration 00021 makes the next release a minor version.

## Context

R87 opened a large event by a code matching the network profile's pattern (R5) or by a job's number of Emergency pages. An alarm level is neither: it's a field one agency's formats extract, and what counts as high is that agency's convention, so the rule belongs in the agency profile, beside the formats that extract the field. Otherwise the page must behave exactly as a page with a code: the same pages count (Emergency, to an aliased capcode, not a heartbeat), the same 12-hour grouping, merging, out-of-order handling and retention (R87a, R87e), one delivery to each large-event integration when the event opens (R87d), and nothing in the app saying what opened an event or showing a level (R87b; owner, #34).

## Decisions

### 1. The key: a list of values

```json
"large_event_fields": [ { "field": "alarm_level", "values": ["3", "4", "5", "6", "7", "8", "9"] } ]
```

Each rule names one text field and lists every value of it that opens an event. The alternative, `"at_least": "3"` with an `"order"` list, was considered and left out:

- **The order list is the same length as the values list.** An order has to name every level anyway (the app can't know that `3A` comes after `2A`), so `at_least` adds a second key without saving any typing.
- **Fewer ways to be wrong.** With an order, a value seen on the air but missing from the list is neither above nor below the threshold, and the profile has to say what that means; `at_least` naming a value not in `order` is another error. With a list, a value opens an event if and only if it's listed: an unlisted level (say a new `10A`) opens nothing until it's added, which is visible in the profile and testable in its golden table.
- **Literal (P3).** Values are compared exactly as the format extracted them, with no case folding, number parsing or ranges, as every other profile rule compares text.

`values` is a list, not a map or a pattern: a pattern would duplicate the format's own `match`, and the levels that count are a short, fixed list. The key is an array of rules so an agency whose pages carry two such fields can list both; a page opens an event when any rule matches.

### 2. Validation

The schema checks the shape: `field` and `values` required, nothing else (keys starting `_` are comments), at least one value, each non-empty and listed once. The loader then refuses, naming the file and JSON path, as for every other profile problem (A14), so the server stops and `validate-profile` fails:

- a field **no format extracts**: not a named group of some format (groups starting `_` are intermediate) nor an `address_split` output;
- a **list field**: `units`, or any field a format splits (a list has no single value to compare);
- the **same field in two rules** (list all its values in one);
- a value with a **space at either end**: extracted values are trimmed, so it could never match.

A field that only the network's job number patterns fill isn't one an agency's formats extract, so it's refused too.

### 3. Where the check runs: when a page is parsed, stored with it

`pipeline.Parse` sets `Parsed.LargeEventField` from `Agency.OpensLargeEvent` after the formats run, for every page that has an agency profile. Ingest stores it in `messages.large_event_field` (migration 00021) with the other parsed columns, and a re-parse (R23a) writes it again with them, so it always follows the profile the page was last parsed with. Priority, alias and heartbeat aren't part of the flag: the events package applies those (`Page.Counts`), as for codes.

`events.Detector.Stored` treats the flag as it treats a code: `coded := p.LargeEventField || HasCode(...)`. Nothing else in the events code changes, so joining, merging, out-of-order pages, retention and the one delivery per opening are the code path's, unchanged. A page that can't count still makes no query (ADR 0027 §2).

**Why a column, not a check at ingest only.** Live detection needs only the parse result, which ingest has. But events are also rebuilt from stored pages when pages are re-parsed (R87f, on the branch `events-regroup`, in review as this is written): a rebuild reads pages from the database and must make what live ingest made, so it needs to know which stored pages a field value opened, without the alias, template and agency profile of each. Working that out again in SQL from the stored fields would repeat the profile's rule in the database. A boolean beside the parsed fields is one column, written where the fields are written, and a rebuild reads it as it reads the text for codes.

**The migration.** `ALTER TABLE messages ADD COLUMN large_event_field boolean NOT NULL DEFAULT false`: a constant default, so PostgreSQL rewrites no rows and a large archive upgrades at once. Pages stored before it read `false` until they're re-parsed; a profile that gains `large_event_fields` changes the profiles' hash, which re-parses every page at start-up (R23a), so they're flagged then. Whether those older pages then form events is the rebuild's business (R87f); on main without it, events are decided as pages arrive (ADR 0027, question 2). The down migration drops the column. A77 runs the upgrade from the latest release.

### 4. Silent in the app

No API field, payload key, page or setting names the trigger. The alarm level itself is an extra field like any other (R8: stored, searchable and shown in a message's detail), because the format extracts it; an event, its view and its deliveries never show it (A88 checks the event's JSON and the delivery's body).

### 5. Golden tables

Agency golden rows may list `"opens_large_event": true|false`, checked only when listed, so existing tables need nothing new: whether the page, as an Emergency page to an alias using that profile, opens an event on its own, by a listed field value or a network code. A network table listing it is a diff (it needs an agency's fields), not silently ignored. Four rows are **proposed** in the example agency's table (level 3 opens, level 2 doesn't, a Non-emergency follow-up naming level 3 doesn't, a code with no level opens); they pass.

### 6. The example profile

The example agency's emergency format gains an optional `ALARM <digit>` segment after the locality (`alarm_level`), and `large_event_fields` lists `3` to `9`. It fits the made-up format: the segment is optional, so every existing page and golden row parses as before, and the lazy locality stops before it because a digit can't be part of a locality. SPEC.md section 10 says so.

## Tests

- `internal/profile`: every validation error above, with its path; the example's rule; `OpensLargeEvent` (listed, unlisted, `10` not listed though higher, another field, a list, no agency); which fields a rule may name (a group, an `address_split` output; not a split field or an `address_split`'s own group).
- `cmd/pagerarchive`: `validate-profile` exits non-zero naming the file and path for an unknown field, an empty value and a malformed rule; the example's 8 agency rows pass.
- `internal/golden`: `opens_large_event` rows (listed and lower levels, no level, a code, Non-emergency), mismatches caught, a network row listing it refused.
- `internal/events`: a page with a listed value that doesn't count (Non-emergency, unaliased, a heartbeat) makes no query.
- `internal/acceptance/m22_fields_test.go`: A88 (`TestA88FieldValueOpensAnEvent`: level 2, unaliased and Non-emergency pages open nothing; level 3 opens an event holding the job's level 2 page too, one delivery within 5 seconds naming no level, no levels or codes in the event's JSON; a code and level 5 join it and send nothing; after 12 quiet hours level 4 opens a new event and a second delivery); `TestFieldValueJoinsCodeEvents` (a code's event joined by a level, a level's event merged with a code's by a page between them); `TestFieldValueFollowsReparse` (the stored flag follows a changed rule on re-parse).

## Follow-up

1. **Rebuilt events.** Once R87f's rebuild is merged, it must read `large_event_field` beside the text's codes (`Candidate.Coded`), and a re-parse must count a page with the flag as one that may open an event, or a rebuild would drop events a field value opened. That's a two-line change on whichever branch merges second.
