# ADR 0053: The page's Content-Security-Policy without 'unsafe-inline' for scripts

## Status

Proposed (the implementer's call, 2026-11-27), for the owner's review on the pull request. A hardening item on the owner's backlog. SPEC.md R36a, A154.

## Context

`web/web.go` sends every app page a strict policy, this origin only, except `script-src 'self' 'unsafe-inline'`. The app is a SvelteKit single-page app (adapter-static, ADR 0001), and its built `index.html` starts with one inline script: it sets `__sveltekit_<id> = { base, version }`, imports the start-up and app modules from `/_app/immutable/`, and registers the service worker (R90, ADR 0037). Its text changes with every build: the version is the build's time, and the module names are content hashes. With `'unsafe-inline'`, any script that got written into the page would run too.

Every page and script the server sends was checked:

- **The app's page** (`index.html`, for every path that isn't a file, R82d's look written onto `<html>`): the one inline script above, no inline event handler, no `javascript:` link. Its only inline style is app.html's `<div style="display: contents">`.
- **The sign-in page, error pages and the rest of the app** are routes of the same page; the server's own errors are plain text (`http.Error`). The app's code sets handlers as properties (`img.onload = …`) and builds its one inline SVG from the icon set (`MapView.svelte`), neither of which is inline script.
- **The offline page** (`offline.html`, written by `scripts/pwa.mjs`): a `<style>` block, no script. **The service worker** (`service-worker.js`) and **the manifest**: files, no inline script.
- **ArchiveUx 0.5.0**: `look.js` only reads and writes the look cookie and `data-look` from the app's own module (the server writes the first paint's look, R92b; its ADR 0002), with no inline snippet for the page. Its components set no inline handlers; `Icon` draws its SVG paths with `{@html}`, which holds no script.

## Decision

**The server works out the hashes from the page it carries.** When `Serve` starts, it reads `index.html` from the embedded build, finds each inline `<script>` (one with no `src`), and hashes its text as a browser does (SHA-256, with CRLF and lone CR read as LF, base64). The page goes out with `script-src 'self' 'sha256-…'`, one hash per distinct script, and nothing else in the policy changes. `.html` files (the offline page) get their own hashes the same way, so `script-src 'self'` today; the service worker gets `script-src 'self'`. The look written onto `<html>` never touches a script, so one policy serves every look.

So the hash always comes from the build that is served: a new build, or a SvelteKit release that changes the start-up script, needs no change here, and no hash is ever copied by hand. `style-src` is left as it was (`'self' 'unsafe-inline'`): the app's markup carries style attributes (app.html's `<div style="display: contents">`, components' `style="…"`), which hashes can't allow without `'unsafe-hashes'`, and the offline page has a `<style>` block. Tightening it is a change of its own.

Options not taken:

- **SvelteKit's `kit.csp` (mode `hash`).** For a prerendered page or the fallback it writes a `<meta http-equiv="content-security-policy">` with the hashes. The server's header would still need them, because a header and a meta policy both apply and the stricter wins; the build would have to hand them to Go (a file read at start, or the meta parsed out). That's the same parse with more parts. And a meta policy can't carry `frame-ancestors`.
- **Hashing at build time into a generated Go file.** It would have to be regenerated and checked on every web build, for a hash the server can work out in microseconds from the same bytes.
- **Moving the start-up script into a file.** SvelteKit writes it into the page and has no option to emit it as a file; doing so would mean rewriting `index.html` after every build and keeping up with what the script does.

## Consequences

- A script written into the page by anything but the build, an injected `<script>`, an inline event handler or a `javascript:` link, doesn't run, and the browser reports it.
- The browser tests collect every `securitypolicyviolation` event on the pages each test opens and fail on any but the one A57 causes on purpose (A154), in Chromium, Firefox and WebKit. A hash that didn't match would stop the app booting, which every browser test would see first.
- ADR 0037, section 6's "the registration is in SvelteKit's inline start-up script, which `'unsafe-inline'` already allowed": the registration is still there, and now runs by its hash.
