# ADR 0040: The `group` field is now `call_sign`

## Status

Proposed (2026-11-17). The owner: "I think we should rename the property `Group` to `Call Sign`. It's confusing because we do have a thing ... called a group. It's a group of brigades in each district there are several groups." First asked for as a label only, like `units` shown as Appliances; then, the same day, as a rename everywhere: "That's going to get really confusing later. If we ever add a feature to list what group a brigade is a member of." SPEC.md R8 (with R31, R33, R33a, R63a, A1, A4 and section 10), with A121. On the branch `callsign-label` from main; the pull request is labelled `feature`.

## Context

The standard field `group` held the code a page names its unit by, often a short code at the end (`#STN1`): the call sign of the unit paged, not a group of anything. A network can also have real groups (brigades or stations in a district, several groups to a district), and a feature may one day list which group a brigade is in. With the call sign called `group` in the profile, the database, the API and the module, that feature would have nowhere to put its own word.

The name was in: the standard fields (`internal/profile`), every agency profile's captures (`(?P<group>…)`), its `fields` options, `labels` and `large_event_fields`, golden tables, the database column `messages.paging_group` and its index, integration filters saved as JSON (`{"group": …}`), the API's messages, search parameters (`group`, `group_like`) and search values, integration payloads (`fields.group`), the web app's search key (`group:`) and addresses (`?group=`), and the PowerShell module's `-Group` and `Group`.

Installs already hold pages, integrations and private profiles that say `group`; the owner's private profile is in a repository of its own, upgraded on its own time.

## Decisions

### 1. One name everywhere: `call_sign`, shown as "Call sign"

- **Profiles and golden tables**: the standard field is `call_sign` (`profile.StandardCallSign`). The example profile captures `(?P<call_sign>…)` and its golden table expects `call_sign`, with the same values.
- **Database**: migration 00028 renames `messages.paging_group` to `call_sign` and `messages_group_idx` to `messages_call_sign_idx`, and rewrites each integration's saved filter from `group` to `call_sign`. Values aren't touched; Down reverses all three. sqlc's names follow (`CallSign`, `SearchCallSigns`).
- **API**: messages carry `call_sign`; `GET /api/v1/search/values` returns `call_sign` beside `alias` and `agency`; search takes `call_sign` (exact, ignoring case) and `call_sign_like`; an integration's filter is `{"call_sign": …}`.
- **Integration payloads** carry `fields.call_sign`, and the payload's `version` is **2** (docs/integrations.md), so a receiver can tell. The large-event payload shares the version number, so it says 2 too, unchanged otherwise.
- **Web app**: the search key is `callsign:` (one word, as keys are), with its own help, hint ("a call sign") and suggestions heading ("Call signs"); the address parameter is `callsign=`; a message's detail view says "Call sign"; its row shows the value with a visually hidden "Call sign" and the same as its tooltip; the integration form's filter field is "Call sign".
- **PowerShell module**: `-CallSign` on `Get-PagerArchiveMessage` and `Watch-PagerArchiveMessage`, output property `CallSign`, shown as "Call sign" in the list view; `ModuleVersion` 0.17.0.
- **Docs**: SPEC.md, docs/profiles.md, docs/integrations.md, docs/powershell.md. Earlier ADRs are records of their time and keep the word they used.

"Group" isn't used for a call sign anywhere new; the old spellings below are the only places it's still read.

### 2. The old name keeps working where it was written before

Nothing an install or a person already has stops working on upgrade:

- **Profiles**: an agency profile that still names `group` (a capture, in `(?P<group>` or `(?<group>`, but not after a backslash; a `fields` option; a `labels` table; a `large_event_fields` rule) is read as `call_sign`. Each place is a **warning**, not an error: `validate-profile` prints `warning: <file>: at <path>: the call sign's field is now call_sign (ADR 0040): rename group to call_sign` and passes, and the server logs the same at start. A format, `fields` or `labels` naming both `group` and `call_sign`, or an `address_split` writing `group`, is an error: which one was meant can't be told. The profile's hash is the file's, so an unchanged profile re-parses nothing.
- **Golden tables**: a row expecting `group` expects `call_sign`, with a warning; the schema keeps `group` as a described old name. A row with both fails.
- **Search**: the web app reads a typed `group:` as `callsign:` and an address's `group=` as `callsign=` (bookmarks, shared links, the list remembered in the browser), and writes `callsign=` from then on. `group:` isn't suggested or shown in the help.
- **Module**: `-Group` is an alias of `-CallSign` (guidelines: other spellings go in `[Alias()]`).

### 3. The API reads the old names but answers only with the new

The API accepts `group` and `group_like` as search parameters (added to `call_sign` and `call_sign_like`), and `group` in an integration's filter when it's created or changed (moved to `call_sign` before it's saved, so it's never stored or returned). Its answers carry only `call_sign`, with no `group` beside it for a release.

Why: what callers send is what breaks silently (a script's `?group=`, an old module's `group_like`, a saved request body): accepting it costs a few lines and no word in any answer. Answering with both would double the field on every message, keep "group" in the API that the rename is meant to free, and help only a caller that reads `group` from an answer: the web app and the module ship with the API, and an integration receiver reads the payload, whose version says the field moved.

### 4. Shown by A121, and by A77 across the upgrade

- A121 (SPEC.md): in the browser, the row, detail view and integration form say "Call sign", `callsign:` is offered and `group:` isn't, and `callsign:<sign>`, a typed `group:<sign>` and an old `?group=<sign>` list the same pages (`web/tests/e2e.spec.ts`, "A121 call sign"); at the API level (`internal/acceptance/a121_test.go`), answers and the payload say `call_sign` and never `group`, the old parameters search, and a filter sent as `group` is stored and returned as `call_sign` and still selects; `validate-profile` warns about, and still checks, a profile and golden table from before (`cmd/pagerarchive`, `TestValidateProfileOldCallSign`; `internal/profile`, `TestOldCallSignName`).
- A77 (upgrade from the last release, v1.15.0 when this was written): the seed's integration now has a call-sign filter, sent as `group` (what that release takes); the seed checks it and the pages' call sign there, and after the upgrade each page's call sign and the filter must be the same (read as `group` before and `call_sign` after), `call_sign=STN1` and `group=STN1` must both find the pages from before and the new one, and the new page must still reach the filtered integration. There are no saved searches on the server to carry over: a search lives in the page's address (R33a), which the web app reads as above.

## Consequences

- **The private profile repository** isn't changed here. It keeps working, with warnings, until it's changed: rename each `(?P<group>…)` capture to `(?P<call_sign>…)`; any `group` key in a format's `fields`, in `labels`, or as a `large_event_fields` rule's `field`, to `call_sign`; and each golden row's `"group":` to `"call_sign":`, with the same values. Then `validate-profile` prints no warning. That change alters the profile's hash, so its pages re-parse once, to the same values.
- **Integration receivers** that read `fields.group` get `fields.call_sign`, with `"version": 2`; the release notes need to say so.
- **The module**: 0.17.0 reads `call_sign` and sends `call_sign_like`, so it needs a PagerArchive with this change: an older one answers with `group`, which it doesn't read, and ignores `call_sign_like`. Upgrade PagerArchive first, then the module. Scripts using `-Group` keep working; scripts reading the `Group` property read `CallSign` instead.
- **"group"** is free for a group of brigades or stations, if that's ever wanted.
