# 12. Milestone M11: the print sheet

Date: 2026-10-29

## Status

Proposed. Waiting for the owner, with two questions (at the end).

## Context

M11 (SPEC.md R67, R68, check A60) adds a print sheet for teaching recruits to read pages:

- **R67.** Any signed-in user ticks messages wherever messages are listed (the message list, search results, the job view and the alias view) to add them to their print sheet, and unticks them there or on the sheet. The sheet keeps them in the order they were added, shows how many it holds, is kept in that browser until the user clears it, and stores message IDs only, never text.
- **R68.** The sheet prints as each message's text exactly as a pager shows it (R31), in full, and nothing else, each inside a heavy border with space between, never split across two paper pages, with no header or navigation, black on white, and can be saved as a PDF from the browser's print dialog. The user documentation says printed pages are outside PagerArchive's control.

What was there before M11:

- One row component, `MessageRow`, used by the message list (live rows included), search results (the list with a search), the job view and the alias view (and the capcode view, the alias view of a capcode with no alias, R35e). The text's link stretches over the whole row, and the alias and job links sit above it (R35d).
- `localStorage` held only the colour theme. The API had `GET /api/v1/messages/{id}` (the detail view, one message with receive times) and `GET /api/v1/messages` (the list, newest first, ignored aliases' pages hidden unless asked for).

## Already decided elsewhere (cited, not re-decided)

- Redaction happens before storing (P1), so a message's stored text, and so every view of it, is already redacted. The list shows `display_text`, the text without its priority marker (R31, A10).
- Every signed-in user sees every stored message; the role only adds admin-only details: flags (R35c) and receivers' names (R39).
- Styles use the tokens in `web/src/app.css` (CLAUDE.md). Pages are checked with axe in both themes (ADR 0006, 0007). Three engines: Chromium, Firefox and WebKit (R36).

## Decisions

### 1. Ticking (R67)

- **A tick box in `MessageRow`**, so every place that lists messages has it: the list and rows that arrive live, search results, the job view, the alias view and the capcode view. The map's list of located messages isn't a message list in R67's sense and has no tick box.
- **At the far end of the row's header**, with the visible label "Print" and the accessible name "Add to print sheet" (which contains the visible word, WCAG 2.5.3). It's a native checkbox, so Space toggles it and its state is announced. Like the alias and job links it's raised above the row's stretched link (`--layer-content`), so a tap on it never opens the detail view, and every R35d target is unchanged (A22 still passes). It's disabled for the moment before the signed-in user is known.
- **A message on the sheet is ticked everywhere** it's listed: the rows read the same shared state.
- **The link to the sheet**, "Print sheet", is in the main navigation for signed-in users, with the count in the same pill as the Admin link's waiting requests; its accessible name is "Print sheet, 4 messages". The M12 redesign decides its final place.

### 2. Storage (R67, A60)

- **`localStorage`**, key `pagerarchive:print-sheet:<user ID>` (the ID from `GET /api/v1/me`), value a JSON array of message IDs in the order added: `[2,3,1,4]`. Nothing else: no text, time, alias or any other field. The key is removed when the sheet is emptied.
- **Per user:** two people signing in to the same browser each have their own key, so neither sees the other's sheet. The sheet isn't removed at sign-out: R67 keeps it until the user clears it, and it holds only IDs, which show nothing without signing in as someone who can see the messages.
- **Reading is defensive:** only positive whole numbers are kept, each once, at most the maximum; anything else in the value is dropped.
- **Unavailable storage** (private modes, blocked site data, a full quota): every read and write is wrapped, and the sheet then works in memory for the page's life and starts empty after a reload.
- **Open tabs keep in step** through the `storage` event: another tab's tick updates this tab's rows, count and sheet. A cleared storage (event key `null`) is followed too.

### 3. The maximum: 100 messages

A sheet holds at most **100 messages**. A60's 30 messages print on 9 A4 pages here, so 100 is about 30 pages, more than a training session needs; and 100 IDs make one request (§4) of about 1 KB. When the sheet is full, ticking another message is refused: its box stays unticked, and a notice under the header says "The print sheet is full: it holds 100 messages. Remove some before adding more.", with a link to the sheet. Unticking always works.

### 4. Showing the sheet: `GET /api/v1/messages/by-id`

| Request | Who | Reply |
|---|---|---|
| `GET /api/v1/messages/by-id?id=2&id=3&id=1` | signed-in users (401 otherwise, A29) | `{"messages": [...], "missing": [...]}` |

- **Exactly the list's rules:** the messages come from the same search query and the same conversion (`toMessage(row, admin)`) as the list, the job view and the detail view. So the text, `display_text` and redaction are the list's, and flags are sent to admins only. Ignored aliases' pages are included (`Filter{Ignored: true}`), as in the detail and job views, because the alias view lists them and they can be ticked there. Receive times aren't sent: the sheet doesn't show them. There's nothing else a user can't see in the list: no per-user visibility exists.
- **In the order asked**, each ID once; `missing` lists the IDs that no longer exist (deleted by retention, R45, or anything else).
- **The limit is the sheet's, 100 IDs** (`server.MaxSheet`); none, more than 100, or one that isn't a positive whole number is `400`. Repeated `id` parameters, as the API's other several-value parameters (R63a).
- **Why a new endpoint:** `GET /api/v1/messages` hides ignored aliases' pages unless asked and sorts by time; 100 calls to `GET /api/v1/messages/{id}` would each also look up receive times.
- **Logging:** errors only, as for the list. IDs aren't page text.

### 5. The sheet (R67)

`/sheet` shows, on screen: the heading, how many messages it holds, **Print** (the browser's print dialog), **Clear** (after the browser's confirmation, "Remove all 3 messages from the print sheet?"), the R68 note, then the messages in the order added. Each shows its time and alias (linking to its detail view), its text as the list shows it, and **Remove** (accessible name "Remove the message from Station 1 at 29 Oct 2026, 12:11:46").

A message the API reports missing leaves the sheet, and a note says "2 messages were no longer available and have been removed from the sheet." It's dropped only when the API answered and listed it as missing; a failed request changes nothing.

### 6. Printing (R68)

Print styles only (`@media print`):

- **The layout** hides the skip link, the header (brand, navigation, theme), the "sheet is full" notice, the footer, and SvelteKit's hidden page-title announcement, so nothing but the page's content can print. This applies to every page's printout.
- **The sheet** hides everything but each message's text: no heading, count, buttons, time, alias, priority badge or icon (the sheet doesn't draw badges or icons at all).
- **Each message** is a block with a **4 px (3 pt) solid black border**, 0.75 rem padding and **1.5 rem space** below; its text is black, monospace, `white-space: pre-wrap` and `overflow-wrap: anywhere` (as in the list), so long text and long unbroken words wrap. Nothing has a height limit or hidden overflow, so nothing is cut off. The text prints at `--text-base` (1 rem), a little larger than the list's `--text-md`, for reading on paper.
- **Never split:** `break-inside: avoid`, and `page-break-inside: avoid` for older engines. Each message, the list and their parents are blocks in print (on screen the message is a grid): page breaks inside grid and flex items aren't avoided reliably in every engine, so print layout stays block layout. The tests check the chain of parents too.
- **Black on white, whatever the theme:** in print, `html`, `body` and each message are `#ffffff` with text and borders `#000000`. **These are fixed colours, not tokens**, an exception to CLAUDE.md's rule that R68 requires: the tokens change with the theme, and dark mode's must never reach paper.
- **PDF:** the browser's print dialog saves it ("Save as PDF").
- **A message taller than a paper page** can't be kept on one page. P2 (shown in full) wins over R68, so the browser splits it rather than cut it. An A4 page holds about 3,000 characters of one message at this size; the ingest limit is 4,096 bytes (`server.MaxTextBytes`), and real POCSAG pages are far shorter (question 1).
- **The browser's own headers and footers** (date, title, address, page numbers) belong to the print dialog, and CSS can't reliably turn them off in all three engines (removing the page margins hides them in some, but prints text at the paper's edge). The documentation tells users to untick "Headers and footers" (question 2).

### 7. Documentation

`docs/print-sheet.md` (linked from the README) explains the sheet and ends with R68's line: "Printed pages are outside PagerArchive's control: retention and deactivation don't reach paper." The same line is on the sheet itself, and in `docs/deploy.md`'s retention section for operators.

### 8. Dependencies

`pdfjs-dist` **v6.4.299** (Apache-2.0, by Mozilla, released 2026-10-03), a **dev dependency** only: the browser tests read the printed PDF's text per page with it. It isn't in the app's build. Its optional dependency `@napi-rs/canvas` (MIT) is installed with it but the tests don't use it (they only read text).

### 9. Tests

- **Go (`internal/acceptance/m11_test.go`, `TestM11MessagesByID`):** for a user and an admin, IDs asked out of time order come back in that order, each identical to the list's message for the same person; A7's page has `REDACTED` and no phone number anywhere in the reply; flags only in the admin's reply; no receive times; an ignored alias's page included; a deleted message and an unknown ID in `missing`; duplicates once; 100 IDs accepted; 101, none, `abc`, `0` and `-5` refused with 400; signed out 401.
- **Browser (`web/tests/e2e.spec.ts`, Chromium, Firefox and WebKit):**
  - **A60 print sheet:** A60 as written. B ticks 3 messages in search results (by a click, by the keyboard, and by the visible label) and 1 in a job view; the sheet lists them in that order, not time order; the row still opens the detail view and the alias link the alias view; they're ticked in the message list and the alias view; `localStorage` holds exactly the theme and B's key, whose value is the 4 IDs, and no storage value contains any text, the search word, the job number, `TRAINING`, `REDACTED` or `FICTION`; each text on the sheet equals the list's, and the redacted one has `REDACTED` and no `0412`; axe in light and dark. Printed (in dark mode): in Chromium, a PDF from `page.pdf()`, whose text is exactly the 4 texts and nothing else; in every engine, with print media, the header, navigation, heading and buttons are hidden, the page's text is exactly the 4 texts, each message is a block in an at-least-3 px solid black border, black on white, `break-inside: avoid`, wrapped, not cut off, in block-only parents, with at least 16 px between messages. Then Remove, Clear dismissed (nothing changes), Clear confirmed (empty, the key gone, the rows unticked).
  - **A60 thirty messages print without splitting:** 30 messages of 40 to 2,400 characters, one with a 160-character unbroken word, ticked in the list. Chromium: an A4 PDF with 1 cm margins, where each text is whole on exactly one page and the PDF holds nothing else; then, as a control in the same run, the same sheet with `break-inside: auto` splits at least one message. Firefox and WebKit: the print-media checks above.
  - **print sheet is per user:** B ticks one message; C, in the same browser, sees an empty sheet and an unticked row, and ticks another; B again sees only B's; storage has one key each.
  - **live rows, other tabs, a full sheet and gone messages:** a row that arrived live, ticked with the keyboard; another tab's link and sheet follow, and a removal there unticks the row here; 100 IDs fill the sheet, and another tick is refused with the notice; those 100 (made up) are dropped with "100 messages were no longer available…"; a ticked message deleted from the database leaves with "1 message was no longer available…".
  - **without storage:** with every `Storage` method throwing, ticking works, the sheet shows the message, a reload shows it empty, and no page error.
  - The axe sweep (every view, both themes, 375 and 1280 px) includes the empty sheet.
- **`scripts/e2e.sh`** adds user C with a session, and `TRAINING DRILL` (in every sheet test's pages) to the API log check.
- **Mutation proof (`scripts/mutation-checks.sh`):** each broken in turn, with the test that must fail; the browser ones run `scripts/e2e.sh` in Chromium:

  | Mutation | Test |
  |---|---|
  | Admin-only details sent to a user from `by-id` | `TestM11MessagesByID` |
  | Message text stored in `localStorage` on ticking | A60 print sheet |
  | The storage key not per user | print sheet is per user |
  | The sheet sorted by time, newest first | A60 print sheet |
  | `break-inside` removed | A60 thirty messages (the PDF) |
  | The header and navigation shown in the printout | A60 print sheet (the PDF) |

## Evidence

To be added after the runs.

## Consequences

- Any signed-in user can collect up to 100 pages and print them as a pager shows them, or save them as a PDF.
- The browser keeps only message IDs; the text always comes from the API, with the person's own view, so redaction (including retroactive redaction, P1), retention and role changes apply to the sheet as soon as it's shown. Nothing does that for paper.
- A new read-only endpoint, `GET /api/v1/messages/by-id`, with the list's rules.
- The print colours are the one place in the browser app with fixed colours (R68).
- `pdfjs-dist` is a new dev dependency.

## Questions for the owner

1. **A message taller than a paper page** (over about 3,000 characters at the print size, up to the 4,096-byte limit) is split across pages, because P2 says it's shown in full. Keep that, or print such a message in smaller type so it fits one page?
2. **The browser's own headers and footers** (date, title, address, page numbers) appear if the print dialog's "Headers and footers" is ticked; the documentation says to untick it. CSS can't reliably turn them off in all three engines. Is the documentation enough?
