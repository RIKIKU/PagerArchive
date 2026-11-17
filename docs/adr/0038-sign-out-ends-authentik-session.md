# ADR 0038: Signing out ends Authentik's session too

## Status

Proposed (2026-11-17). The owner: "when someone signs out, we don't want to secretly keep them signed in. It would make troubleshooting more difficult if we can't rely on that feature." SPEC.md R40b and R90c, with A117. It replaces ADR 0037's decision 7 on signing out. On the branch `signout` from main.

## Context

Until now `POST /auth/logout` ended PagerArchive's session and sent the browser to Authentik's end-session endpoint with nothing else. The browser provider's invalidation flow was Authentik's `default-provider-invalidation-flow`, which has no stages: Authentik deleted the app's tokens, showed "You've logged out of PagerArchive" and kept its own session, so the next visit signed straight back in. Installed on a phone (ADR 0037), closing Authentik's view went back to the app, which signed in again.

What Authentik 2026.8 (the image in `compose.yaml`) does, from its source at `version/2026.8.5` (`authentik/providers/oauth2/views/end_session.py`, `authentik/flows/stage.py`, `authentik/stages/user_logout/`, `authentik/blueprints/`):

- **End-session** (`/application/o/<slug>/end-session/`, GET or POST; POST isn't exempt from Django's CSRF check, so a cross-site POST fails) plans the provider's `invalidation_flow`, adds Authentik's own last stage (`SessionEndStage`) and deletes the app's access tokens for that Authentik session. An unauthenticated request is a valid no-op that still runs the flow.
- **The user-logout stage** (`authentik_stages_user_logout.userlogoutstage`) ends the browser's Authentik session (Django's `logout`). Authentik's own brand invalidation flow (`default-invalidation-flow`) is that one stage. Deleting the session deletes the access tokens tied to it (cascade); refresh tokens are only unlinked (`SET_DEFAULT`), so the PowerShell module's sign-in survives it (ADR 0009, E5).
- **The return to the app**: `post_logout_redirect_uri` is accepted only when it matches a redirect URI of type `logout` registered on the provider (`redirect_uri_type: logout`; `strict` is an exact string comparison), and, once any such URI is registered, only **with an `id_token_hint`**, an ID token signed by this provider for this client (expiry isn't checked; nothing else about it is). `client_id` isn't read. With the URI accepted, `SessionEndStage` redirects there; without one, after the session has ended, it redirects to Authentik's root, which for a signed-out visitor is Authentik's sign-in page.
- **Blueprints** are discovered when the worker starts and every hour, and a file whose hash changed is applied again. Entries with the default state (`present`) update an existing object's attributes in place (a partial update), so a changed `invalidation_flow` or `redirect_uris` on the existing provider is updated, not skipped.

## Decisions

### 1. A PagerArchive invalidation flow with the user-logout stage

The blueprint adds a flow, `pagerarchive-sign-out` (designation `invalidation`, authentication `none`, title "Signing out"), with one stage, a user-logout stage `pagerarchive-sign-out`, and makes it the browser provider's `invalidation_flow`. It registers `APP_URL/signin?signed_out=1` on the provider as a `strict` redirect URI of type `logout`, beside the callback. Existing installs get both on the next start of Authentik after the upgrade (the file changed), with no manual step; A77's check signs out after the upgrade from the latest release and checks Authentik asks again.

Rejected: **Authentik's `default-invalidation-flow`** (the same stage, but its contents and title are Authentik's to change, and the brand uses it for Authentik's own sign-out); **a redirect stage after the logout stage**, sending every end-session request to the sign-in page with no ID token (it works, but it isn't the protocol's way, and the owner asked for RP-initiated logout's `post_logout_redirect_uri`); **ending the person's Authentik sessions through Authentik's API** (it would sign them out on every device).

### 2. The ID token is kept with the session, server-side

Authentik needs the `id_token_hint` with the return address, so the callback keeps the raw ID token in the session's row (`sessions.id_token`, migration 00027), and `EndSession` returns it as it deletes the row. It's never logged, and never in a cookie or in an answer the app's pages can read. It holds what the app already keeps (subject, email, name, the invitation claim), and isn't accepted by the API as a credential (the PowerShell module's tokens are checked against their own issuer and client). It goes with the session: signing out, deactivation, deleting the account and expiry delete it.

### 3. Signing out is two hops, so the browser's script never sees the token

`POST /auth/logout` (the app's fetch, as before) ends the session first, whatever happens next, clears the cookie, and answers `{"redirect": "/auth/end-session"}`. The ID token is held in memory for at most 2 minutes under a random one-time handle, which goes to the browser in an HttpOnly, `SameSite=Lax` cookie, `pa_logout`, on the path `/auth/end-session` only. `GET /auth/end-session` takes the token (once), clears the cookie, and answers `303` (`Cache-Control: no-store`) to Authentik's end-session endpoint with `id_token_hint` and `post_logout_redirect_uri=APP_URL/signin?signed_out=1`. Authentik ends its session and its flow returns the browser to that page, which says "You've signed out." The service worker leaves `/auth/` to the browser (ADR 0037), so the installed app does the same.

The token is in the URL to Authentik, as OIDC's GET form has it, and so in that request's line wherever Authentik's requests are logged. The app's pages never see it: they get the hop's path, and the server sends `Referrer-Policy: no-referrer`.

- **No handle** (a link to `/auth/end-session` from elsewhere, a used or expired one): the sign-in page; nothing is sent to Authentik.
- **A session from before this change**, or none at all (it had expired): no token, so the hop goes to the end-session endpoint without a hint or a return address, which Authentik would refuse together. Its flow still ends Authentik's session, and the browser stops on Authentik's sign-in page instead of the app's. Sessions are renewed by use, so this is each browser's first sign-out after the upgrade; after the next sign-in it ends on the app's page.
- **An API restart between the hops**: the handle is gone, so the sign-in page, with the app's session already ended. Rare enough not to keep the handles in the database.

### 4. Authentik unreachable

Before setting the handle, `POST /auth/logout` asks Authentik for the provider's discovery document, waiting at most 3 seconds. If that fails (no answer, an error, or not `200`), the app's session has already ended; the answer is `/signin?signed_out=1&authentik=unreachable`, and the sign-in page adds that the sign-in service couldn't be reached to sign them out there too, so the next sign-in on that device may not ask for the password. The API logs a warning without the token. The API reaches Authentik as the browser can't always (the internal URL), so it's a good guess, not proof: if Authentik answers the API but not the browser, the browser shows the proxy's error, as before.

### 5. The PowerShell provider is left alone

`Disconnect-PagerArchive` revokes the module's refresh and access tokens at Authentik's revocation endpoint and deletes the saved copy (ADR 0009); it never uses the end-session endpoint, so its provider's invalidation flow is never run, and changing it would change nothing. A browser signing out of PagerArchive in the browser the module signed in with ends that browser's Authentik session, which only unlinks the module's refresh token: the module stays signed in, as ADR 0009's table says for "the browser signs out of Authentik".

## Tests

- Go (`internal/acceptance/signout_test.go`, `TestA117SignOut`) against a stand-in for Authentik's discovery: the hops, the query, the cookies, the one-time handle, a session with no token, no session, Authentik answering `503` or nothing for 10 seconds (signed out within the 3-second wait, sent to the `unreachable` page), and the token in no answer and not in the log.
- Stack (`internal/stacktest/signout_test.go`, phase `restarted`): signs out through the real Authentik, twice, and checks the end, the app's `401` and that `/auth/login` stops at Authentik's identification step; then a session whose ID token was cleared in the database, as from before the upgrade. A77's check does the same after the upgrade from the latest release.
- Playwright (`web/tests/stack.spec.ts`, Chromium in CI): A117 in a plain tab, and A116's installed-app test with the service worker in control. `web/tests/e2e.spec.ts`, whose stack has no Authentik: the unreachable note and the ended session.
