# 9. Milestone M8: the PowerShell module

Date: 2026-10-20

## Status

Proposed. Waiting for the owner's answers to the four questions below. No module code is written until then.

## Context

M8 (SPEC.md R59–R63a, owner 2026-10-20) moves admin command-line work from the `pagerarchive` program to a **PowerShell module**, `PagerArchive`, that uses the same API as the browser. The module signs in as a person through Authentik (R60), stays signed in through Microsoft's SecretManagement (R61), is for admins only (R62), and has template, alias and message commands, including an alias import through the pipeline (R63, R63a). `pagerarchive` keeps `serve`, `validate-profile`, `client` and `version` (R59). The module follows `docs/powershell-guidelines.md`. The checks are A20 and A21 (now through the module) and A51–A55.

Everything about Authentik below was tested on **Authentik 2026.5.5** (the version in `compose.yaml`), on a throwaway stack on cld01, with `scripts/oauth-probe.py` (§2). Its output is in §2.

## Questions for the owner

1. **No vault set up (R61).** SecretManagement saves nothing until a vault is registered: `Set-Secret` fails with `SetSecretCommandFailNoVault` ("no vault was provided and there is no default vault designated"). Tested in PowerShell 7.4. What should `Connect-PagerArchive` do then?
   - **Recommended:** sign in anyway, keep the sign-in **in memory for this PowerShell window only**, and warn: "No SecretManagement vault is set up, so this sign-in ends when this window closes. To stay signed in, register one", followed by the two commands from §5. The module never falls back to a file and never registers a vault for the user.
   - Alternative: refuse to connect until a vault is registered.
2. **SecretManagement is archived.** Microsoft's pages for SecretManagement and SecretStore now say (2026-06-22): "feature complete and will no longer be actively developed. The modules will continue to be supported for security and critical bug fixes. The code repository has been archived." The newest versions are SecretManagement 1.1.2 and SecretStore 1.0.6, both from January 2022. R61 names SecretManagement, so I'll use it unless you say otherwise. The module uses only `Get-SecretVault`, `Get-SecretInfo`, `Get-Secret`, `Set-Secret` and `Remove-Secret`, all in one private file, so swapping it out later is a small change.
3. **A51 says "sign in in the browser with the emailed code".** The normal sign-in is email and password (owner, 2026-10-13). The emailed code is for enrolment and password reset. The module doesn't change the sign-in: it opens whatever Authentik's sign-in asks for. The A51 test therefore signs in with a password. If you want an emailed code at every sign-in, that's a separate change to the sign-in flow, for the browser too. Is the password sign-in what you meant?
4. **What `-WhatIf` on the import shows.** PowerShell's convention is that `-WhatIf` changes nothing, prints one "What if:" line per change, and outputs no objects.
   - **Recommended:** follow the convention, with lines such as `What if: Performing the operation "Add alias" on target "0300002 (Brigade South)"` for each add, and the same with "Update alias" under `-Update`. Differences found without `-Update`, and rows squashed, come out as warnings, so a dry run still shows everything.
   - Alternative: also output the result objects, marked `Applied = $false`.

Two smaller points don't need an answer unless you disagree:

- **PowerShell 7.4 and 7.5 lose support on 10 November 2026** (Microsoft's lifecycle page). The guideline says the manifest names the oldest supported release "when the module is built", so the manifest says **7.4** today, and a Pester test fails once that date has passed, so the first build after 10 November must raise it to **7.6** (LTS, supported to 14 November 2028). CI tests 7.4 and 7.6.
- **Pester 5, not 6.** The guidelines say Pester 5. The Gallery now has Pester 6.2.0 (September 2026). CI pins **Pester 5.9.1**, the newest 5.x, and PSScriptAnalyzer 1.25.0.

## Already decided elsewhere (cited, not re-decided)

- Sign-in is Authentik over OIDC, and the app never handles passwords (R40). Guessing protection (R44a) is in the sign-in flow, so it applies to the module's sign-in too.
- The app's own database decides whether someone is active and what role they have (ADR 0004). Deactivation disables the user in Authentik straight afterwards. Deleting an account deletes the Authentik user, and the app's row keeps only an anonymous placeholder whose `subject` is `NULL` (`DeleteUserAccount`).
- Import rules (R27a, R30, R58): one batch, conflicts change nothing, identical rows are squashed, existing aliases change only with the update option, and changes go into the alias history (R57, ADR 0007).
- No API keys (R60). Clients keep their own keys, created on the server (R59).

## Decisions

### 1. The module's client in Authentik

The blueprint (`deploy/authentik/blueprints/pagerarchive.yaml`) gains its own provider and application, **PagerArchive PowerShell**:

| Setting | Value | Why |
|---|---|---|
| `client_type` | `public`, no secret | R60: a script on someone's PC can't keep a secret |
| `client_id` | `pagerarchive-powershell` | The API checks the audience against it |
| Application slug | `pagerarchive-powershell` | Gives its own issuer: `AUTHENTIK_URL/application/o/pagerarchive-powershell/` |
| `redirect_uris` | regex `^http://127\.0\.0\.1:[0-9]{1,5}/$` | RFC 8252 §7.3: a loopback redirect on any port. `localhost` isn't allowed (§8.3) |
| `grant_types` | `authorization_code`, `refresh_token`, `urn:ietf:params:oauth:grant-type:device_code` | Authentik 2026.5 refuses any grant not listed (`grant_type_not_configured`) |
| Scopes | `openid email profile offline_access` | `offline_access` is what gives a refresh token |
| `sub_mode` | `user_id` | The same subject as the browser's sign-in, so it maps to the same `users.subject` |
| `access_token_validity` | `minutes=5` | As for the browser. The API checks status and role on every request anyway (§3) |
| `refresh_token_validity` | `days=30` | R61: a sign-in not used for 30 days ends |
| `refresh_token_threshold` | `seconds=0` | Every refresh issues a new refresh token valid for 30 more days and revokes the old one, so 30 days count from the **last** use |
| `access_code_validity` | `minutes=10` | Authentik also uses it as the device code's lifetime, so people have time to type the code. Codes are still single use and need PKCE |
| Signing key | `authentik Self-signed Certificate` | The same key as the browser's provider. The issuer and audience keep the two apart |
| Authorization flow | implicit consent | The normal sign-in, then straight back, as for the browser |
| `meta_launch_url` | `blank://blank` | Not offered in Authentik's application library |

**PKCE is enforced by a policy.** Authentik 2026.5.5 lets a public client exchange a code without PKCE (the probe's first run: "authorize without code_challenge: code issued; exchange without verifier: 200 tokens"). RFC 8252 §8.1 says servers should refuse that. The application gets one expression policy, `pagerarchive-powershell-pkce`: on requests to `/application/o/authorize/` it passes only when `code_challenge_method=S256` and a `code_challenge` are present, and it passes everything else (device codes, Authentik's own access checks). Authentik evaluates application policies on the authorize request without caching (`PolicyAccessView` sets `use_cache = False`), so a request without PKCE can't reuse an earlier pass. It doesn't evaluate them on the token request for this grant, which is why the check is on the authorize side.

**Device code** (RFC 8628). Authentik's `/device` page needs a "device code flow" on the brand. The blueprint adds `pagerarchive-device`: no stages, `authentication: require_authenticated`. Authentik sends a signed-out visitor through the brand's normal sign-in (with guessing protection), back to `/device`, where Authentik adds its own "enter the code" step. Making the sign-in flow itself the device flow didn't work (a flow error), and wasn't needed.

### 2. Evidence: each feature tested before building on it

`scripts/oauth-probe.py` creates users, signs in through Authentik's flow executor API (as `internal/stacktest` does), edits token rows to simulate time, and restarts Authentik. It never prints a token. Output on 2026-10-20 (`AUTHENTIK 2026.5.5`, abridged only where marked):

```
== E1 PKCE with a loopback redirect on a random port (RFC 8252)
PASS code issued to http://127.0.0.1:43226/
PASS exchange with the wrong code_verifier refused  (400 invalid_grant)
PASS exchange with the right code_verifier gives tokens  (200, expires_in=300, refresh_token length 128)
PASS the same code a second time refused  (400 invalid_grant)
PASS another random port (53480) also accepted
PASS redirect http://localhost:43226/ refused  (STOPPED 400 …/application/o/authorize/…)
PASS redirect http://127.0.0.1.example.test:43226/ refused  (STOPPED 400 …)
PASS redirect https://example.test/ refused  (STOPPED 400 …)
PASS redirect http://127.0.0.1:43226/x refused  (STOPPED 400 …)
PASS authorize without a code_challenge refused (blueprint policy)  (STOPPED 200 …)
PASS authorize with the plain method refused  (STOPPED 200 …)

== E2 the access token
  header: {'alg': 'RS256', 'kid': 'Pa1Jw3cn…', 'typ': 'JWT'}
  claims: {'iss': 'http://localhost:9000/application/o/pagerarchive-powershell/', 'aud': 'pagerarchive-powershell',
           'azp': 'pagerarchive-powershell', 'sub': '8', 'scope': 'offline_access profile openid email'}  exp-iat = 300
PASS issuer is the module's application
PASS audience is the module's client
PASS subject is the Authentik user id (sub_mode user_id)  (8)
PASS kid is in the issuer's JWKS
  id_token differs by: ['azp', 'scope', 'uid']

== E3 refresh renews the refresh token (threshold 0)
  first refresh token: 2026-11-19 04:22|f|t                  (expires | revoked | linked to a browser session)
PASS refresh gives a new access token and a new refresh token  (200)
  old token now: 2026-11-19 04:22|t|t   new token: 2026-11-19 04:22|f|t
PASS the old refresh token is refused after rotation  (400 invalid_grant)

== E4 30 days unused
  made 29 days old: 2026-10-21 04:22|f|t
PASS a 29-day-old refresh token still renews  (200)
  renewed token: 2026-11-19 04:22|f|t
PASS a refresh token unused for 30 days is refused  (400 invalid_grant)

== E5 the browser's Authentik session ends
  refresh token linked to a session: 2026-11-19 04:22|f|t
  deleted the user's Authentik sessions: 3  token now: 2026-11-19 04:22|f|f
PASS refresh still works after the browser session ended  (200)

== E6 revocation (RFC 7009), public client, no secret
PASS revoke answers 200  (200)
  token row after revoke: (deleted)
PASS a revoked refresh token is refused  (400 invalid_grant)
  revoke without client_id: 401

== E7 device code (RFC 8628)
PASS device authorization gives a user code and a verification URI  (verification_uri=http://localhost:9000/device, expires_in=600, interval=5)
PASS polling before the code is entered: authorization_pending  (400 authorization_pending)
  browser on the other device ended at: AT ak-provider-oauth2-device-code-finish: {"flow_info": {"title": "Redirecting to PagerArchive PowerShell", …
PASS after sign-in and the code: tokens, with a refresh token  (200)
PASS device token's subject is that user
PASS the device code can't be used twice  (400 invalid_grant)
PASS device sign-in's refresh token renews  (200)

== E8 deactivation in Authentik (the app's SetActive)
  disable with the app's API token: 200  token row: (deleted)
PASS a deactivated user's refresh token is refused  (400 invalid_grant)
PASS and stays refused after reactivation  (400 invalid_grant)

== E9 Authentik restart
  restarted, discovery answers after 12 s
PASS signing key ids unchanged  (['Pa1Jw3cn…'])
PASS a refresh token from before the restart still renews  (200)

== E10 account deletion in Authentik (the app's DeleteUser)
  delete with the app's API token: 204
PASS a deleted user's refresh token is refused  (400 invalid_grant)

0 failed
```

What the source of 2026.5.5 says about the same things (`authentik/providers/oauth2/`):

- `RefreshToken.session` is `on_delete=SET_DEFAULT`, "as we want refresh tokens to persist even when the session is terminated" (E5). Access tokens are deleted with the session, but the API checks access tokens itself (§3), and the module renews them.
- `signals.py: user_deactivated` deletes all of a user's access, refresh and device tokens whenever the user is saved inactive (E8). Reactivating doesn't bring them back.
- `create_refresh_response` with threshold 0 always issues a new refresh token and marks the old one revoked (E3). Reusing a revoked one is refused and logged by Authentik as a `suspicious_request` event. It doesn't revoke the newer token.
- **Authentik logs rejected refresh tokens.** `token.py` logs `Refresh token does not exist`, `is expired` and `is revoked` at warning level with the token's value (seen in the stack's log). Those tokens no longer work, and this is Authentik's own log, not the module's or the API's (R61). `docs/deploy.md` will say so, next to the existing advice on logs.

### 3. How the API accepts the module's tokens (R60, R62)

`signedIn` (the middleware every `/api/v1` route except page posting goes through) accepts either the browser's session cookie, as now, or `Authorization: Bearer <token>`. A request carrying both is refused (`400`), so it's never unclear who is acting. Page posting (`POST /api/v1/pages`) keeps client API keys and never accepts a sign-in token.

For a bearer token, in this order:

1. **Signature, issuer, expiry and audience,** with `go-oidc`'s verifier for the module's issuer (`AUTHENTIK_URL/application/o/pagerarchive-powershell/`) and client ID `pagerarchive-powershell`. Keys come from the issuer's JWKS, reached through the same internal transport as the browser's sign-in, cached and fetched again when an unknown key ID appears (key rotation). RS256 only. A token for the browser's client (`aud: pagerarchive`, another issuer), a token signed with another key, an expired token, or one from another issuer is refused with `401 invalid_token`.
2. **It must be an access token,** not the ID token handed out with it: `azp` must be `pagerarchive-powershell` and `scope` must be present (E2: the ID token has neither).
3. **The person.** `sub` is looked up in `users.subject`, **on every request**, as the session lookup does now:
   - no such user (never signed in to PagerArchive, or the account was deleted, whose `subject` is now `NULL`): `401 not_signed_in`, "No PagerArchive account for this sign-in.";
   - `status` isn't `active`: `401 deactivated`, the same message as the browser's sign-in refusal;
   - `role` isn't `admin`: **`403 powershell_admins_only`**, "PowerShell access is for admins." (R62, A53).

   The request then runs as that user, with the same handlers and checks as the browser. Nothing is cached between requests, so demotion, deactivation and deletion take effect on the next command (A53).

New settings, both with defaults: `POWERSHELL_CLIENT_ID` (`pagerarchive-powershell`) and the issuer, derived from `AUTHENTIK_URL`. The verifier is set up on first use, like the browser's OIDC, so the API still starts while Authentik is starting.

**`GET /auth/powershell`** (outside `/api/v1`, no sign-in) tells the module what it needs from just the app's URL (R60): `{"issuer": …, "client_id": "pagerarchive-powershell", "scope": "openid email profile offline_access"}`. The module reads everything else from the issuer's `.well-known/openid-configuration`.

Logs: the API logs "signed in with PowerShell" once per token (by `jti`, with the user ID), and refusals with their code. It never logs a token or the `Authorization` header.

### 4. Following each token

| Event | Browser session | Module access token (5 min) | Module refresh token (30 days from last use) |
|---|---|---|---|
| **Sign-in** (`Connect-PagerArchive`) | unchanged | issued | issued, saved in the vault (§5) |
| **Renewal** (any command, when the access token has under 60 s left) | n/a | a new one | a new one, saved to the vault at once; the old one is revoked by Authentik (E3) |
| **Disconnect** | unchanged | revoked in Authentik and dropped from memory; the API no longer sees it after its 5 minutes, but no copy exists outside this window's memory | revoked in Authentik (E6), removed from the vault |
| **Deactivation** | deleted (ADR 0004) | refused by the API at once (§3 step 3) | deleted by Authentik when the app disables the user (E8) |
| **Demotion to user** | stays, as a user | refused at once with `powershell_admins_only` | still renews, but every request is refused while they're a user. Made admin again, it works again |
| **Account deletion** | deleted | refused at once (no user for `sub`) | deleted with the Authentik user (E10) |
| **Authentik restart** | unaffected | still valid: same signing key (E9); the API's cached keys still match | still renews (E9) |
| **Unused for 30 days** | n/a | long expired | refused by Authentik (E4); the next command asks the user to connect again |
| **The browser signs out of Authentik** | ended | Authentik deletes its copy; the API doesn't consult it and accepts it for its remaining minutes | unaffected (E5) |

Two PowerShell windows share one saved sign-in. Rotation means a window holding an old refresh token would be refused, so before each renewal the module reads the current refresh token from the vault, and after a refusal it reads the vault once more and retries if another window saved a newer one. If Authentik refuses the token (`invalid_grant`), the module removes the saved sign-in and stops with "Your PagerArchive sign-in has ended. Run Connect-PagerArchive."

If revocation fails during `Disconnect-PagerArchive` (Authentik unreachable), the sign-in is still removed from the vault and from memory, and a warning says it couldn't be revoked and ends by itself after 30 days unused.

### 5. Staying signed in: the vault (R61)

- **Which vault:** the user's **default** SecretManagement vault, on every platform, or the vault named by `Connect-PagerArchive -Vault`. The module doesn't choose a vault per platform and never registers or configures one, because the vault holds the user's other secrets too. `about_PagerArchive` and `docs/powershell.md` recommend **Microsoft.PowerShell.SecretStore** on Windows, Linux and macOS alike, because it's Microsoft's and the only cross-platform vault Microsoft lists. They also mention the community vaults Microsoft lists for the operating system's own store (CredMan on Windows, KeyChain on macOS).
- **Set-up commands** (in the docs and in the warning from question 1):

  ```powershell
  Install-PSResource -Name Microsoft.PowerShell.SecretStore -Repository PSGallery
  Register-SecretVault -Name SecretStore -ModuleName Microsoft.PowerShell.SecretStore -DefaultVault
  ```

- **SecretStore's defaults** (tested): it asks for a password when first used, and again after 15 minutes (`PasswordTimeout 900`). In a non-interactive session it fails with `PasswordRequiredException`. The module passes that error on with "Unlock your vault with Unlock-SecretStore", and never asks for the vault's password itself. The docs suggest a longer `PasswordTimeout`, not `-Authentication None`, which leaves the key readable by anyone with the user's files.
- **What's saved:** one secret per server, named `PagerArchive:<origin>` (e.g. `PagerArchive:https://pager.example.org`), whose value is the refresh token as a `SecureString`. A plain `SecureString` works with every vault. The name carries the server, so a new window finds the saved sign-in without being told the URL (A51). The access token is never saved: a new window gets one by renewing. If several servers are saved, commands in a new window ask for `Connect-PagerArchive -Uri …`, which reuses that server's saved sign-in without opening the browser.
- **No vault:** see question 1.

### 6. Tokens in the module (guidelines §9)

- Tokens are `[securestring]` throughout. API calls use `Invoke-RestMethod -Authentication Bearer -Token`.
- **Authentik's token endpoint** is the one place where a token must be read from, or put into, an HTTP body, and `Invoke-RestMethod` would turn it into a plain string. A private function sends those requests with `System.Net.Http.HttpClient`. The form body is built as bytes, with the refresh token copied out of its `SecureString` (`Marshal.SecureStringToGlobalAllocUnicode`), and the buffers are zeroed after sending. The JSON reply is read as bytes, and the token values (`access_token`, `refresh_token`, ASCII by Authentik's design) are copied character by character into new `SecureString`s, so they never become .NET strings in the module. Other fields (`expires_in`, `error`) are read the same way. Error replies hold no tokens and are read normally.
- The loopback listener (§7) reads the authorization code the same way, into a `SecureString`. The PKCE verifier is a plain string: it's random, made in the module, lives for one sign-in, and is useless without the code.
- **Never** in a file, the console, `-Verbose`/`-Debug`/warning/error text, `$Error` or a transcript. Errors from the token endpoint carry Authentik's `error` code only. A Pester test runs every command with `-Verbose -Debug`, captures all streams and `$Error`, and searches them for the token (read from the test's vault), the test pages' text and addresses (guidelines §6, §9).
- **Plain HTTP** needs `-AllowUnencryptedAuthentication` (the name `Invoke-RestMethod` uses) and gives a warning. The choice is remembered in the saved sign-in's name (an `http://` origin), and a later window warns again each time it uses it. Authentik's URL, from `/auth/powershell`, must use the same scheme rule.
- The module only ever talks to the app's URL and the issuer that `/auth/powershell` names (guidelines §9).

### 7. Signing in

`Connect-PagerArchive -Uri <app URL>`:

1. `GET <app>/auth/powershell`, then the issuer's discovery document.
2. If the vault has a saved sign-in for that server, renew it and stop there (no browser).
3. Otherwise, **browser sign-in** (default): a `System.Net.Sockets.TcpListener` on `127.0.0.1`, port 0 (the system picks a free port). `HttpListener` isn't used, because on Windows it needs a URL reservation for `127.0.0.1`. The module creates the PKCE verifier and S256 challenge and a random `state`, then opens the system browser at the authorize URL with `Start-Process` (Windows, macOS and Linux's `xdg-open`). The listener takes one request, checks `state`, reads `code`, answers with a short page ("You're signed in to PagerArchive PowerShell. You can close this tab."), and closes. It waits up to 5 minutes, in short polls so Ctrl+C works. The code and verifier are exchanged for tokens.
4. **`-UseDeviceCode`**: the module asks Authentik's device endpoint for a code and shows "To sign in, open <AUTHENTIK_URL>/device on any device and enter the code ABCD-EFGH" as a **warning**, which is how `Connect-AzAccount -UseDeviceAuthentication` does it, because `Write-Host` isn't allowed and information messages are hidden by default. It polls at Authentik's interval (5 s) until signed in, refused or expired (10 minutes).
5. `GET /api/v1/me` checks the account. A non-admin gets the API's refusal (§3), and the sign-in is revoked and not saved (A53).
6. The refresh token is saved (§5), and a `PagerArchive.Connection` object is output: `Uri`, `Name`, `Email`, `Role`, `Vault`, `ExpiresAt` (of the access token).

If the browser can't be started, the error suggests `-UseDeviceCode`. The browser-starting and code-showing steps are two private functions, so the Pester tests replace them with a function that signs in through Authentik's flow executor, as `internal/stacktest` does in Go.

`Disconnect-PagerArchive` revokes, removes and clears (§4). Neither command changes PagerArchive data, so neither has `-WhatIf`.

### 8. API changes

**One batch import** (R27a, R63): `POST /api/v1/admin/aliases/import`, admins only.

```json
{
  "rows": [{"pattern": "0300001", "name": "Brigade", "template": "Example Fire", "colour": "#c00", "ignored": false}],
  "agency_profiles": {"Example Fire": "example-fire"},
  "update": false,
  "dry_run": true,
  "plan": null
}
```

- The rows are applied in **one transaction**, through the same catalog code as before: rows for one capcode or pattern (after zero-padding, R27a) that differ in any imported field are a **conflict**, and any conflict changes nothing; identical rows are **squashed**, keeping the first; new capcodes are **added**; existing aliases that agree are **unchanged**, and those that differ are **different**, applied only with `update` (then **updated**, recorded in history as made by the signed-in admin, R57). One template is created per new agency name, with the profile from `agency_profiles`. An unknown profile name refuses the whole import (`422 unknown_profile`), because a typing mistake would otherwise make that agency's pages text only. Before M8 it wasn't checked.
- **The reply has one result per row**, in the order sent: `{index, pattern, name, result, changes, squashed_into}`, plus `conflicts` (each with every row index involved), `invalid` (rows refused on their own, e.g. a pattern that isn't 1–7 digits, `%` and `_`, or an empty name; they're skipped, as before), `templates_created`, and `repeated_names` (A19). A conflict answers `409 import_conflict` with the same body and nothing applied.
- **Plans** keep `-WhatIf` and `-Confirm` honest. `"plan": null` with `"dry_run": true` computes everything and changes nothing. The module then asks ShouldProcess per change, and sends the confirmed rows with `"plan"` set to the dry run's fingerprint of those rows' outcomes (SHA-256, as for the tree preview in ADR 0008 §5). If anything changed in between (someone added one of the capcodes), the server answers `409 import_changed` with the new results, and nothing is applied. The module reports that, and the admin runs it again.
- At most **20,000 rows** and 8 MB per request. Larger imports are refused with the limit named. The module sends one request whatever the number of rows, so the batch stays whole.
- A re-parse is queued once, as now.
- The overrides file and its code are removed (R27a: "there's no overrides file").

**Several values per filter** (R33a, R63a) on `GET /api/v1/messages` (and the live stream, which uses the same filter):

- Repeating a parameter means **any of** those values; different parameters must all match. This applies to `capcode`, `alias`, `alias_id`, `agency`, `group`, `priority`, `category`, `job` and the new `flag`. Existing single-value requests mean the same as before.
- New `alias_like`, `agency_like` and `group_like` take case-insensitive `LIKE` patterns. The module turns PowerShell wildcards into them (`*` → `%`, `?` → `_`, with `%`, `_` and `\` escaped), so `-Alias 'Station*'` works as a PowerShell user expects and an exact name matches only itself. The browser's partial matching (R33a, "contains") stays as it is. The single box with exact and partial values is M12.
- `flag=parse_failed|address_unsplit|location_mismatch`, admins only, any of. The old `parse_failed=true` etc. still work for the browser.
- `limit` (1–200, default 50), so `-First 2` asks for two.
- `receptions=true` adds each message's receive times, one extra query per page, because R63a puts them in every message.

**Templates for admins** (what `pagerarchive template` did): `GET /api/v1/admin/templates` (with alias counts), `POST` and `PUT /api/v1/admin/templates/{id}` (name, agency, colour, agency profile; a changed profile re-parses, R23a) and `DELETE` (refused while aliases or waiting requests use it, saying how many, R64). M10 builds the browser's template screens and icons on these.

**Aliases for admins:** `GET /api/v1/admin/aliases` gains `after` (keyset paging), so the module can list every alias, and `colour`. The rest is already there (ADR 0007).

### 9. `pagerarchive` keeps only server jobs (R59)

The `template` and `alias` commands, `cmd/pagerarchive/catalog.go`, the CSV reader, the overrides and the CLI-only catalog functions are removed. `pagerarchive help` lists `serve`, `validate-profile`, `client` and `version` (A54). `docs/deploy.md`, `docs/profiles.md` and the README stop mentioning the removed commands and point to the module.

### 10. The module

**Layout** (guidelines §10), in `powershell/PagerArchive/`:

```
PagerArchive.psd1            manifest: FunctionsToExport and AliasesToExport listed, never '*'
PagerArchive.psm1            Set-StrictMode -Version Latest; dot-sources Private/ and Public/
PagerArchive.Format.ps1xml   table views
PagerArchive.Types.ps1xml    DefaultDisplayPropertySet per type
Public/<Verb-Noun>.ps1       one exported function per file
Private/*.ps1                API calls, tokens, vault, loopback, wildcard to LIKE, error mapping
en-US/about_PagerArchive.help.txt
```

Tests are in `powershell/tests/`. The analyzer settings are `powershell/PSScriptAnalyzerSettings.psd1`. The manifest: `RootModule`, `ModuleVersion` (set from the release tag when publishing), `CompatiblePSEditions = 'Core'`, `PowerShellVersion = '7.4'` (see above), `RequiredModules = 'Microsoft.PowerShell.SecretManagement'`, MIT licence, project URI.

**Commands** (each with its `Pa` alias, e.g. `Get-PaAlias`):

| Command | Does | Pipeline input | Notes |
|---|---|---|---|
| `Connect-PagerArchive` | §7 | `-Uri` by property name | `-UseDeviceCode`, `-Vault`, `-AllowUnencryptedAuthentication` |
| `Disconnect-PagerArchive` | §4 | `-Uri` by property name (a connection object) | |
| `Get-PagerArchiveTemplate` | lists templates | `-Name` (wildcards) | |
| `New-PagerArchiveTemplate` | adds one | `-Name`, `-Agency`, `-Colour`, `-AgencyProfile` by property name | outputs it |
| `Set-PagerArchiveTemplate` | changes agency, colour, profile | `-Name` (wildcards) | `''` clears colour or profile; `-PassThru` |
| `Remove-PagerArchiveTemplate` | deletes one no alias uses | `-Name` (wildcards) | ConfirmImpact High |
| `Get-PagerArchiveAlias` | lists aliases | `-Name` (wildcards) | also `-Pattern` (wildcards), `-Template` |
| `New-PagerArchiveAlias` | adds one | `-Pattern`, `-Name`, `-Template`, `-Ignored` by property name | outputs it |
| `Set-PagerArchiveAlias` | changes template or ignored | alias objects (`-Pattern` by property name), or `-Name` (wildcards) | `-Template ''` removes the template, `-Ignored` is `[Nullable[bool]]`; `-PassThru` |
| `Rename-PagerArchiveAlias` | renames | as `Set` | `-NewName`; `-PassThru` |
| `Remove-PagerArchiveAlias` | deletes | as `Set` | ConfirmImpact High |
| `Import-PagerArchiveAlias` | §8 import | rows by property name | `-AgencyProfile` hashtable, `-Update` (ConfirmImpact High), `-WhatIf` |
| `Get-PagerArchiveMessage` | §8 search | `-Capcode` (strings, or by property name), `-Alias` (by property name, also from `Name`) | `SupportsPaging` |

Template renaming isn't in M8, because the old command line couldn't do it. M10's template screens add it, with icons (R65). The old text `icon` isn't shown or set: icons become images in M10, and the import ignores the CSV's `icon` column (R30).

**Objects and parameters line up** (guidelines §3):

- `PagerArchive.Alias`: `Pattern` (the capcode or pattern), `Name`, `Template`, `Colour` (the template's), `Ignored`. These are exactly the import's parameters: `-Pattern` (alias `address`), `-Name` (alias `alias`), `-Template` (alias `agency`), `-Colour` (alias `color`), `-Ignored` (alias `ignore`). So `Get-PagerArchiveAlias | Export-Csv` writes a file that imports as all "unchanged" (A54), and the browser's export (`id,address,alias,agency,icon,color,ignore`) imports through `Import-Csv` too. The object has no `Agency` or `Capcode` property on purpose: either would bind a second parameter from the pipeline (`Template` twice, or a message search by capcode).
- `-Ignored` on the import takes `true`/`false`/`1`/`0`/`yes`/`no`/empty from a CSV through a small argument-transformation class in the `.psm1`, because `[switch]` turns any non-empty string, including `"false"`, into `$true`.
- `PagerArchive.Template`: `Name`, `Agency`, `Colour`, `AgencyProfile`, `AliasCount`.
- `PagerArchive.ImportResult`: `Index` (the row's position in the pipeline, from 1), `Pattern`, `Name`, `Result` (`Added`, `Unchanged`, `Different`, `Updated`, `Squashed`), `Changes` (e.g. `name: "Brigade" → "Brigade North"`), `SquashedInto` (index).
- `PagerArchive.Message` (R63a, what the detail view shows): `Id`, `SentAt` (`[datetimeoffset]`), `Capcode`, `Alias`, `Agency`, `Priority`, `Text` (as a pager shows it, without the marker, in full), `RawText` (as stored, with the marker), `Group`, `JobNumber`, `EventType`, `Category`, `Description`, `Address`, `Locality`, `GridRef`, `Units`, `Fields` (extra fields), `Location` (`Longitude`, `Latitude`, `Approximate`, `Source`), `ParseFailed`, `AddressUnsplit`, `LocationMismatch`, `Heartbeat`, `Ignored`, `Receptions` (`Client`, `ReceivedAt`). The default table shows `SentAt`, `Priority`, `Alias`, `Text`.

**Get-PagerArchiveMessage** parameters: `-Text`, `-Capcode`, `-Alias`, `-Agency`, `-Group` (wildcards on the last three), `-Priority` (`Emergency`, `Non-emergency`, `Admin`, `None`), `-Category` (completed from the server's categories), `-JobNumber`, `-After`, `-Before` (`[datetimeoffset]`), `-Unaliased`, `-IncludeIgnored`, `-Flag`. Array parameters mean "any of" (R63a). Pipeline input is collected in `process` and sent as **one** search in `end` (A55: one request for messages), so piped aliases combine into a single filter. Pages are fetched one at a time (200 messages, or fewer for `-First`), and each page's messages are written before the next is requested, so `-First` and `Select-Object -First` stop further requests. With no filter it returns every message.

**Import through the pipeline:** rows are collected in `process`, then in `end` there's one dry run, then: a conflict becomes one non-terminating error per row involved (`ErrorId` `import_conflict`, the row as `TargetObject`), and nothing is applied (guidelines §7); invalid rows become their own errors and are skipped; for each add or update, ShouldProcess (question 4); then one apply with the plan. `Write-Progress` shows while the batch is sent and applied.

**Errors** follow guidelines §7: the API's code is the `ErrorId`, its message the message, the input item the `TargetObject`, and the category comes from the guidelines' table. Not being signed in, or the server being unreachable, is terminating. `-Verbose` shows each API call's method, path and query parameter **names** only, because values (a text search, an address) could hold page text (P1).

### 11. Tests

**Pester meta-tests** (`powershell/tests/Meta.Tests.ps1`), run for every exported command, one test per [pester] rule in the guidelines: approved verbs and the `PagerArchive` noun prefix; a `Pa` alias for each; manifest exports listed exactly; `[CmdletBinding()]`, `[OutputType()]` naming a `PagerArchive.*` type; every parameter set has a pipeline parameter and at most one `ValueFromPipeline`; `SupportsShouldProcess` and the right `ConfirmImpact` on every command that changes data (including the `-Update` prompt); `-PassThru` on `Set` and `Rename`; wildcards on `-Name` (case-insensitive); `SupportsPaging` where many items can come back; a format view and a `DefaultDisplayPropertySet` for each output type; `.SYNOPSIS`, `.DESCRIPTION`, `.EXAMPLE`, `.INPUTS`, `.OUTPUTS` (naming the type) and `.LINK` on each command; no `.PARAMETER` and help text for every parameter in `Get-Help -Full`; `about_PagerArchive` exists; error records carry the API's code and the table's category; tokens never in output, `$Error` or a transcript; HTTP refused without `-AllowUnencryptedAuthentication`; `CompatiblePSEditions = 'Core'`, and `PowerShellVersion` not past Microsoft's end of support (table in the test, from the lifecycle page).

**Unit tests** (`Module.Tests.ps1`) mock the private API function: pipeline binding (aliases into messages, CSV rows into the import), wildcard to `LIKE` conversion, paging stopping, import plans, the `-Ignored` transformation, error mapping.

**PSScriptAnalyzer** with `PSScriptAnalyzerSettings.psd1`: every default rule plus `PSProvideCommentHelp`, `PSUseShouldProcessForStateChangingFunctions`, `PSAvoidUsingPositionalParameters`, `PSAvoidUsingCmdletAliases`, `PSUseOutputTypeCorrectly`, `PSAvoidUsingPlainTextForPassword`, `PSAvoidUsingConvertToSecureStringWithPlainText`, and the `CodeFormattingOTBS` formatting rules with 4-space indents. Zero warnings, or CI fails.

**Behaviour against the real stack** (`powershell/tests/Stack.Tests.ps1`, run by `scripts/stack-checks.sh` after the Go stack tests): A51 (browser sign-in replaced by the flow-executor sign-in; a second `pwsh` process as the "new window"; a search of every file under the test's home and temp directories, outside SecretStore's own files, for the token; disconnect, then the old refresh token refused by Authentik), A52 (device code), A53 (user B refused, C demoted, re-promoted and deactivated, with the old refresh token refused), A54, A55, A20 and A21. A test vault is SecretStore with `-Authentication None` in the test's own home directory.

**Go:** A53's token checks at API level (`internal/server`) with a local test issuer: expired, another key, another audience (the browser's client), another issuer, an ID token, a user, a deactivated user, a deleted account. Against real Authentik (`internal/stacktest`): a real browser-client token and a forged token are both refused. The import's API checks (A19–A21, conflicts, squashing, plans, `import_changed`, unknown profiles) replace the old CSV tests.

**CI** (`.github/workflows/ci.yml`): a `powershell` job on `ubuntu-24.04` and `windows-2025`, each with PowerShell **7.4** and **7.6** (installed from Microsoft's release archives at a pinned patch version), runs the analyzer, the meta-tests and the unit tests. The behaviour tests need the Linux compose stack, so they run in the existing `stack` job, under both 7.4 and 7.6. GitHub's Windows runners can't run the Linux containers.

### 12. Publishing to the PowerShell Gallery

- A new `publish-powershell` job runs **only for a `v*` tag push** (a release), and `needs` every check job (`test`, `web`, `stack`, `images`, `budget`, `powershell`), so it runs only when they all passed.
- Its first step checks whether the secret `PSGALLERY_API_KEY` exists. If it doesn't, the job logs "PowerShell Gallery key not set: skipping publishing" and succeeds without doing anything. You create the secret. Nothing is published from a branch, and I won't create a tag.
- It copies the module, sets `ModuleVersion` from the tag (`v1.2.3` → `1.2.3`), runs `Test-ModuleManifest`, and runs `Publish-PSResource -Repository PSGallery -ApiKey` with the key from the environment.

### 13. Receiving clients (docs/client.md)

- "Images aren't published yet" is removed: both GHCR images (`ghcr.io/rikiku/pagerarchive`, `ghcr.io/rikiku/pagerarchive-client`) pull without signing in.
- The client binary is pure Go (`CGO_ENABLED=0` in `Dockerfile.client`), so a `release-binaries` job (on `v*` tags, after every check) builds `pagerarchive-client` for linux/amd64 and linux/arm64, with SHA-256 checksums, and attaches them to the GitHub release for the tag (creating the release if it doesn't exist). The "Raspberry Pi (no Docker)" section stays and links to the release.
- Blacklisting `dvb_usb_rtl28xxu`, so the kernel's DVB-T driver doesn't claim the dongle and `rtl_fm` can open it (`/etc/modprobe.d/blacklist-rtl.conf`, then unload it or reboot). This is needed on the host for Docker too.
- A remote receiver must reach the server's public URL (through the reverse proxy), or a VPN to the server's network if the server stays LAN-only.

### 14. The live copy on cld01

Upgraded in place as before: `pg_dump` backup, the old source kept, the new source in place, `api` rebuilt and restarted (no migration in M8), and `authentik-server` and `authentik-worker` restarted so the blueprint adds the PowerShell client. Accounts, messages and integrations stay. The live copy is plain HTTP on the LAN, so `Connect-PagerArchive` needs `-AllowUnencryptedAuthentication` there.

## Consequences

- Admins have one command-line tool, on their own machines, with the same rules and history as the browser. Server shells are needed only for receiving clients.
- A second OAuth client exists in Authentik. Its refresh tokens are new long-lived credentials (30 days from last use), kept in the user's vault. Deactivation and deletion remove them in Authentik, and the API's per-request checks don't depend on them.
- The blueprint now holds one more expression policy, the PKCE check. An Authentik upgrade could change what it relies on (the authorize path and its parameters). `scripts/oauth-probe.py` and the stack checks catch that: the policy fails closed, refusing sign-ins rather than allowing them without PKCE.
- Authentik's own log may contain rejected (no longer working) refresh tokens.
- The import now refuses unknown agency profile names, and the overrides file is gone.
