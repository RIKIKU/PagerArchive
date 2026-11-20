# ADR 0049: The design tokens come from ArchiveUx

## Status

Accepted (owner, 2026-11-19 and 2026-11-20). The owner decided that the design system would be a shared package in its own repository, ArchiveUx (`RIKIKU/ArchiveUx`, its ADR 0001), taken as a git dependency pinned to a release tag. On 2026-11-20 the owner decided its findings (below) and the move order of the components. ArchiveUx v0.1.0 was tagged on 2026-11-20 (commit ff5f73e). After a demo of it the same day, the owner took the alert edge off again ("The amber edge is very intense. I prefer it without."), and ArchiveUx v0.1.1 (commit 537c44c) removed it; this pull request adds the dependency at v0.1.1. SPEC.md R82c and R82d; ADR 0015 and ADR 0028 have notes.

## Context

PagerArchive and RadioArchive must feel like one application to the user. Neither app owns the design system: it stands on its own, in ArchiveUx. ArchiveUx's stage 1 moved PagerArchive's tokens (`web/src/app.css` at 6dfa1f0, unchanged since) and its base styles there, with their names and values unchanged. It added the rules (`docs/design-system.md`: which colour may be drawn on which, and the patterns), and a CI check that measures every allowed pairing against WCAG 2.1 AA, in both looks and both themes. The owner's contrast rule (2026-11-19, "WCAG AA it is"):

- text at least 4.5:1 on its own background;
- edges and controls at least 3:1;
- must-notice elements (notices, anything that appears on its own) an edge at least 4.5:1 against the page.

The check found two things the old tokens failed, and the owner decided both on 2026-11-20:

1. **An admin's alerts** (backup, upgrade) are a `--warn-bg` fill with no edge, 1.09-1.48:1 against the page. ArchiveUx 0.1.0 gave them a `--border-width-strong` edge in a new `--alert-edge` (the release notice's amber). Seeing it in a demo, the owner decided against it: "The amber edge is very intense. I prefer it without." ArchiveUx 0.1.1 (its ADR 0001, decision 13) removes `--alert-edge`: an admin's alerts are the fill only, as before, noticed by their place and wording, not their outline, so they aren't must-notice elements. Their text is still at least 4.5:1, and the fill isn't measured against the page, as `--border` dividers aren't.
2. **The "When it's busy" grid** (ArchiveUx 0.1.0): in Console dark, the busiest shade's count was `--fg` at 3.55:1. Each shade now has its own text token, `--heat-0-fg` to `--heat-4-fg`. Every one is `--muted` (none) or `--fg`, as before, except Console dark's busiest, which is `--accent-fg` (dark ink, 4.54:1). The owner first asked for the page background colour there, but `--bg` measures 4.43:1, just under the floor. The owner approved `--accent-fg` on 2026-11-20. The shades don't change.

The owner also confirmed that only the map's tokens stay in PagerArchive.

## Decisions

### 1. A git dependency, pinned to the tag and locked to its commit

`web/package.json` has `"archiveux": "github:RIKIKU/ArchiveUx#v0.1.1"` (a devDependency: it's CSS, built into the app). `web/package-lock.json` locks it to the tag's commit, `git+ssh://git@github.com/RIKIKU/ArchiveUx.git#537c44c53e362116eccfff9612221cd36dedc1f3`. `npm ci` installs exactly that commit whatever the tag later points at, and a different commit needs a change to the lock file, which a pull request shows. Not GitHub Packages, whose npm registry needs a token even for public packages, and not a submodule (ArchiveUx ADR 0001).

**How it's fetched, and why the build image doesn't change.** For a GitHub dependency with a commit in the lock file, npm doesn't run git. It downloads that commit's tarball over HTTPS from GitHub (`codeload.github.com`), and falls back to git only if that fails. The fallback is never needed: ArchiveUx is public and has no `prepare` script, which would need a clone. This was checked in the server image's own build stage, `node:24-slim` at the Dockerfile's digest, which has no git: `npm ci` with this lock file installs `archiveux` (`which git` → none; `node_modules/archiveux/src` has `archiveux.css`, `base.css` and `tokens.css`). So the Dockerfile, CI's `npm ci` steps and the release's attested image builds stay as they are, with no new tool in the build. The build already fetches its npm packages over the network, and this is one more HTTPS fetch, pinned by commit.

Options not taken:

- **Installing git in the build stage.** That adds an unpinned Debian package and its mirrors to every attested image build, and a larger stage, for something npm doesn't need.
- **A tarball URL** (`https://github.com/RIKIKU/ArchiveUx/archive/refs/tags/v0.1.1.tar.gz`, or a release asset). The lock would pin the tarball's hash. But GitHub doesn't promise its generated archives are byte-for-byte stable, so a re-compressed archive would fail every build. A release asset would add a step to every ArchiveUx release. It also isn't the form the owner chose.

The lock has no `integrity` hash for a git dependency (npm records one only for registry and tarball packages). The commit's SHA is the pin.

**Upgrading is deliberate.** A new ArchiveUx release can change the look, so the app moves to one by hand: change the tag in `web/package.json`, run `npm install`, and read ArchiveUx's `CHANGELOG.md` between the two tags. Renovate leaves it alone (`.github/renovate.json5`).

### 2. What comes from ArchiveUx, and what stays

`routes/+layout.svelte` imports `archiveux/archiveux.css` (ArchiveUx's tokens, then its base styles), then `../app.css`. `web/src/app.css` keeps only:

- the map's tokens, `--map-*`, `--detail-map-width` and `--area-tag-*`, for Harbour (`:root`) and for Console (`:root[data-look='console']`);
- the full-screen map's rule (`:root.map-full`).

Everything else in it, PagerArchive's copies of the tokens and base styles, is deleted.

- **`web/scripts/check-tokens.mjs` (A73)** reads ArchiveUx's `tokens.css` (resolved through the package, so it's the pinned release's) before app.css. So a component's `var(--…)` may name either, and a token neither defines still fails. It checks that every look sets the same tokens in both files. In app.css, the default look is its one top-level `:root` rule.
- **`web/scripts/pwa.mjs` (R90)** writes the manifest, the offline page and the theme-color lines from ArchiveUx's tokens. Their values didn't change. Only the comments naming the source did.

### 3. The look doesn't change, except for the heat grid

This was checked by script, last against ArchiveUx v0.1.1 and main at 56b68ec. The static build was served from main before the change and from this branch after it. In Chromium, Firefox and WebKit, for Harbour and Console in light and dark (12 sets), the script read every custom property any stylesheet defines, as each browser computes it on the root, and as a colour where it is one.

| | Before | After |
|---|---|---|
| Tokens per set | 231 | 236 |
| Tokens with the same value and colour (all 12 sets) | 2,772 | all 2,772 |
| Changed | | 0 |
| Removed | | 0 |

The 5 new tokens are `--heat-0-fg` to `--heat-4-fg`. Their values are the ones above, and the same in all three browsers. The repository has no screenshot comparison, so none was added.

The one visible change is applied: the busy-times grid colours each shade `--heat-N` with `--heat-N-fg`. `BackupAlert.svelte` and `UpgradeAlert.svelte` are as they were on main: the `--warn-bg` fill, no edge.

## Tests

- **The contrast test** ("M13, M15: WCAG AA contrast of every look") also measures, as drawn, in every look and theme:
  - the release notice's edge against the page (4.5:1);
  - each heat step's count on its shade (4.5:1).

  It reads `color(srgb …)` too, which is how a browser gives a `color-mix()` shade.
- **"Back up now" and R84h** check that the backup and upgrade alerts draw no edge on any side (the owner's decision above).
- **A120** checks that every cell of the busy-times grid writes its count in its own step's `--heat-N-fg` on `--heat-N`, in light and dark.
