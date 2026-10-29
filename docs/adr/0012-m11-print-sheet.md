# 12. Milestone M11: the print sheet

Date: 2026-10-29

## Status

Proposed. Waiting for the owner, with four questions (at the end).

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

Run on cld01 on 2026-10-29 (Go 1.27.2, Node 24.21.0 with Playwright's Chromium, Firefox and WebKit, pwsh 7.6.7) at f208d1b, from a snapshot (`scripts` run one after another by `~/m11-run.sh`).

- **CI's checks:** `gofmt -l .` empty; `go vet ./...` exit 0; `sqlc diff` exit 0 (no query changed); `validate-profile -v profiles/example` "7 passed, 0 failed" (and the agency rows); `npm run check` "0 errors and 0 warnings".
- **`PAGERARCHIVE_REQUIRE_DB=1 go test -count=1 -v ./...`:** every package `ok` (`internal/acceptance` 142.4 s); 226 passed, 0 failed, 21 skipped (the stack tests, which need the compose stack). `TestM11MessagesByID`: "user@example.com got [2 3 1] in the order asked, as the list shows them, flags false", the same for the admin with flags true, "deleted message 3 and unknown 999999999 reported missing: [3 999999999]".
- **Pester:** `Passed 341, failed 0, skipped 4` (the module didn't change).
- **`scripts/e2e.sh`** (all three browsers): `138 passed (14.9m)`, "API log lines checked: 527, page texts found: 0". From A60 in each engine:
  - storage: `{"theme":"system","pagerarchive:print-sheet:2":"[81,82,80,83]"}`, the 4 IDs in the order ticked (b, c, a, d; time order would be a, b, c, d or d, c, b, a);
  - Chromium's PDF of the 4: one page holding exactly the 4 texts and nothing else, the redacted one as "… BRAVO TEXT REDACTED WHEN DONE";
  - print media, in dark mode, in all three: `{"display":"block","breakInside":"avoid","pageBreakInside":"avoid","border":"4px solid rgb(0, 0, 0)","background":"rgb(255, 255, 255)","colour":"rgb(0, 0, 0)","chain":"ol:block main:block div:contents body:block html:block"}`; axe clean on the sheet in light and dark;
  - 30 messages, Chromium: "9 PDF pages; the page each message is on: M1:1 M2:1 M3:1 M4:1 M5:2 … M30:9", each whole on one page; the control without `break-inside`: "split across pages: M5 (2200 characters), M9 (1800 characters), M12 (2400 characters), M15 (1200 characters), M18 (150 characters), M25 (2300 characters)". Firefox and WebKit: "print media: 30 messages in heavy black borders, break-inside avoid, block layout, black on white, nothing cut off";
  - per user: `{"theme":"system","pagerarchive:print-sheet:3":"[115]","pagerarchive:print-sheet:2":"[114]"}`.
  - The PDFs, rendered, show each message in its border, whole; the naive one shows M5 running from page 1 onto page 2.
- **`scripts/stack-checks.sh`** (the API gained an endpoint): every phase `ok` (`internal/stacktest` 6.2 s, 444.4 s, 424.1 s, 411.3 s), 34 passed, no `FAIL`; "API log lines checked: 559, page texts or email addresses found: 0".
- **`scripts/stack-e2e.sh --project=chromium`:** `1 passed`; "API log lines checked: 23, page texts or email addresses found: 0".
- **`scripts/mutation-checks.sh`:** 34 mutations, 34 guarded, none survived or failed to apply; the unmutated copy passed (`internal/acceptance` ok, the 5 print sheet tests in Chromium "5 passed", Pester `Passed 341, failed 0`). The 6 new ones:

  | Mutation | Failing check |
  |---|---|
  | `toMessage(m, true)` in `by-id` | `TestM11MessagesByID`: flags in the user's reply |
  | `localStorage.setItem("pagerarchive:print-sheet-text:" + m.id, m.display_text)` on ticking | A60: the storage check (`toEqual`) |
  | The key `"pagerarchive:print-sheet"` for everyone | per user: C's sheet "The print sheet is empty." not shown |
  | `.toSorted(…sent_at…)` on the sheet | A60: the sheet's order (`toEqual`) |
  | `break-inside` and `page-break-inside` removed | 30 messages: "messages not whole on exactly one page" (the PDF) |
  | `.top` not hidden in print | A60: "the PDF holds the texts and nothing else", received "…#STN1PagerArchiveMessagesMapReports…" |

## CI: the watch tests' hang, and faster runs (owner, 2026-10-29)

Two requests on this branch, separate from the print sheet.

### A. The hang in Watch.Tests.ps1

**What was seen.** Main's run 40795721403 (the M10 merge, 07ba39e), attempt 1: in "PowerShell module (ubuntu-24.04, PowerShell 7.6.0)", Icon, Meta, Module and SignIn passed, then Watch.Tests.ps1 printed nothing for 50 minutes until it was cancelled. The rerun passed. The reviewer once saw the same on Windows.

**Looking for it.** With the old tests: Watch.Tests.ps1 654 times (cld01: 163 under 7.6.0, 182 under 7.6.0 pinned to one CPU with two more cores kept busy, 159 under 7.6.7, 50 times inside the whole suite as CI runs it; this PC: 100 under 7.6.9), each run killed after 300 s, plus 300 stops timed at random moments just after a dropped connection. No hang. One timing failure: under load the first message took over 2 s, the limit of that one wait (now the file's usual 10 s; the check that output arrives while the stream is open is unchanged).

**Every wait got a time limit** (requirement 2). In the old tests, the test thread could only hang in `PowerShell.Stop()` (in every `AfterEach` and the Ctrl+C test), `EndInvoke` (the `Select-Object -First` test), closing a runspace, or a socket write; everything else in the file already had a limit. Now `Stop-TestRunspace` stops with `BeginStop` and waits at most 10 s, `Close-TestRunspace` closes in at most 10 s, `EndInvoke` waits at most 10 s, and writes time out like reads. In the module, the one wait in the watch without a limit (the reply's stream after its headers) now has the connect limit. A watch that won't stop fails its test with what it was doing: its last `-Verbose` and `-Debug` lines and its script call stack (`Runspace.Debugger.GetCallStack()`); with `PA_TEST_DUMP_DIR` set (CI) the test also writes a dump of the process with `createdump` (CI uploads it when the job fails; `Install-PowerShell.ps1` now marks `createdump` executable, which 7.6.0's archive doesn't). Proved on a copy whose waits can't be interrupted: the file failed in 53 s naming three tests ("The watch didn't stop within 10000 ms of Ctrl+C; … at Wait-PaTask:34 < …"), where it used to hang.

**Caught.** The first CI run with the limits (40815368401) failed 3 tests in 2 of the 4 PowerShell jobs (Windows 7.6.0, Ubuntu newest): "The watch didn't stop within 10000 ms of Ctrl+C; state Stopping". The test process then stayed alive for 14 minutes, until a PowerShell timer crashed it (`NullReferenceException` in `RunspacePoolInternal.CleanupCallback`). On cld01 the same happened 4 times in about 390 runs. In every case the watch had just lost its connection (each test ends by dropping it) and was then stopped. Sampling the stuck watch every 10 s for 70 s showed it **still running normally**: reconnecting again and again with growing backoff (attempts 3 and 4; 217 attempts in one case), its warnings giving the dropped connection as the reason, no error, and its state "Stopping" throughout. The stop request had been lost, not blocked.

**The cause: PowerShell loses a stop that arrives while a command runs its own `finally` block.** To let `finally` blocks run commands while a pipeline is stopping, PowerShell clears the pipeline's stopping flag for the block and afterwards restores the value it had on entry. A stop that arrives meanwhile is overwritten, unless a command called from the block is running at that moment (that command keeps its own mark and throws when it returns). Further stops are ignored, because the pipeline is already "Stopping". Reproduced on its own, without the module: a command that spends its time in its own `finally` block lost 60 of 60 stops on Windows (7.6.9) and 20 of 20 on Linux (7.6.0); the same work outside a `finally` lost 0, and so did a `finally` calling a function. The watch closed its stream in a `finally` block of its own after every dropped connection, so a Ctrl+C at that moment was ignored for good: the old tests' `Stop()` then waited forever (main's hang), and a user's Ctrl+C would do nothing, and nor would the next, until the window was closed. It's rare because the window is short, which is why 654 local runs missed it.

**The fix** (in the module, not only the tests):

- `Watch-PagerArchiveMessage` has no `finally` block: the stream is closed in normal flow after the read loop, and a stop or error that skips that is covered by its `clean` block, which closes `$stream`.
- As a second line, the watch takes `$PSCmdlet.PipelineStopToken` (PowerShell 7.6, the module's minimum), which every stop cancels and nothing restores, and passes it to its waits: `Wait-PaTask` (every 200 ms slice, through `Open-PaEventStream` and the stream's state) and `Wait-PaRetry` (which now waits on the token instead of `Start-Sleep`, so Ctrl+C ends a 30 s backoff at once). Each reconnect also checks it. A cancelled token ends the command with `PipelineStoppedException`, as Ctrl+C does. With the token, the reproduction above lost 0 of 60 (Windows) and 0 of 20 (Linux 7.6.0).
- The other commands' `finally` blocks stay: they run once at the end of a command that ends anyway, so a lost stop there costs nothing.
- **Tests** (`Watch-PagerArchiveMessage and a lost Ctrl+C`): the watch has no `finally` block (from its syntax tree, with the reason); `Wait-PaTask` and `Wait-PaRetry` end as soon as the token is cancelled, with no `Stop()` at all, as when PowerShell has lost the stop; and a command ending on the token alone stops even while PowerShell loses the stop, the PowerShell behaviour the fix relies on. Mutation proof (`mutation-checks.sh`): a `finally` put back around the close, `Wait-PaTask` ignoring the token, and `Wait-PaRetry` ignoring it, each make a test fail.

**After the fix:** EVIDENCE-A.

**Requirements 3 and 4.** Every CI job has `timeout-minutes`, set well above its usual time: tests 20 (usually 2), browser 45 (14), PowerShell 20 (1), each stack part 40 (8 to 11), stack-e2e 30 (5), images 30 (1.5 and 7.5), A15 40 (11), publishing 20, the client manifest and the Gallery 10, release binaries 15. `Invoke-Tests.ps1` uses Pester's Detailed output, so each test's name is printed as it finishes.

### B. Faster CI

**Browser tests in Playwright's image.** The browser job runs in `mcr.microsoft.com/playwright:v1.64.0-noble`, pinned by its index digest `sha256:06a9939e57531807f8d5fd76ce44b53165ffb7d7501d87ab10e285c20b1e971f`, the exact version in `web/package-lock.json`, checked by a step (`npx playwright --version`). It has the browsers and their libraries, and Node 24.21.0. It has no Docker, which `scripts/e2e.sh` used for PostGIS and the tests used for `psql`, so:

- PostGIS (`postgis/postgis:18-3.6`, as before) is a service container, `postgres` on the job's network. `e2e.sh` takes its URL in `PA_E2E_DATABASE_URL` and applies the three logging settings it otherwise starts its container with (`ALTER SYSTEM`, then a reload). Without the variable, `e2e.sh` runs its own container as before.
- `psql` comes from Ubuntu's `postgresql-client` (16.15), installed in the job; the tests' direct database changes (A50's join, A60's deleted message, the demoted admin) go through one helper, `sql()`, using `psql` with the URL or `docker exec` as before.
- The job runs as root, so `HOME` is `/root` (Firefox won't start with someone else's home folder), and git is told to trust the runner's checkout, or `go build` can't stamp its version (the first attempt failed on exactly that).

**The stack checks as three parallel jobs.** `scripts/stack-checks.sh` takes `PA_STACK_PART`: `accounts` (A23-A28, A33, A50's join, the locked-accounts metric), `secretstore` or `none` (the module's checks with that vault). Each part starts its own compose stack and sets up the first admin and the restart, as the whole run does; without the variable it runs everything, as before. CI runs the three as a matrix. I made the account checks a third job rather than adding them to one vault's job, so each check runs once and the three take about the same time. Each part ran on cld01 on its own: `none` and `secretstore` the same 8 module tests as the whole run, `accounts` the same 13 (427 s; a first try failed only on Go's 10-minute test limit while cld01 was at load 10 from the hang loops). Only the module parts install PowerShell; none of them needs Node or a browser.

**stack-e2e as its own job, with the browser cached.** Section 11 in a browser runs the compose stack, which needs the runner's Docker and its ports on localhost, so it can't run in Playwright's container. It caches `~/.cache/ms-playwright` by Playwright's version (`actions/cache@v6`); the browser's system libraries are still installed with apt each time.

**Unchanged:** the tests themselves, the browsers (Chromium, Firefox and WebKit, Playwright 1.64.0), Go, Node 24, PostGIS 18-3.6, PowerShell 7.6.0 for the stack. Publishing still needs every check: `needs` lists `test`, `web`, `stack` (all three parts), `stack-e2e`, `images`, `budget` and `powershell`. The container setup was first tried on cld01 the way CI runs it: `138 passed (15.9m)`.

**Times** (GitHub's job timings, minutes):

| Job | Before (39881826098) | Before (40805279958, this morning) | After (EVIDENCE-RUN) |
|---|---|---|---|
| Browser tests | 29.6 (installing browsers 18.4) | 14.9 (installing browsers 1.4) | EVIDENCE-WEB |
| Against Authentik | 30.6, one job | 25.7, one job | EVIDENCE-STACK |
| Section 11 in a browser | in the job above (2.5) | in the job above (2.9) | EVIDENCE-SE2E |
| A15 short run | 10.5 | 10.5 | EVIDENCE-A15 |
| Images (arm64) | 7.6 | 7.5 | EVIDENCE-ARM |
| **Whole run** | **31.7** | **26.7** | **EVIDENCE-TOTAL** |

Installing the browsers took 18 minutes in the run named in the request but 81 seconds this morning: apt's speed varies. The container removes that variation; most of the time saved comes from running the stack's parts in parallel. The longest job now is the browser tests themselves (about 12 minutes of tests).

## Consequences

- Any signed-in user can collect up to 100 pages and print them as a pager shows them, or save them as a PDF.
- The browser keeps only message IDs; the text always comes from the API, with the person's own view, so redaction (including retroactive redaction, P1), retention and role changes apply to the sheet as soon as it's shown. Nothing does that for paper.
- A new read-only endpoint, `GET /api/v1/messages/by-id`, with the list's rules.
- The print colours are the one place in the browser app with fixed colours (R68).
- `pdfjs-dist` is a new dev dependency.

## Questions for the owner

1. **A message taller than a paper page** (over about 3,000 characters at the print size, up to the 4,096-byte limit) is split across pages, because P2 says it's shown in full. Keep that, or print such a message in smaller type so it fits one page?
2. **The browser's own headers and footers** (date, title, address, page numbers) appear if the print dialog's "Headers and footers" is ticked; the documentation says to untick it. CSS can't reliably turn them off in all three engines. Is the documentation enough?
3. **The lost Ctrl+C is PowerShell's bug** (section A): a stop that arrives while a command runs its own `finally` block is dropped, in 7.6.0 and 7.6.9. The watch now works around it. Should I write it up for PowerShell's issue tracker (with the reproduction, no PagerArchive details), for you to post?
4. **Three stack jobs, not two.** You asked for the two vault settings as two parallel jobs; I made the account checks a third, so nothing runs twice and the three take about the same time (section B). Keep three, or fold the account checks into one vault's job?
