# ADR 0055: The app switcher follows an Authentik group

## Status

Status: Proposed (the implementer's calls, 2026-11-28), for the owner's review on the pull request. The owner's "yes", 2026-11-28, to the switcher following RadioArchive's gate. SPEC.md R77f and R92e; A140, A141, A158 and A159. It changes decisions 6 and 7 of ADR 0051: who sees the switcher is no longer a mark kept on the user. It adds a migration (00041) and changes no API path, so a release with it is a minor version. The PowerShell module doesn't change.

## Context

RadioArchive is in preview behind a gate that admits only the members of one Authentik group (`radioarchive-preview`). PagerArchive's switcher had its own list of who sees it (`users.sees_switcher`, ADR 0051), set by an admin. Two lists can disagree: someone could see a switcher to a radio that won't let them in, or be let in and never see the switcher. The owner wants one source of truth.

The group was made by hand in production, and holds people PagerArchive knows and at least one service account it doesn't. PagerArchive's sessions last 30 days (R40), so anything read only at sign-in would be up to a month old.

## Decisions

### 1. The group is the source of truth, named by a setting

`RADIOARCHIVE_GROUP` names it, `radioarchive-preview` by default, and is read only with `RADIOARCHIVE_URL` set. PagerArchive keeps no list of its own. "Show the switcher to everyone" stays a PagerArchive setting, for launch day, when the gate goes.

### 2. Read from Authentik's API every 30 seconds, never per request

Not the ID token's `groups` claim: it's read once, at sign-in, and a session lasts 30 days. Instead, one goroutine reads the group's members (`GET /api/v3/core/groups/?name=…`, the users' IDs, which are the OIDC subjects) at start and every 30 seconds, and `/api/v1/me` answers from what it read last. A request never waits on Authentik, so an Authentik that's slow or down can't slow the app's first page. A change made in Authentik shows within 30 seconds, at the person's next page load; a change made through PagerArchive updates what was read at once. One small read every 30 seconds is nothing for Authentik.

While reads fail, the last members read stay (a brief Authentik restart shouldn't hide the switcher from everyone), and the failure is logged once, as is the read that works again. Before the first good read nobody is a member.

A write can race a read that started before it: the read's answer would undo the write for up to 30 seconds. Every write bumps a counter, and a read that finishes after a write is dropped.

### 3. The admin's mark adds and removes the membership

`PUT /api/v1/admin/users/{id}/switcher` keeps its body (`{"sees_switcher": true}`) and now calls `add_user` or `remove_user` on the group. The UI keeps its tag and buttons, and says the group's name and that the radio uses it too. People in the group who aren't PagerArchive users are never shown and never touched: PagerArchive only adds or removes the user it's asked about. Someone who has never signed in has no Authentik user, so the mark is refused (409) saying so. Authentik not answering, or refusing, changes nothing and says so (503).

### 4. PagerArchive's blueprint finds or makes the group, and never sets members

The blueprint has an entry for the group by name (from `PA_RADIOARCHIVE_GROUP`, the same setting), `state: present`, with no `users` and nothing else that sets membership, so applying it again keeps a group made by hand, and its members. It's made on every install, standalone too, rather than only with `RADIOARCHIVE_URL` set: a blueprint condition would need Authentik's containers recreated whenever the setting changes, and an empty group nothing reads costs nothing. The role gets `view_group`, `add_user_to_group` and `remove_user_from_group` on that one group, as object permissions, the way R92e granted `pagerarchive-users`; nothing lets it change the group itself. If the group is missing anyway (the blueprint not applied yet, or deleted), reads say so: the Users page says Authentik has no such group, and marks are refused (409).

### 5. The old marks move once, then the column goes

Migration 00041 copies the marked users' IDs into a small table, `switcher_marks_to_move`, and drops `users.sees_switcher` (a column drop changes only the catalogue, R91a; the copy is the few marked rows). After a start with `RADIOARCHIVE_URL` set, in the background, the API adds each of them to the group (only adds: it never removes anyone) and deletes their row once Authentik has them. A row whose user has never signed in, or that Authentik refused, waits for the next start; a deleted user's row goes with the user. Standalone, nothing moves until RadioArchive is set. Going down, 00041 puts the column back with the marks still waiting; a mark already moved is in the group, not the column.

## Consequences

- The switcher and the radio's gate read one list. Hiding the switcher for someone in PagerArchive also keeps them out of the radio while the gate stands, and the page says so.
- The API's Authentik role can now remove members, but of that one group only.
- A change made in Authentik takes up to 30 seconds to show, and then only on the next page load (the frame reads `/api/v1/me` when it loads).
- Standalone installs get one empty group in Authentik that nothing reads.
