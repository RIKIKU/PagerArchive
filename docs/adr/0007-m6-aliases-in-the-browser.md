# 7. Milestone M6: aliases in the browser

Date: 2026-10-16

## Status

Proposed. Waiting for the owner on Q1–Q4.

## Context

Milestone M6 (section 7.12, owner 2026-10-16) moves alias management into the browser. Admins add, edit and delete aliases in Administration (R53). Any signed-in user can request a new alias, a change or a deletion (R54). Admins approve or reject requests (R55), users follow their own (R56), and the alias view lists each alias's history (R57, R35e). From M6 the database is the source of truth: import only adds, and admins can export (R58). The checks are A42–A47.

The owner asked this ADR to record: the data model for requests and history, how approval applies a change, how import reports differences, and the export format. Other rules from the owner for this milestone:

- Admin screens go in Administration (R44b). Styles use the tokens in `web/src/app.css`.
- Page text never appears in logs. Request notes and feedback are kept out of logs too (§8).
- Every person and request is followed through deactivation, deletion and alias deletion (§5).

## Questions for the owner

### Q1. What's kept when an alias is deleted (R55, R57)

Deleting an alias closes its waiting requests as "alias deleted" (R55). Their owners still see them in My requests (R56), so closed requests are kept either way. The question is the alias's **history**.

- **Recommended: keep it, attached to the capcode or pattern.** The deletion is the last entry ("deleted by …, requested by …"). An admin who later adds an alias for the same capcode or pattern sees the earlier entries under it, marked as belonging to the deleted alias "Station 9". The capcode view (R35e, a capcode with no alias) shows them too. History and requests are kept with no time limit, like accounts: they're small and hold no page text.
- Alternative: history goes with the alias. Simpler, but nobody can then see who deleted an alias or why.

### Q2. How people are named in history (R57)

R57 shows who requested and who approved or made each change, on the alias view, which every signed-in user can open. Today the app names a user to others as "name (email)" (the inviter on the Account page).

- **Recommended:** history shows the person's **name** (asked at enrolment) to everyone; admins also see the email address, as on the Users screen. A deleted account shows "deleted user" (R43c). Changes made with the command line or the import show "command line" or "import".
- Alternative: name and email to everyone, as the Account page does for inviters.

### Q3. `alias import --update` (R27a, A21) against R58

R58 says that from M6 the import only adds, and for existing aliases "lists the differences and changes nothing". R27a and A21 still describe `--update`, which applies them.

- **Recommended:** remove `--update`. Given it, the import stops before changing anything and says why: "--update was removed in M6: the database is the source of truth for aliases. Change them in Administration." A21's first half stays as it is. Its second half is replaced by that refusal. The owner updates the wording of R27a and A21 in `SPEC.md` (the implementer doesn't edit acceptance checks).
- Alternative: keep `--update` for the command line only, as an operator's escape hatch. That contradicts R58.

### Q4. Integrations that select a deleted alias (R48)

An integration's selection can hold aliases (M5). Today, deleting an alias removes it from every selection (cascade), so an integration that selected only "Station 9 (0100009)" quietly stops forwarding anything. With admins deleting aliases in the browser, that will happen more often.

- **Recommended:** when an alias for **one capcode** is deleted, integrations that selected it keep that capcode instead (R48 in reverse: a capcode with no alias is selected as a capcode). A deleted **range** alias is just removed from selections, because its capcodes now resolve to other aliases (ADR 0006 §2) and forwarding the whole range would be new. The admin's delete confirmation says how many integrations select the alias. Owners aren't told separately.
- Alternative: keep today's behaviour (removed from selections), with the same count in the confirmation.

When an admin changes an alias's capcode (R53), integrations keep following the alias, as today: they selected "Station 9", wherever it is.

## Already decided elsewhere (cited, not re-decided)

- One alias per capcode or pattern, enforced by the database (R27a, migration 00005). Precedence (R27). An alias may have no template (ADR 0003 §3a).
- Names, agencies, colours and the ignored flag are read through `messages.alias_id`, so a rename shows straight away. A template or capcode change queues a re-parse in the same transaction (R23a, ADR 0003 §3).
- Templates are still managed by import or the command line (Out of scope). Users and admins choose from existing templates.
- Deactivating or deleting a user withdraws their waiting requests (R56). A deleted user is "deleted user" (R43c). User rows are kept with status `deleted` (ADR 0004).
- Admin endpoints answer `403` to non-admins, and the admin layout refuses them (R44b, A29, A41).

## Implementer decisions

### 1. Data model (migration 00010)

| Table | Holds |
|---|---|
| `alias_requests` | `id`; `kind` (`add`, `change`, `delete`); `requested_by` (user); `alias_id` (`change` and `delete`; `ON DELETE SET NULL`; set on approval for `add`); `pattern` (normalised: what was asked for, or the alias's capcode or pattern when it was submitted); `alias_name` (the alias's name when submitted, for showing a request after its alias is gone); the proposal (below); `note` (up to 500 characters, optional); `base_change` (§3); `status` (`waiting`, `approved`, `rejected`, `withdrawn`, `alias_deleted`); `feedback` (rejections, up to 500 characters); `reviewed_by`; `created_at`, `updated_at`, `closed_at` |
| `alias_changes` | The history (R57). `id`; `alias_id` (`ON DELETE SET NULL`, see Q1); `pattern_key` (the alias's normalised capcode or pattern at the time); `alias_name` (its name at the time); `action` (`added`, `changed`, `deleted`); `changes` (JSON: field → `[before, after]`, with template **names**, so history reads the same after a template is renamed or deleted); `made_by` (user who made or approved it; null for the command line and import); `requested_by` (user, for approved requests); `request_id`; `source` (`admin`, `request`, `cli`, `import`); `at` |

- **The proposal.** An `add` request has a name, a template (or none) and the ignored flag. A `change` request stores **only the fields it changes**: `name` (null: unchanged), `set_template` with `template_id` (null: none), and `ignored` (null: unchanged). A `delete` has none. A change that changes nothing is refused.
- Requests name templates by id (`ON DELETE SET NULL`), with the template's name copied beside it. Deleting a template that a **waiting** request names is refused, like one an alias uses (section 14), so a waiting request never loses its template.
- A database check ties fields to kinds (an `add` has a pattern and name; a `change` has at least one field; `rejected` has feedback).
- An index on `(status)` where `status = 'waiting'` keeps the count on the Admin link cheap.

### 2. Who can do what (R53–R56)

| | User | Admin |
|---|---|---|
| Add, edit (name, capcode or pattern, template, ignored) and delete aliases | Refused (`403`, A47) | Directly, at once (R53) |
| Request an alias, a change or a deletion | Yes | Yes (rarely useful; they can edit) |
| See requests | Their own | All |
| Edit or withdraw a request | Their own, while waiting | Their own, while waiting. They can't edit anyone else's (R55) |
| Approve or reject | No | Any waiting request, their own included |
| Export | No | Yes |

- **Checks when a request is submitted or edited** (R54): `add` must be for a capcode or pattern with no alias (R27a, same message as R27a: it names the existing alias and suggests requesting a change to it instead). `change` and `delete` must name an existing alias, and can't change the capcode or pattern. The template must exist. A name is 1–100 characters, with no control characters, and doesn't start with `=`, `+`, `-` or `@` (§6). A user has at most one waiting request per alias, or per capcode or pattern for `add` (they're told to edit the one they have), and at most 20 waiting in all.
- Two users may both request an alias for the same capcode. The first approved wins; approving the second is refused by R27a (A45).
- Admin edits use the same rules and messages (R53), with `409` for a duplicate (A42).

### 3. "Changed since requested" (R55, A45)

- A `change` or `delete` request records the id of the alias's latest history entry when it's submitted (`base_change`, null if it has none). It's **changed since requested** while a newer history entry exists for that alias. Every change writes history (§4), whoever makes it, so nothing is missed.
- An `add` request whose capcode or pattern has gained an alias meanwhile is marked "<capcode> now has the alias "<name>"", and approving it is refused (A45).
- Both marks are worked out when requests are listed, not stored. Users and admins see them. The request shows the **current** values beside the **proposed** ones (R55), read from the alias at that moment.
- Editing a waiting request re-checks it and records the alias's latest entry again: the user has seen the alias as it is now.

### 4. How a change is applied (R53, R55, R57)

All alias changes go through `internal/catalog`, which already queues re-parsing (R23a). Each catalog function now takes **who** (`source`, the user who made or approved it, the requester and request) and, in the **same transaction**:

1. locks the alias row (`FOR UPDATE`) and re-checks R27a against the database's unique constraint;
2. applies the change, and queues a re-parse when the template or capcode changed (both the old and the new capcode or pattern);
3. writes one history entry with each changed field's before and after (no entry when nothing actually changed);
4. on deletion: closes the alias's waiting requests as `alias_deleted` (R55) and moves integrations' selections (Q4).

**Approval** locks the request (`FOR UPDATE`, so two admins can't both review it), checks it's still waiting, and then applies it with the steps above in the same transaction:

- `add`: creates the alias exactly as requested. If the capcode or pattern has an alias now, approval is refused with the reason, naming it (`409`, A45). The request stays waiting, so the admin can reject it with feedback.
- `change`: sets only the fields the request changes, to the requested values, even if the alias changed meanwhile ("exactly as submitted"). Other fields are left as they are now.
- `delete`: deletes the alias. Its other waiting requests close as "alias deleted".
- The request becomes `approved`, with the reviewer and time, and the history entry names both the requester and the approver (A43).

**Rejection** needs feedback (1–500 characters, after trimming; `422` without it, A44). It changes no alias and writes no history: history lists changes (R57).

Admins can't edit a request (R55): there's no endpoint for it. The command line (`pagerarchive alias add|set|delete`) stays, writing history as "command line".

### 5. People and requests over time

- **Deactivation** (R43b, R56): in the same transaction as the rest of deactivation (one user or a whole invite tree), their waiting requests become `withdrawn` (A47). Reactivating doesn't reopen them.
- **Deleting your account** (R43c): waiting requests become `withdrawn`, and the **notes** in all your requests are cleared, as the invitation addresses are. The requests and history entries stay, credited to "deleted user" (R57).
- **A reviewer or approver who is later deactivated or deleted** stays credited by name, or "deleted user".
- **Alias deletion**: §4. A closed request keeps the capcode or pattern and the name the alias had, so My requests still makes sense.
- **Template deletion** (command line): refused while a waiting request names the template (§1).
- **Retention** (R45) deletes messages only. Requests and history hold no page text and aren't touched (Q1).

### 6. Export format (R58)

- `GET /api/v1/admin/aliases/export` (admins) and `pagerarchive alias export [file]` write the **import format**: a CSV with the header `id,address,alias,agency,icon,color,ignore`, one row per alias, ordered by capcode or pattern. `id` is the alias's id (usable in an overrides file); `address` is the capcode or pattern; `agency` is the **template's name** (empty for none), because that's what the import looks templates up by; `icon` and `color` are the template's; `ignore` is `true` or `false`. UTF-8, no byte-order mark, `\r\n` line ends (RFC 4180). The file is named `pagerarchive-aliases-<date>.csv`.
- Templates' agency names and profiles aren't in the CSV. The import format can't carry them, and they're managed separately. An export imported into an empty database creates templates named after the template names; the mapping file attaches profiles as usual. On the same database, an exported file imports with **no differences** (A46).
- **Spreadsheet formulas:** a value starting with `=`, `+`, `-`, `@`, tab or carriage return is written with a leading `'`, so a spreadsheet doesn't run it. The import removes one leading `'` before those characters, so the round trip is exact. New names can't start with them anyway (§2); this covers names imported earlier.

### 7. Import (R58, R30)

- The import **only adds** aliases for capcodes and patterns with none. For each existing alias it compares name, template and ignored flag, lists the differences as before (`name: "Station 1" → "Station One"`) and changes nothing. `--update` goes (Q3). Duplicates, overrides, squashing, repeated names and the mapping file are unchanged (R27a, R30).
- Each alias added writes a history entry, `source = import`.
- A waiting `add` request for a capcode the import fills is left waiting; it's marked and approving it is refused (§3).
- The report also counts aliases added and differences, as now. It never prints request notes.

### 8. Logs

No log line carries page text (P1), and alias request handlers log only ids, kinds and outcomes: never names, notes or feedback. The acceptance tests' log check (every page text posted) is extended with each test's notes and feedback.

### 9. API

All need a signed-in user; `admin/` ones need an admin (`403` otherwise).

| Method and path | Does |
|---|---|
| `GET /api/v1/templates` | Templates to choose from: id, name, agency, icon, colour |
| `GET /api/v1/aliases/{id}` | As before, plus `history` (§1, Q2) and the signed-in user's waiting request for it, if any |
| `GET /api/v1/capcodes/{capcode}` | As before, plus history kept from deleted aliases (Q1) |
| `GET /api/v1/alias-requests` | The signed-in user's requests, newest first, with current values and marks (§3) |
| `POST /api/v1/alias-requests` | Submit (`kind`, `alias_id` or `pattern`, the proposal, `note`) |
| `PUT /api/v1/alias-requests/{id}` | Edit one's own waiting request |
| `POST /api/v1/alias-requests/{id}/withdraw` | Withdraw one's own waiting request |
| `GET /api/v1/admin/alias-requests?status=waiting\|closed` | Requests to review (waiting, oldest first), or the last 100 closed |
| `POST /api/v1/admin/alias-requests/{id}/approve` | §4 |
| `POST /api/v1/admin/alias-requests/{id}/reject` | `{"feedback": "…"}`, required |
| `GET /api/v1/admin/aliases?q=` | Aliases, filtered by name, capcode or pattern, with their template and number of messages |
| `POST /api/v1/admin/aliases` | Add (R53) |
| `PUT /api/v1/admin/aliases/{id}` | Edit name, capcode or pattern, template, ignored |
| `DELETE /api/v1/admin/aliases/{id}` | Delete; the response says how many integrations it was selected by |
| `GET /api/v1/admin/aliases/export` | §6 |

`GET /api/v1/meta` gains `waiting_requests` for admins only (the Admin link's count).

### 10. Screens

- **Header:** the Admin link shows the number of waiting requests, "Admin 1", read as "Admin, 1 alias request waiting" by screen readers. It refreshes on each page load and every minute, like the receivers badge. Signed-in users get a **Requests** link (My requests).
- **Administration** start page gains **Aliases** and **Alias requests** (with the waiting count).
- **Aliases** (`/admin/aliases`): a search box, a table of aliases (name, capcode or pattern, template, ignored), **Add alias** and **Export CSV**. Each alias has an edit page (`/admin/aliases/{id}`) with the form, **Delete** (with a confirmation that names the alias, its waiting requests and the integrations that select it) and its history.
- **Alias requests** (`/admin/requests`): waiting requests, oldest first. Each shows what's asked, by whom and when, the note, and a **Current | Proposed** table with changed rows highlighted, plus the marks from §3. **Approve**, and **Reject** with a required feedback box. A second tab lists recently closed requests.
- **Alias view** (R35e): admins get **Edit**. Users get **Request a change** (name, template, ignored, note) and **Request deletion** (note). If they already have a waiting request for it, they see it instead, with a link to edit it. Below the details, **History**: newest first, "Name changed from "Station 1" to "Station One" · requested by B · approved by A · 16 Oct 2026, 14:05".
- **Capcode view** (a capcode with no alias): **Request an alias for this capcode**.
- **My requests** (`/requests`): each request's status (waiting, approved, rejected, withdrawn, alias deleted), the feedback, the marks, and Edit and Withdraw while waiting. **Request a new alias** for any capcode or pattern.
- All forms use the shared tokens and the existing input styles; checkboxes and radios keep their small size (b7e5d65). Pages are covered by the axe checks at 375 px and 1280 px in both themes.

### 11. Tests

- **Go acceptance tests** (`internal/acceptance/m6_test.go`, real PostGIS, as before): A42–A47 at API level, plus deactivating an invite tree, deleting an account, deleting an alias with waiting requests and with integrations selecting it, editing and withdrawing, two admins approving at once, the second-add race, the export round trip with formula-like names, and logs free of names, notes and feedback.
- **Browser tests** (`web/tests/e2e.spec.ts`): A42 (rename in Administration, then the list shows "Station One"; duplicate refused), A43 (the count on the Admin link, approve, history, My requests), A44 and A45 in the review screen, and the new pages in the axe checks. The three browsers share one database, so each uses its own capcodes, seeded by `scripts/e2e.sh`, rather than renaming the fixture's Station 1.
- At least two checks are shown failing against deliberately broken code, then passing again.
