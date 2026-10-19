# 8. Milestone M7: invite tree, deactivation preview, trusted users and records

Date: 2026-10-17

## Status

Accepted (owner, 2026-10-20). The owner confirmed both choices under "Questions for the owner": the last admin is spared rather than the whole tree refused, and a role change counts as the tree changing while a new invitation doesn't.

## Context

Milestone M7 (owner, 2026-10-17) changes how an admin deactivates an invite tree (R42). Each user's page in Administration shows their invite tree (R42a). Deactivating a tree first shows a preview with a tick beside each person, which the admin can change, and is refused if the tree changed in the meantime (R42b). Admins can mark users **trusted**, and trusted users start unticked (R42c). Each tree deactivation is recorded (R42d). The checks are A48–A50. A25 and A28 must still pass.

Two small items ride along on the same branch:

- If the database container restarts, Authentik must be restarted afterwards (`docs/deploy.md`), and `compose.yaml` should prevent the problem where that's safe (§11).
- The admin Users page links to the invite form on the Account page (R41, R44b), and A41's browser check covers the link.

## Questions for the owner

None block the build. Two choices change slightly what admins see, and the owner may want to change them:

- **The last admin in a tree** (§4). Before M7, a tree deactivation that included the last active admin was refused as a whole (ADR 0004 §6). R42b now says the last active admin "can't be ticked", so the preview shows them unticked and locked, and the rest of the tree can still be deactivated. A request that ticks them anyway is refused, and nothing changes (A28).
- **What counts as "the tree changed"** (§5). R42b lists who joined, status and the trusted mark. A change of **role** also refuses the confirmation, because the preview shows roles. New **invitations** made by ticked people during the preview don't refuse it. They're cancelled with the rest (R43b), and the result gives the real number.

## Already decided elsewhere (cited, not re-decided)

- The invite tree is `users.invited_by`. It goes through deactivated people and through deleted accounts, which stay as anonymous placeholders so the tree stays connected (ADR 0004, owner decision 3).
- An admin's re-invitation of a deactivated user relinks the same account, and the admin becomes its inviter (ADR 0004, owner decision 2). That person then leaves the old tree.
- Admins never deactivate themselves (ADR 0004 §6). The last active admin is never deactivated (R44), and the active admins are locked (`SELECT … FOR UPDATE`) in the same transaction as any change that could remove one.
- What deactivating one person does (R43b, R51, R56): status set, sessions deleted, live connections closed, unused invitations cancelled, integrations stopped and waiting alias requests withdrawn, all in one transaction. Authentik is disabled straight afterwards, and a failure there is reported so the admin can repeat it.
- Deactivating **one** user (not a tree) is unchanged and has no preview and no record.

## Implementer decisions

### 1. How the tree is stored and queried

- **No new storage.** The tree stays an adjacency list, `users.invited_by`, which is indexed (migration 00006). A deployment has tens or hundreds of users, so each request runs one recursive query from the root and builds the nested tree in Go. Materialised paths or closure tables would have to be kept right through re-invitations, for no measurable gain.
- **Descendants:** `WITH RECURSIVE` from the root, giving each person's parent and depth, ordered by date joined. Each user has exactly one inviter, so the only possible cycle runs through the root. That happens when someone re-invites, as an admin, the person who once invited them. The recursion skips the root, so it always ends.
- **"Who invited them"** (R42a) is one level up: the root's inviter, shown above the tree and linked to their own page.

### 2. Data model (migration 00011)

- `users.trusted_at`, `users.trusted_by`: the trusted mark (R42c), with who set it and when. Both are set together, or neither. Removing the mark clears both, so only the current mark's setter is kept. Deleting the account clears the mark too (R43c leaves only an anonymous placeholder).
- `tree_deactivations`: one row per confirmed tree deactivation (R42d). It holds the root, the acting admin, the optional reason (at most 500 characters), the number of unused invitations cancelled, and the time.
- `tree_deactivation_people`: one row per person in that tree who was **deactivated** or **spared**. Spared means active and left unticked, whether the admin unticked them or they couldn't be ticked (themselves, the last admin). People already deactivated and deleted placeholders weren't affected, so they aren't listed.
- People are referenced by user ID and named as they are *now*, as "name (email)", or "deleted user" once they delete their account. This is the same rule as alias history (ADR 0007 §5). Users are never removed from the table, so the references always resolve.
- Records are kept with no time limit, like accounts and alias history. They hold no page text. The reason is shown only to admins, and is never logged.

### 3. The preview (R42b)

`GET /api/v1/admin/users/{id}/deactivation-preview` returns the tree with, for each person: name, email, role, date joined, status, the trusted mark, their unused invitations (pending and not expired), whether they **can be ticked** (and if not, why), and whether they **start ticked**. It also returns a **version** (§5).

| Person | Can be ticked | Starts ticked |
|---|---|---|
| Active user or admin | yes | yes |
| Trusted (R42c) | yes | **no** |
| The admin doing it ("you") | no | no |
| The last active admin (R44) | no | no |
| Already deactivated (including one waiting on an admin's re-invitation) | no: greyed out, the tree continues through them | no |
| Deleted account | no: shown as "deleted user", the tree continues through them | no |

- The root can be unticked like anyone else ("untick anyone").
- **Untick** spares only that person. **Spare this branch** unticks that person and everyone below them who can be ticked. Unticked people can be ticked again one at a time, trusted people included.
- **Totals** update as ticks change: "4 people will be deactivated and 1 unused invitation cancelled". They count the ticked people and their unused invitations. Confirming with nobody ticked is refused.
- The acting admin is always an active admin and can't be ticked, so the last-admin case only comes up when that admin is the last one and is in the tree (A28). The server still checks R44 when confirming, whatever the browser sent.

### 4. Confirming

`POST /api/v1/admin/users/{id}/deactivate-tree` with `{version, ticked: [ids], reason}`. In one transaction:

1. Lock the active admins, as today.
2. Find the tree, lock its rows (`SELECT … FOR UPDATE`), then read the tree again and compute its version.
3. If the version differs from the preview's, refuse with `409 tree_changed` and the updated preview in the reply body (§5).
4. Check the ticks. Everyone ticked must be in the tree and able to be ticked. Ticking the last active admin is refused with `409 last_admin` (A28), and ticking yourself with `409 self`. Anything else is `422`.
5. Deactivate exactly the ticked people, as for one user (R43b, R51, R56), and cancel only their unused invitations. Spared people, and their invitations, are untouched. Nobody's inviter changes, so spared people keep their recorded inviter (R42d, A49).
6. Write the record.

After the commit, live connections are closed and Authentik is updated, as before.

`POST /api/v1/admin/users/{id}/deactivate` now deactivates **one** user only. Its `tree` field is removed, so the preview can't be skipped. A request that still sends `tree` gets `400`.

### 5. Detecting "the tree changed since the preview"

- **The version is a fingerprint.** It's a SHA-256 over the root and, for each person in the tree, sorted by ID: ID, status, role and trusted mark. The server computes it for the preview and again when confirming. Nothing is stored between the two, so there's nothing to clean up, several admins can preview at once, and a restart in between does no harm.
- **What changes it:** someone joining or leaving the tree, and any change of status (active, deactivated, deleted), role or trusted mark. Role is included because the preview shows it and admins decide on it. Names and invitations don't change it (see Questions).
- **No gap between the check and the change.** The tree's rows are locked before the version is computed again, and stay locked until the commit. Someone joining under a person in the tree inserts a user whose `invited_by` points at a locked row. PostgreSQL's foreign-key check takes a `FOR KEY SHARE` lock on that row, so the insert waits, and then sees the inviter's invitations cancelled. The same goes for a new invitation from someone in the tree, and for any status, role or trust change. A join that commits before the lock is caught by the version.
- **The updated preview** keeps the admin's ticks for people whose status, role and trusted mark are unchanged. New and changed people get the defaults in §3 and are tagged "new" or "changed", so the admin sees what moved before confirming again (A50).

### 6. Trusted users (R42c)

- `PUT /api/v1/admin/users/{id}/trusted` with `{trusted: true|false}`, for admins. Any account that isn't deleted can be marked, including deactivated people, who keep the mark if reactivated.
- Admins see the mark on the Users list and on the user's page, with who set it and when. The users' own API (`/api/v1/me`) never includes it.
- The mark changes only a preview's starting ticks. It doesn't protect anyone from being deactivated on their own, or from being ticked deliberately.

### 7. Records on users' pages (R42d)

The page of every person a record names (the root, everyone deactivated and everyone spared) lists that record: when, by whom, the reason, whose tree it was, who was deactivated, who was spared, and how many unused invitations were cancelled. The newest is first.

### 8. API

| Method and path | What it does |
|---|---|
| `GET /api/v1/admin/users/{id}` | Now also returns `tree` (inviter, the user and everyone below), `trusted` (by and when) and `deactivations` (records naming them) |
| `GET /api/v1/admin/users/{id}/deactivation-preview` | §3 |
| `POST /api/v1/admin/users/{id}/deactivate-tree` | §4; `409 tree_changed` carries `preview` |
| `PUT /api/v1/admin/users/{id}/trusted` | §6 |
| `POST /api/v1/admin/users/{id}/deactivate` | One user only (the `tree` field is gone) |

### 9. Screens

- **User's page in Administration:** the invite tree as an indented, nested list. The inviter comes first, then the user, marked "this user", then everyone below them. Each person shows name, email, role, date joined, and "Trusted" or "Deactivated" where those apply. Deactivated people are greyed out (`--muted`), and deleted ones read "deleted user". Each name links to that person's page. The page also has a "Trusted" control, the records, and a "Deactivate with invite tree…" link to the preview.
- **Preview page** (`/admin/users/{id}/deactivate-tree`): the same tree with a checkbox per person. People who can't be ticked have a disabled checkbox and say why. Anyone with people below them has a "Spare this branch" button. The page also has live totals (`aria-live`), an optional reason, and Confirm and Cancel buttons. A `tree_changed` refusal shows a message and the updated preview in place.
- **Users list:** "Deactivate with invite tree" goes to the preview instead of asking to confirm. A "Trusted" tag appears beside trusted users. An **"Invite someone"** link at the top goes to the invite form on the Account page (`/account#invite`): admins invite like everyone else (R41), and Administration doesn't get a second form.

### 10. Logs

Log lines name only user and record IDs, and counts. They never include reasons, names or addresses, which `scripts/stack-checks.sh` already checks for addresses.

### 11. Authentik after a database restart (deploy item)

The owner reported that Authentik's workers can hang on stale database connections after the `db` container restarts, and asked for the restart command in `docs/deploy.md` and for `compose.yaml` to prevent it where that's safe.

**What Authentik 2026.5.5 holds open.** By default (`conn_max_age: 0`), Authentik's web requests open a fresh connection each time. The worker's task broker is different: it keeps two long-lived connections, one for locks and one waiting on `LISTEN` for new tasks, and replaces them only when a query on them fails. A connection that dies without being closed would leave the `LISTEN` connection waiting forever.

**What was tried** on cld01, against the stack in `compose.yaml` with Mailpit. The probe after each step was: Authentik's API creates an invitation and emails it, which goes through the worker, and Mailpit must receive it within 180 s.

| Database event | As shipped before M7 | With this ADR's settings |
|---|---|---|
| `docker compose restart db` | email after 2 s | Compose restarted Authentik too; email after 2 s |
| `docker compose up -d --force-recreate db` | email after 2 s | Authentik not restarted (only `db` named); email after 2 s |
| `db`'s settings changed, then `docker compose up -d` (an upgrade) | not run | Compose recreated `db`, then restarted Authentik's server and worker |
| `db` killed (SIGKILL), then started | email after 2 s | email after 2 s |
| Everything stopped, then started at once (as at boot) | email after 2 s | not run |
| `db` restarted while cut off from the network, so Authentik's connections were never closed | email after 2 s | email after 4 s |

The live copy on cld01 showed the same thing. Its `db` was recreated on 2026-10-16 while Authentik kept running since 2026-10-13. The worker logged `server closed the connection unexpectedly` and `terminating connection due to administrator command`, then reconnected, and all 3,016 of its tasks are `done`.

So the hang didn't reproduce with 2026.5.5. Its worker queries the database every few seconds (its scheduler), so a dead connection fails quickly, and the broker then replaces all its connections. The owner may have seen it with an earlier version or another setup. The changes below are cheap and safe, so they're made anyway:

- **`depends_on: db: {condition: service_healthy, restart: true}`** on both Authentik services. Compose restarts Authentik whenever it restarts or recreates `db` itself, which is the owner's rule done for them. It needs Docker Compose 2.17 or later. It does nothing for a crash or a reboot, where Docker starts the containers without Compose.
- **TCP keepalives** through Authentik's own setting, `AUTHENTIK_POSTGRESQL__CONN_OPTIONS`: base64 JSON passed to libpq, `{"keepalives":1,"keepalives_idle":30,"keepalives_interval":10,"keepalives_count":3,"tcp_user_timeout":60000}`. A connection that died silently fails within about a minute, even if the worker stopped querying. The run above checked that Authentik's connections carry keepalive timers (`/proc/net/tcp` in both containers), and that Authentik works with them.
- Not changed: Authentik's `conn_health_checks` only matters with persistent connections (`conn_max_age` above 0), which this deployment doesn't use. Restart policies stay `unless-stopped`.

`docs/deploy.md` gives the command for the cases Compose can't cover, `docker compose restart authentik-server authentik-worker`, and a query that shows whether the worker is stuck: tasks still `queued` after 5 minutes.

### 12. Tests

- **API level** (`internal/acceptance/m7_test.go`, fake Authentik): A48, A49 and A50 as written. Also, for each person type in §3: an already deactivated person, a deleted account in the middle of the tree, an admin who isn't the last, the acting admin, the last admin (A28), a trusted person ticked deliberately, a join during the preview, a status, role or trust change during the preview, a new invitation during the preview, ticking someone outside the tree, and nobody ticked.
- **A25–A28** go through the preview and confirm with everyone who can be ticked, as the browser does. A28's tree case checks that the preview locks the last admin's tick, and that a confirmation ticking them anyway is refused with nothing changed.
- **Against the real stack** (`internal/stacktest`, run by `scripts/stack-checks.sh`): A25 and A28 the same way. A50's join during the preview is a real Authentik enrolment, and confirming is refused.
- **Browser** (`web/tests/e2e.spec.ts`): A48–A50 on the preview page, with trees seeded per browser in `scripts/e2e.sh`. A50's join during the preview is inserted into the database by the test. A41 also follows the new "Invite someone" link to the invite form. The new pages join the axe accessibility sweep.

## Consequences

- An admin can no longer deactivate a whole tree in one request without seeing who's in it. The API needs a preview first.
- A tree containing the last admin is no longer refused outright. The last admin is spared, and everyone else can still be deactivated.
- Deactivation records and trusted marks are new personal data about users, visible only to admins and kept as long as the accounts. They name people only through the user ID, so deleting an account leaves "deleted user" in them.
