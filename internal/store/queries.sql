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
JOIN templates t ON t.id = a.template_id
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
JOIN templates t ON t.id = a.template_id
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
JOIN templates t ON t.id = a.template_id;

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

-- name: GetSetting :one
SELECT value FROM settings WHERE key = $1;

-- name: SetSetting :exec
INSERT INTO settings (key, value) VALUES ($1, $2)
ON CONFLICT (key) DO UPDATE SET value = EXCLUDED.value;

-- name: SearchMessages :many
-- R33. Every filter is optional (NULL = any). Keyset pagination on
-- (sent_at, id), newest first. cat_want compares "event_type is in the
-- category's codes" with true (that category) or false (Other).
SELECT m.id, m.capcode, m.text, m.priority, m.heartbeat, m.sent_at, m.format,
       m.job_number, m.event_type, m.description, m.address, m.locality, m.grid_ref,
       m.paging_group, m.units, m.extra, m.parse_failed, m.address_unsplit,
       m.location_mismatch, a.name AS alias_name, COALESCE(a.ignored, false)::bool AS alias_ignored,
       t.agency, t.icon, t.colour, t.agency_profile
FROM messages m
LEFT JOIN aliases a ON a.id = m.alias_id
LEFT JOIN templates t ON t.id = a.template_id
WHERE (sqlc.narg(only_id)::bigint IS NULL OR m.id = sqlc.narg(only_id))
  AND (sqlc.narg(q)::text IS NULL OR m.text ILIKE sqlc.narg(q)
       OR EXISTS (SELECT 1 FROM jsonb_each_text(m.extra) e WHERE e.value ILIKE sqlc.narg(q)))
  AND (sqlc.narg(capcode)::text IS NULL OR m.capcode = sqlc.narg(capcode))
  AND (sqlc.narg(alias)::text IS NULL OR a.name ILIKE sqlc.narg(alias))
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
  AND (sqlc.narg(before_sent)::timestamptz IS NULL
       OR (m.sent_at, m.id) < (sqlc.narg(before_sent), sqlc.narg(before_id)::bigint))
ORDER BY m.sent_at DESC, m.id DESC
LIMIT sqlc.arg(max_rows)::int;

-- name: UnaliasedCapcodes :many
-- R33: capcodes with no alias, with message count and last-seen time.
SELECT capcode, count(*) AS messages, max(sent_at)::timestamptz AS last_seen
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
