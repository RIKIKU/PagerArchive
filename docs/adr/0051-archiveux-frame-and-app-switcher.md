# ADR 0051: The frame is ArchiveUx's, with an app switcher for whom an admin says

## Status

Proposed (the implementer's calls, 2026-11-25), for the owner's review on the pull request. #106, items 1 and 2, and the owner's soft launch decision of 2026-11-25. SPEC.md R77d, R77e and R77f; A140, A141 and A142.

## Context

ArchiveUx 0.3.0 moved PagerArchive's frame into the design system as Svelte components (its ADR 0002, decisions 7 and 8): `Frame`, `Menu`, `BottomBar`, `AccountMenu` and `Icon`, with the app's tree, counts, version and account as props, and an app switcher drawn when the app names the other app. PagerArchive still drew its own copies, and took only ArchiveUx's tokens and base styles (ADR 0049) at v0.2.1. RadioArchive's frame is ArchiveUx's already. The two apps feel like one application only if both draw the same frame, and the switcher lives in it.

The owner, 2026-11-25, on RadioArchive's soft launch: "We should use a feature toggle so that only selected users can see the new RadioArchive button in PagerArchive. It should be hidden for the rest."

## Decisions

1. **ArchiveUx v0.5.0, the latest release.** 0.3.0 to 0.5.0 change nothing PagerArchive draws until it opts in (their changelogs); this pull request opts in to the frame only. `GroupPage` and `ReleaseNotice` stay PagerArchive's (ArchiveUx ADR 0002, decision 7).
2. **The layout fills ArchiveUx's `Frame` with its snippets**: `Menu` in the side, the breadcrumbs as the top bar's start, the search box as its tools, `AccountMenu`, and `BottomBar`. PagerArchive's own `Menu.svelte`, `BottomBar.svelte` and `AccountMenu.svelte` are gone. `nav.ts` stays the one tree for the menu and the breadcrumbs; `frame.ts` turns it into ArchiveUx's `Entry` and `Group` shapes with the counts and dots, each with its words (`says`), so every accessible name is the one it was. `Icon.svelte` stays as a thin wrapper that passes PagerArchive's icon (from `icons.ts`, still compiled in) to ArchiveUx's `Icon` as markup, so the 25 components that draw an icon by name don't change.
3. **What PagerArchive keeps in the frame's parts.** The receivers' alert (R39) is the menu's `foot` snippet (`ReceiverAlert.svelte`, with its styles, as ArchiveUx's Menu has no alert of its own). The address data's credit line sits in the frame's last row, beside the menu, where it was. On a phone the search box and account menu keep their own row under the breadcrumbs (a rule in the layout: ArchiveUx's frame lets them share it). The release notice's wrapper is taken out of the frame's grid (`position: fixed`), so it takes no cell.
4. **The visible differences are ArchiveUx's by design, and only these**: a count of 0 shows no pill (ArchiveUx's `count`: "0 or none shows none"), so an empty print sheet and an empty album lose their "0"; the names still say "0 messages". And More is current on a phone when the page is in its sheet: on a group's start page (Tools, Reports, Account) it now is, and on a desktop-only page (the print sheet, Administration's pages) it no longer is. Nothing else moves, by the before and after screenshots on the pull request.
5. **The switcher's settings: `RADIOARCHIVE_URL` and `RADIOARCHIVE_NAME`** ("Radio" by default, a neutral word, not a product's name the deployment might not use). No URL, no switcher and no admin controls: exactly as before. `GET /api/v1/meta` carries both when set. The URL is checked at start (an `http(s)` URL, or the API won't start), as `APP_URL` is.
6. **Who sees it is the server's, per request.** `users.sees_switcher` (migration 38: a constant default, so only the catalogue changes, R91a) and the settings row `switcher_everyone`. `GET /api/v1/me` carries `show_switcher`, true only when the URL is set and the user is marked or it's on for everyone; never for a service account. The frame draws the switcher only when both meta and me say so. It hides the switcher only: it isn't access control, as the owner said ("hidden for the rest").
7. **The controls are on Users, beside the user**, as the brief says ("like the existing trusted mark"): a section with "Show the switcher to everyone" (a checkbox, saved at once, for launch day), and on each person's card a tag and a Show switcher or Hide switcher button. Unlike the trusted mark it's on the list, not each user's page, because the soft launch marks a handful of people at once. The admin's own card has it too, so they can try the switcher first. A mark records nothing but itself (no who or when): the log line says who changed it, as the large event threshold's does, because PagerArchive has no audit trail of user or settings changes.
8. **Tested on a second API, for its own test only.** The run's API stays standalone, so every other test sees the frame exactly as a standalone install does. A140 and A141 start a second API on the same database with `RADIOARCHIVE_URL` set, and stop it when they end (`scripts/e2e.sh` hands them the build), so no background worker runs twice while the other tests run.

## Consequences

- PagerArchive picks up ArchiveUx's frame changes with each pinned release, after reading its changelog (ADR 0049, decision 1).
- RadioArchive's switcher to PagerArchive and this one are the same component. A combined deployment sets each app's URL to the other.
- Moving `GroupPage` and `ReleaseNotice` to ArchiveUx later needs `nav.ts` and the release API cut loose; nothing here blocks it.
