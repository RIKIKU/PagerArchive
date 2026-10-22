# 9. Milestone M8: the PowerShell module

Date: 2026-10-20, updated 2026-10-21

## Status

Accepted (owner, 2026-10-22). The owner answered its four questions and changed where the sign-in is saved on 2026-10-21 (commits 237aaed and 27d91b8), then, while testing, asked for local times in views (d87fb9b) and own `-First` and `-Skip` instead of `SupportsPaging` (cf39d1d). This ADR was updated to match.

## Context

M8 (SPEC.md R59–R63a, owner 2026-10-20) moves admin command-line work from the `pagerarchive` program to a **PowerShell module**, `PagerArchive`, that uses the same API as the browser. The module signs in as a person through Authentik (R60), stays signed in (R61: a DPAPI-encrypted file on Windows, SecretManagement on Linux and macOS), is for admins only (R62), and has template, alias and message commands, including an alias import through the pipeline (R63, R63a). `pagerarchive` keeps `serve`, `validate-profile`, `client` and `version` (R59). The module follows `docs/powershell-guidelines.md`. The checks are A20 and A21 (now through the module) and A51–A55.

Everything about Authentik below was tested on **Authentik 2026.5.5** (the version in `compose.yaml`), on a throwaway stack on cld01, with `scripts/oauth-probe.py` (§2). Its output is in §2.

## The owner's answers (2026-10-21)

1. **No vault.** On Linux and macOS with no SecretManagement vault registered (or SecretManagement not installed), the module signs in for that session only and warns how to set one up (§5). On Windows this case doesn't arise: the sign-in is saved with DPAPI (§5).
2. **SecretManagement** stays, for Linux, macOS and `-Vault`, although Microsoft has archived it ("feature complete … supported for security and critical bug fixes"; SecretManagement 1.1.2, SecretStore 1.0.6). It isn't in `RequiredModules`: it's loaded only when needed (§5).
3. **Sign-in method.** "Emailed code" in R60 and A51 was a spec mistake. The module uses the normal email-and-password sign-in. The module doesn't change the sign-in: it opens whatever Authentik's sign-in asks for.
4. **`-WhatIf` on import.** Import calls `ShouldProcess` **once for the whole batch**, with a summary such as "412 aliases: 380 new, 12 different". Under `-WhatIf` it still outputs every row's result, marked as not applied (`Applied = $false`), so the results can be filtered before the real import (R63, A54, guidelines §4). Details in §8 and §10.

Smaller points, as agreed:

- `PowerShellVersion = '7.6'` (guidelines §1). There's no date-based test. CI runs on 7.6.0 (the minimum) and the newest stable release, on Windows and Linux (§11).
- CI pins **Pester 5.9.1** and PSScriptAnalyzer 1.25.0.
- The import refuses unknown agency profile names (§8).
- `docs/deploy.md` says that Authentik logs rejected refresh tokens (§2).

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
| **Sign-in** (`Connect-PagerArchive`) | unchanged | issued | issued, saved (§5: DPAPI file, vault, or this session only) |
| **Renewal** (any command, when the access token has under 60 s left) | n/a | a new one | a new one, saved at once in the same place; the old one is revoked by Authentik (E3) |
| **Disconnect** | unchanged | revoked in Authentik and dropped from memory; the API checks access tokens itself, so it would accept it for its remaining minutes, but no copy exists outside this session's memory | revoked in Authentik (E6); the saved copy is deleted, whichever place it's in (the DPAPI file, or the vault secret and its file) |
| **Deactivation** | deleted (ADR 0004) | refused by the API at once (§3 step 3) | deleted by Authentik when the app disables the user (E8) |
| **Demotion to user** | stays, as a user | refused at once with `powershell_admins_only` | still renews, but every request is refused while they're a user. Made admin again, it works again |
| **Account deletion** | deleted | refused at once (no user for `sub`) | deleted with the Authentik user (E10) |
| **Authentik restart** | unaffected | still valid: same signing key (E9); the API's cached keys still match | still renews (E9) |
| **Unused for 30 days** | n/a | long expired | refused by Authentik (E4); the next command asks the user to connect again |
| **The browser signs out of Authentik** | ended | Authentik deletes its copy; the API doesn't consult it and accepts it for its remaining minutes | unaffected (E5) |

Two PowerShell windows share one saved sign-in. Rotation means a window holding an old refresh token would be refused, so before each renewal the module reads the current refresh token from where it's saved (§5), and after a refusal it reads it once more and retries if another window has saved a newer one (the file's `version` changed). If Authentik refuses the token (`invalid_grant`), the module removes the saved sign-in and stops with "Your PagerArchive sign-in has ended. Run Connect-PagerArchive."

If revocation fails during `Disconnect-PagerArchive` (Authentik unreachable), the saved copy is still deleted and the sign-in dropped from memory, and a warning says it couldn't be revoked and ends by itself after 30 days unused.

### 5. Staying signed in: where the sign-in is saved (R61)

What's saved is the **refresh token**, as a `SecureString`. The access token is never saved: a new window gets one by renewing. Where it goes:

| Where | Store | Set-up needed |
|---|---|---|
| **Windows** (no `-Vault`) | A file in the user's profile, holding the refresh token encrypted with DPAPI by `ConvertFrom-SecureString` with no key, so only that Windows account on that machine can read it | None. SecretManagement isn't loaded |
| **Linux and macOS** (no `-Vault`) | The user's **default** SecretManagement vault, secret `PagerArchive:<origin>` | A registered default vault |
| **Linux and macOS, no vault** (SecretManagement not installed, or no vault registered) | Nowhere: the sign-in lasts for **this session only**, with a warning | None |
| **Anywhere, `-Vault <name>`** | That SecretManagement vault, secret `PagerArchive:<origin>` | That vault. Without SecretManagement installed, `-Vault` stops with "-Vault needs the Microsoft.PowerShell.SecretManagement module. Install it with: Install-PSResource -Name Microsoft.PowerShell.SecretManagement" (`ErrorId` `secretmanagement_missing`, category `NotInstalled`) |

- **The file.** `[Environment]::GetFolderPath('LocalApplicationData')/PagerArchive/SignIns/`, which is `%LOCALAPPDATA%\PagerArchive\SignIns\` on Windows. Local, not roaming, because a refresh token rotates on every use (§4) and two machines sharing one would sign each other out. One file per server, named from the first 16 hex digits of the SHA-256 of the server's origin (`<hash>.json`), holding JSON: `uri` (the app's URL), `store` (`dpapi` or `vault`), `vault` (the vault's name, for `vault`), `version` (a new GUID on every save) and, for `dpapi` only, `refreshToken`, the output of `ConvertFrom-SecureString` (a DPAPI blob in hex). For a `vault` sign-in the file holds no token: it tells a new window which vault to read, so a sign-in made with `-Vault` carries across windows too. Files are written under a temporary name in the same folder and then moved over the old one, so another window never reads half a file.
- **`ConvertFrom-SecureString` with no key is used only on Windows.** Off Windows it doesn't encrypt: it writes the string's UTF-16 bytes in hex. One private function saves and loads the file's token, and it checks `$IsWindows` before either. A Pester test proves both halves: on Linux, `ConvertFrom-SecureString` with no key gives hex that decodes to the plain text, and the module, connecting on Linux with no vault, never calls it and writes no file (§11).
- **SecretManagement is loaded only when needed**: on Linux and macOS, or with `-Vault`. It's not in `RequiredModules`. The module calls only `Get-SecretVault`, `Get-Secret`, `Set-Secret` and `Remove-Secret`, from one private file.
- **The no-vault warning** (Linux and macOS): "No SecretManagement vault is registered, so this sign-in lasts until this PowerShell session ends. To stay signed in, register a vault: Install-PSResource -Name Microsoft.PowerShell.SecretManagement, Microsoft.PowerShell.SecretStore; Register-SecretVault -Name SecretStore -ModuleName Microsoft.PowerShell.SecretStore -DefaultVault". A new session finds no saved sign-in and asks the user to run `Connect-PagerArchive` (A51). The module never registers or configures a vault itself, because a vault holds the user's other secrets too.
- **SecretStore's defaults** (tested): it asks for a password when first used, and again after 15 minutes (`PasswordTimeout 900`). In a non-interactive session it fails with `PasswordRequiredException`. The module passes that on with "Unlock your vault with Unlock-SecretStore", and never asks for the vault's password itself. The docs suggest a longer `PasswordTimeout`, not `-Authentication None`, which leaves the key readable by anyone with the user's files.
- **Finding the sign-in in a new window.** The first command reads the files in `SignIns`. With one, it uses that server. With several, it asks for `Connect-PagerArchive -Uri …`, which reuses that server's saved sign-in without opening the browser.
- **Plain HTTP.** A sign-in saved with `-AllowUnencryptedAuthentication` keeps its `http://` URL in the file, and each new window warns again when it uses it.

### 6. Tokens in the module (guidelines §9)

- Tokens are `[securestring]` throughout. API calls use `Invoke-RestMethod -Authentication Bearer -Token`.
- **Authentik's token endpoint** is the one place where a token must be read from, or put into, an HTTP body, and `Invoke-RestMethod` would turn it into a plain string. A private function sends those requests with `System.Net.Http.HttpClient`. The form body is built as bytes, with the refresh token copied out of its `SecureString` (`Marshal.SecureStringToGlobalAllocUnicode`), and the buffers are zeroed after sending. The JSON reply is read as bytes, and the token values (`access_token`, `refresh_token`, ASCII by Authentik's design) are copied character by character into new `SecureString`s, so they never become .NET strings in the module. Other fields (`expires_in`, `error`) are read with `ConvertFrom-Json` from a copy of the reply whose token values were overwritten first. The reply's bytes are then zeroed. Error replies hold no tokens.
- The loopback listener (§7) reads the authorization code the same way, into a `SecureString`. The PKCE verifier is a plain string: it's random, made in the module, lives for one sign-in, and is useless without the code.
- **Never** in a file, the console, `-Verbose`/`-Debug`/warning/error text, `$Error` or a transcript. Errors from the token endpoint carry Authentik's `error` code only. A Pester test runs every command with `-Verbose -Debug`, captures all streams and `$Error`, and searches them for the tokens (read from the test's saved sign-in), the test pages' text and addresses (guidelines §6, §9).
- **Plain HTTP** needs `-AllowUnencryptedAuthentication` (the name `Invoke-RestMethod` uses) and gives a warning. The choice is remembered by the saved sign-in's `http://` URL (§5), and a later window warns again each time it uses it. Authentik's URL, from `/auth/powershell`, must use the same scheme rule.
- The module only ever talks to the app's URL and the issuer that `/auth/powershell` names (guidelines §9).

### 7. Signing in

`Connect-PagerArchive -Uri <app URL>`:

1. `GET <app>/auth/powershell`, then the issuer's discovery document.
2. If there's a saved sign-in for that server (§5), renew it and stop there (no browser).
3. Otherwise, **browser sign-in** (default): a `System.Net.Sockets.TcpListener` on `127.0.0.1`, port 0 (the system picks a free port). `HttpListener` isn't used, because on Windows it needs a URL reservation for `127.0.0.1`. The module creates the PKCE verifier and S256 challenge and a random `state`, then opens the system browser at the authorize URL with `Start-Process` (Windows, macOS and Linux's `xdg-open`). The listener takes one request, checks `state`, reads `code`, answers with a short page ("You're signed in to PagerArchive PowerShell. You can close this tab."), and closes. It waits up to 5 minutes, in short polls so Ctrl+C works. The code and verifier are exchanged for tokens.
4. **`-UseDeviceCode`**: the module asks Authentik's device endpoint for a code and shows "To sign in, open <AUTHENTIK_URL>/device on any device and enter the code ABCD-EFGH" as a **warning**, which is how `Connect-AzAccount -UseDeviceAuthentication` does it, because `Write-Host` isn't allowed and information messages are hidden by default. It polls at Authentik's interval (5 s) until signed in, refused or expired (10 minutes).
5. `GET /api/v1/me` checks the account. A non-admin gets the API's refusal (§3), and the sign-in is revoked and not saved (A53).
6. The refresh token is saved (§5), and a `PagerArchive.Connection` object is output: `Uri`, `Name`, `Email`, `Role`, `SavedIn` (`DPAPI file`, `Vault <name>` or `This session only`), `ExpiresAt` (of the access token).

If the browser can't be started, the error suggests `-UseDeviceCode`. The browser is opened with `Start-Process` on Windows, `open` on macOS and `xdg-open` on Linux, so the stack tests hand the URL to the Go test with a stand-in `xdg-open` (§11), and the code is shown as a warning, which the test reads from the window's output.

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
- **The reply has one result per row**, in the order sent: `{index, pattern, name, result, changes, squashed_into, problem}`, plus `conflicts` (each with every row index involved), `templates_created`, `repeated_names` (A19), `plan` and `applied`. A row refused on its own (a pattern that isn't 1–7 digits, `%` and `_`, or an empty name) has the result `invalid` and a `problem`, and is skipped, as before; the module reports it as an error. A conflict answers `409 import_conflict` with the reason and the report, rows in conflict marked `conflict`, and nothing applied.
- **Plans** keep `-WhatIf` and `-Confirm` honest. `"dry_run": true` computes everything and changes nothing, and its reply carries `plan`, a fingerprint (SHA-256, as for the tree preview in ADR 0008 §5) of every row's outcome. The module asks ShouldProcess **once for the whole batch** with the dry run's summary (owner, 2026-10-21), and if the answer is yes it sends the same rows again with that `plan`. If anything changed in between (someone added one of the capcodes), the server answers `409 import_changed` with the new results, and nothing is applied. The module reports that, and the admin runs it again.
- At most **20,000 rows** and 8 MB per request. Larger imports are refused with the limit named. The module sends one request whatever the number of rows, so the batch stays whole.
- A re-parse is queued once, as now.
- The overrides file and its code are removed (R27a: "there's no overrides file").

**Several values per filter** (R33a, R63a) on `GET /api/v1/messages` (and the live stream, which uses the same filter):

- Repeating a parameter means **any of** those values; different parameters must all match. This applies to `capcode`, `alias`, `alias_id`, `agency`, `group`, `priority`, `category`, `job` and the new `flag`. Existing single-value requests mean the same as before.
- New `alias_like`, `agency_like` and `group_like` take case-insensitive `LIKE` patterns. The module turns PowerShell wildcards into them (`*` → `%`, `?` → `_`, with `%`, `_` and `\` escaped), so `-Alias 'Station*'` works as a PowerShell user expects and an exact name matches only itself. The browser's partial matching (R33a, "contains") stays as it is. The single box with exact and partial values is M12.
- `flag=parse_failed|address_unsplit|location_mismatch`, admins only, any of. The old `parse_failed=true` etc. still work for the browser.
- `limit` (1–200, default 50), so `-First 2` asks for two.
- `receptions=true` adds each message's receive times, one extra query per page, because R63a puts them in every message.

**Templates for admins** (what `pagerarchive template` did): `GET /api/v1/admin/templates` (with alias counts), `POST /api/v1/admin/templates` (name, agency, colour, agency profile) and `PATCH /api/v1/admin/templates/{id}` (agency, colour, agency profile; only the fields sent change; a changed profile re-parses, R23a) and `DELETE` (refused while aliases or waiting requests use it, saying how many, R64). M10 builds the browser's template screens and icons on these.

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

Tests are in `powershell/tests/`. The analyzer settings are `powershell/PSScriptAnalyzerSettings.psd1`. The manifest: `RootModule`, `ModuleVersion` (set from the release tag when publishing), `CompatiblePSEditions = 'Core'`, `PowerShellVersion = '7.6'` (guidelines §1), no `RequiredModules` (SecretManagement is loaded only when needed, §5), MIT licence, project URI.

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
| `Get-PagerArchiveMessage` | §8 search | `-Capcode` and `-Alias` (also from `Name`), both by property name | its own `-First` and `-Skip` (`[uint64]`), no `-IncludeTotalCount` (owner, 2026-10-22) |

Template renaming isn't in M8, because the old command line couldn't do it. M10's template screens add it, with icons (R65). The old text `icon` isn't shown or set: icons become images in M10, and the import ignores the CSV's `icon` column (R30).

**Objects and parameters line up** (guidelines §3):

- `PagerArchive.Alias`: `Pattern` (the capcode or pattern), `Name`, `Template`, `Colour` (the template's), `Ignored`. These are exactly the import's parameters: `-Pattern` (alias `address`), `-Name` (alias `alias`), `-Template` (alias `agency`), `-Colour` (alias `color`), `-Ignored` (alias `ignore`). So `Get-PagerArchiveAlias | Export-Csv` writes a file that imports as all "unchanged" (A54), and the browser's export (`id,address,alias,agency,icon,color,ignore`) imports through `Import-Csv` too. The object has no `Agency` or `Capcode` property on purpose: either would bind a second parameter from the pipeline (`Template` twice, or a message search by capcode).
- `-Ignored` on the import takes `true`/`false`/`1`/`0`/`yes`/`no`/empty from a CSV through a small argument-transformation class in the `.psm1`, because `[switch]` turns any non-empty string, including `"false"`, into `$true`.
- `PagerArchive.Template`: `Name`, `Agency`, `Colour`, `AgencyProfile`, `AliasCount`.
- `PagerArchive.ImportResult`: `Index` (the row's position in the pipeline, from 1), `Pattern`, `Name`, `Result` (`Added`, `Unchanged`, `Different`, `Updated`, `Squashed`), `Changes` (e.g. `name: "Brigade" → "Brigade North"`), `SquashedInto` (index), `Applied` (`$false` under `-WhatIf`, or when the batch was declined at the prompt; `$true` once the batch has been applied). The default table shows `Index`, `Result`, `Pattern`, `Name`, `Changes` and `Applied`.
- `PagerArchive.Message` (R63a, what the detail view shows): `Id`, `SentAt` (`[datetimeoffset]`), `Capcode`, `Alias`, `Agency`, `Priority`, `Text` (as a pager shows it, without the marker, in full), `RawText` (as stored, with the marker), `Group`, `JobNumber`, `EventType`, `Category`, `Description`, `Address`, `Locality`, `GridRef`, `Units`, `Fields` (extra fields), `Location` (`Longitude`, `Latitude`, `Approximate`, `Source`), `ParseFailed`, `AddressUnsplit`, `LocationMismatch`, `Heartbeat`, `Ignored`, `Receptions` (`Client`, `ReceivedAt`). The default table shows `SentAt`, `Priority`, `Alias`, `Text`.

**Piping capcodes.** `-Capcode` takes pipeline input by property name only. With `ValueFromPipeline` as well, PowerShell also tries to turn a piped alias object into a capcode string after binding `-Alias` from its `Name`, which fails validation and drops the object. Capcodes are piped as objects with a `Capcode` property (a message, or `Select-Object Capcode`).

**Get-PagerArchiveMessage** parameters: `-Text`, `-Capcode`, `-Alias`, `-Agency`, `-Group` (wildcards on the last three), `-Priority` (`Emergency`, `Non-emergency`, `Admin`, `None`), `-Category` (completed from the server's categories), `-JobNumber`, `-After`, `-Before` (`[datetimeoffset]`), `-Unaliased`, `-IncludeIgnored`, `-Flag`, `-First`, `-Skip`. `-First` and `-Skip` are its own `[uint64]` parameters, not `SupportsPaging`, so there's no `-IncludeTotalCount` (guidelines §3, owner, 2026-10-22). Array parameters mean "any of" (R63a). Pipeline input is collected in `process` and sent as **one** search in `end` (A55: one request for messages), so piped aliases combine into a single filter. Pages are fetched one at a time (200 messages, or fewer for `-First`), and each page's messages are written before the next is requested, so `-First` and `Select-Object -First` stop further requests. With no filter it returns every message.

**Import through the pipeline:** rows are collected in `process`. In `end`:

1. One dry run. A conflict becomes one non-terminating error per row involved (`ErrorId` `import_conflict`, the row as `TargetObject`), and nothing is applied or asked (guidelines §7). Invalid rows become their own errors and are skipped, as before.
2. **One** `ShouldProcess` call for the batch (guidelines §4), with the target "412 aliases: 380 new, 12 different, 3 squashed, 17 unchanged" (counts of zero are left out; with `-Update`, "12 to update"). The action is "Import aliases", or "Import aliases and update existing ones" with `-Update`.
3. Under `-WhatIf`, or if the prompt is declined: every row's dry-run result is output with `Applied = $false`, and nothing changes, not even new capcodes (A54). Otherwise one apply with the plan, then every row's result with `Applied = $true`.

`ConfirmImpact` is `Medium`. With `-Update` the import must count as `High` (guidelines §4), which an attribute can't express for one parameter, so when `-Update` is given and `-Confirm` isn't, the function lowers its own `$ConfirmPreference` from `High` to `Medium`, so the prompt appears by default exactly as for `ConfirmImpact = 'High'`. `-Confirm:$false` and `$ConfirmPreference = 'None'` still skip it. A Pester test checks both. `Write-Progress` shows while the batch is sent and applied.

**Errors** follow guidelines §7: the API's code is the `ErrorId`, its message the message, the input item the `TargetObject`, and the category comes from the guidelines' table. HTTP errors don't throw inside `Invoke-RestMethod` (`-SkipHttpErrorCheck`), because its exception keeps the request, whose `Authorization` header holds the token, and would land in `$Error`. Each error is written once to the error stream; `-ErrorVariable` also collects errors that private functions raise and the command catches, which is PowerShell's behaviour for caught errors, so the tests count the error stream. Not being signed in, or the server being unreachable, is terminating. `-Verbose` shows each API call's method, path and query parameter **names** only, because values (a text search, an address) could hold page text (P1).

### 11. Tests

**Pester meta-tests** (`powershell/tests/Meta.Tests.ps1`), run for every exported command, one test per [pester] rule in the guidelines: approved verbs and the `PagerArchive` noun prefix; a `Pa` alias for each; manifest exports listed exactly; `[CmdletBinding()]`, `[OutputType()]` naming a `PagerArchive.*` type; every parameter set has a pipeline parameter and at most one `ValueFromPipeline`; `SupportsShouldProcess` and the right `ConfirmImpact` on every command that changes data (including the `-Update` prompt); `-PassThru` on `Set` and `Rename`; wildcards on `-Name` (case-insensitive); its own `-First` and `-Skip` (`[uint64]`) and no `SupportsPaging` or `-IncludeTotalCount` where many items can come back; a format view and a `DefaultDisplayPropertySet` for each output type; `.SYNOPSIS`, `.DESCRIPTION`, `.EXAMPLE`, `.INPUTS`, `.OUTPUTS` (naming the type) and `.LINK` on each command; no `.PARAMETER` and help text for every parameter in `Get-Help -Full`; `about_PagerArchive` exists; error records carry the API's code and the table's category; tokens never in output, `$Error` or a transcript; HTTP refused without `-AllowUnencryptedAuthentication`; `CompatiblePSEditions = 'Core'` and `PowerShellVersion = '7.6'`. No test depends on the date: the minimum is raised in a normal change (guidelines §1).

**Unit tests** (`Module.Tests.ps1`) mock the private API function: pipeline binding (aliases into messages, CSV rows into the import), wildcard to `LIKE` conversion, paging stopping, import plans, the `-Ignored` transformation, error mapping.

**PSScriptAnalyzer** with `PSScriptAnalyzerSettings.psd1`: every default rule plus `PSProvideCommentHelp`, `PSUseShouldProcessForStateChangingFunctions`, `PSAvoidUsingPositionalParameters`, `PSAvoidUsingCmdletAliases`, `PSUseOutputTypeCorrectly`, `PSAvoidUsingPlainTextForPassword`, `PSAvoidUsingConvertToSecureStringWithPlainText`, and the `CodeFormattingOTBS` formatting rules with 4-space indents. Zero warnings, or CI fails.

**Behaviour against the real stack** (`internal/stacktest/powershell_test.go`, run by `scripts/stack-checks.sh` after the other stack tests). Each "window" is a `pwsh` process kept open and fed commands on stdin; the Go test plays the browser through Authentik's flow executor, at the URL the module opens (a stand-in `xdg-open` on `PATH` hands it over), so the module's own loopback listener and device-code polling are exercised. A51 (a fresh browser signs in with email and password; a second `pwsh` process as the "new window"; every stream with `-Verbose -Debug`, a transcript and `$Error` searched for the tokens and page text; a search of every file under the test's home and temp directories, outside SecretStore's own encrypted files, for the tokens in plain text, UTF-16 and hex; disconnect, then the old refresh token refused by Authentik), A52 (device code), A53 (user B refused, C demoted, re-promoted and deactivated, with the old refresh token refused), A54, A55, A20 and A21. It runs twice on Linux: with a SecretStore vault registered (with `-Authentication None`, in the test's own home directory), and with no vault at all, where A51's second session must be asked to sign in and no file may be written under `SignIns`.

**Go:** A53's token checks at API level (`internal/acceptance/powershell_test.go`) with a local test issuer whose signing keys replace discovery (`PowerShell.Keys`), so the production verifier and its configuration are what's tested: expired, another key, another audience (the browser's client), the audience alone wrong, another issuer, the issuer alone wrong, an ID token, another client's `azp`, no signature, a user, a demoted admin, a deactivated user, a deleted account, a token with a session cookie. Against real Authentik (`internal/stacktest`): a real access token works and the same token with its audience changed is refused. The import's API checks (A19–A21, conflicts, squashing, plans, `import_changed`, unknown profiles, the 20,000-row limit) replace the old CSV tests.

**Mutation proof** (`scripts/mutation-checks.sh`): each security check is broken in turn in a throwaway copy, and the test that guards it must fail: the audience, issuer and expiry checks, the access-token check (`azp` and `scope`), admins only, the status check, a token with a cookie, DPAPI saving off Windows, the no-vault store off Windows, and refusing plain HTTP. Writing it found two gaps in the tests, not the code: the audience and issuer checks each have a second line of defence (`azp`, and the signature) that hid them from the tests, so tokens wrong only in the audience, and only in the issuer, were added.

**Saving the sign-in** (`SignIn.Tests.ps1`): on Windows, the DPAPI file round-trips in a second `pwsh` process, holds no token in plain text, UTF-16 or Base64, can't be read back as plain hex, and is deleted by `Disconnect-PagerArchive` (with Authentik's revocation mocked). On Linux, `ConvertFrom-SecureString` with no key is shown to give reversible hex (why it's never used there), and connecting with no vault never calls it, writes nothing under `SignIns`, and warns; with a SecretStore vault the secret is saved, read in a second process, and removed on disconnect; `-Vault` without SecretManagement installed stops with `secretmanagement_missing`.

**CI** (`.github/workflows/ci.yml`): a `powershell` job on `ubuntu-24.04` and `windows-2025`, each with PowerShell **7.6.0** (the minimum, installed from Microsoft's release archive) and the **newest stable release** (the version is read at run time from Microsoft's `https://aka.ms/pwsh-buildinfo-stable` and logged), runs the analyzer, the meta-tests, the unit tests and the sign-in tests (the DPAPI path on Windows; SecretStore and no vault on Linux). The behaviour tests need the Linux compose stack, so they run in the existing `stack` job on Linux, once with a SecretStore vault and once with none. The stack job runs them with 7.6.0, the minimum. GitHub's Windows runners can't run the Linux containers. PSScriptAnalyzer runs once per file, as a Pester test; its `PSUseCorrectCasing` rule sometimes throws a `NullReferenceException` inside 1.25.0 while looking up commands, so the test retries it up to three times.

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

Not touched until the owner has verified the branch (owner, 2026-10-21). Then it's upgraded in place as before: `pg_dump` backup, the old source kept, the new source in place, `api` rebuilt and restarted (no migration in M8), and `authentik-server` and `authentik-worker` restarted so the blueprint adds the PowerShell client. Accounts, messages and integrations stay. The live copy is plain HTTP on the LAN, so `Connect-PagerArchive` needs `-AllowUnencryptedAuthentication` there.

## Consequences

- Admins have one command-line tool, on their own machines, with the same rules and history as the browser. Server shells are needed only for receiving clients.
- A second OAuth client exists in Authentik. Its refresh tokens are new long-lived credentials (30 days from last use), kept encrypted (DPAPI on Windows, a vault elsewhere) or, with no vault, only for the session. Deactivation and deletion remove them in Authentik, and the API's per-request checks don't depend on them.
- The blueprint now holds one more expression policy, the PKCE check. An Authentik upgrade could change what it relies on (the authorize path and its parameters). `scripts/oauth-probe.py` and the stack checks catch that: the policy fails closed, refusing sign-ins rather than allowing them without PKCE.
- Authentik's own log may contain rejected (no longer working) refresh tokens.
- The import now refuses unknown agency profile names, and the overrides file is gone.
