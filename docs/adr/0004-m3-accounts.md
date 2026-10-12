# 4. Milestone M3: accounts, invitations and deactivation

Date: 2026-10-13

## Status

Proposed

## Context

Milestone M3 adds sign-in through Authentik (R40), invite-only enrolment with an emailed code (R41, R43a), the invite tree and deactivation (R42, R43, R43b), deleting your own account (R43c), and roles with a first admin (R44). Its checks are A23–A29 and section 11 step 2. The owner reviewed the checks on 2026-10-13 and added the cancellation of unused invitations to R43b and R43c, and the refusal message to R43 (spec commit 2b3fdf8).

Before the build, the implementer raised the gaps below. The owner decided those that change what users see, what data is kept, or security. The rest are the implementer's choices (SPEC.md "How to use", item 7).

## Owner decisions (2026-10-13)

1. **Signing in after enrolment: email and password.** The password is chosen at enrolment. The emailed code is used only at enrolment and to reset a password. It is never a second factor at sign-in.
2. **An admin re-inviting a deactivated user reactivates the same account** when it's used. The admin becomes its inviter. The people that user invited stay deactivated.
3. **Deleting your own account leaves only an anonymous placeholder** (no email, name or Authentik user), so the invite tree stays connected: deactivating the tree above a deleted user still reaches the people they invited. Their address is free to be invited again like any new one. The invitation that enrolled them forgets their address too.
4. **Addresses are compared ignoring case and any `+tag`**: `B+x@Example.com` is `b@example.com`, both for the re-invite block (R43) and for "one account per address". Emails are still sent to the address as entered.
5. **Sessions last 30 days and are renewed by use.** Deactivation, signing out and deleting the account end them at once.
6. **M3's admin screens are users, invitations and roles only.** Aliases, templates and clients stay on the CLI in v1 (R35e), and retention's screen comes with M4. This supersedes ADR 0003's promise of template screens in M3.
7. **Unused invitations are cancelled when their inviter is deactivated or deletes their account**, and a non-admin re-inviting a deactivated user is told why (R43, R43b, R43c as amended).

## Implementer decisions

### 1. The app is the source of truth; Authentik holds credentials

- Roles, status (`active`, `deactivated`, `deleted`), who invited whom and invitations live in the app's database (`users`, `invitations`, migration 00006). ADR 0003 expected "Authentik roles". Keeping roles in the app lets the last-admin rule (R44) be checked in the same transaction as the change, with the active admins locked (`SELECT … FOR UPDATE`).
- An account is linked to its Authentik user by the OIDC subject. The provider uses `sub_mode: user_id`, so the subject is also Authentik's user ID for API calls.
- Only users enrolled through an app invitation can use the app. A sign-in with no matching invitation (for example Authentik's own `akadmin`) is refused.
- Deactivation is a database change first: status set, sessions deleted, unused invitations cancelled, and live connections closed, all at once. The user is then disabled in Authentik, which ends their Authentik sessions too. If Authentik can't be reached, the user is still locked out of the app, and the admin is told to repeat the request, which syncs Authentik again.

### 2. Server-side sessions

- After sign-in the browser holds only a random 256-bit token in an `HttpOnly`, `SameSite=Lax` cookie (`Secure` when `APP_URL` is https). The database holds its SHA-256. No OIDC token reaches the browser.
- Every request to `/api/v1/` (except client page posting, which keeps its API keys) looks up the session and its user's status. That one indexed query is how deactivation takes effect immediately (A25). Sessions are renewed at most once an hour, and expired ones are deleted hourly.
- **CSRF:** Go's `http.CrossOriginProtection` refuses state-changing browser requests from other sites (`Sec-Fetch-Site`/`Origin`), on top of `SameSite=Lax`. Clients posting pages send neither header and are unaffected.
- Sign-in is the OIDC authorization-code flow with PKCE and a nonce. The state, nonce and verifier are kept in a short-lived cookie scoped to `/auth/`. The OIDC provider is discovered on first use, so the API starts, and serves clients, while Authentik is still starting.

### 3. Authentik configuration (`deploy/authentik/blueprints/pagerarchive.yaml`)

The blueprint is applied automatically. It sets up:

- **Enrolment flow.** Invitation (required) → name and password → user created *inactive* → one-time code emailed to the invited address (Authentik's Email Authenticator stage) → user activated → signed in → back to the app. The invitation's fixed data sets the email, the username (the normalised key) and the app's invitation ID, and the invitee is never asked for an address. So the link only enrols the invited address (R43a), and a wrong code leaves no usable account (A23). Authentik's ordinary email stage sends a link, not a code, so it isn't used.
- **Sign-in flow:** email, then password, on separate pages so each password attempt can be checked for guessing (section 9). **Password reset:** the emailed code, then a new password.
- **User settings: name only.** Authentik's default settings page lets users change their email address, which would get around the allowed domains (R41).
- **The OIDC provider and application**, with a `pagerarchive` scope that carries the invitation ID.
- **The app's API account**, a service account with a least-privilege role: invitations, plus viewing, changing and deleting users. It isn't a superuser.
- **Email templates** in `deploy/authentik/templates`, so the invitation and code emails say PagerArchive rather than naming the sign-in host.

Other Authentik settings:

- Authentik sends both emails with its own SMTP settings, so only Authentik needs mail configuration. The app asks Authentik's API to send the invitation.
- Privacy: avatars are initials only (no Gravatar), and analytics, error reporting and update checks are off.
- Authentik is pinned to 2026.5.5, the latest stable release on 2026-10-13. 2026.8 is still a release candidate.

### 4. Invitations

- **Single-use and 7 days.** Authentik deletes a single-use invitation when it's used and refuses an expired one when enrolment starts. The app also checks its own expiry at first sign-in, so an enrolment begun just before expiry and finished after it is refused, and its Authentik user removed (owner review, 2026-10-13). The app keeps its own record (`pending`, `used` or `cancelled`, with expiry shown as `expired`).
- **One open invitation per address**, enforced by the database: a unique index on the address of pending invitations (migration 00007), so no writer can make a second. A second invitation is refused and names the first one's inviter. Expired invitations are cancelled before a new one is made, so they don't block it.
- **Admins' invitations also need an allowed domain.** The only exception is the first admin's (R44).
- **A cancelled invitation is deleted in Authentik**, so its link stops working. If someone was midway through enrolling when it was cancelled, the app refuses their first sign-in and removes the Authentik user it created.
- **Leftover Authentik users are cleaned up.** An inactive Authentik user is left behind if someone abandons enrolment before entering the code. It's removed when that address is next invited, if no account owns it.
- **An admin's re-invitation of a deactivated user** removes that user's old Authentik user when it's created (Authentik usernames are unique). The new enrolment is relinked to the same account. Until it's used, "Reactivate" is refused for that user, with an explanation.

### 5. The first admin (R44)

While no active admin exists and `INITIAL_ADMIN_EMAIL` is set, the sign-in page offers "Set up the first admin". The button sends a same-site POST to `/auth/first-admin` (never a GET, because it changes state; owner review, 2026-10-13). It's sent with `fetch`, and the reply names the enrolment link: a plain form post couldn't follow a redirect to Authentik's host, because the page's Content Security Policy allows forms to submit only to the app (`form-action 'self'`). The request creates a fresh one-hour, domain-exempt invitation for that address and sends the browser straight to enrolment. Only the address's owner can finish, because the code goes to that address. Once any admin exists, the link and the setting do nothing (A23).

### 6. Who may do what

| Action | Who |
|---|---|
| Invite, see and cancel own invitations | Everyone |
| Delete own account | Everyone except the last active admin |
| See all users and invitations, cancel any invitation | Admins |
| Deactivate a user or invite tree, reactivate, promote or demote | Admins, never on themselves |

The last active admin is protected in every path, including tree deactivation, which is refused as a whole (A28).

### 7. Live connections

Each live-update subscription records its user. Deactivation, sign-out and account deletion close that user's connections. When the browser app sees its stream refused, it checks the session and goes to the sign-in page if it has ended.

### 8. Deployment and tests

- **`compose.yaml`** adds `authentik-server` and `authentik-worker` (no Redis, since 2025.10). An init script creates Authentik's database in the shared PostgreSQL container (R40, ADR 0001). The API reaches Authentik over the compose network (`AUTHENTIK_INTERNAL_URL`) while keeping the public host, so issuer and link URLs match what browsers see. Publishing images to GHCR stays with M4 (ADR 0002 allows M2–M4).
- **Tests run at three levels:**
  - API-level Go tests for A24–A29, invitation rules, CSRF and the first-admin set-up, with a fake Authentik (`internal/acceptance`).
  - `scripts/stack-checks.sh`: A23–A28 and A33 against the real compose stack, with Authentik and a Mailpit mail catcher, through Authentik's flow executor API, across an API restart for A23. A33 shifts the recorded failure times in Authentik's database to check the 15 minutes without waiting.
  - `scripts/stack-e2e.sh`: section 11 step 2 in a real browser, on Authentik's own pages.

  The M2 browser tests seed a user and an admin session directly into their throwaway database.
- **A test-only mail catcher.** `deploy/test/compose.yaml` adds Mailpit, with made-up settings in `deploy/test/stack.vars`. No real mail server is used in tests.

### 9. Guessing protection (R44a, A33; owner review 2026-10-13)

**What happened before.** A33 was first run against the stack as built in the first round. Five wrong reset codes, five wrong passwords, or five wrong enrolment codes, each followed at once by the right one, ended in the right one being accepted every time, including 14 minutes later. Authentik's only built-in limits were:

- the email device's own throttle, which doubles a short delay after each wrong code (1, 2, 4, 8 … seconds), so guessing slows but never stops;
- the password stage's "cancel after 5 failures", which only ends that one flow.

**Why not Authentik's reputation policy.** Its score is kept per (address, IP), so it does track an account. But two things don't fit R44a:

- Its expiry is set when the first failure is recorded and isn't extended. A lock would end 15 minutes after the *first* failure, not the fifth.
- A wrong enrolment code doesn't lower it at all, because that stage doesn't report failed sign-ins.

**What was built** (in the blueprint):

- **Counting failures.** An expression policy, `pagerarchive-not-locked`, counts an account's failures:
  - Authentik's `login_failed` events cover wrong passwords and wrong reset codes. They're per account, whichever flow or IP they came from.
  - The failure count on the account's email device covers wrong enrolment codes.
- **When the lock applies.** Five failures within 15 minutes lock the account until 15 minutes after the fifth. A successful sign-in starts the count again.
- **Checking every attempt.** The policy is bound to each stage that checks a secret: the password stage, the reset-code stage and the enrolment-code stage. These bindings are re-evaluated by Authentik on every request, so every attempt is checked: in a restarted flow, within one flow, and when a new code is requested.
- **How a locked attempt is refused.** When the policy fails, Authentik drops the stage. So each of those stages is followed by a deny stage ("Too many wrong attempts for this account. Wait 15 minutes, then try again."). Its policy reads a mark the first policy sets in the same request, so dropping a check stage always means denial. It can never skip ahead without the secret.
- **The password gets its own page.** It used to be on the email page. A password typed with the email can't be tied to an account until after the stage has run, so it had to move to a separate stage.

**What A33 checks:**
- the right answer after five wrong ones, for reset codes and passwords in new flows each time, and for the enrolment code within one enrolment;
- that the right answer is still refused 14 minutes later and accepted at 16;
- a reset code within one flow;
- that another account is unaffected.

**Limits:**
- The lock is per account, so anyone who knows an address can lock it for 15 minutes at a time. R44a accepts this.
- An enrolment can't be restarted with the same invitation, which is single-use. If the inviter sends a fresh invitation, the half-finished enrolment, and its count, is replaced.
- Authentik's exponential code throttle stays on underneath, as an extra limit.

### 10. Authentik's administrator (owner review, 2026-10-13)

Authentik's own superusers (`akadmin`) sign in through the same public sign-in flow, and Authentik's API is shared with the pages users need. Hiding `/if/admin/` at the proxy therefore doesn't protect the account on its own.

- **A second factor for superusers.** The sign-in flow has an extra stage that applies only to them: a TOTP app, passkey or recovery codes, with a TOTP app set up on first sign-in. PagerArchive users never see it.
- **Deployment guidance.** `docs/deploy.md` keeps `/if/admin/` LAN-only in the proxy example. It also says to set up the first admin while both hostnames are still LAN-only, then open the app.

## Consequences

- Deactivation is immediate and doesn't depend on Authentik being up. Authentik is synced right after.
- Authentik's database load is now part of the stack. It counts against the `db` budget measured by A15 (M4), as ADR 0001 planned.
- A deleted user's placeholder row stays forever. It has no personal data, and it keeps the invite tree connected.
- Users change their name and password on Authentik's pages, linked from the app's account page.
- Signing in takes two pages (email, then password), and the lock can refuse a person who made five typos. The message says to wait 15 minutes.
- Authentik's email stage templates and flows are now part of the app. An Authentik upgrade must re-run `scripts/stack-checks.sh`.
