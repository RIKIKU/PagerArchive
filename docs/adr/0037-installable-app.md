# ADR 0037: Installable on a phone

## Status

Proposed (2026-11-17). The owner: "do the installable part, hold off on notifications ... I don't want people to use it as an alerting service for operational pages"; "the PWA must work on Android as well as iPhone, with Android an equal target". SPEC.md R90-R90c and the non-goal in section 4, with A116. On the branch `pwa` from main; the pull request is labelled `feature`.

## Context

PagerArchive is a SvelteKit single-page app (adapter-static, `ssr = false`) built into the Go binary and served by `web/web.go`: `index.html` for every path that isn't a file, sent `no-cache, private` with the user's look written into it (ADR 0028) and the page's Content-Security-Policy; files under `_app/immutable/` (named by their content) for a year; other static files `no-cache`. Authentik, which every sign-in goes through, is on its own host (R40); signing in is a top-level redirect to it and back to `/auth/callback`, with the login state in a `SameSite=Lax` cookie on `/auth/`; signing out ends the session and sends the browser to Authentik's end-session endpoint.

Installing a web app needs a manifest, icons and, for Chrome on Android to make a real app of it (a WebAPK, offered as "Install app" rather than a home-screen shortcut), HTTPS and a service worker with a fetch handler. iOS reads the manifest too (16.4 and later), keeps its own `apple-touch-icon` and `apple-mobile-web-app-*` tags, and gives an app on the home screen its own cookies and storage, apart from Safari's.

## Decisions

### 1. SvelteKit's own service worker

`web/src/service-worker/index.ts`, which SvelteKit builds to `/service-worker.js` and registers itself (as a module worker) from the page's inline start-up script. It has its own `tsconfig.json` (`$app/tsconfig/service-worker`, the WebWorker library), checked by `npm run check` with `tsc`; the app's own tsconfig leaves it out, as SvelteKit asks. Its lists come from `$app/manifest` (`immutable`, `assets`) and its version from `$app/env` (SvelteKit 3's replacements for `$service-worker`).

### 2. What it keeps: the shell, per build

One cache, `shell-<version>` (the build's version, `version.json`), holding the build's files under `/_app/immutable/` and, of `web/static`, only the icons, the favicons, the manifest and `offline.html`: never the map's glyphs and sprites (`static/map/`). It's filled on install. A file under `/_app/immutable/` is named by its content, so one an earlier build's cache already holds is copied from there rather than downloaded again: an upgrade downloads only what changed (about 2.7 MB in all for a first install, mostly MapLibre and its worker).

The fetch handler touches:

- a GET for one of those paths (no query): from this build's cache, or the network if it isn't there, without storing it;
- a navigation, except to `/auth/` or `/api/`: from the network; if the network can't be reached, the cached `offline.html`. Navigation preload stays off: with it on, the browser requests every navigation in scope as a preload and again for any the worker leaves to it, so `/auth/callback` arrived twice and the second found the sign-in's state already used ("the sign-in expired"). A116's stack test found it, and checks the callback is requested once per sign-in.

Nothing else: no `respondWith`, so the request goes to the network as if there were no worker, and nothing it answers can be stored, by construction. API answers, the live stream, searches, template icons, map tiles and glyphs, and the app's page itself (which carries the user's look and depends on the session) are never in a cache. A116 checks the cache after browsing every kind of page.

Rejected: **caching pages or API answers for offline reading** (P1: nothing of a page is kept on the phone by the app; and an archive that's read live doesn't need it), and **caching the app's page** to start offline (it would boot an app that can't reach its API, which is worse than saying it's offline).

### 3. How a release reaches an installed app

- The browser checks `/service-worker.js` on every navigation (and SvelteKit asks it to before reloading for a new version). The server sends it `no-cache` like every non-immutable file. A new build's worker differs (its version and file lists), so it installs, calls `skipWaiting()`, and on activating deletes every other cache and claims the open pages.
- The app's page always comes from the network, so after an upgrade the next page opened is the new release's, with the new release's file names, whichever worker is running: an old worker passes file names it doesn't know to the network.
- An app left open (on a phone, often for days) checks `/_app/version.json` when it's brought back to the front, on focus and every hour (SvelteKit's defaults). When a new version is out, the root layout's `beforeNavigate` turns the next client-side navigation into a full page load, so the old code doesn't go on talking to the new API. If an old page's lazily loaded file has gone, SvelteKit itself checks the version, updates the worker and reloads.

### 4. The manifest, the offline page and the colours come from the tokens

`web/scripts/pwa.mjs` (like `icons.mjs`) writes `static/manifest.webmanifest`, `static/offline.html` and the theme-color lines in `src/app.html` from Harbour's tokens in `app.css`, so the colours and sizes there are tokens too (R82c); CI runs it with `--check`. The manifest has one theme and one background colour, so it has the light theme's `--surface` (the top bar) and `--bg`; the manifest standard has no colours per theme. `app.html` has a theme-color line for each theme (`media="(prefers-color-scheme: …)"`), for the first paint, and `src/lib/statusBar.ts` sets them to the top bar's colour as drawn whenever the look (R82d), the chosen theme or the system's theme changes, so Console and a chosen Dark get their own bar. The splash screen on Android uses the light `--bg` in every theme.

The offline page copies the tokens it uses from Harbour, written as `light-dark()` pairs, so it follows the system's theme. It has no script, and says nothing about the archive.

iOS: `apple-mobile-web-app-capable`, `apple-mobile-web-app-title` and `apple-mobile-web-app-status-bar-style` `default` (the status bar above the page, so no safe-area padding is needed at the top), with `mobile-web-app-capable` beside them for other browsers.

### 5. Icons

`web/scripts/app-icons.mjs` renders, in Chromium, from `static/favicon.svg` (R82e): the favicon as it is at 192 and 512 (`any`); a maskable version at 192 and 512, with the green to every edge and the pager at 0.75 of its size, so its far corner is 11.7 of 32 units from the middle, inside the 12.8 (40%) every launcher's mask keeps; and `apple-touch-icon.png` again, now with the green to every edge, because iOS rounds the icon itself and shows a transparent corner as black. They were checked on light and dark home screens, in a circle, a rounded square and a small rounded inset. Run by hand when the favicon changes; the PNGs are committed.

### 6. The Content-Security-Policy isn't loosened

The page's policy (`default-src 'self'; script-src 'self' 'unsafe-inline'; …`) already allows the manifest (`manifest-src` falls back to `default-src 'self'`) and a worker from this origin (`worker-src` falls back to `script-src`), and the registration is in SvelteKit's inline start-up script, which `'unsafe-inline'` already allowed. Nothing is added. The server now also sends the same policy with `/service-worker.js`, whose own policy governs what it may fetch (this origin only), and with `.html` files such as `offline.html` (its `<style>` is allowed by `style-src 'unsafe-inline'`, as the app's are). `.webmanifest` is sent as `application/manifest+json`.

### 7. Signing in and out when installed

The worker leaves navigations to `/auth/` alone, so the browser follows sign-in's redirects (to Authentik, back to `/auth/callback`, then to the page asked for, R40a) itself, as it does without a worker; the callback's landing page then comes through the worker from the network. The login state's cookie is `SameSite=Lax`, which a top-level return from Authentik's host carries.

- **Android, Chrome:** the installed app (a WebAPK) shares Chrome's cookies. A navigation outside the app's scope (Authentik's host) shows over the app with a toolbar naming the site (a Custom Tab-style view), and the return to the app's scope closes it, so the user is back in the app, signed in. Authentik's own session is shared with Chrome, so a person signed in to Authentik in Chrome isn't asked again.
- **iPhone, Safari:** an app on the home screen has its own cookies, apart from Safari's: it's signed in on its own, and signing in or out in Safari doesn't change it. A top-level redirect to another host is expected to stay inside the app (with a bar naming the site and a Done button), keeping the app's cookies, so the return to `/auth/callback` finds the login state and signs the app in. If an iOS version opened Authentik in a separate Safari sheet with Safari's cookies instead, the callback wouldn't find the login state and would say the sign-in expired: that's the first thing the owner's iPhone check looks for. No test browser can show it.
- **Signing out** ends the app's session and the person's Authentik session, and comes back to the app's own sign-in page (R40b, ADR 0038, which replaced what was decided here: Authentik's `default-provider-invalidation-flow` kept Authentik's session, so going back into the app signed straight back in). The hops through `/auth/end-session` and Authentik's end-session endpoint are top-level navigations the worker leaves to the browser; on a phone, Authentik's view opens over the app and the return to `/signin?signed_out=1`, in the app's scope, closes it. The stack test checks both sessions end and the way back in asks again with the worker in control.

A116's stack test signs in, out and in again in Chromium with the worker in control. A real Android phone and a real iPhone have to check what a test browser can't: the install prompt, the WebAPK, the Custom Tab and iOS's in-app view, and their cookies (the pull request lists the steps).

### 8. Tests

The Playwright config blocks service workers (`serviceWorkers: 'block'`) for every test that doesn't allow one: with a worker controlling a page, `page.route` doesn't always see its requests (in WebKit not at all), which most tests rely on. A116's tests allow it and browse the app with it, in Chromium, Firefox and WebKit; the offline check skips Firefox, whose offline emulation in Playwright doesn't reach a worker's own requests. Chromium's DevTools protocol checks the manifest (`Page.getAppManifest`) and installability (`Page.getInstallabilityErrors`), Chrome's own criteria.

### 9. No notifications

No push subscription, no Notifications permission request, no background sync or periodic sync: the worker has only `install`, `activate` and `fetch` handlers. SPEC.md section 4 says why.
