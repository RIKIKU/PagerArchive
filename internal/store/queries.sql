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
INSERT INTO aliases (pattern, name, template_id) VALUES ($1, $2, $3) RETURNING id;

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
    paging_group, units, extra, parse_failed, address_unsplit, function, baud
) VALUES (
    $1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16, $17, $18, $19, $20
)
RETURNING id;

-- name: KeepEarliestSentAt :exec
UPDATE messages SET sent_at = LEAST(sent_at, sqlc.arg(received_at)::timestamptz) WHERE id = sqlc.arg(id);

-- name: InsertReception :exec
INSERT INTO receptions (message_id, client_id, received_at)
VALUES ($1, $2, $3)
ON CONFLICT DO NOTHING;
