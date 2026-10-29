# 11. Milestone M10: templates in Administration, and icons stored as images

Date: 2026-10-23

## Status

Accepted (owner, 2026-10-29). The owner answered the questions on 2026-10-23 (at the end), recorded in SPEC.md (R63, R65, R66) and the guidelines (§3), and tested adding an icon on the live copy.

## Context

M10 (SPEC.md R64–R66, checks A56–A58) moves template management into the browser and replaces the template's `icon` text with an image:

- **R64.** Admins list, add, edit and delete templates in Administration: name, agency, icon, colour, agency profile, and how many aliases use each. A template in use can't be deleted, and the refusal says how many use it. Users can't change templates.
- **R65.** An icon is an image stored in PagerArchive's database, never a link. PNG, ICO, GIF, JPEG and WebP of at most 100 KB are accepted, SVG and anything else refused with the reason, and every icon becomes a 64×64 PNG, keeping nothing else from the file. The API does the checks and the conversion, whether the icon comes from the browser or the PowerShell module. The module can set an icon from a file or a URL, and downloads a URL on the admin's own machine.
- **R66.** The icon shows beside the alias name wherever a page's sender is shown, on a light tile, with the agency name as its text alternative. Each browser fetches an icon once and reuses it until it changes.

What was there before M10:

- The template API from M8 (ADR 0009 §8): `GET`, `POST`, `PATCH` and `DELETE /api/v1/admin/templates`, admins only, with alias counts, profile checks, re-parsing on a new profile (R23a) and the refusal with a count. It couldn't rename a template.
- `templates.icon text`, holding icon names from old CSV imports. Nothing used it: R30 ignores the CSV's `icon` column, and the alias view printed the name.
- The browser app's Content-Security-Policy, with `img-src 'self' data:` (ADR 0005).

## Already decided elsewhere (cited, not re-decided)

- Admin-only screens live in Administration, reached by the one Admin link (R44b). Admin endpoints refuse users with 403 (A29).
- Changes to templates take effect at once for every alias using them, and a new agency profile re-parses their pages in the background (R23a, R28).
- The CSV's `icon` column is accepted and ignored on import, and export leaves it empty (R30). That was already so, and stays.
- The module follows `docs/powershell-guidelines.md`; refusals become `ErrorRecord`s whose `ErrorId` is the API's code (§7).

## Decisions

### 1. Templates in Administration (R64)

- **Admin → Templates** lists every template: name (linking to its page), icon, agency, colour (with a swatch), agency profile and how many aliases use it. A profile that names one the server didn't load is marked "not loaded: text only" (section 14 says such a template is reported to admins).
- **Add template** opens a form for name, agency, colour and agency profile, then goes to the new template's page, where its icon is added. Keeping the upload on the template's page means a refused icon never leaves a half-finished form, and the add form needs no file handling of its own.
- **A template's page** edits name, agency, colour and profile; adds, replaces or removes the icon; and deletes the template. Delete asks to confirm, and the server's refusal ("\"Example Fire\" can't be deleted: 3 aliases use it. Move them to another template first.") is shown as it comes. Changing the profile says the template's pages are re-parsed in the background.
- **The agency profile** is chosen from the profiles loaded at start-up, or none: a new `GET /api/v1/admin/agency-profiles` lists them, sorted. The server already refused unknown names (`unknown_profile`).
- **Renaming:** `PATCH /api/v1/admin/templates/{id}` now also takes `name`, refused with `template_exists` when another template has it. Templates still have no history (R64).
- The Aliases screen's note now points to Templates, and the Administration start page links to it.

### 2. The icon API (R65)

| Request | Who | What it does |
|---|---|---|
| `PUT /api/v1/admin/templates/{id}/icon` | admins | The body is the image file, as it is. The reply is `{"icon": "<URL>"}` |
| `DELETE /api/v1/admin/templates/{id}/icon` | admins | Removes the icon |
| `GET /api/v1/icons/{sha256}.png` | signed-in users (§5) | The stored icon |

The browser sends the file the admin chose; the module sends a file's bytes or what it downloaded. **No endpoint takes a URL.** The template endpoints reject unknown JSON fields, so `icon`, `icon_url` or `icon_uri` in a template's body is a `400`; and an upload whose bytes are a URL is refused with `icon_url`, "PagerArchive never fetches icons from other sites. Download the image and upload the file (the PowerShell module's -Uri does that on your own computer)."

Refusals, each with a message the admin can act on:

| Code | Status | When |
|---|---|---|
| `icon_too_large` | 413 | Over 100 KB |
| `icon_empty` | 422 | No bytes |
| `icon_svg` | 422 | An SVG: "SVG icons aren't accepted, because an SVG file can hold scripts and links to other sites. Save it as a PNG and upload that." |
| `icon_url` | 422 | A web address instead of a file |
| `icon_type` | 422 | Anything else that isn't one of the five types, judged by its bytes |
| `icon_dimensions` | 422 | It declares more than 1024 pixels on a side (§3), with its size in the message |
| `icon_animated_webp` | 422 | An animated WebP (§3) |
| `icon_unreadable` | 422 | It starts like one of the five types but can't be decoded |

The API logs each upload and refusal with the template, the admin and the code or the sizes, never the file's contents.

### 3. Checking and converting (`internal/icon`)

In this order, so nothing large is ever read or decoded:

1. **Size, before reading.** A declared `Content-Length` over the limit is refused at once, unread. Otherwise the body is read through `http.MaxBytesReader`, which stops one byte past the limit. The limit is **100 KB = 102,400 bytes** (KiB, as the spec's other sizes). The Content-Type header and any file name are never looked at.
2. **Type, from the bytes.** The file's first bytes decide: PNG (`89 50 4E 47 0D 0A 1A 0A`), GIF (`GIF87a`, `GIF89a`), JPEG (`FF D8 FF`), WebP (`RIFF`…`WEBP`) and ICO (`00 00 01 00`). Anything else is refused; the first kilobyte is only read again to choose the message (`icon_svg` if it holds `<svg`, `icon_url` if it starts with `http://` or `https://`, else `icon_type`).
3. **Declared size, before decoding.** `image.DecodeConfig` (or the ICO reader's own header read) gives the size the image claims, and anything over **1024 pixels on a side** is refused. A 1024×1024 image decodes to at most 8 MB: 8 bytes a pixel for a PNG with 16 bits per channel, the largest kind any of the five types decodes to (8-bit images take at most 4 MB). Scaling it down then adds Catmull-Rom's working buffer, 2 MB (64 × 1024 pixels of four `float64`s), so one conversion allocates about 11 MB at most: measured with Go's `runtime.MemStats`, a 38 KB 16-bit 1024×1024 PNG allocated 11.3 MiB in all during `Convert`, and an 8-bit one 7.3 MiB. Real icons are far smaller (ICO's own maximum is 256), and a logo wider than 1024 pixels is better resized before upload anyway. This only protects anything if the decoder then allocates by that same size. I checked each one in the source of the versions used (Go 1.27.2, `golang.org/x/image` v0.47.0): PNG reads one IHDR; JPEG refuses a second SOF marker; GIF's `Decode` stops after the first frame and refuses frames larger than the canvas; WebP refuses a VP8 or VP8L frame whose size differs from the VP8X canvas. `TestHugeCanvasNotDecoded` checks that a 67-byte PNG claiming 10000×10000 is refused having allocated under 1 MB, so an upgrade that changed this would fail.
4. **Decode.**
   - **ICO**: a small reader of my own (about 130 lines), rather than a library. It reads the directory, picks **one** entry, the smallest that's at least 64 pixels on a side, or else the largest, preferring more colours between equals, and decodes only that one: a PNG through the steps above, or a bitmap (1, 4, 8, 24 or 32 bits a pixel, with its AND mask for transparency; a 32-bit bitmap's own alpha wins when it has one). The other entries' bytes are never read, which a test with garbage in them proves.
   - **Animated GIF**: the first frame, placed on the GIF's canvas.
   - **Animated WebP** is refused with `icon_animated_webp`, because `x/image/webp` can't decode animations (question 3: the owner accepted this, R65).
5. **Convert.** The image is scaled to fit 64×64, keeping its aspect ratio, centred on transparency (Catmull-Rom, which also shrinks large images smoothly), and **encoded afresh** as a PNG by Go's encoder. The result has only `IHDR`, `IDAT` and `IEND` chunks: no text, EXIF, colour profile or anything else from the upload. Conversion is deterministic, so the same file gives the same bytes and the same URL.

Conversions run one at a time (a mutex), so several uploads at once can't add up to more than one decoded image.

**Libraries.** `golang.org/x/image` **v0.47.0** (BSD-3-Clause, by the Go authors, compatible with MIT) for WebP decoding and the Catmull-Rom scaler: the standard library has neither. It's pure Go. Its `go.mod` moves `golang.org/x/text` from v0.41.0 to v0.43.0, and `golang.org/x/sync` (indirect) from v0.22.0 to v0.24.0. No ICO library: the reader above is short and covers what ICO files hold. No cgo and no ImageMagick.

### 4. Storage

Migration `00012_template_icons.sql` **drops `templates.icon`** (names from old CSV imports, which R30 already ignored) and adds:

- `icon_png bytea`, the 64×64 PNG the API made, 1 to 65,536 bytes (a 64×64 PNG is at most about 17 KB);
- `icon_sha256 text`, its SHA-256 in hex, which names it in its URL; both or neither (a check constraint), indexed.

Queries that show templates or messages select only the hash; only the icon endpoint reads the bytes. Replacing an icon overwrites both; removing sets both to NULL. Icons are in the database's backups like everything else.

### 5. Serving icons (R66)

- **Who can fetch them: any signed-in user**, with a browser session or a module token, as for every other `/api/v1` path (A29). Every page that shows an icon is behind sign-in, and an icon shows which agencies a deployment follows, as alias names do, so there's no public endpoint. Without a session it's `401`.
- **A URL that changes when the icon changes:** `/api/v1/icons/<sha256>.png`. Replacing an icon gives a new URL, and the old one answers `404` unless another template has the same image.
- **Headers:** `Content-Type: image/png`, `X-Content-Type-Options: nosniff` (already on every response), `Cache-Control: private, max-age=31536000, immutable` (the browser keeps it for a year and never revalidates it; `private` keeps it out of shared caches, since it needed a sign-in), and `Content-Security-Policy: default-src 'none'; sandbox`, so the file, opened on its own, can do nothing.
- **The page's CSP is unchanged:** `img-src 'self' data:`. Icons come from PagerArchive's own origin, so nothing needed loosening, and nothing from another site can be loaded.

### 6. Showing icons (R66)

- A small `AgencyIcon` component draws the icon on a tile whose colour is a new token, `--icon-tile` (`#ffffff`), defined once in `app.css` and deliberately not redefined for dark mode, with a `--border` outline. Its `alt` is the agency name.
- **Where:** beside the alias name in every message row (the list, live updates, search results, the job view, and the alias view's list, which use the same row), in the alias view's heading, in the message detail view's heading, and in the map's list of located messages. R66 names the rows and the alias view; the detail view and the map list also show the sender, so they follow "wherever a page's sender is shown" (question 5: the owner kept them, and R66 now names them).
- **Size and position:** 1.25 em in rows (the row's small text), 2.5 rem in headings, placed just before the name. The M12 redesign decides the final look.
- **With no icon**, nothing is drawn and the row is as before, colour bar included. The alias view no longer has an "Icon" row: the icon is beside the name.

### 7. The PowerShell module (R63, R65)

| Command | New parameters | Notes |
|---|---|---|
| `New-PagerArchiveTemplate` | `-Path`, `-LiteralPath`, `-Uri` | Parameter sets `NoIcon` (default), `Path`, `LiteralPath`, `Uri` |
| `Set-PagerArchiveTemplate` | `-Path`, `-LiteralPath`, `-Uri`, `-RemoveIcon` | Parameter sets `Settings` (default), `Path`, `LiteralPath`, `Uri`, `RemoveIcon`; agency, colour and profile can be changed in any of them |
| `Get-PagerArchiveTemplate` | | Objects gain `HasIcon`, in the default table |
| `Rename-PagerArchiveTemplate` (new, alias `Rename-PaTemplate`) | `-Name`, `-NewName`, `-PassThru` | Built like `Rename-PagerArchiveAlias` (question 2) |

- **Names, by the guidelines.** Files use `-Path` (wildcards, which must match exactly one file) and `-LiteralPath`, which has the `PSPath` alias (§3, "Files (from M10: icons)"). The alias was first on `-Path`, as §3 then said; at the owner's answer to question 1 it moved to `-LiteralPath`, as on Microsoft's own cmdlets, because a provider path is literal: `-PSPath (Get-Item …).PSPath` now finds a file named `icon[1].png`. `-LiteralPath` doesn't take pipeline input: these commands take templates from the pipeline, and a piped file's `Name` would bind to `-Name`. A web address is `-Uri`, typed `[uri]` (§2's standard name, §3's strong types). Removing is a `[switch]`, `-RemoveIcon` (§3: on/off is a switch), in its own parameter set so it can't be combined with a new icon. I didn't use `-Path ''`, which clashes with `ValidateNotNullOrEmpty` and wildcard paths, or a `Remove-PagerArchiveTemplateIcon` command, because `Remove` deletes an item (§2) and an icon is a setting of the template.
- **Files.** The path is resolved through PowerShell's providers, so `~` and drives work. A file over 100 KB is refused before it's read.
- **Downloads** happen on the admin's machine, with .NET's `HttpClient`, because `Invoke-WebRequest` reads a whole reply before handing it over and so can't stop at 100 KB: only `http` and `https`; at most 5 redirects; no cookies, no default credentials and never the PagerArchive token; the system's proxy settings; a reply that declares more than 100 KB is refused before its body is read, and any other is read in 16 KB pieces and dropped as soon as it passes 100 KB; **30 seconds** for the whole download; waits in 200 ms slices so Ctrl+C works. Errors: `icon_too_large`, `icon_download_timeout` (`OperationTimeout`), `icon_download_failed` (`ConnectionError`, with the site's status), `icon_uri`.
- **When.** `Set-PagerArchiveTemplate` reads or downloads the icon once, after the first template's `ShouldProcess`, and uploads it to every template given, so `-WhatIf` reads and downloads nothing. `New-PagerArchiveTemplate` reads or downloads before adding the template, so a missing file or a failed download adds nothing; if PagerArchive then refuses the icon, the template stays without one and the error says so ("Template 'X' was added without an icon: …", with the API's code) (question 4: the owner kept this, R65).
- **Renaming** (question 2). `Rename-PagerArchiveTemplate -Name <name> -NewName <name>` sends `PATCH /api/v1/admin/templates/{id}` with only `name` (§1). `-Name` is one template, matched case-insensitively and without wildcards (names are unique, so a wildcard matching several could only fail after the first), from the pipeline by value or by the `Name` property, so `Get-PagerArchiveTemplate … | Rename-PagerArchiveTemplate -NewName …` works. `ShouldProcess` at `ConfirmImpact = 'Medium'` ("Rename template to '…'"), errors with the API's code (`template_exists` for a taken name, `ResourceExists`) or `no_template` for a name that matches nothing, nothing output unless `-PassThru`, which outputs the renamed `PagerArchive.Template`. The aliases keep the template and show the new name at once.
- `Invoke-PaApi` gains a raw body, sent as `application/octet-stream`.
- Help, `about_PagerArchive` and `docs/powershell.md` describe the icons and renaming.

### 8. Tests

- **Go, `internal/icon`:** every accepted type (including an ICO whose chosen entry is a bitmap, and an animated GIF's first frame); the aspect ratio kept with transparent padding; metadata gone (a PNG `tEXt` chunk and a JPEG's EXIF, and a 64×64 PNG still encoded afresh); every refusal and its code (SVG, text, HTML, URLs, 200 KB, one byte over, PNG, JPEG, GIF, WebP and ICO claiming huge sizes, animated WebP, damaged files); the huge-canvas PNG refused with under 1 MB allocated; the ICO entry choice with garbage in the others; 1-, 4-, 8-, 24- and 32-bit ICO bitmaps; and a fuzz test (any bytes give an icon or a refusal, never a panic), whose seeds run in CI.
- **The test icons** (`testdata/icons/`) are all made up: drawn shapes and generated pixels, written by `go test ./internal/icon -run TestFixtures -update`, and checked against that code on every run. `example-icon.png` (32×32, a navy circle with a yellow triangle), `not-kept.png` (the same with a `tEXt` chunk "NOT-KEPT"), `example.svg`, `large-200kb.png` (random pixels, 203,217 bytes), `not-an-image.png` (text), `several-sizes.ico` (16, 32, 64 and 256 pixels; the 64-pixel bitmap is the one to pick), `animated.gif` (three frames), `huge-canvas.png` (67 bytes claiming 10000×10000), `with-exif.jpg` and `wide.webp` (40×20, for the aspect ratio).
- **Go, API (`internal/acceptance/m10_test.go`):** A56 exactly as written (with the standard fixture), plus renaming, a taken name, the profile list, re-parsing after a profile change, deleting an unused template, and user B refused on every template and icon endpoint with nothing changed; A57's API side (the icon on the list, detail, job, alias and template views and in a live update; the served headers; 401, 404s; the three refusals and a huge canvas, each leaving the icon unchanged; all five types; replacing and removing); the size limit applied before reading (`TestIconSizeLimitBeforeReading`: a 10 MB body is refused having read at most 102,401 bytes, or none when its length is declared); A58's API side (the stored icon is a fresh 64×64 PNG with no "NOT-KEPT" in any table; a URL sent as the icon, or in any template field, is refused and a test web server gets no request).
- **Browser (`web/tests/e2e.spec.ts`, Chromium, Firefox and WebKit):** A56 through the screens; A57 through the screens, in light and dark mode with axe, with the list reached through a proxy in the test that counts the icon requests the server gets; an image from another site blocked by the CSP; the three refusals. The Administration and axe tests include the two new screens.
- **Full stack (`internal/stacktest/templates_test.go`, `web/tests/stack.spec.ts`):** A56 and A58 through the module against real Authentik, with each vault setting, A56 including `Rename-PagerArchiveTemplate` (a rename seen by the template's aliases, a taken name refused with `template_exists`, and renaming back); A57 in the browser on the full compose stack.
- **Pester (`powershell/tests/Icon.Tests.ps1`, `Module.Tests.ps1`):** uploads from `-Path`, `-LiteralPath`, `-PSPath` (a provider path to `icon[1].png`, on both commands) and `-Uri`; a test web server on a real socket for downloads (a redirect, no credentials sent, a declared 200 KB refused, a chunked reply cut off after 100 KB, a stalled reply stopped by the time limit, a 404); `-RemoveIcon`; `-WhatIf` touching nothing; `HasIcon`; parameter sets; no file contents or tokens in `-Verbose`/`-Debug`; `Rename-PagerArchiveTemplate` (only `name` sent, pipeline input, `-PassThru`, `template_exists`, `no_template`, `-WhatIf`). The guideline meta-tests and PSScriptAnalyzer cover the new parameters.
- **Mutation proof (`scripts/mutation-checks.sh`):** each check broken in turn: the size limit, the type by bytes, the SVG refusal, the declared-size limit, re-encoding, admins only on each of the five template and icon endpoints, the no-URL rule, and the module's size and time limits while downloading.

## Evidence

Run on cld01 on 2026-10-23 (Go 1.27.2, Node with Playwright's Chromium, Firefox and WebKit, pwsh 7.6.7, the compose stack with Authentik 2026.5.5), at commits 9fe4de3–6cf3b49.

- **CI's checks:** `gofmt -l .` empty; `go vet ./...` clean; `sqlc diff` clean; `validate-profile -v profiles/example` all rows passed; `npm run check` 0 errors and 0 warnings.
- **`PAGERARCHIVE_REQUIRE_DB=1 go test -count=1 -v ./...`:** every package `ok` (`internal/icon` 0.17 s, `internal/acceptance` 136 s); 158 tests passed, and the 21 skipped are the stack tests, which need the compose stack.
- **Fuzzing** `FuzzConvert` for 60 s (121,478 inputs): no panic, no non-refusal error, and, timed in a second run, no input over 1 s.
- **`scripts/e2e.sh`** (all three browsers): `123 passed (13.0m)`, "API log lines checked: 407, page texts found: 0". A57 in each engine and theme: "icon on a white tile, fetched 1 time(s) for 3 page loads, origins requested http://127.0.0.1:<proxy>, another site blocked by img-src", and after the icon changed, its new URL was fetched once more.
- **`scripts/stack-checks.sh`:** every test in every phase passed, with both vault settings: "A56: … \"Example Fire\" can't be deleted: 3 aliases use it."; "A56: user B refused (403) editing a template and uploading an icon"; "A58: not-kept.png (292 bytes, with a tEXt chunk) stored as a 2304-byte 64×64 PNG without it"; "A58: -Uri downloaded once by the module (no credentials), the site stopped, and the list's page shows /api/v1/icons/4eb21574….png (2304 bytes)", byte for byte what converting the file in the test gives. "API log lines checked: 554, page texts or email addresses found: 0". A first run found a bug in the new stack test's helper (it escaped the `?` of a query, so the request got 404 after the module's steps had passed), fixed in 6cf3b49.
- **`scripts/stack-e2e.sh --project=chromium`:** `1 passed`; "A57: icon /api/v1/icons/4eb21574….png shown beside Station 1 in light and dark mode; 3 files refused; origins requested: http://localhost:8080"; "API log lines checked: 23, page texts or email addresses found: 0".
- **Pester:** `Passed 321, failed 0, skipped 4` (the skips are Windows-only), PSScriptAnalyzer with no findings.
- **`scripts/mutation-checks.sh`:** all 28 mutations (first reported here as 29, a miscount: the script has 24 `mutate` lines, one of them a loop over the 5 template handlers), the 13 new ones included, made a test fail; the unmutated copy passed. The new ones and what failed:

  | Mutation | Failing check |
  |---|---|
  | Size checked after reading the whole body | "read 10485760 bytes of the body" (declared and undeclared) |
  | Type taken from the Content-Type when the bytes aren't an image | not-an-image.png "200, want 422 icon_type", and the icon changed |
  | SVG not refused with its own reason | "422 icon_type …, want 422 icon_svg" |
  | Declared size not checked before decoding | huge-canvas.png "422 icon_unreadable …, want 422 icon_dimensions" |
  | Upload stored as it came | "stored: Width:32 Height:32", "the stored icon kept the uploaded file's text" |
  | Admins only off, for each of create, edit, delete, upload icon, remove icon | user B's request "201/204/409/200/204, want 403" |
  | The server fetches an icon sent as a URL | "a URL as the icon: 200", "the server fetched the URL 1 times" |
  | The module's download with no size limit | "stops a download as soon as it passes 100 KB": no error |
  | The module's download with no time limit | "stops a download that takes longer than the time limit": `icon_download_failed` after 15 s, not the timeout |

**After the owner's answers** (run on cld01 on 2026-10-23, at bbeacb5, with main merged):

- **`PAGERARCHIVE_REQUIRE_DB=1 go test -count=1 ./...`:** every package `ok` (`internal/acceptance` 136.7 s); `gofmt -l .` empty, `go vet ./...` clean.
- **Pester:** `Passed 341, failed 0, skipped 4` (the 4 are Windows-only), including the `-PSPath` test and `Rename-PagerArchiveTemplate`'s, its guideline meta-tests and PSScriptAnalyzer.
- **`scripts/stack-checks.sh`:** every phase `ok`, no `FAIL`, with both vault settings: A56 renamed "Example Rescue 103" to "Example Rescue 103 North" and back, Station 4 kept it, and renaming it to "Example Fire" gave `ERROR template_exists,Rename-PagerArchiveTemplate There's already a template called "Example Fire".` "API log lines checked: 565, page texts or email addresses found: 0".
- **`scripts/mutation-checks.sh`:** 28 mutations, 28 guarded, none survived; the unmutated copy passed (`internal/acceptance` ok, Pester `Passed 341, failed 0`).
- **`scripts/ci/Save-TestModules.ps1`** (CI: Gallery 404s on main's run for 3d49c1c): run with a stand-in `Save-PSResource` whose Pester save fails the first *n* times. *n* = 0 saves all four modules; *n* = 2 warns "Saving Pester 5.9.1 failed on try 1 of 3; trying again in 10 seconds: Response status code does not indicate success: 404 (Not Found)." and the same for try 2, then saves all four; *n* = 3 warns for tries 1 and 2, then "failed on try 3 of 3, the last: …", and the script stops with that error.
- `e2e.sh` wasn't rerun: the browser app didn't change.

## Consequences

- Admins manage templates in the browser; the module and the browser use the same API, so the same checks apply to both.
- Every icon PagerArchive serves is a PNG it made itself, from its own database, on its own origin. Uploading anything else, or something huge, costs at most 100 KB of reading and about 11 MB of decoding and scaling (8 MB of it the decoded image), one at a time.
- Browsers fetch each icon once a year at most, per browser; a changed icon has a new URL.
- The old icon names in `templates.icon` are gone after the migration. They were never shown or used.
- `golang.org/x/image` is a new dependency, and `x/text` and `x/sync` move up with it.

## Questions for the owner, and the answers (2026-10-23)

1. **`PSPath` on `-Path`.** Guidelines §3 give `-Path` the `PSPath` alias. Microsoft gives it to `-LiteralPath`, because a provider path is literal: a file named `icon[1].png` piped or passed as `-PSPath` is read as a wildcard and not found. I followed the guidelines. Should the alias move to `-LiteralPath` (here and in the guidelines)?
   **Answer:** yes, on `New-` and `Set-PagerArchiveTemplate`, as an alias only: `-LiteralPath` doesn't take pipeline input, because a piped file's `Name` would bind to `-Name` (guidelines §3, changed on main in 352fd4a). Done (§7), with a Pester test.
2. **Renaming templates from the module.** The API and the browser can rename a template now. The module can't: `Rename-PagerArchiveTemplate -Name … -NewName …` would match `Rename-PagerArchiveAlias`. Add it?
   **Answer:** yes, alias `Rename-PaTemplate`, built like `Rename-PagerArchiveAlias`, using the `PATCH` (R63). Done (§7), tested in Pester and against the real API.
3. **Animated WebP** is refused (`icon_animated_webp`), because the pure-Go decoder can't read animations. An animated GIF gives its first frame. Is refusing animated WebP with that reason acceptable?
   **Answer:** yes (R65). No change.
4. **A refused icon on `New-PagerArchiveTemplate`.** The template stays, without an icon, and the error says so. The alternative is to delete the template again, which would hide that it had been created. Keep it as it is?
   **Answer:** keep it (R65). No change.
5. **Icons beyond R66's list.** The detail view's heading and the map's list also show the icon, since they show the sender. Keep them, or limit icons to the rows and the alias view until M12?
   **Answer:** keep them; R66 now names the message detail view and the map's list of messages. No change.
