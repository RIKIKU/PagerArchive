-- name: CreateClient :exec
INSERT INTO clients (id, name, key_hash) VALUES ($1, $2, $3);

-- name: RevokeClient :execrows
UPDATE clients SET revoked_at = now() WHERE id = $1 AND revoked_at IS NULL;

-- name: ListClients :many
SELECT id, name, created_at, revoked_at, last_seen_at, clock_ahead_since FROM clients ORDER BY id;

-- name: MarkClockAhead :exec
UPDATE clients SET clock_ahead_since = COALESCE(clock_ahead_since, now()) WHERE id = $1;

-- name: ActiveClientByKeyHash :one
SELECT id FROM clients WHERE key_hash = $1 AND revoked_at IS NULL;

-- name: TouchClient :exec
UPDATE clients
SET last_seen_at = GREATEST(COALESCE(last_seen_at, sqlc.arg(seen_at)::timestamptz), sqlc.arg(seen_at)::timestamptz),
    clock_ahead_since = NULL
WHERE id = sqlc.arg(id);

-- name: CreateTemplate :one
INSERT INTO templates (name, agency, icon, colour, agency_profile)
VALUES ($1, $2, $3, $4, $5)
RETURNING id;

-- name: CreateAlias :one
INSERT INTO aliases (pattern, name, template_id, ignored) VALUES ($1, $2, $3, $4) RETURNING id;

-- name: AliasesMatching :many
SELECT a.id, a.pattern, a.name, t.agency_profile
FROM aliases a
LEFT JOIN templates t ON t.id = a.template_id
WHERE sqlc.arg(capcode)::text LIKE a.pattern;

-- name: LockDedupKey :exec
SELECT pg_advisory_xact_lock(hashtextextended(encode(sqlc.arg(dedup_key)::bytea, 'hex'), 0));

-- name: FindDuplicate :one
-- A copy within 10 s of the message's sent time, either side (R19, section 14).
SELECT id, sent_at
FROM messages
WHERE dedup_key = sqlc.arg(dedup_key)
  AND sent_at BETWEEN sqlc.arg(received_at)::timestamptz - interval '10 seconds'
                  AND sqlc.arg(received_at)::timestamptz + interval '10 seconds'
ORDER BY abs(extract(epoch FROM sent_at - sqlc.arg(received_at)::timestamptz)), id
LIMIT 1;

-- name: InsertMessage :one
INSERT INTO messages (
    capcode, text, priority, heartbeat, sent_at, dedup_key, format,
    job_number, event_type, description, address, locality, grid_ref,
    paging_group, units, extra, parse_failed, address_unsplit, function, baud, alias_id
) VALUES (
    $1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16, $17, $18, $19, $20, $21
)
RETURNING id;

-- name: KeepEarliestSentAt :exec
UPDATE messages SET sent_at = LEAST(sent_at, sqlc.arg(received_at)::timestamptz) WHERE id = sqlc.arg(id);

-- name: InsertReception :exec
INSERT INTO receptions (message_id, client_id, received_at)
VALUES ($1, $2, $3)
ON CONFLICT DO NOTHING;

-- name: ListTemplates :many
SELECT t.id, t.name, t.agency, t.icon, t.colour, t.agency_profile,
       (SELECT count(*) FROM aliases a WHERE a.template_id = t.id) AS alias_count
FROM templates t
ORDER BY t.name;

-- name: TemplateByName :one
SELECT id, name, agency, icon, colour, agency_profile FROM templates WHERE name = $1;

-- name: UpdateTemplate :exec
UPDATE templates SET agency = $2, icon = $3, colour = $4, agency_profile = $5 WHERE id = $1;

-- name: DeleteTemplate :execrows
DELETE FROM templates WHERE id = $1;

-- name: ListAliases :many
SELECT a.id, a.pattern, a.name, a.ignored, t.name AS template_name
FROM aliases a
LEFT JOIN templates t ON t.id = a.template_id
ORDER BY a.pattern;

-- name: AliasByPattern :one
SELECT id, pattern, name, template_id, ignored FROM aliases WHERE pattern = $1;

-- name: AliasPatternsForTemplate :many
SELECT pattern FROM aliases WHERE template_id = $1;

-- name: UpdateAlias :exec
UPDATE aliases SET name = $2, template_id = $3, ignored = $4 WHERE id = $1;

-- name: DeleteAlias :execrows
DELETE FROM aliases WHERE id = $1;

-- name: AllAliases :many
SELECT a.id, a.pattern, a.name, a.ignored, t.agency_profile
FROM aliases a
LEFT JOIN templates t ON t.id = a.template_id;

-- name: RequestReparse :exec
-- R23a: queue a re-parse and wake the API's worker.
WITH r AS (
    INSERT INTO reparse_requests (capcode_pattern, reason) VALUES ($1, $2)
)
SELECT pg_notify('pagerarchive_reparse', '');

-- name: ClaimReparseRequests :many
DELETE FROM reparse_requests RETURNING capcode_pattern, reason;

-- name: MessagesToReparse :many
SELECT id, capcode, text, priority, heartbeat
FROM messages
WHERE capcode LIKE sqlc.arg(pattern)::text AND id > sqlc.arg(after_id)::bigint
ORDER BY id
LIMIT sqlc.arg(batch)::int;

-- name: UpdateParsed :exec
UPDATE messages SET
    alias_id = $2, format = $3, job_number = $4, event_type = $5, description = $6,
    address = $7, locality = $8, grid_ref = $9, paging_group = $10, units = $11,
    extra = $12, parse_failed = $13, address_unsplit = $14
WHERE id = $1;

-- name: SetLocation :exec
-- R24-R26: where a message is placed, set after parsing (ingest and re-parse).
UPDATE messages SET
    location = CASE WHEN sqlc.narg(lon)::float8 IS NULL THEN NULL
                    ELSE ST_SetSRID(ST_MakePoint(sqlc.narg(lon)::float8, sqlc.narg(lat)::float8), 4326)::geography END,
    location_source = sqlc.narg(source)::text,
    location_mismatch = sqlc.arg(mismatch)::bool
WHERE id = sqlc.arg(id)::bigint;

-- name: AddressPoint :one
SELECT ST_X(geom::geometry)::float8 AS lon, ST_Y(geom::geometry)::float8 AS lat
FROM address_points
WHERE locality = sqlc.arg(locality)::text AND street = sqlc.arg(street)::text AND number = sqlc.arg(number)::text
LIMIT 1;

-- name: StreetPoint :one
SELECT ST_X(geom::geometry)::float8 AS lon, ST_Y(geom::geometry)::float8 AS lat
FROM street_points
WHERE locality = sqlc.arg(locality)::text AND street = sqlc.arg(street)::text;

-- name: LocalityExtent :one
SELECT min_lon, min_lat, max_lon, max_lat FROM localities WHERE name = $1;

-- name: DistanceToStreet :one
-- R26 for a street-only address: metres from a point to the nearest address
-- point on the street (or to the street's own point if it has no addresses).
SELECT COALESCE(
    (SELECT min(ST_Distance(a.geom, p.g)) FROM address_points a
     WHERE a.locality = sqlc.arg(locality)::text AND a.street = sqlc.arg(street)::text),
    (SELECT ST_Distance(s.geom, p.g) FROM street_points s
     WHERE s.locality = sqlc.arg(locality)::text AND s.street = sqlc.arg(street)::text),
    -1)::float8 AS metres
FROM (SELECT ST_SetSRID(ST_MakePoint(sqlc.arg(lon)::float8, sqlc.arg(lat)::float8), 4326)::geography AS g) p;

-- name: GetSetting :one
SELECT value FROM settings WHERE key = $1;

-- name: SetSetting :exec
INSERT INTO settings (key, value) VALUES ($1, $2)
ON CONFLICT (key) DO UPDATE SET value = EXCLUDED.value;

-- name: SearchMessages :many
-- R33. Every filter is optional (NULL = any). Keyset pagination on
-- (sent_at, id), newest first. cat_want compares "event_type is in the
-- category's codes" with true (that category) or false (Other).
SELECT m.id, m.capcode, m.alias_id, m.text, m.priority, m.heartbeat, m.sent_at, m.format,
       m.job_number, m.event_type, m.description, m.address, m.locality, m.grid_ref,
       m.paging_group, m.units, m.extra, m.parse_failed, m.address_unsplit,
       m.location_mismatch, a.name AS alias_name, COALESCE(a.ignored, false)::bool AS alias_ignored,
       t.agency, t.icon, t.colour, t.agency_profile,
       m.lon, m.lat, m.location_source
FROM messages m
LEFT JOIN aliases a ON a.id = m.alias_id
LEFT JOIN templates t ON t.id = a.template_id
WHERE (sqlc.narg(only_id)::bigint IS NULL OR m.id = sqlc.narg(only_id))
  AND (sqlc.narg(q)::text IS NULL OR m.text ILIKE sqlc.narg(q)
       OR EXISTS (SELECT 1 FROM jsonb_each_text(m.extra) e WHERE e.value ILIKE sqlc.narg(q)))
  AND (sqlc.narg(capcode)::text IS NULL OR m.capcode = sqlc.narg(capcode))
  AND (sqlc.narg(alias)::text IS NULL OR a.name ILIKE sqlc.narg(alias))
  AND (sqlc.narg(alias_id)::bigint IS NULL OR m.alias_id = sqlc.narg(alias_id))
  AND (sqlc.narg(agency)::text IS NULL OR t.agency ILIKE sqlc.narg(agency))
  AND (sqlc.narg(paging_group)::text IS NULL OR upper(m.paging_group) = upper(sqlc.narg(paging_group)))
  AND (sqlc.narg(priority)::text IS NULL
       OR (sqlc.narg(priority) = 'none' AND m.priority IS NULL)
       OR m.priority = sqlc.narg(priority))
  AND (sqlc.narg(cat_want)::bool IS NULL
       OR COALESCE(m.event_type = ANY(sqlc.arg(cat_codes)::text[])
                   OR m.event_type LIKE ANY(sqlc.arg(cat_prefixes)::text[]), false) = sqlc.narg(cat_want))
  AND (sqlc.narg(job_number)::text IS NULL OR m.job_number = sqlc.narg(job_number))
  AND (sqlc.narg(sent_from)::timestamptz IS NULL OR m.sent_at >= sqlc.narg(sent_from))
  AND (sqlc.narg(sent_to)::timestamptz IS NULL OR m.sent_at < sqlc.narg(sent_to))
  AND (NOT sqlc.arg(unaliased)::bool OR m.alias_id IS NULL)
  AND (sqlc.arg(include_ignored)::bool OR a.ignored IS NOT TRUE)
  AND (NOT sqlc.arg(parse_failed)::bool OR m.parse_failed)
  AND (NOT sqlc.arg(address_unsplit)::bool OR m.address_unsplit)
  AND (NOT sqlc.arg(location_mismatch)::bool OR m.location_mismatch)
  AND (NOT sqlc.arg(located)::bool OR m.location IS NOT NULL)
  AND (sqlc.narg(before_sent)::timestamptz IS NULL
       OR (m.sent_at, m.id) < (sqlc.narg(before_sent), sqlc.narg(before_id)::bigint))
ORDER BY m.sent_at DESC, m.id DESC
LIMIT sqlc.arg(max_rows)::int;

-- name: UnaliasedCapcodes :many
-- R33: capcodes with no alias, with message count and first- and last-seen times.
SELECT capcode, count(*) AS messages, min(sent_at)::timestamptz AS first_seen,
       max(sent_at)::timestamptz AS last_seen
FROM messages
WHERE alias_id IS NULL
GROUP BY capcode
ORDER BY max(sent_at) DESC;

-- name: MessageReceptions :many
SELECT r.client_id, c.name AS client_name, r.received_at
FROM receptions r
JOIN clients c ON c.id = r.client_id
WHERE r.message_id = $1
ORDER BY r.received_at, r.client_id;

-- name: AliasByKey :one
-- R27a: the alias for a normalised capcode or pattern, if any.
SELECT id, pattern, name FROM aliases WHERE pattern_key = sqlc.arg(pattern_key)::text;

-- name: AliasDetailByKey :one
-- Import: an existing alias with its template's name, to compare with a CSV row.
SELECT a.id, a.name, a.ignored, t.name AS template_name
FROM aliases a
LEFT JOIN templates t ON t.id = a.template_id
WHERE a.pattern_key = sqlc.arg(pattern_key)::text;

-- name: AliasView :one
-- R35e: an alias with its template's presentation.
SELECT a.id, a.pattern, a.pattern_key, a.name, a.ignored, a.template_id, t.name AS template_name, t.agency, t.icon, t.colour
FROM aliases a
LEFT JOIN templates t ON t.id = a.template_id
WHERE a.id = $1;

-- name: UnaliasedCapcode :many
-- R35e: one capcode's pages with no alias (no rows if it has none).
SELECT count(*) AS messages, min(sent_at)::timestamptz AS first_seen,
       max(sent_at)::timestamptz AS last_seen
FROM messages
WHERE capcode = $1 AND alias_id IS NULL
GROUP BY capcode;

-- Accounts (R40-R44, ADR 0004).

-- name: UserBySubject :one
SELECT * FROM users WHERE subject = $1;

-- name: UserByID :one
SELECT * FROM users WHERE id = $1;

-- name: UserByEmailKey :one
SELECT * FROM users WHERE email_key = $1;

-- name: CreateUser :one
INSERT INTO users (subject, email, email_key, name, role, invited_by)
VALUES ($1, $2, $3, $4, $5, $6)
RETURNING *;

-- name: RelinkUser :exec
-- An admin's re-invitation was used: the same account, active again, with a
-- new Authentik user and the admin as its inviter (owner, 2026-10-13).
UPDATE users SET subject = $2, name = $3, invited_by = $4, status = 'active', deactivated_at = NULL
WHERE id = $1;

-- name: SetUserName :exec
UPDATE users SET name = $2 WHERE id = $1 AND status <> 'deleted';

-- name: ClearUserSubject :exec
UPDATE users SET subject = NULL WHERE id = $1;

-- name: ListUsers :many
SELECT u.id, u.email, u.name, u.role, u.status, u.created_at, u.deactivated_at,
       u.invited_by, i.status AS inviter_status, i.email AS inviter_email, i.name AS inviter_name,
       -- R44b: how many integrations, and how many paused for failing.
       (SELECT count(*) FROM integrations g WHERE g.owner_id = u.id) AS integrations,
       (SELECT count(*) FROM integrations g WHERE g.owner_id = u.id AND g.paused_by = 'failing') AS integrations_failing
FROM users u
LEFT JOIN users i ON i.id = u.invited_by
WHERE u.status <> 'deleted'
ORDER BY u.created_at, u.id;

-- name: LockActiveAdmins :many
-- Taken before any change that could remove the last active admin (R44).
SELECT id FROM users WHERE role = 'admin' AND status = 'active' ORDER BY id FOR UPDATE;

-- name: CountActiveAdmins :one
SELECT count(*) FROM users WHERE role = 'admin' AND status = 'active';

-- name: InviteTree :many
-- A user and everyone they invited, recursively, through deleted accounts
-- too (R42). Deleted accounts are placeholders and are returned as such.
WITH RECURSIVE tree AS (
    SELECT u.id FROM users u WHERE u.id = sqlc.arg(root)::bigint
    UNION
    SELECT c.id FROM users c JOIN tree t ON c.invited_by = t.id
)
SELECT u.* FROM users u JOIN tree t ON t.id = u.id ORDER BY u.id;

-- name: DeactivateUsers :many
UPDATE users SET status = 'deactivated', deactivated_at = now()
WHERE id = ANY(sqlc.arg(ids)::bigint[]) AND status = 'active'
RETURNING id, subject;

-- name: ReactivateUser :execrows
UPDATE users SET status = 'active', deactivated_at = NULL WHERE id = $1 AND status = 'deactivated';

-- name: SetUserRole :execrows
UPDATE users SET role = $2 WHERE id = $1 AND status <> 'deleted';

-- name: DeleteUserAccount :exec
-- R43c: only an anonymous placeholder stays, so the invite tree stays
-- connected (owner, 2026-10-13).
UPDATE users SET status = 'deleted', deleted_at = now(), subject = NULL, email = NULL,
    email_key = NULL, name = NULL, role = 'user', deactivated_at = NULL
WHERE id = $1;

-- name: ScrubInvitationsTo :exec
-- The invitation that enrolled a deleted user names them: forget the address.
UPDATE invitations SET email = NULL, email_key = NULL WHERE used_by = $1;

-- name: CreateSession :exec
INSERT INTO sessions (token_hash, user_id, expires_at) VALUES ($1, $2, $3);

-- name: SessionUser :one
SELECT u.*, s.last_used_at AS session_used_at
FROM sessions s JOIN users u ON u.id = s.user_id
WHERE s.token_hash = $1 AND s.expires_at > now() AND u.status = 'active';

-- name: TouchSession :exec
UPDATE sessions SET last_used_at = now(), expires_at = $2 WHERE token_hash = $1;

-- name: DeleteSession :exec
DELETE FROM sessions WHERE token_hash = $1;

-- name: DeleteUserSessions :exec
DELETE FROM sessions WHERE user_id = ANY(sqlc.arg(ids)::bigint[]);

-- name: DeleteExpiredSessions :exec
DELETE FROM sessions WHERE expires_at <= now();

-- name: CreateInvitation :one
INSERT INTO invitations (email, email_key, invited_by, first_admin, reactivates, expires_at)
VALUES ($1, $2, $3, $4, $5, $6)
RETURNING *;

-- name: SetInvitationAuthentikID :exec
UPDATE invitations SET authentik_id = $2 WHERE id = $1;

-- name: InvitationByID :one
SELECT * FROM invitations WHERE id = $1;

-- name: PendingInvitationTo :one
SELECT i.*, u.email AS inviter_email, u.name AS inviter_name
FROM invitations i LEFT JOIN users u ON u.id = i.invited_by
WHERE i.email_key = $1 AND i.status = 'pending' AND i.expires_at > now()
LIMIT 1;

-- name: UseInvitation :execrows
UPDATE invitations SET status = 'used', used_by = $2, used_at = now()
WHERE id = $1 AND status = 'pending';

-- name: CancelInvitation :one
UPDATE invitations SET status = 'cancelled', cancelled_at = now()
WHERE id = $1 AND status = 'pending'
RETURNING authentik_id;

-- name: CancelInvitationsBy :many
-- R43b, R43c: a deactivated or deleted user's unused invitations.
UPDATE invitations SET status = 'cancelled', cancelled_at = now()
WHERE invited_by = ANY(sqlc.arg(ids)::bigint[]) AND status = 'pending'
RETURNING authentik_id;

-- name: CancelPendingInvitationsTo :many
-- Expired or replaced invitations to one address.
UPDATE invitations SET status = 'cancelled', cancelled_at = now()
WHERE email_key = $1 AND status = 'pending'
RETURNING authentik_id;

-- name: CancelFirstAdminInvitations :many
UPDATE invitations SET status = 'cancelled', cancelled_at = now()
WHERE first_admin AND status = 'pending'
RETURNING authentik_id;

-- name: ListInvitations :many
-- One inviter's invitations, or every one (inviter NULL), newest first.
SELECT i.id, i.email, i.status, i.created_at, i.expires_at, i.used_at, i.cancelled_at,
       i.first_admin, i.reactivates, i.invited_by, u.email AS inviter_email, u.name AS inviter_name, u.status AS inviter_status
FROM invitations i LEFT JOIN users u ON u.id = i.invited_by
WHERE (sqlc.narg(inviter)::bigint IS NULL OR i.invited_by = sqlc.narg(inviter)::bigint)
  AND i.email IS NOT NULL
ORDER BY i.created_at DESC, i.id DESC
LIMIT 200;

-- name: DeleteMessagesBefore :execrows
-- R45: one batch of messages older than the cut-off. Receptions go with them
-- (cascade); jobs and locations are columns of the message (section 14).
DELETE FROM messages
WHERE id IN (SELECT id FROM messages WHERE sent_at < sqlc.arg(cutoff)::timestamptz LIMIT sqlc.arg(batch)::int);

-- name: MessagesForRedaction :many
SELECT id, capcode, text FROM messages
WHERE id > sqlc.arg(after_id)::bigint
ORDER BY id
LIMIT sqlc.arg(batch)::int;

-- name: UpdateRedactedText :exec
-- P1: retroactive redaction replaces the text and its dedup hash; the
-- caller then re-parses the message so parsed fields follow.
UPDATE messages SET text = $2, dedup_key = $3 WHERE id = $1;

-- name: MessagesByIDs :many
SELECT id, capcode, text, priority, heartbeat
FROM messages
WHERE id = ANY(sqlc.arg(ids)::bigint[])
ORDER BY id;

-- name: CapcodeReport :many
-- R38: Emergency messages to one capcode in a date range, by event type.
-- Heartbeats are excluded (R35b).
SELECT event_type, count(*) AS messages
FROM messages
WHERE capcode = sqlc.arg(capcode)::text
  AND priority = 'Emergency'
  AND NOT heartbeat
  AND sent_at >= sqlc.arg(sent_from)::timestamptz
  AND sent_at < sqlc.arg(sent_to)::timestamptz
GROUP BY event_type
ORDER BY event_type NULLS LAST;

-- Integrations (R47-R51c, ADR 0006).

-- name: CreateIntegration :one
INSERT INTO integrations (uid, owner_id, name, kind, content, config, secrets, filter)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
RETURNING *;

-- name: UpdateIntegration :exec
UPDATE integrations SET name = $2, content = $3, config = $4, secrets = $5, filter = $6, updated_at = now()
WHERE id = $1;

-- name: IntegrationByID :one
SELECT i.*, u.role AS owner_role, u.status AS owner_status
FROM integrations i JOIN users u ON u.id = i.owner_id
WHERE i.id = $1;

-- name: IntegrationsByOwner :many
SELECT * FROM integrations WHERE owner_id = $1 ORDER BY created_at, id;

-- name: CountIntegrationsByOwner :one
SELECT count(*) FROM integrations WHERE owner_id = $1;

-- name: AdminIntegrationsOfUser :many
-- R44b: one user's integrations as admins see them: that they exist,
-- never what they send or where (R47).
SELECT id, name, kind, status, paused_by, last_delivered_at, created_at
FROM integrations WHERE owner_id = $1 ORDER BY created_at, id;

-- name: DeleteIntegrationsByOwner :exec
-- R43c: a deleted account's integrations go with it.
DELETE FROM integrations WHERE owner_id = $1;

-- name: DeleteIntegration :exec
DELETE FROM integrations WHERE id = $1;

-- name: IntegrationIDsByOwner :many
SELECT id FROM integrations WHERE owner_id = $1;

-- name: AllIntegrationSecrets :many
-- Key rotation: every encrypted blob.
SELECT id, uid, secrets FROM integrations WHERE secrets IS NOT NULL;

-- name: SetIntegrationSecrets :exec
UPDATE integrations SET secrets = $2 WHERE id = $1;

-- name: ClearIntegrationCapcodes :exec
DELETE FROM integration_capcodes WHERE integration_id = $1;

-- name: AddIntegrationCapcodes :exec
INSERT INTO integration_capcodes (integration_id, capcode)
SELECT sqlc.arg(id)::bigint, unnest(sqlc.arg(capcodes)::text[]);

-- name: ClearIntegrationAliases :exec
DELETE FROM integration_aliases WHERE integration_id = $1;

-- name: AddIntegrationAliases :exec
INSERT INTO integration_aliases (integration_id, alias_id)
SELECT sqlc.arg(id)::bigint, unnest(sqlc.arg(alias_ids)::bigint[]);

-- name: IntegrationCapcodes :many
SELECT capcode FROM integration_capcodes WHERE integration_id = $1 ORDER BY capcode;

-- name: IntegrationAliases :many
SELECT a.id, a.pattern, a.name
FROM integration_aliases ia JOIN aliases a ON a.id = ia.alias_id
WHERE ia.integration_id = $1
ORDER BY a.name, a.pattern;

-- name: ExistingAliasIDs :many
SELECT id FROM aliases WHERE id = ANY(sqlc.arg(ids)::bigint[]);

-- name: SearchAliases :many
-- R48: aliases whose name or capcode/pattern contains what was typed.
SELECT id, pattern, name FROM aliases
WHERE strpos(lower(name), lower(sqlc.arg(q)::text)) > 0 OR strpos(pattern, sqlc.arg(q)::text) > 0
ORDER BY lower(name), pattern
LIMIT 20;

-- name: ExactAliases :many
-- R48: the aliases of exact capcodes (zero-padded), which select them.
SELECT id, pattern_key FROM aliases WHERE pattern_key = ANY(sqlc.arg(capcodes)::text[]);

-- name: CapcodeAliasCandidates :many
-- R48: every alias covering each capcode, to name the one its pages show.
SELECT c.capcode::text AS capcode, a.id, a.pattern, a.name
FROM unnest(sqlc.arg(capcodes)::text[]) AS c(capcode)
JOIN aliases a ON c.capcode LIKE a.pattern;

-- name: MatchingIntegrations :many
-- Active integrations of active owners whose selection covers a stored page.
SELECT i.id, i.uid, i.kind, i.filter, u.role AS owner_role
FROM integrations i JOIN users u ON u.id = i.owner_id
WHERE i.status = 'active' AND u.status = 'active'
  AND (EXISTS (SELECT 1 FROM integration_capcodes c WHERE c.integration_id = i.id AND c.capcode = sqlc.arg(capcode)::text)
       OR (sqlc.narg(alias_id)::bigint IS NOT NULL
           AND EXISTS (SELECT 1 FROM integration_aliases a WHERE a.integration_id = i.id AND a.alias_id = sqlc.narg(alias_id)::bigint)));

-- name: InsertDelivery :exec
-- R51b: at most one row per page per integration, whatever the code path.
INSERT INTO deliveries (integration_id, message_id, status, next_attempt_at)
VALUES ($1, $2, $3, $4)
ON CONFLICT DO NOTHING;

-- name: DueDeliveries :many
SELECT integration_id, message_id, attempts
FROM deliveries
WHERE status = 'pending' AND next_attempt_at <= sqlc.arg(now)::timestamptz
ORDER BY next_attempt_at, message_id
LIMIT 500;

-- name: NextDeliveryDue :one
SELECT min(next_attempt_at)::timestamptz FROM deliveries WHERE status = 'pending';

-- name: DeliveryByKey :one
SELECT * FROM deliveries WHERE integration_id = $1 AND message_id = $2;

-- name: FinishDelivery :exec
-- A delivery leaves the queue: delivered, failed, too_old or cancelled.
UPDATE deliveries SET status = sqlc.arg(status)::text, attempts = sqlc.arg(attempts)::int,
    next_attempt_at = NULL, last_result = sqlc.narg(last_result), last_error = sqlc.narg(last_error),
    delivered_at = CASE WHEN sqlc.arg(status)::text = 'delivered' THEN sqlc.arg(at)::timestamptz END,
    updated_at = sqlc.arg(at)::timestamptz
WHERE integration_id = sqlc.arg(integration_id) AND message_id = sqlc.arg(message_id) AND status = 'pending';

-- name: RetryDelivery :exec
UPDATE deliveries SET attempts = sqlc.arg(attempts)::int, next_attempt_at = sqlc.arg(next_attempt_at)::timestamptz,
    last_result = sqlc.narg(last_result), last_error = sqlc.narg(last_error), updated_at = sqlc.arg(at)::timestamptz
WHERE integration_id = sqlc.arg(integration_id) AND message_id = sqlc.arg(message_id) AND status = 'pending';

-- name: CancelPendingDeliveries :execrows
UPDATE deliveries SET status = 'cancelled', next_attempt_at = NULL, updated_at = now()
WHERE integration_id = ANY(sqlc.arg(ids)::bigint[]) AND status = 'pending';

-- name: CancelPendingDeliveriesOfOwners :execrows
-- R51: deactivating a user stops their integrations.
UPDATE deliveries d SET status = 'cancelled', next_attempt_at = NULL, updated_at = now()
FROM integrations i
WHERE i.id = d.integration_id AND i.owner_id = ANY(sqlc.arg(owner_ids)::bigint[]) AND d.status = 'pending';

-- name: IntegrationDelivered :exec
UPDATE integrations SET failed_pages = 0, last_delivered_at = sqlc.arg(at)::timestamptz WHERE id = sqlc.arg(id);

-- name: IntegrationPageFailed :one
-- One more page in a row that wasn't delivered (ADR 0006 §3).
UPDATE integrations SET failed_pages = failed_pages + 1, last_error = sqlc.arg(error)::text, last_error_at = sqlc.arg(at)::timestamptz
WHERE id = sqlc.arg(id)
RETURNING failed_pages, status;

-- name: IntegrationAttemptFailed :exec
UPDATE integrations SET last_error = sqlc.arg(error)::text, last_error_at = sqlc.arg(at)::timestamptz WHERE id = sqlc.arg(id);

-- name: PauseIntegration :execrows
UPDATE integrations SET status = 'paused', paused_by = sqlc.arg(paused_by)::text, paused_at = sqlc.arg(at)::timestamptz
WHERE id = sqlc.arg(id) AND status = 'active';

-- name: ResumeIntegration :execrows
UPDATE integrations SET status = 'active', paused_by = NULL, paused_at = NULL, failed_pages = 0
WHERE id = $1 AND status = 'paused';

-- name: RecentDeliveries :many
SELECT d.message_id, d.status, d.attempts, d.last_result, d.last_error, d.delivered_at, d.created_at, d.updated_at,
       m.sent_at, m.capcode
FROM deliveries d JOIN messages m ON m.id = d.message_id
WHERE d.integration_id = $1
ORDER BY d.created_at DESC, d.message_id DESC
LIMIT 100;

-- name: PruneDeliveries :execrows
-- Delivery records are kept for 30 days (ADR 0006 §1).
DELETE FROM deliveries WHERE status <> 'pending' AND created_at < sqlc.arg(before)::timestamptz;

-- Metrics (R52).

-- name: CountFlagged :one
SELECT (SELECT count(*) FROM messages WHERE parse_failed)::bigint AS parse_failed,
       (SELECT count(*) FROM messages WHERE address_unsplit)::bigint AS address_unsplit,
       (SELECT count(*) FROM messages WHERE location_mismatch)::bigint AS location_mismatch;

-- name: CountIntegrationsByStatus :many
SELECT status, count(*) AS n FROM integrations GROUP BY status;

-- Aliases in the browser: history and requests (R53-R58, ADR 0007).

-- name: AliasForUpdate :one
-- Locks an alias for a change, with its template's name for history.
SELECT a.id, a.pattern, a.pattern_key, a.name, a.template_id, a.ignored, t.name AS template_name
FROM aliases a
LEFT JOIN templates t ON t.id = a.template_id
WHERE a.id = $1
FOR UPDATE OF a;

-- name: AliasIDByKey :one
SELECT id FROM aliases WHERE pattern_key = sqlc.arg(pattern_key)::text;

-- name: SetAlias :exec
UPDATE aliases SET pattern = $2, name = $3, template_id = $4, ignored = $5 WHERE id = $1;

-- name: TemplateByID :one
SELECT id, name, agency, icon, colour, agency_profile FROM templates WHERE id = $1;

-- name: InsertAliasChange :one
INSERT INTO alias_changes (alias_id, pattern_key, alias_name, action, changes, source, made_by, requested_by, request_id)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)
RETURNING id;

-- name: LatestAliasChange :one
SELECT coalesce(max(id), 0)::bigint FROM alias_changes WHERE alias_id = $1;

-- name: AliasHistory :many
-- R57: an alias's changes, and those of deleted aliases that had its
-- capcode or pattern (owner, 2026-10-16), newest first. For a capcode with
-- no alias, alias_id is NULL. Admins only (owner, 2026-10-16).
SELECT c.id, c.alias_id, c.pattern_key, c.alias_name, c.action, c.changes, c.source, c.at, c.request_id,
       m.name AS made_by_name, m.email AS made_by_email, m.status AS made_by_status,
       r.name AS requested_by_name, r.email AS requested_by_email, r.status AS requested_by_status
FROM alias_changes c
LEFT JOIN users m ON m.id = c.made_by
LEFT JOIN users r ON r.id = c.requested_by
WHERE c.alias_id = sqlc.narg(alias_id)::bigint
   OR c.alias_id IN (SELECT d.alias_id FROM alias_changes d
                     WHERE d.action = 'deleted' AND d.pattern_key = sqlc.arg(pattern_key)::text)
ORDER BY c.id DESC
LIMIT 200;

-- name: CloseRequestsOfAlias :execrows
-- R55: deleting an alias closes its waiting requests.
UPDATE alias_requests SET status = 'alias_deleted', closed_at = now(), updated_at = now()
WHERE alias_id = $1 AND status = 'waiting';

-- name: CountIntegrationsSelectingAlias :one
SELECT count(*) FROM integration_aliases WHERE alias_id = $1;

-- name: SelectCapcodeInsteadOfAlias :exec
-- Q4 (owner, 2026-10-16): integrations that selected a deleted one-capcode
-- alias select its capcode instead. The alias's rows then go by cascade.
INSERT INTO integration_capcodes (integration_id, capcode)
SELECT integration_id, sqlc.arg(capcode)::text FROM integration_aliases WHERE alias_id = sqlc.arg(alias_id)
ON CONFLICT DO NOTHING;

-- name: CreateAliasRequest :one
INSERT INTO alias_requests (kind, requested_by, alias_id, pattern, alias_name, name, set_template, template_id, template_name, ignored, note, base_change)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12)
RETURNING id;

-- name: EditAliasRequest :execrows
UPDATE alias_requests
SET name = $2, set_template = $3, template_id = $4, template_name = $5, ignored = $6, note = $7, base_change = $8, updated_at = now()
WHERE id = $1 AND status = 'waiting';

-- name: AliasRequestForUpdate :one
SELECT * FROM alias_requests WHERE id = $1 FOR UPDATE;

-- name: CloseAliasRequest :exec
UPDATE alias_requests
SET status = sqlc.arg(status)::text, reviewed_by = sqlc.narg(reviewed_by)::bigint, feedback = sqlc.narg(feedback)::text,
    alias_id = coalesce(sqlc.narg(alias_id)::bigint, alias_id), closed_at = now(), updated_at = now()
WHERE id = sqlc.arg(id) AND status = 'waiting';

-- name: WaitingRequestOf :one
-- A user's other waiting request for the same alias, or for the same
-- capcode or pattern if it asks for a new one (ADR 0007 §2).
SELECT id FROM alias_requests
WHERE requested_by = sqlc.arg(requested_by) AND status = 'waiting' AND id <> sqlc.arg(not_id)::bigint
  AND ((sqlc.narg(alias_id)::bigint IS NOT NULL AND alias_id = sqlc.narg(alias_id)::bigint)
       OR (kind = 'add' AND sqlc.narg(alias_id)::bigint IS NULL AND pattern = sqlc.arg(pattern)::text))
LIMIT 1;

-- name: CountWaitingRequestsOf :one
SELECT count(*) FROM alias_requests WHERE requested_by = $1 AND status = 'waiting';

-- name: CountWaitingRequests :one
SELECT count(*) FROM alias_requests WHERE status = 'waiting';

-- name: CountWaitingRequestsForTemplate :one
SELECT count(*) FROM alias_requests WHERE status = 'waiting' AND set_template AND template_id = $1;

-- name: WithdrawRequestsOf :execrows
-- R56: deactivating or deleting a user withdraws their waiting requests.
UPDATE alias_requests SET status = 'withdrawn', closed_at = now(), updated_at = now()
WHERE requested_by = ANY(sqlc.arg(user_ids)::bigint[]) AND status = 'waiting';

-- name: ClearRequestNotesOf :exec
-- R43c: a deleted account's notes go; its requests stay, as "deleted user".
UPDATE alias_requests SET note = NULL WHERE requested_by = $1;

-- name: ListAliasRequests :many
-- Requests with the people involved, the alias as it is now, and what's
-- needed for the marks in ADR 0007 §3. Newest first.
SELECT r.id, r.kind, r.requested_by, r.alias_id, r.pattern, r.alias_name, r.name, r.set_template, r.template_id,
       r.template_name, r.ignored, r.note, r.base_change, r.status, r.feedback, r.created_at, r.updated_at, r.closed_at,
       u.name AS requester_name, u.email AS requester_email, u.status AS requester_status,
       v.name AS reviewer_name, v.email AS reviewer_email, v.status AS reviewer_status,
       a.pattern AS cur_pattern, a.name AS cur_name, a.ignored AS cur_ignored, a.template_id AS cur_template_id,
       ct.name AS cur_template_name,
       (SELECT coalesce(max(c.id), 0) FROM alias_changes c WHERE c.alias_id = r.alias_id)::bigint AS latest_change,
       occ.id AS occupant_id, occ.name AS occupant_name
FROM alias_requests r
JOIN users u ON u.id = r.requested_by
LEFT JOIN users v ON v.id = r.reviewed_by
LEFT JOIN aliases a ON a.id = r.alias_id
LEFT JOIN templates ct ON ct.id = a.template_id
LEFT JOIN aliases occ ON r.kind = 'add' AND r.status = 'waiting' AND occ.pattern_key = r.pattern
WHERE (sqlc.narg(requested_by)::bigint IS NULL OR r.requested_by = sqlc.narg(requested_by)::bigint)
  AND (sqlc.narg(id)::bigint IS NULL OR r.id = sqlc.narg(id)::bigint)
  AND (sqlc.narg(alias_id)::bigint IS NULL OR r.alias_id = sqlc.narg(alias_id)::bigint)
  AND (sqlc.arg(which)::text = 'all' OR (sqlc.arg(which)::text = 'waiting') = (r.status = 'waiting'))
ORDER BY r.id DESC
LIMIT sqlc.arg(max_rows)::int;

-- name: AdminAliases :many
-- R53: aliases for Administration, filtered by name, capcode or pattern.
SELECT a.id, a.pattern, a.name, a.ignored, a.template_id, t.name AS template_name,
       (SELECT count(*) FROM alias_requests r WHERE r.alias_id = a.id AND r.status = 'waiting')::bigint AS waiting
FROM aliases a
LEFT JOIN templates t ON t.id = a.template_id
WHERE sqlc.arg(q)::text = '' OR strpos(lower(a.name), lower(sqlc.arg(q)::text)) > 0 OR strpos(a.pattern, sqlc.arg(q)::text) > 0
ORDER BY a.pattern
LIMIT sqlc.arg(max_rows)::int;

-- name: ExportAliases :many
-- R58: every alias in the import format.
SELECT a.id, a.pattern, a.name, a.ignored, t.name AS template_name, t.icon, t.colour
FROM aliases a
LEFT JOIN templates t ON t.id = a.template_id
ORDER BY a.pattern;
