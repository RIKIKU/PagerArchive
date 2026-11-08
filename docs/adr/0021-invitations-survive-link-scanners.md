# ADR 0021: Invitations that survive mail link scanners, and resending (M20)

## Status

Status: Proposed (implementer, 2026-11-08), for the owner to accept. The owner's brief for M20: SPEC.md R43a and R43d as amended on 2026-11-08, A81 and A24, on the branch `m20` from main at 4803f89. To be released as 0.17.0. It changes ADR 0004 §4 ("single-use and 7 days") and §9's limit on restarting an enrolment. The PowerShell module doesn't manage invitations, so it doesn't change.

## Context

The first production install invited two people. For both, about an hour after the email, a host in a data centre (a mail provider's link scanner) opened the link and ran the enrolment page. Authentik's invitation stage deletes a single-use invitation as soon as it runs (`invitation_used`, then `model_deleted`), so when the invitee clicked, Authentik said the link wasn't found, while the app still showed the invitation as Waiting.

R43a now says an invitation is used up only when enrolment finishes (the code verified and the account made), never by opening the link, as often as anyone likes and from anywhere. It must still enrol only its own address, need the emailed code (R41), expire after 7 days, and make one account: of two browsers finishing at once, one makes the account and the other is refused. The app and Authentik must agree on each invitation's state.

What Authentik 2026.5.5 does, from its source:

- The invitation stage looks the invitation up with `Invitation.objects.filter(pk=token).first()`, merges its fixed data (email, username, the app's invitation ID) into the flow, and, if it's single-use, calls `invite.delete()` without checking that it deleted anything. Expired invitations are hidden by the model's default manager.
- The user-write stage with `always_create` makes a new user every run. A second run of the same invitation fails on the username, which is unique.
- The email-code stage keeps its device in the flow until the code is right, but a wrong code saves it (django-otp's throttle), and the stage refuses a user who already has a device. That saved device is also where ADR 0004's lock counts wrong enrolment codes (R44a).

So a fix has to let the flow run any number of times, from the first stage to the code page, and still use the invitation up exactly once.

## Decision

**Invitations aren't single-use in Authentik. The enrolment flow uses one up after the code is verified, by deleting it, and that delete is the one step two finishing browsers race for.**

### 1. Opening the link uses nothing up

The app creates invitations with `single_use: false`. Opening the link, as often as anyone likes and from any address, only reads the invitation. Authentik still refuses it once it's expired, deleted by a resend (§4) or cancelled (ADR 0004), and its fixed data still sets the address, so it enrols only that address (R43a).

### 2. An unfinished enrolment can be carried on

A run that reached the code page has made an inactive user. A new policy, `pagerarchive-enrolment-resume`, bound to the user-write stage, finds that user and the next run carries on with it instead of failing on its username. The user-write stage changes from `always_create` to `create_when_required`. The policy only takes a user that is:

- inactive;
- under `users/pagerarchive`, with the invitation's username;
- marked with the same app invitation ID;
- without a confirmed email code.

So it never takes over a member's account, a deactivated member (an admin's re-invitation deletes that user first, ADR 0004 §4), or another invitation's user.

The email stage allows one device per user. If the earlier run saved a device with a wrong code, the policy deletes it, but first carries its wrong codes onto the user (`pagerarchive_code_failures`: count and time). `pagerarchive-not-locked` adds them to the new run's, so restarting the flow doesn't reset R44a's count of 5 in 15 minutes. Activation clears them.

### 3. Used up once, after the code

- **On every visit to the code page**, `pagerarchive-enrolment-invitation-live` checks that the invitation still exists. The binding's mode becomes "all", beside `pagerarchive-not-locked`. If it doesn't exist (another browser finished, or it was resent, cancelled or has expired), a deny stage says the link no longer works, and to use the newest email or ask the inviter.
- **After a correct code**, `pagerarchive-enrolment-activate` deletes the invitation with `Invitation.objects.filter(pk=…).delete()` and goes on only if that deleted one row. PostgreSQL lets only one of two concurrent deletes of a row delete it, so one browser activates the user and the other gets the same deny stage. Then, as before, the user is signed in, sent to the app, and the app makes the account at first sign-in (ADR 0004 §4), checking its own record: cancelled, expired or used is still refused.
- **When the app makes the account**, it also deletes the invitation's current Authentik link, in case a resend replaced the one that was used.

Two browsers that both reach the code page share the one unfinished user (§2), so whoever finishes keeps their own name and password: activation writes that browser's copy of the user.

### 4. Resending (R43d)

`POST /api/v1/invitations/{id}/resend` is for the inviter, or any admin. It's offered on the inviter's invite page, on the user's page in Administration (a new "Invitations they sent" list), and in the Users screen's unused invitations. A resend:

- works on a waiting or expired invitation, or one whose link was lost (§5);
- refuses a used or cancelled one ("invite again instead");
- refuses the first admin's set-up link, which is restarted from the sign-in page;
- follows a new invitation's rules: an allowed domain, not a member already, and R43's re-invite block for non-admins. R43b and R43c's cancelling applies, because it's the same row.

Inside one transaction, with the invitation's row locked, it:

1. creates a new Authentik invitation with the same app ID;
2. sets the row's new link and expiry (now + 7 days) and `resent_at`;
3. emails the new link;
4. deletes the old link.

If any step fails, it removes the new link and the row is unchanged. It's the same invitation and the same inviter, so the list still shows one invitation to that address. An unfinished enrolment from the old link carries on with the new one (§2), since the app ID is the same.

### 5. The app and Authentik agree

On every start (beside the API, retried every 5 s for 10 minutes while Authentik starts, like R44c's check) and then hourly, the API checks each waiting, unexpired invitation's link in Authentik (`CheckInvitations`):

- **Single-use (made before this release):** it's made reusable.
- **Missing, and nobody used or cancelled it:** it's marked `link_lost_at`. The lists show it as **"Link no longer works"** with Resend and Cancel, never as Waiting. It still counts as the address's one pending invitation, so a new invitation to that address names it.
- **Missing because enrolment finished in Authentik** (an active user holds the username, and the account is made at first sign-in): it's left alone.

The log line names the invitation's ID only, never the address (R73).

### Rejected

- **Reading the invitation early and running the invitation stage last.** The fixed data must reach the stages before it, so its lookup would be copied into a policy. And the stage's own delete isn't conditional: two browsers that both find the invitation both go on. Using it last needs the conditional delete anyway, so the stage gains nothing at the end.
- **Authentik invitations not single-use, used up only by the app at first sign-in.** Between Authentik activating the user and the app's sign-in, the link still works. A second browser finishing would reach Authentik's username clash or sign in as the same user, rather than be refused cleanly. Deleting in the flow keeps "used" and "can't enrol again" one step.
- **Binding the invitation to the first browser or address that opens it.** The scanner opens it first.
- **A "click to continue" page before enrolment.** Scanners run scripts and follow buttons, and it would add a step for every invitee.

## Upgrading from 0.16 (A77)

`docker compose pull && docker compose up -d` is enough. The release doesn't need "Upgrade steps".

- `compose.yaml`'s config label for Authentik changes, so `up -d` recreates its containers and the new blueprint applies.
- Migration 00016 adds `resent_at` and `link_lost_at` to `invitations`.
- **Waiting invitations whose links still work** are made reusable by the API's check, within seconds of Authentik answering after the upgrade. If one is opened before that, Authentik uses it up at the start, as 0.16 did. The new flow accepts that (`single_use`, already used up), so its enrolment still finishes. Nothing else changes for it: same link, same expiry.
- **Waiting invitations that Authentik already deleted** (production's two) show as "Link no longer works" after the first check. The inviter, or an admin, resends them. The app doesn't email anyone by itself on an upgrade.
- **Expired** invitations stay expired, and can now be resent.
- **An inactive user from a half-finished 0.16 enrolment** carries the same app invitation ID, so a resend or the link carries on with it.

## Consequences

- An invitation link works until it's used, cancelled, resent or 7 days old, however often it's opened. The invite page and the email no longer say it "works once".
- Anyone holding the link can run the flow as far as the code page. Each run that gets there emails a code to the invitee, and leaves an inactive Authentik user, as abandoning an enrolment always did (ADR 0004 §4 removes it when the address is next invited). Only someone who can read the invitee's mail can finish. A real scanner doesn't fill in a password, so it stops on the first page and sends no code.
- R44a's lock still holds across restarted enrolments, through the carried count.
- The enrolment flow has three new policies and one new deny stage. An Authentik upgrade must still re-run `scripts/stack-checks.sh` and `scripts/stack-e2e.sh` (ADR 0004).
- Migration 00016: fix-search (0.16.2) merged first with 00015, so this branch took the next number.

## How it's checked

- **A81 in a real browser** on the compose stack (`web/tests/stack.spec.ts`, run by `scripts/stack-e2e.sh` in CI's `stack-e2e` job). A headless browser with no cookies opens A's invitation to g, fills in the name and password, and reaches the code page, twice, from two addresses (`X-Forwarded-For` 192.0.2.10 and 198.51.100.20, which Authentik takes from the Docker bridge as a trusted proxy). The invitation is still Waiting in A's list, and G enrols with the same link and the emailed code, with A as inviter. Then:
  - A resends an expired invitation to h: a new link for 7 days, the old one refused, one waiting invitation, and H enrols with A as inviter;
  - the admin resends B's invitation from B's page: B stays its inviter, and B's old link is refused;
  - resending a used invitation is refused;
  - two browsers finishing one invitation at once make one account.
- **The same test on main's code**, committed alone first (2740cea, CI run 42450944874), failed as production did: the first scan used the invitation up, so the second found no enrolment page.
- **API-level tests** with the fake Authentik (`internal/acceptance/m20_test.go`): resending, its refusals, and the start-up check's three cases. Also `internal/authentik`'s `TestRepairInvitation`.
- A24, A25, A27 and A48–A50 run unchanged, at the API and on the stack.
