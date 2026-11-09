# ADR 0023: The search box (M21)

## Status

Status: Proposed. The owner's brief for M21 of 2026-11-09: R33a as amended that day (Suggestions, Help, Clear all, Dates) and A82, built to the approved prototype (`search.html`, made-up data) with the Clear all icon variant. On the branch `m21` from main at 0379d89 (v1.0.0), to be released as 1.1.0. The PowerShell module doesn't change, so its version stays.

## Context

Since M13 the box suggested values only after a key, from `GET /api/v1/search/suggest?key=…&q=…`: one request per keystroke (debounced), and for `group:` a loose index scan of the messages' group index each time. Production has about 2,300 aliases, and M21 adds suggestions for words with no key, which look in aliases, agencies, groups and categories at once. The dates were two date fields, From and To, beside the box (behind a Dates button on phones), and Help was a text button.

## Decisions

### 1. The values are asked for once, and matched in the browser

`GET /api/v1/search/values` (signed-in users, like the rest) returns every alias name, template agency and paging group, in order: `{"alias": [...], "agency": [...], "group": [...]}`. Categories come from `meta`, as before. The browser asks when the box is first focused and again after a minute (`web/src/lib/searchValues.ts`), so typing makes no requests, and none scans messages. 2,300 names are about 50 KB of JSON once per page load, against a request for every keystroke.

It replaces `GET /api/v1/search/suggest`, whose only user was the box: `TestSearchSuggestions` became `TestSearchValues`, and A29's list of signed-in-only endpoints names the new one. The groups are still found with a loose index scan (one step per group), now once per page load rather than per keystroke.

### 2. What's suggested (`web/src/lib/search.ts`, `suggestions`)

- **Focus or a click:** the suggestions for the word at the caret; in an empty box, or between words, every key with a few words each (flag: for admins only). (WebKit can deliver the focus after the first input, so focus doesn't always mean the keys.) Picking one puts it in at the cursor, as a word of its own, and lists its values.
- **After a key:** its values containing what's typed, at most 20. Values that start with it come first, then those with a word that starts with it, then the rest, each in order, so `alias:sta` gives Station 1 before Upstairs Station.
- **A word with no key:** "Search text: …" first (picking it is Enter), then the keys starting with the word (so `fl` still offers `flag:` to admins, A61), then at most 8 values from aliases, agencies, groups and categories, in that order and ranked as above, under a heading per kind. Never capcodes or job numbers (there are no capcode or job values to suggest from).
- **Picking a value** replaces the word with the filter and searches, as Enter does (A82: "picking the first lists only that alias's pages"). An alias, agency, group or category value is always quoted (`group:"STN1"`), because quotes are what make a value match exactly (R33a's Partial values); R33a's "quoted when the value has a space" is the case where the quotes also hold the value together. Priority, `is:` and `flag:` values aren't quoted.
- What was typed after picking stays in the box: the box takes the address's search only when the address changes to a search the box didn't just send (Back, a link, the breadcrumbs).
- The count is announced ("9 filter keys", "3 suggestions"); options are grouped with `role="group"` and a heading, as in the APG's grouped listbox.

### 3. Help and Clear all are icon buttons with tooltips

Help uses the existing `help` icon (Tabler's info-circle) and Clear all Tabler's `filter-x`, as in the prototype; the calendar's months use `caret-left` and `caret-right`, because `chevron-right` already means a closed menu group (R77a: one icon, one meaning). The tooltips are hidden from assistive technology, since the accessible names say the same; Escape hides them (WCAG 1.4.13); they appear after a moment and go at once. Clear all keeps its place when hidden (`visibility: hidden`), so the row doesn't move when it appears. It navigates to the view's own address with no search (on the message list, the plain list), which is a new history entry, so Back restores the box and the dates. Focus goes to the box.

### 4. The calendar (`web/src/lib/DateRange.svelte`)

One month, Monday first; days after today (in the deployment's time zone) can't be picked. First pick is From, second To, swapped if earlier; the same day twice is one day; a third pick starts again; closing after one pick is that one day. The bar is previewed as the pointer moves, and as the arrow keys move the focus. The days are a `role="grid"` of buttons with one in the tab order (arrows, Home and End, Page Up and Page Down, Shift for a year); Enter picks; Escape or Done closes and returns focus to the Dates button.

The search box searches when the calendar closes with a different range; until then nothing is sent. The range is also in the form as hidden `from` and `to` fields, so the capcode report's GET form sends it with Run report, as before. Its default range (the last 30 days) is shown as text beside the icon. A reversed range can no longer be made in the browser; one typed into the address is still reported by the page, and the API isn't asked (the "To can't be before From" tests).

### 5. Tokens

New tokens in `app.css` (R82c): `--icon-size-large` (the icon buttons' icons), `--tip-gap`, `--calendar-width`, `--calendar-day`, `--calendar-mark`, `--calendar-bar-inset` and `--preview-opacity`. The tooltip and icon-button styles are shared rules in `app.css` (`.has-tip`, `.tooltip`, `.icon-button`).

## Evidence

A82 (`web/tests/e2e.spec.ts`), in Chromium, Firefox and WebKit, by mouse in light mode and by keyboard alone in dark mode, with axe at each step; A61, the print page's search, A69 and the date tests updated for the new controls. Screenshots: the Playwright report's `a82-*` files.
