# ADR 0048: Service accounts

## Status

Accepted (2026-11-19). The owner's decisions of 2026-11-19 ("yes, mark it ready to build"; on personal details, "agreed, no personal details"), built as decided. SPEC.md R59, R60, R62, R62a, the glossary and A130. The pull request is labelled `feature`.

## Context

Every PagerArchive account is a person who enrolled with an invitation to an address in `ALLOWED_EMAIL_DOMAINS` (R41, R43a). A tool, such as the owner's coding agent, has no such address, so it can't have an account. The owner wants one to query the archive over HTTPS (when ssh is unavailable) and to use the PowerShell module regularly, so it can report the module's usability problems. A service account won't fit many jobs, and it skips people's sign-in flows: both accepted.

R60 says there are no API keys. The module signs people in through Authentik with short-lived tokens; the API checks each token and then the person's status and role on every request.

## Decision

### 1. An Authentik service account, through client credentials

Each service account is an Authentik **service-account user** (Authentik's own `POST /api/v3/core/users/service_account/`: no usable password, type `service_account`) with one **app password**, which is its credential. The tool trades the app password for a 5-minute access token at Authentik's token endpoint with OAuth's **client-credentials grant**, Authentik's form of it with `username` and `password`, against a new client, **`pagerarchive-service`**. The API checks those tokens exactly as it checks the module's (issuer, audience, signature, expiry; an access token, by `azp` and `scope`), then the account on every request.

The client, in `deploy/authentik/blueprints/pagerarchive.yaml`:

- **public**, `grant_types: [client_credentials]` only, `sub_mode: user_id`, 5-minute access tokens, scopes `openid` and `profile`. No secret is given to anyone, so R60's "no client secret" holds for every client a tool uses.
- Its stored secret is left as Authentik generates it (random, given to no one). With a username, Authentik's client-credentials grant ignores the client secret; without one, a request bearing the client's own secret gets a token for an automatic user (`ak-<provider>-client_credentials`). An empty secret, as the module's client has, would hand that to anyone.
- An expression policy, `pagerarchive-service-only`, bound to its application with `policy_engine_mode: all`, passes only the client-credentials grant, for a user of type `service_account` with the attribute `pagerarchive_service_account: true`, which the app sets when it makes one. People, Authentik's automatic user and service accounts made by hand in Authentik are refused.

Probed on Authentik 2026.8 (the compose stack's), with tokens never printed: the client-credentials grant on a public client with a username and app password gives a token with `iss` the client's issuer, `aud` and `azp` `pagerarchive-service`, `sub` the user's ID and `scope` `profile openid`, valid 300 s; before the attribute is set it's refused (`invalid_grant`); a wrong password, the request with no username, the module's client, an inactive user and a deleted user are all refused (`invalid_grant`), and no automatic user is made.

The app's Authentik API account gains `authentik_core.add_user` and `authentik_core.add_token`, which the service-account call needs. For a non-superuser, Authentik's token API only ever makes tokens for the caller itself, and making a user can't put it in a superuser group without a further permission it doesn't have; changing and deleting users it already had.

Options not taken: **PagerArchive-issued keys** (against R60; the brief ruled them out without the owner); a **confidential client** whose secret the tool holds (a second secret beside the app password, for nothing: the grant is decided by the user's app password either way); **JWT client assertions** (the tool would hold a private key and Authentik a federation source for it: more moving parts for the same trust).

### 2. In PagerArchive: a user of kind `service`

A service account is a row in `users` with `kind = 'service'` (migration 00034): its name, no email, role `user`, `invited_by` its owning admin, and `credential_expires_at`. So it sits in its owner's invite tree, and everything that walks the tree (R42-R42d) takes it: a tree's deactivation deactivates it and disables it in Authentik; the preview shows it, marked, ticked as anyone else. Its role is never `admin`, so it never counts as one (R44's last admin, the first-admin set-up). The checks on `users` that every live account has an email apply to people only; a service account has a name and an owner, never an email.

- **Made on the server only** (R59's reasoning): `pagerarchive service-account create <name> --owner <ID or email> | list | revoke <name> | rotate <name>`, which needs the database and Authentik's API, as the API container has. The name is the Authentik username: 3 to 40 lower-case letters, digits and hyphens, so it can't be an address (people's usernames) or Authentik's own `ak-…`.
- **Expiry**: 90 days, as the app password's own expiry and as `credential_expires_at`. Authentik doesn't check an app password's expiry when it's traded for a token (its client-credentials code looks the token up by key), so the API checks `credential_expires_at` on every request.
- **Rotation** deletes the account's Authentik user and makes a new one, with a new app password and a new 90 days, and records its new subject. Tokens from the old credential name a subject PagerArchive no longer knows, so they're refused at once, though they haven't expired.
- **Revocation** marks the row deleted (keeping its name, so `list` says which was revoked) and deletes its Authentik user: refused at once, and no more tokens.
- An owner **deleting their account** (R43c) revokes their service accounts with it.
- **Administration** lists it, marked "Service account", and can deactivate it (alone or with a tree); reactivating, promoting and marking it trusted are refused (`service_account_shell_only`). A browser sign-in for its subject is refused (`service_account`); it has no email, so Authentik's email-only sign-in can't reach it anyway.
- The `revoke` and `rotate` commands run in their own process, so they can't close a live stream the API holds (R63b): such a stream ends when its token expires, within 5 minutes, and every other request is refused at once.

### 3. Read-only, and no personal details

A service account passes `adminOnly` and sees what admins see in reads (`isAdmin`), behind one gate on every `/api/v1` route (`serviceGate`):

- anything but `GET`/`HEAD` is refused, 403, `service_account_read_only`, before its handler runs;
- **the personal-details rule fails closed**: every signed-in read is classified in `serviceReads`, as `serviceAllowed` or `servicePersonal`. A read that isn't classified is refused as personal, and `TestServiceAccountReadsClassified` (with A130) fails while any read in `apiRoutes()` is unclassified, so a new read can't land without a decision. Personal, refused 403 `service_account_personal_details`: the user list, a user's page (emails, invite tree, deactivation records), a user's invitations, a tree's preview, all invitations, the account's own invitations, and alias requests (who asked, their notes and feedback);
- in the allowed reads, people are hidden, as "(hidden from service accounts)": its owner on `/api/v1/me` (`invited_by`); who made or asked for each change in an alias's history (the capcode and alias views); and, on an admin's alias page, who asked for and reviewed its waiting requests, with their notes and feedback.

The allowed reads were swept for people's names and addresses besides the account's own. Messages' flags are booleans; receive times name receiving clients (machines); albums and integrations are each person's own, so a service account sees only its own, of which it has none; reports count pages, jobs, appliances and places; backups, the re-parse, retroactive redaction, retention, large-event settings and automatic upgrades record no one who started or changed them (that's in the log, by user ID); templates, aliases, the export and shared names name aliases and templates only. The three found are hidden as above.

A unit test runs every route through the gate, so a new write route is covered without a list to keep.

### 4. Logged and traced

Each request is logged at INFO with its route pattern, the account's user ID and name ("service account request", or "service account refused" with the reason), and its first use of each token as "signed in as a service account". Its spans carry `user.id`, `user.role` = `service_account` and `pagerarchive.service_account` (its name): a tool's name, not a person's, and nothing else (R70). The API has no general request limit; a service account is under the same per-user limits as a person, and the ones it could meet (invitation emails, Send test) are on writes, which it can't make. The credential is never logged by the command, the API or the module.

### 5. The PowerShell module

`Connect-PagerArchive -Uri <URL> -ServiceAccount <PSCredential>`: its name as the user name, the credential as the password (`Get-Credential -UserName report-bot`). A PSCredential fits R61: the password is a SecureString from the prompt on, and it's saved exactly where a person's refresh token is (the DPAPI file on Windows, the vault elsewhere, or this session only), with the account's name in the file. `GET /auth/powershell` now also names the service client (`service_issuer`, `service_client_id`, `service_scope`). There's no refresh token: whenever the access token has under 60 seconds left, the module trades the saved credential again. If Authentik refuses it (rotated, revoked, expired or deactivated), the saved copy is deleted and the error, `service_account_refused`, says to ask the server's operator. A write command gets the API's 403 and its message. `Disconnect-PagerArchive` revokes the access token and deletes the saved credential; the credential itself ends only on the server. The module's version goes to 0.20.0.

## Consequences

- R60's "no API keys" holds: the API only ever accepts Authentik's short-lived tokens. The app password is a long-lived secret the tool holds, bounded by 90 days, held by Authentik, and ended at once by rotation, revocation or deactivation.
- One more client in the blueprint, and two more permissions for the app's Authentik account: an upgrade applies both with the release's blueprint.
- Deploy steps for an operator: upgrade (the blueprint and migration come with the release), then on the server `docker compose exec api pagerarchive service-account create <name> --owner <your email>`, and give the printed credential to the tool once; `rotate` before 90 days, `list` to see expiry, `revoke` to end it (docs/deploy.md, docs/powershell.md).
- Tests: A130 at API level and against the real stack through the module; the gate's unit test over every route; Pester tests for the module; mutation checks for the read-only rule, personal details (a route reclassified, an unclassified read let through), expiry, the clients' kinds, the hidden owner, history and requests, and browser sign-in.
