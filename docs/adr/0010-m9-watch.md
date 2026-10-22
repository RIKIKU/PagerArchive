# 10. Milestone M9: watching from PowerShell

Date: 2026-10-22

## Status

Proposed. The owner decides the questions at the end.

## Context

M9 (SPEC.md R63b, check A59) adds `Watch-PagerArchiveMessage` (alias `Watch-PaMessage`): new messages go down the PowerShell pipeline as they arrive, filtered as `Get-PagerArchiveMessage` filters, until Ctrl+C. It must survive lost connections without skipping or repeating a page, renew its sign-in for days, and stop with an error when the person is deactivated or loses admin (R63b). It follows `docs/powershell-guidelines.md`.

Two things in the server didn't support that:

- **A role change didn't reach open streams.** Only deactivation and account deletion closed a user's live connections (`SignedOut` → `Hub.CloseUsers`). `SetRole` didn't, and `stream()` worked out `isAdmin` once, at connect. A demoted admin's open stream, in the browser too, kept sending admin-only details (flags, client names in receive times) until it reconnected.
- **A bearer-token stream outlived its checks.** The API checks a module token, and the person's status and role, on every request (R60, ADR 0009 §3). A stream is one request, so a stream opened with a token kept running after the token expired, and after a revocation in Authentik that the API never hears about.

## Already decided elsewhere (cited, not re-decided)

- Live updates are Server-Sent Events on `GET /api/v1/stream`, carrying message IDs through the in-process hub; each connection checks each new message against its own filter with the message list's query (ADR 0001, A4). There's no message queue (section 8). The API is one process.
- The module's access tokens last 5 minutes and are renewed with a rotating refresh token (ADR 0009 §1, §4). Tokens never become .NET strings in the module (ADR 0009 §6, guidelines §9).
- The API's own database decides status and role (ADR 0004). Deactivation disables the user in Authentik, which deletes their refresh tokens (ADR 0009 §2, E8).
- Errors: the API's code is the `ErrorId` (guidelines §7).

## Decisions

### 1. The command

`Watch-PagerArchiveMessage` takes the same filters as `Get-PagerArchiveMessage` except `-After`, `-Before`, `-First` and `-Skip` (R63b): `-Text`, `-Capcode`, `-Alias` (also bound from `Name`), `-Agency`, `-Group`, `-Priority`, `-Category`, `-JobNumber`, `-Unaliased`, `-IncludeIgnored` and `-Flag`. Wildcards, "any of" for arrays, and pipeline input of aliases and capcodes work as for `Get-PagerArchiveMessage`: piped values are collected in `process` and the watch starts in `end` with all of them in one filter (so `Get-PaAlias -Name 'Station*' | Watch-PaMessage` watches every station).

It outputs `PagerArchive.Message` objects, built by the same function as `Get-PagerArchiveMessage`'s, so the views, `Export-Csv` and the pipeline work unchanged. Each message is written the moment its event is read, so `Watch-PaMessage | ForEach-Object { … }` reacts at once. `Receptions` holds the receive times known when the message was stored, normally the first client's: copies from other clients arrive later (R19's 10 seconds), and `Get-PagerArchiveMessage -JobNumber …` or the detail view shows them all.

The first connection's failure (not signed in, the server unreachable, a refusal, a bad filter) is a terminating error, as for every other command: nothing has been watched yet, so there's nothing to resume. Only a connection that was working is retried (§5).

### 2. What the stream gains (`GET /api/v1/stream`)

The browser's use doesn't change. The module uses four additions:

| Addition | What it does |
|---|---|
| `after_id=<message ID>` | Before going live, the stream first sends every matching message stored after that one, oldest first. The module sends it when it reconnects (§3). |
| `event: ready` | Sent once the stream is live, with `{"caught_up": n, "last_id": m}`: how many messages the catch-up sent, and the newest message ID already accounted for (the catch-up's last, or without `after_id` the newest stored message). The module's next `after_id` starts from `last_id`, so even a watch that has output nothing yet resumes in the right place. |
| `receptions=true` | Each message carries its receive times, as `GET /api/v1/messages?receptions=true` does (ADR 0009 §8). |
| `event: reconnect` | Sent just before the server ends a stream it ends on purpose, with `{"reason": …}`: `expired` (the bearer token's expiry, §4), `account` (the user's status or role changed, §4), or `behind` (the connection fell more than 64 messages behind, which the hub already handled by dropping it). The module reconnects at once, with no warning, and the catch-up fills the gap. The browser ignores the event (it listens only for `message`) and reconnects as before. |

`after_id` isn't the SSE `Last-Event-ID` header on purpose: browsers send that header on their own reconnects, and the browser reloads its list instead (it would get every message twice).

### 3. Exactly once: no gap, no repeat

Three rules together:

1. **Stored and announced in ID order.** `POST /api/v1/pages` stores a page and announces it to the hub under one in-process lock, so message IDs are assigned, committed and published in the same order, and every connection receives IDs in increasing order. Without the lock, two pages posted at the same moment could commit and be announced out of ID order; a watch that had output the higher ID and then lost its connection would never be sent the lower one. At hobby scale (about 1 page a minute, 1,500 a day; A15's 20× rate is 0.35 a second) holding the lock for one page's store (milliseconds) costs nothing measurable.
2. **Subscribe, then catch up.** The server subscribes to the hub **before** it reads the messages stored after `after_id` (in batches of 200, in ID order, through the same filter query as live messages, so the catch-up and the live stream always agree), or, without `after_id`, the newest message's ID. A page stored meanwhile is either in those reads or announced to the subscription afterwards, possibly both. Rule 1 means every ID up to the last one read is already stored, so the hub's copy of any ID up to it is skipped.
3. **The module never outputs an ID twice.** It keeps the newest ID accounted for (the last message it output, or `ready`'s `last_id`), sends it as `after_id`, and skips any message whose ID isn't higher (rule 1 makes IDs only increase). That's a cheap guard against anything the server might send twice.

The catch-up has **no time limit** (owner, 2026-10-22): after an outage of any length, every matching page stored meanwhile comes out, in arrival order, before live ones. If more than 1,000 came out, the module warns how many, after outputting them ("1,234 messages arrived while the connection was lost; they were output first, oldest first."). Retention can delete the oldest during a very long outage; those are gone for everyone.

### 4. Account changes and the bearer stream's lifetime

**A role change closes that user's live connections.** `accounts.Service.SignedOut` becomes `CloseLive` ("these users' live connections must close"), and `SetRole` calls it after committing an actual change of role, as deactivation and deletion already did. The browser's `EventSource` reconnects 3 s later (`retry: 3000`), the list reloads (as it already does after any reconnect), and from then on messages come with the new role's view: no flags for a demoted admin. The list also reloads `/api/v1/meta` on a reconnect and tells the page header, so the Admin link and the admin-only filters appear or disappear too.

**No gap between the check and the subscription.** The sign-in check runs before the stream subscribes, so a change committed in between would miss the closing. After subscribing, the stream reads the user once more: if their status or role differs from the sign-in check's, it sends `reconnect` (`account`) and ends. A change committed after that read finds the subscription and closes it.

**A bearer stream ends when its access token expires.** The middleware passes the token's expiry to the handler, and the stream sends `reconnect` (`expired`) and ends at that moment, so a stream never runs on a token the API would now refuse. The module renews the token (as every command does) and reconnects with `after_id`: no warning, no gap. This is what makes revocations the API never hears about take effect: a sign-in revoked in Authentik, or a user disabled directly in Authentik's admin interface, can't renew, and the watch stops at the latest when the current access token expires. Browser sessions are unchanged: they're the app's own, and deactivation and role changes close their streams.

**How the module stops.** After `reconnect` it reconnects, and the API's answer decides:

| What happened | API's answer | The watch | Time to stop (measured, §9) |
|---|---|---|---|
| Deactivated in the app | `401 deactivated` | stops: `ErrorId` `deactivated`, category `AuthenticationError` | 56–69 ms after the deactivation |
| Demoted to user | `403 powershell_admins_only` | stops: `ErrorId` `powershell_admins_only`, category `PermissionDenied` | 58–71 ms after the role change |
| Account deleted | `401 not_signed_in` | stops: `ErrorId` `not_signed_in` | not measured on the stack: the same path as deactivation (Pester checks the refusal stops it) |
| Sign-in revoked in Authentik (another window's `Disconnect-PagerArchive`, or an Authentik admin) | (the renewal fails first) | stops: `sign_in_ended`, "Your PagerArchive sign-in has ended. Run Connect-PagerArchive.", the same as every other command when renewal is refused | 4 min 45 s after the revocation, 0.1–0.5 s after the access token expired; at most the token's remaining life (5 min) |
| Disabled directly in Authentik, not in the app | (the renewal fails: Authentik deleted the tokens) | stops: `sign_in_ended` | 4 min 35–39 s after it was disabled, 0.1–0.5 s after the access token expired; at most 5 min |
| Access token expires | (renewal works) | carries on, no warning, nothing lost | reconnects at the expiry; no page posted across it took more than 0.6 s to come out |

None of these is retried: a refusal is an answer, not a lost connection.

### 5. Lost connections (R63b)

**Lost** means: the TCP connection fails or closes without a `reconnect` event, a read fails, the server answers 5xx or 429 (a restarting API behind a proxy answers 502), Authentik can't be reached for a renewal, or **nothing at all arrives for 60 seconds**. The server sends a keepalive comment every 25 s, so 60 s of silence means a connection that died without closing (a dropped Wi-Fi, a NAT that forgot it), which TCP alone wouldn't notice for hours.

Then:

- **One warning** per outage: "Lost the connection to PagerArchive at <origin> (<reason>). Retrying until it's back; messages that arrive meanwhile will follow." The reason is the network error's own text, which never holds page text.
- **Retries with backoff**: after 1, 2, 4, 8, 16, then every 30 seconds, for as long as it takes, each with `Write-Verbose` ("Reconnecting to PagerArchive in 4 s (attempt 3)."). A refusal (§4) ends the retries with that error.
- **An information message** when it's back ("Connected to PagerArchive again after 1 min 4 s."), then the catch-up (§3), then live messages.

A `reconnect` event isn't a lost connection: the module reconnects at once, quietly. If that reconnection fails, the outage starts there.

### 6. Talking to the stream without making the token a string

`Invoke-RestMethod` reads the whole response before returning, so it can't read a stream. `HttpClient` can, but takes the `Authorization` header only as a `string`, which ADR 0009 §6 rules out for tokens (and `HttpRequestMessage.ToString()` prints its headers, a risk for `$Error` and traces).

So the module speaks HTTP/1.1 itself, over a `TcpClient` and, for `https`, an `SslStream` with the system's normal certificate checks. The request is built as bytes, with the access token copied out of its `SecureString` (as the token endpoint's form is, ADR 0009 §6) and the buffer zeroed once sent. The reply's status line and headers are read, then the body: chunked (Go's streaming responses) or not. Errors are read as JSON for their `code` and message. That's about 400 lines of PowerShell, comments included, in one private file (`Private/EventStream.ps1`), tested against a local test server in Pester (§9) and against the real stack.

One consequence: system proxy settings (`HTTPS_PROXY`, Windows' proxy) aren't used by the watch, while `Invoke-RestMethod` uses them for every other command. See the questions.

### 7. Ctrl+C

Every wait is in slices of 200 ms (`Task.Wait(200)` on the socket's read, `Start-Sleep` between retries), so PowerShell can stop the command between slices. Ctrl+C stops the pipeline, which isn't an error: nothing is written to the error stream or `$Error`. The `clean` block (PowerShell 7.3+) closes the socket and disposes everything, whichever way the command ends (Ctrl+C, an error, or `Select-Object -First` stopping it), and the server sees the connection close at once.

### 8. Feedback (guidelines §6)

`-Verbose`: the stream request's method, path and query parameter **names** (as ADR 0009 §10), each retry, and each catch-up's count. `-Debug`: each event's type and message ID, and each `reconnect` reason. Never page text, an address or a token (P1, guidelines §9), which a Pester test checks.

### 9. Tests and evidence

- **A59 against real Authentik** (`internal/stacktest/watch_test.go`, run by `scripts/stack-checks.sh` with both vault settings). The watch connects through a TCP proxy in the test, which is stopped and started to cut the connection for real (its connections are closed and new ones refused). The checks:
  - only the matching page comes out, and the time from each post to its line in the window's output is under 2 seconds (R63b), for the page posted while connected and for every page posted across the token's expiry (pages posted while the connection is cut come out when it's back);
  - the connection cut: one warning, verbose retries, the information message, and every page posted before, during and after the outage (one every 100 ms, including while the module reconnects and catches up) comes out exactly once, in order;
  - deactivation and demotion (separate admins) each stop the watch with the API's code, with the time to stop;
  - a token expiry mid-watch: pages posted every 100 ms across the expiry all come out once, with no warning;
  - a refresh token revoked in Authentik, and a user disabled directly in Authentik: the watch stops with `sign_in_ended` by the next expiry, with the time to stop;
  - Ctrl+C: a watch in a child runspace of a window, through its own proxy, is stopped with `PowerShell.Stop()` (what the console's Ctrl+C calls): it ends `Stopped` with no error, and the proxy sees its connection close. (SIGINT to a `pwsh` reading commands from a pipe, as the test's windows do, doesn't reach the running command, so it can't stand in for a console.)
- **API level** (`internal/acceptance/m9_test.go`): a role change closes the user's streams (browser and bearer), and they reconnect with the new role's view (the module is refused); promotion too; an unchanged role closes nothing; a bearer stream sends `reconnect` (`expired`) at its token's expiry, and a browser stream isn't timed; without `after_id`, `ready` names the newest message; `after_id` catches up 450 stored messages (half matching) oldest first, while 60 more are posted from 8 clients around the subscription, and every matching page comes out once with IDs increasing, with `receptions=true`.
- **Browser** (`web/tests/e2e.spec.ts`): an admin demoted while viewing the list loses the flags, the admin-only filters and the Admin link without reloading the page, and new pages arrive live without flags.
- **Pester** (`powershell/tests/Watch.Tests.ps1`), with a local test server on a real socket: the request carries the token and the filter; chunked replies; catch-up dedupe (repeated and old IDs skipped, `after_id` sent on reconnect); backoff (1, 2, 4, 8, 16, 30, 30, 30 s over 8 refused tries, one warning, a verbose line per retry, the information message); deactivation, demotion, a deleted account and a refused renewal stop without retrying; the first connection's failure stops it; a token refused as invalid is renewed once; more than 1,000 caught up gives the count; `Select-Object -First` stopping it closes the socket; a `reconnect` event reconnects quietly; 60 s of silence counts as lost (tested with 1 s); Ctrl+C (`PowerShell.Stop()`, which is what Ctrl+C calls) writes no error and the server sees the socket close; no page text or token in `-Verbose`/`-Debug` output.
- **Mutation proof** (`scripts/mutation-checks.sh`): removing the role change's closing, the bearer stream's expiry, or the module's stop on `deactivated` each makes a test fail.

**Measured** on 2026-10-22 on cld01 (the compose stack with Authentik 2026.5.5, pwsh 7.6.7, both vault settings; times from the stack test's log):

| What | SecretStore vault | No vault |
|---|---|---|
| A matching page, post to the window's output | 128 ms | 69 ms |
| Connection cut (proxy stopped for 4 s) | out 7.2 s; 1 warning, 3 verbose retries, "Connected to PagerArchive again after 7 s."; all 82 pages posted every 100 ms around it came out once, in order | out 7.2 s; the same; all 81 pages once, in order |
| Demoted while watching | stopped 58 ms later, `powershell_admins_only`, no retry | 71 ms |
| Deactivated while watching | stopped 69 ms later, `deactivated`, no retry | 56 ms |
| Access token expiry | renewed and reconnected once, no warning; 56 pages posted every 100 ms across it all came out once, slowest 577 ms after posting | 57 pages, slowest 483 ms |
| Refresh token revoked in Authentik | stopped 4 min 45 s later, 446 ms after the access token expired, `sign_in_ended` | 4 min 45 s, 105 ms after expiry |
| Disabled in Authentik only | stopped 4 min 35 s later, 448 ms after expiry, `sign_in_ended` | 4 min 39 s, 108 ms after expiry |
| Ctrl+C (`PowerShell.Stop()`) | state `Stopped`, 0 errors, the proxy saw the connection close | the same |

At API level, a role change closed the browser's and the module's streams 10–19 ms after it, and a bearer stream sent `reconnect` 0–1 ms after its token's `exp`. In the browser (Chromium, Firefox and WebKit), a demoted admin's open list showed the user's view 3.2–3.9 s after the change, without a reload: the `EventSource` retry of 3 s, then a reload of the list and of `/api/v1/meta`.

### 10. Small items in this branch

1. **Authentik's readiness for the stack tests.** `stack_up` waited until the app's API token could see the enrolment flow, but on a cold start Authentik's flow executor could still take more than Playwright's 5 s to answer its first visitor (`stack-e2e.sh` failed once on the spinner). `stack_up` now also waits until the flow executor answers an anonymous visitor for both the sign-in and the enrolment flow, three times in a row, and `stack.spec.ts` waits for the executor's answer before looking for the form, instead of a fixed time.
2. **CI's Node 20 warning.** `actions/upload-artifact@v4` runs on Node 20 (deprecated). It moves to `@v7`, and `actions/download-artifact@v4` (also Node 20; it runs only on `main` and tags, so it hadn't warned on a branch yet) to `@v8`, the current majors. Neither change affects how they're used here (by name and pattern; v8's default digest check fails on a corrupted download instead of warning).
3. **A join racing a tree deactivation** (ADR 0008 §5). The invitee's sign-in read the invitation as pending, then waited on the tree's locks while the deactivation cancelled it; its "mark used" then changed nothing and was reported as "already used", and the Authentik user it had just enrolled stayed behind. The sign-in now reads the invitation again when marking it used changes nothing: if it was cancelled, the invitee sees "Your invitation was cancelled before you finished enrolling." and the Authentik user is deleted, as for any cancelled invitation. An API-level test holds the deactivation's locks while the sign-in runs.

## Consequences

- A watch runs for days on 5-minute tokens, reconnecting about every 5 minutes without anyone noticing, and stops within a tenth of a second of deactivation or demotion and within 5 minutes of a revocation in Authentik.
- Every bearer stream reconnects about every 5 minutes, with one short catch-up query each time.
- Pages are stored one at a time. At this project's scale that changes nothing measurable; a deployment with many pages a second would notice it first here.
- A demoted or promoted browser sees its new role's view within about 3 seconds, without reloading.
- The module has its own small HTTP/1.1 reader for the stream, which doesn't use system proxy settings.

## Questions for the owner

1. **Proxies.** The watch connects directly (§6), while every other command follows the system's proxy settings. Is that acceptable, or should the watch support an HTTP proxy (a `CONNECT` tunnel for `https`)?
2. **A revoked sign-in's error.** I used `sign_in_ended` ("Your PagerArchive sign-in has ended. Run Connect-PagerArchive."), which every command gives when renewal is refused, for "the not-signed-in error". `not_connected` ("Not signed in to PagerArchive…") is the error for having no sign-in at all. Is `sign_in_ended` what you meant?
