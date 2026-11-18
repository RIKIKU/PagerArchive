# ADR 0045: Integrations at or above a level, and a parse hash without display keys

## Status

Status: Proposed (2026-11-18). The owner's decision of 2026-11-18: "the integrations should be able to fire on [an agency's] alarm level", at or above a chosen level, with the levels shown in the integration form only: pages, the job view and notifications still show no level (R87b). R7, R49a, A126. No migration. `VERSION` and the compose image stay as they are (ADR 0024); the pull request is labelled `feature`.

## Context

Some agencies dispatch with a level on each page (a first alarm, a second, and so on). A profile already extracts it as an extra field (the example's `alarm_level`), and `large_event_fields` (R7, ADR 0029) lists the values that open a large event. An integration's filter (R49) couldn't use it: there's no ordering, and nothing says what the values are called.

Separately, the profiles' hash (section 14, R23a) was each agency profile's whole file, so changing a label re-parsed the whole archive: about a day on a production one. Adding levels to a profile would do the same, though neither changes what a page says.

## Decisions

### 1. The profile key: `levels`

```json
"levels": [
  {
    "field": "alarm_level",
    "label": "Alarm level",
    "values": [
      { "value": "1", "label": "First alarm" },
      { "value": "2", "label": "Second alarm" }
    ]
  }
]
```

An array of rules, like `large_event_fields`, so an agency with two ordered fields can list both. Each rule names a text field the formats extract, what the field is called, and its values **lowest first**, each with a label. Order is the list's order, not a number or a pattern: the app can't know that `3A` comes after `2A`, and values are compared exactly as extracted (P3). A value the list doesn't have (a new `10`) is no level: never at or above any, so a filter never fires on it until it's added, which shows in the profile.

It's separate from `labels` on purpose: `labels` are shown wherever a field's value is (a message's detail, the payload's `labels`), and a level's label must show only in the integration form (R87b). A profile that also wants a level field's values named on pages labels them in `labels` too (owner, 2026-11-18): the two are independent, and the app doesn't copy one into the other. It's separate from `large_event_fields` too: what opens an event and what an integration may filter on are different questions, and one can be set without the other.

**Validation** (schema, then the loader, naming the file and JSON path; the server stops and `validate-profile` fails): `field`, `label` and `values` required, and each value's `value` and `label`; a field no format extracts, a list field, the same field in two rules, a value listed twice or with a space at either end, a blank label, and two levels with one label are refused.

### 2. The filter: one level of one agency profile

```json
"filter": { "level": { "profile": "example-fire", "field": "alarm_level", "at_least": "2" } }
```

Stored in the integration's filter JSON with the other criteria (no migration). A page matches when its template's agency profile is `profile`, and its `field`'s value is `at_least` or a value after it in the profile's order as loaded now. It combines with the other criteria by AND, as they all do.

**The level names its own agency.** A level field belongs to an agency profile, but the filter's Agency criterion is free text matched against the template's agency name (R33), which can't say which profile is meant. So the level filter carries the profile: with a level chosen, only that profile's pages can match, whatever the Agency field says (it still narrows, by AND). Pages of other profiles, of capcodes with no alias or profile, with no value, or with a value not listed don't match. One level per integration: an owner who wants two agencies' levels makes two integrations (R47 allows several for the same pages).

**Saving** checks that the profile is loaded, has a level field by that name, and lists `at_least`; otherwise it's refused (422) with the reason. **A profile changed after saving**: matching uses the profile as loaded now, so a level renamed or removed matches nothing until the integration is edited, and the form says the saved field isn't in the profiles now.

**Escalation.** An integration sees each page on its own (R49): a job's later page at a higher level matches by that page, and is sent. Nothing is added to stop it: an integration sends a page at most once (R51b, by the page's ID, however many receivers heard it), a repeat of the same page within 10 seconds is one page (R19), and every capcode's copy is its own page (R20), as for every other filter.

### 3. The form, and nowhere else

`GET /api/v1/integrations/levels` (any signed-in user, while integrations are on) lists each loaded agency profile's level fields: the profile, the agency (the profile's `description`, or its name), the field, its label and its levels by label, lowest first. The integration form shows a **Level** group when there's one: **Field** ("Any level", or "Example Fire Service: Alarm level") and, once a field is chosen, **At or above**, listing the levels by label (a new field starts at its lowest). It's the only place levels are shown. Nothing else changes: the message list and detail (where the level's raw value already shows as an extra field, R8), the job view, events, the integration's card, and every delivery. The payload (`extra`, `labels`) is as it was: a Full payload already carries the page's level as `extra.alarm_level` (and in `text`), as before, and never a level's label; a Summary payload and a preset with Summary content carry no level at all.

The PowerShell module doesn't manage integrations, so it has nothing to add.

### 4. The profiles' hash leaves out display-only keys

An agency profile's hash, which decides whether pages are re-parsed (R23a), is now its file's with its top-level `labels`, `levels`, `description` and comment (`_…`) members cut out, as the network's leaves out `areas` (R88d). Each is display or filter only: the pipeline never reads them, and nothing stored with a page depends on them (payload labels are looked up when a delivery is built). Everything else counts: the formats (with their own comments), `large_event_fields` (it sets a stored flag and the events, R87f) and the name. The cut is textual, member by member, so a profile without those members hashes exactly as its file.

**The upgrade re-parses nothing.** Every existing install saved the old hash (whole files), and most profiles have labels, so the new hash differs from the saved one for unchanged profiles. The worker is given both: the new hash and the old way's (`reparse.ProfileHashes` returns both). At start-up a saved hash equal to the new one is unchanged, as before; one equal to the old way's, worked out from the same files, means the profiles haven't changed since the last start either, so the new hash is saved in its place and nothing is re-parsed (logged once). Anything else is a change and re-parses, as ever. So the upgrade itself costs nothing, and only a later change to what a page says re-parses.

## Tests

- `internal/profile`: the example's levels and their order (`Rank`, an unlisted `10` and `02`); every validation error with its path; the hash unchanged by a label, a level's label, the field's label, a level more, the description, a top-level comment and the levels removed, and changed by a format and `large_event_fields`; a profile with none of those members hashing as its file; the call sign rename's hashes from the raw file.
- `internal/integrations`: the filter at levels 2, 3, 9 (match), 1, none, `10`, `02`, another profile, no profile, a level, field or profile no longer there (no match), and with another criterion; saving refuses an unknown profile, field or level; the form's choices.
- `internal/acceptance/a126_test.go`: A126 at the API (`TestA126LevelFilter`: the levels offered; refusals; a Full webhook and a Summary ntfy at Second alarm on Station 1 fire for the level 2 and 3 pages only, none naming a level's label, the payload's `extra` and `labels` as before, the notification with no level); `TestA126ProfileHashLeavesOutLabelsAndLevels` (labels and levels changed: nothing re-parsed; a format changed: re-parsed; an old-way hash of the same files: nothing re-parsed and the new hash saved; of other files: re-parsed).
- `web/tests/e2e.spec.ts`: A126 in the browser (each project): the form lists the field and its nine levels by label, saves Second alarm, gets the level 2 and 3 pages and not 1 or none, keeps the choice when edited again; the card, the message list and a page's detail show no level's label.
