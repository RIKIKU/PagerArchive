# ADR 0047: Call signs apart from appliances

## Status

Accepted (2026-11-19). The owner chose option (b) and decided the four questions below (see "The owner's decisions"); built as decided. Proposed on 2026-11-18, design only. The owner's rules (2026-11-17/18): a listed call sign alone is a call sign, not an appliance; a call sign with a number or a suffix is an appliance; the roles a person holds go with the call signs ("a bit more like a callsign than an appliance"), with no Roles list of their own. The network's list of call signs is private reference data, so the engine takes it from the profile and never has it built in. The owner chose to batch the private profile's `group` to `call_sign` rename (ADR 0040) with this, so production re-parses once. SPEC.md R7, R8, R23a, R38h, the glossary, section 7.13, section 10 (proposed), A128 and M27. The pull request is labelled `feature`.

## Context

`units` (R8, shown as Appliances) is a list a format splits out of a page. On a real network it holds more than appliances. In made-up terms, a page's list reads `P25 PT31 CHILL CVALE DO01`: appliance codes (letters and digits: `P25`, `PT31`, `P35A`), call signs of the units dispatched (letters only: `CHILL`, `CVALE`), and roles a person holds that week (a duty officer's code, say, and district roles that follow a pattern). On a production-sized archive the call signs are a large share of the list (thousands of distinct values, on more pages than the appliances), so Appliance activity (R38h) and Job size's appliances per job (R38f) count units and people as appliances.

`call_sign` (R8, ADR 0040) is one value: the call sign of the unit a copy was paged to, often at the end of the page (`#CHILL`). Each copy of a page sent to several capcodes is its own message with its own call sign (R20, A1). A page can also name several dispatched call signs in its list, usually its own among them.

A token's kind can't be told from its shape alone in general: on the real network a call sign is letters only and an appliance has digits, but a call sign plus a suffix (`CHILLPT`, `CHILLR`) is an appliance, and a role may be letters only too. What can tell them apart is the network's list of call signs (about 1,400 entries on the real one) and a pattern for roles. Both belong to the private profile.

## Options weighed

### (a) `call_sign` becomes a list

The dispatched call signs and roles join the paged unit's call sign in one list.

- **A page shows** a list where it showed one value, in the message list's row (R31) and the detail view; which one the copy was paged to is lost, so A1's "each with its own call sign" no longer reads off the field.
- **Search** `callsign:` matches any element. **Integrations' filters** on a call sign would then fire for every copy of a page that names it: one page sent to two capcodes that both name `CVALE` tells a `CVALE` integration twice (R20, R49), where today it's told once.
- **Reports**: appliances are fixed as in (b); nothing counts call signs.
- **API and payload**: `call_sign` changes from a string to an array: a breaking change for every caller and receiver, so the payload's version goes to 3, and ADR 0040's `group` compatibility has to be redone for a list.
- **PowerShell**: `CallSign` becomes an array; scripts comparing it to a string break.
- **Profile syntax**: the same `into` as (b), aimed at `call_sign`; the end-of-page capture and the moved tokens both write it.
- **Golden tables**: every agency row's `call_sign` becomes a list (or a string is read as a list of one), the example's and the private profile's alike.
- **Migration and re-parse**: the column's type changes from `text` to `text[]`, a rewrite of the whole `messages` table, which R91a keeps out of a migration: a new column, a background long job (R91) to copy every page's value into it, then the old one dropped, besides the re-parse. The re-parse only reaches profiles that changed, so the copy is needed for every other page.

Most cost, and it muddles two facts (who this copy was paged to, and who the page dispatches) in one field.

### (b) A new standard list field, `call_signs`, beside `units` (recommended)

- **A page shows** Call signs after Appliances in its detail view ("T1, STN2T" / "STN2, DO01"); the row keeps the paged unit's call sign only (R31). The display name is generic: it's the existing word "call sign", not any agency's word for its stations.
- **Search**: no key for it in this change. Its tokens are in the page's text, so text search finds them already, and `callsign:` stays the paged unit's call sign (see the owner's decisions below). Being a standard, stored field, a key, suggestions or an integration filter can be added later with no re-parse.
- **Integrations' filters**: unchanged (`callsign:` as today), so no integration starts firing twice.
- **Reports**: R38h, R38f and R38a's Jobs count `units` only, so once pages are re-parsed call signs and roles drop out of the appliance counts: the point of the change. No report counts call signs yet.
- **API and payload**: messages gain `call_signs` (an array or `null`) beside `units`. A Full payload's `fields` gains `call_signs`; the version stays 2, because a key is only added and receivers ignore keys they don't read. `fields.units` holds fewer tokens for a profile that moves them: the release notes say so. Summary payloads and large-event payloads are unchanged (they carry no units).
- **PowerShell**: messages gain `CallSigns` (an array), shown as "Call signs" after Appliances in the list view. An older module ignores the new field and a newer one reads `null` from an older API: compatible both ways. `ModuleVersion` rises with the code.
- **Profile syntax**: `into` on a split field (below).
- **Golden tables**: `call_signs` is a standard list key of agency rows. A row that doesn't list it expects `null`, so no existing row of a profile without `into` changes.
- **Migration and re-parse**: one quick migration adds a nullable `messages.call_signs text[]` (no default, so no rewrite: R91a). Nothing is copied into it: the re-parse a changed profile already causes fills it, as a long job (R91). One full re-parse of the private profile's pages, shared with the `group` rename.

### (c) An extra field the profile names, with only a routing option

The private profile moves the tokens into an extra field of its own, named with the agency's own word; the only engine change is `into`.

- **A page shows** the field with the other extra fields, after Location, named from the key (`humanise`), so the private profile can use the agency's own word and the public repo never sees it.
- **Search**: text only, as for every extra field (section 14); no key, no suggestions. **Integrations' filters**: text terms only, which match parts of words (a text term `CHILL` also finds `CHILLPT`).
- **Reports**: appliances are fixed as in (b).
- **API and payload**: the tokens appear under `extra` (and a Full payload's `extra`) as a list; no new key. **PowerShell**: under the message's other parsed fields.
- **Golden tables**: rows list it under `extra`.
- **Migration and re-parse**: no migration; the same one re-parse.

Cheapest, but every network names the list differently, so the app can't build anything on it (a key, a filter, a report, the job view). Promoting it to a standard field later would mean another re-parse of the whole archive.

### Why (b)

Dispatching units by call sign isn't one network's habit: any paging network can name the units it sends, and the spec already has the concept (Call sign). (b) gives the moved tokens one generic home that the API, payload, module and any later feature can rely on, with additive changes only (a nullable column, a new key, a new property), while (a) breaks the API, the module and every golden row and needs a backfill, and (c) leaves the data where nothing generic can use it. The routing option is the same in (b) and (c), and (b)'s version of it can still aim at an extra field, so a network that wants a list of its own isn't stopped.

## Decisions

### 1. The standard field `call_signs`

A list field, like `units` (`profile.ListFields`): the call signs of the units a page dispatches and the roles it names, apart from its appliances. Shown as **Call signs**. It's filled by a format that captures it (`(?P<call_signs>…)` with a `split`) or that moves tokens into it (decision 2); otherwise it's `null`. `call_sign` is unchanged: one value, the paged unit's.

### 2. The profile option `into`, on a split field

```json
"fields": {
  "units": {
    "split": " ",
    "into": {
      "field": "call_signs",
      "values_file": "example-call-signs.txt",
      "match": "DO[0-9]{2}"
    }
  }
}
```

- `field`: the list the tokens move to: `call_signs`, `units`, or an extra field's name.
- `values`: a list of known values, in the profile. `values_file`: the same in a file beside the profile (relative to it), plain UTF-8 text, one value per line; each line is trimmed, and blank lines and lines starting with `#` are skipped. Name it `.txt`, so it's never read as a profile. Either or both; a file suits a long list that several agency profiles share.
- `match`: an RE2 pattern a **whole** token must match (it's anchored as `^(?:…)$`), for values that follow a shape, such as roles.
- At least one of `values`, `values_file` and `match` is given.

After the split (elements trimmed, empty ones dropped, as now), each token that is one of the values, or matches the pattern, moves to `field`; the others stay. Both lists keep the tokens' order; a list left with no tokens is `null`. Values are compared exactly as extracted (P3): `CHILL` moves, `CHILLPT` and `chill` don't. That one rule gives the owner's: a listed call sign alone is a call sign; with a number or a suffix it's no longer a listed value, so it stays an appliance. Roles come from `match`, or from `values` for a fixed code. One target is enough for the owner's rules (no separate Roles list); `into` can later take an array of these objects if a second target is ever wanted.

Problems that stop the server and fail `validate-profile`, as for `large_event_fields` and `levels`: `into` without `split` beside it; no `field`; a `field` that is the split field itself, a single-value standard field (`job_number`, `call_sign` and the like), or a field the same format also captures; none of `values`, `values_file` and `match`; an empty `values`; a value empty, listed twice (across `values` and the file too) or with a space at either end; a value the split pattern would split (it could never be one token); a `values_file` missing, unreadable or with no values; a `match` that doesn't compile; any other key. A `large_event_fields` or `levels` rule naming the target fails as a list field does now.

### 3. What re-parses

The profile's parse hash (R23a, ADR 0045) counts `into`, as part of its format, and the **contents** of each `values_file`, so editing the list of call signs re-parses the profile's pages, as it changes what they say. Nothing else changes in the hash.

### 4. Shown, sent and counted

As option (b) above: the detail view, the API's messages, a Full payload's `fields` (version 2), the module's `CallSigns`; not the message list's row, the print sheet, the job view, large events or Summary payloads. Appliances in R38a's Jobs, R38f and R38h are `units` only. The search box and integrations' filters don't change.

### 5. Migration

One quick migration: `ALTER TABLE messages ADD COLUMN call_signs text[]` (nullable, no default, no index), and Down drops it. No backfill: until a profile with `into` re-parses its pages, every page's `call_signs` is `null`, as before the change. The reports' counts (migration 00023's job tables) change by the re-parse, as they do for any change to `units`.

### 6. Shown by A128

SPEC.md A128, on section 10's example: the made-up call signs `STN1` to `STN4` in `example-call-signs.txt`, the role pattern `DO[0-9]{2}`, and a proposed golden row whose list `T1 STN2 STN2T DO01` gives units `[T1, STN2T]` and call_signs `[STN2, DO01]`. The example's golden rows are the owner's: the row is proposed, and no existing row changes.

## The owner's decisions (2026-11-19)

1. **Option**: (b), a new standard list field `call_signs` beside `units`. `call_sign` stays the paged unit's own call sign.
2. **Names**: `call_signs`, shown as "Call signs": agreed.
3. **Search and integrations**: no new search key and no integration filter on it ("I doubt anyone is going to want to search for the call sign or trigger an integration on it"). Text search finds its tokens in the page's text, as it does today; `callsign:` and the integrations' call-sign filter keep matching `call_sign` only.
4. **Payload version**: stays 2, as the field is only added.

## Consequences

- **The code**: `internal/profile` (the field, `into`, its validation, values files, the hash), the parser, migration 00033 (`messages.call_signs`), the store and its queries, the API's message, the payload, the web app's detail view, the module's object, list view and help (`CallSigns`, ModuleVersion 0.19.0), the profile and golden-table schemas, the example profile (`example-call-signs.txt` and a proposed golden row) and the docs (docs/profiles.md, docs/integrations.md, docs/powershell.md), with A128.
- **The schema**: a field's options may now hold `split` with `into` (and comments); `split` and `address_split` together are refused by validate-profile rather than by the schema's old one-key limit.
- **The private profile repository** needs, by key and shape only: in each agency profile whose format splits the appliance list, an `into` on that field's options: `{"field": "call_signs", "values_file": "<file of the network's call signs>", "match": "<RE2 pattern for the roles that follow a shape>"}`, with the fixed role codes (a duty officer's, say) either in that file or in `values`; the file of call signs itself, one per line, beside the profiles; golden rows whose lists name call signs or roles, proposed with those tokens moved from `units` to `call_signs`; and, in the same change, ADR 0040's rename (`group` to `call_sign` in captures, `fields`, `labels`, `large_event_fields` and golden rows). Then `validate-profile` passes with no warning, and one change of the hash means one re-parse.
- **The re-parse**: one full re-parse of the private profile's pages, hours to about a day on production, as a long job (R91): observable, pauseable, resumable. It sends nothing to integrations and rebuilds large events and report counts as any re-parse does. Until it reaches a page, the page keeps its old `units` and no `call_signs`.
- **Integration receivers** that read `fields.units` for a network that moves call signs see fewer tokens there and the rest in `fields.call_signs`; the release notes say so.
