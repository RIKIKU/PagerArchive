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
INSERT INTO templates (name, agency, colour, agency_profile)
VALUES ($1, $2, $3, $4)
RETURNING id;

-- name: CreateAlias :one
INSERT INTO aliases (pattern, name, template_id, ignored) VALUES ($1, $2, $3, $4) RETURNING id;

-- name: AliasesMatching :many
SELECT a.id, a.pattern, a.name, a.ignored, t.agency_profile
FROM aliases a
LEFT JOIN templates t ON t.id = a.template_id
WHERE sqlc.arg(capcode)::text LIKE a.pattern;

-- name: AliasIDsNamed :many
-- The aliases a search's alias: values name (SearchMessages' aliases and
-- alias_likes), so the search can use the index on messages.alias_id.
SELECT id FROM aliases
WHERE (cardinality(sqlc.arg(aliases)::text[]) = 0 OR name ILIKE ANY(sqlc.arg(aliases)::text[]))
  AND (cardinality(sqlc.arg(alias_likes)::text[]) = 0 OR name ILIKE ANY(sqlc.arg(alias_likes)::text[]));

-- name: LockCapcode :exec
-- R19, R19a: copies of pages to one capcode wait for each other, so two
-- copies of one page, exact or near, can't both be stored as messages.
SELECT pg_advisory_xact_lock(hashtextextended('dedup ' || sqlc.arg(capcode)::text, 0));

-- name: NearCandidates :many
-- R19a: the messages to a capcode sent within 10 s of a copy's receive
-- time, either side, nearest first, with what a near duplicate's choice and
-- the swap need (ADR 0050). heard says the copy's client has a copy of it
-- already ('' for an import: never).
SELECT m.id, m.text, m.priority, m.heartbeat, m.sent_at, m.job_number,
       EXISTS (SELECT 1 FROM large_event_messages lem WHERE lem.message_id = m.id) AS in_event,
       EXISTS (SELECT 1 FROM receptions r WHERE r.message_id = m.id AND r.client_id = sqlc.arg(client_id)::text) AS heard
FROM messages m
WHERE m.capcode = sqlc.arg(capcode)::text
  AND m.sent_at BETWEEN sqlc.arg(received_at)::timestamptz - interval '10 seconds'
                    AND sqlc.arg(received_at)::timestamptz + interval '10 seconds'
ORDER BY abs(extract(epoch FROM m.sent_at - sqlc.arg(received_at)::timestamptz)), m.id
LIMIT 20;

-- name: SwapMessageText :exec
-- R19a (ADR 0050): a better copy of a page came in after the first was
-- stored: the message takes its text (redacted, P1), dedup hash, priority
-- and what it parsed to, and its sent time stays the earliest (R21). Where
-- it's placed is written after, as a re-parse writes it.
UPDATE messages SET
    text = sqlc.arg(text), dedup_key = sqlc.arg(dedup_key), priority = sqlc.narg(priority),
    alias_id = sqlc.narg(alias_id), format = sqlc.narg(format), job_number = sqlc.narg(job_number),
    event_type = sqlc.narg(event_type), description = sqlc.narg(description), address = sqlc.narg(address),
    locality = sqlc.narg(locality), grid_ref = sqlc.narg(grid_ref), call_sign = sqlc.narg(call_sign),
    units = sqlc.arg(units), call_signs = sqlc.arg(call_signs), extra = sqlc.arg(extra),
    parse_failed = sqlc.arg(parse_failed), address_unsplit = sqlc.arg(address_unsplit),
    large_event_field = sqlc.arg(large_event_field),
    sent_at = LEAST(sent_at, sqlc.arg(received_at)::timestamptz)
WHERE id = sqlc.arg(id);

-- name: SwapReceptionTexts :many
-- R19a: when a message takes a better copy's text, the copies that had its
-- old text keep that as their own, and a copy whose text is the new one's
-- needs none. variant says which a row became.
UPDATE receptions SET text = CASE WHEN text IS NULL THEN sqlc.arg(old_text)::text END
WHERE message_id = sqlc.arg(id) AND (text IS NULL OR text = sqlc.arg(new_text)::text)
RETURNING client_id, (text IS NOT NULL)::boolean AS variant;

-- name: SwapImportReceptionTexts :exec
-- As SwapReceptionTexts, for the copies from import files (R83a).
UPDATE import_receptions SET text = CASE WHEN text IS NULL THEN sqlc.arg(old_text)::text END
WHERE message_id = sqlc.arg(id) AND (text IS NULL OR text = sqlc.arg(new_text)::text);

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
    call_sign, units, call_signs, extra, parse_failed, address_unsplit, function, baud, alias_id, large_event_field,
    location, location_source, location_mismatch
) VALUES (
    $1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16, $17, $18, $19, $20, $21, $22, $23,
    CASE WHEN sqlc.narg(lon)::float8 IS NULL THEN NULL
         ELSE ST_SetSRID(ST_MakePoint(sqlc.narg(lon)::float8, sqlc.narg(lat)::float8), 4326)::geography END,
    sqlc.narg(location_source)::text,
    sqlc.arg(location_mismatch)::bool
)
RETURNING id;

-- name: KeepEarliestSentAt :exec
UPDATE messages SET sent_at = LEAST(sent_at, sqlc.arg(received_at)::timestamptz) WHERE id = sqlc.arg(id);

-- name: InsertReception :exec
-- text is the copy's own, redacted (P1), when it's a near duplicate whose
-- text isn't its message's (R19a); NULL otherwise.
INSERT INTO receptions (message_id, client_id, received_at, text)
VALUES ($1, $2, $3, sqlc.narg(text))
ON CONFLICT DO NOTHING;

-- name: ListTemplates :many
SELECT t.id, t.name, t.agency, t.icon_sha256, t.colour, t.agency_profile,
       (SELECT count(*) FROM aliases a WHERE a.template_id = t.id) AS alias_count
FROM templates t
ORDER BY t.name;

-- name: TemplateByName :one
SELECT id, name, agency, icon_sha256, colour, agency_profile FROM templates WHERE name = $1;

-- name: TemplatesNamedIgnoringCase :many
-- R64: templates whose names differ from $1 only in letter case, or match it.
SELECT id, name FROM templates WHERE lower(name) = lower($1) ORDER BY id;

-- name: CaseDuplicateTemplates :many
-- R64: templates whose names differ only in letter case from another's, from
-- before names were unique ignoring case (migration 00022), in groups.
SELECT id, name, lower(name) AS name_key FROM templates t
WHERE EXISTS (SELECT 1 FROM templates o WHERE o.id <> t.id AND lower(o.name) = lower(t.name))
ORDER BY lower(name), id;

-- name: SettleCaseDuplicates :exec
-- R64: after a template is renamed or deleted, a group left with no template
-- under the unique index (all marked case_duplicate) puts its oldest back
-- under it.
UPDATE templates t SET case_duplicate = false
WHERE t.case_duplicate
  AND t.id = (SELECT min(o.id) FROM templates o WHERE lower(o.name) = lower(t.name))
  AND NOT EXISTS (SELECT 1 FROM templates o WHERE lower(o.name) = lower(t.name) AND NOT o.case_duplicate);

-- name: UpdateTemplate :exec
-- A renamed template is no longer a case duplicate (R64, migration 00022).
UPDATE templates SET name = $2, agency = $3, colour = $4, agency_profile = $5,
    case_duplicate = case_duplicate AND name = $2
WHERE id = $1;

-- name: SetTemplateIcon :execrows
-- R65: a 64×64 PNG the API encoded and its SHA-256, or NULL for none.
UPDATE templates SET icon_png = sqlc.narg(png), icon_sha256 = sqlc.narg(sha256) WHERE id = sqlc.arg(id);

-- name: IconBySHA256 :one
SELECT icon_png FROM templates WHERE icon_sha256 = $1 LIMIT 1;

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

-- name: CountMessagesToReparse :one
-- R23b: how many messages a re-parse of one pattern has to do, counted once
-- when it starts, for its progress.
SELECT count(*) FROM messages WHERE capcode LIKE sqlc.arg(pattern)::text;

-- name: MessagesToReparse :many
-- R23a. in_event and live tell the re-parse which pages large events are
-- rebuilt around (ADR 0027 §9): a page in an event, or a live page (one a
-- client heard, not only an import: R83b) that may now count.
SELECT m.id, m.capcode, m.text, m.priority, m.heartbeat, m.sent_at,
       EXISTS (SELECT 1 FROM large_event_messages lem WHERE lem.message_id = m.id) AS in_event,
       EXISTS (SELECT 1 FROM receptions r WHERE r.message_id = m.id) AS live
FROM messages m
WHERE m.capcode LIKE sqlc.arg(pattern)::text AND m.id > sqlc.arg(after_id)::bigint
ORDER BY m.id
LIMIT sqlc.arg(batch)::int;

-- name: GetReparseRun :one
-- R23c (ADR 0043): the re-parse's run, the latest; there's at most one.
SELECT id, state, reasons, requests, total, done, epoch, started_at, changed_at, finished_at FROM reparse_runs;

-- name: LockReparseRun :one
-- As GetReparseRun, locked until the transaction ends: a batch's end, an
-- admin's change or the start-up check, one at a time.
SELECT id, state, reasons, requests, total, done, epoch, started_at, changed_at, finished_at FROM reparse_runs FOR UPDATE;

-- name: DeleteReparseRuns :exec
-- Before a new run: the last one, with its patterns and days, goes.
DELETE FROM reparse_runs;

-- name: CreateReparseRun :one
INSERT INTO reparse_runs (state, reasons, requests, total) VALUES ('running', $1, $2, $3) RETURNING id;

-- name: RestartReparseRun :exec
-- From the beginning again (Start again, or the profiles changed): its
-- patterns are put back by the caller, and its days go.
UPDATE reparse_runs SET state = sqlc.arg(state)::text, reasons = sqlc.arg(reasons)::text, requests = sqlc.arg(requests)::int,
    total = sqlc.arg(total)::bigint, done = 0, epoch = epoch + 1, started_at = now(), changed_at = now(), finished_at = NULL
WHERE id = sqlc.arg(id)::bigint;

-- name: SetReparseRunState :exec
UPDATE reparse_runs SET state = sqlc.arg(state)::text, changed_at = now() WHERE id = sqlc.arg(id)::bigint;

-- name: FinishReparseRun :exec
-- Pages stored while it ran are re-parsed by it too, so its total is then
-- what it did (R23b).
UPDATE reparse_runs SET state = 'finished', changed_at = now(), finished_at = now(), total = GREATEST(total, done)
WHERE id = $1;

-- name: AddReparseRunDone :exec
UPDATE reparse_runs SET done = done + sqlc.arg(n)::bigint WHERE id = sqlc.arg(id)::bigint;

-- name: DeleteReparseRunPatterns :exec
DELETE FROM reparse_run_patterns WHERE run_id = $1;

-- name: AddReparseRunPattern :exec
INSERT INTO reparse_run_patterns (run_id, capcode_pattern) VALUES ($1, $2) ON CONFLICT DO NOTHING;

-- name: ReparseRunPatterns :many
SELECT capcode_pattern, after_id, done FROM reparse_run_patterns WHERE run_id = $1 ORDER BY capcode_pattern;

-- name: SetReparseRunPattern :exec
-- Where a pattern's walk has got: written with the batch it ends.
UPDATE reparse_run_patterns SET after_id = sqlc.arg(after_id)::bigint, done = sqlc.arg(done)::bool
WHERE run_id = sqlc.arg(run_id)::bigint AND capcode_pattern = sqlc.arg(pattern)::text;

-- name: AddReparseRunDay :exec
INSERT INTO reparse_run_days (run_id, day, from_at, to_at) VALUES ($1, $2, $3, $4)
ON CONFLICT (run_id, day) DO UPDATE SET from_at = LEAST(reparse_run_days.from_at, EXCLUDED.from_at),
    to_at = GREATEST(reparse_run_days.to_at, EXCLUDED.to_at);

-- name: ReparseRunDays :many
SELECT from_at, to_at FROM reparse_run_days WHERE run_id = $1 ORDER BY day;

-- name: DeleteReparseRunDays :exec
DELETE FROM reparse_run_days WHERE run_id = $1;

-- name: CountReparseRequests :one
-- Requests waiting for the next run (R23c).
SELECT count(*) FROM reparse_requests;

-- name: NotifyReparse :exec
-- Wakes the API's re-parse worker (R23a).
SELECT pg_notify('pagerarchive_reparse', '');

-- name: UpdateParsed :execrows
-- A page's parsed fields, written only beside the text they were parsed
-- from (P1): 0 rows when its text changed meanwhile (retroactive redaction,
-- in its own transaction), and the caller parses it again.
UPDATE messages SET
    alias_id = $2, format = $3, job_number = $4, event_type = $5, description = $6,
    address = $7, locality = $8, grid_ref = $9, call_sign = $10, units = $11,
    extra = $12, parse_failed = $13, address_unsplit = $14, large_event_field = $15, call_signs = $17
WHERE id = $1 AND text = $16;

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

-- name: SetMessageLocality :exec
-- R24, ADR 0034: a page's derived locality, for a page whose profile gives
-- none. Unchanged rows aren't written, so its job isn't counted again.
INSERT INTO message_localities (message_id, locality) VALUES (sqlc.arg(id)::bigint, sqlc.arg(locality)::text)
ON CONFLICT (message_id) DO UPDATE SET locality = EXCLUDED.locality
WHERE message_localities.locality IS DISTINCT FROM EXCLUDED.locality;

-- name: DeleteMessageLocality :exec
DELETE FROM message_localities WHERE message_id = $1;

-- name: SetMessageArea :exec
-- R88a, ADR 0035: the area of each layer a page is in, from where it's
-- placed now; a layer it's in none of (no location, or in no area) loses its
-- row. Unchanged rows aren't written.
WITH found AS (
    SELECT f.layer, f.area FROM (
        SELECT l.layer, area_at(l.layer, m.location) AS area
        FROM messages m CROSS JOIN (SELECT DISTINCT layer FROM areas) l WHERE m.id = sqlc.arg(id)::bigint
    ) f WHERE f.area IS NOT NULL
),
gone AS (
    DELETE FROM message_areas ma
    WHERE ma.message_id = sqlc.arg(id)::bigint AND NOT EXISTS (SELECT 1 FROM found WHERE found.layer = ma.layer)
)
INSERT INTO message_areas (message_id, layer, area)
SELECT sqlc.arg(id)::bigint, found.layer, found.area FROM found
ON CONFLICT (message_id, layer) DO UPDATE SET area = EXCLUDED.area
WHERE message_areas.area IS DISTINCT FROM EXCLUDED.area;

-- name: MessageAreas :many
-- R88b: the areas of the messages a page of results shows, each layer's.
SELECT message_id, layer, area FROM message_areas WHERE message_id = ANY(sqlc.arg(ids)::bigint[]);

-- name: JobAreas :many
-- R88b: the areas a job's pages are in, each layer's, by name.
SELECT DISTINCT ma.layer, ma.area FROM messages m JOIN message_areas ma ON ma.message_id = m.id
WHERE m.job_number = sqlc.arg(job)::text
ORDER BY ma.layer, ma.area;

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
-- R33. Every filter is optional (NULL or an empty array = any). An array
-- matches any of its values (R63a). Keyset pagination on (sent_at, id),
-- newest first. cat_filter keeps messages whose event_type is in the chosen
-- categories' codes, or (cat_other) in no category's codes. only_ids, when
-- not empty, keeps just those messages (a live update, a catch-up batch).
SELECT m.id, m.capcode, m.alias_id, m.text, m.priority, m.heartbeat, m.sent_at, m.format,
       m.job_number, m.event_type, m.description, m.address, m.locality, m.grid_ref,
       m.call_sign, m.units, m.call_signs, m.extra, m.parse_failed, m.address_unsplit,
       m.location_mismatch, a.name AS alias_name, COALESCE(a.ignored, false)::bool AS alias_ignored,
       t.agency, t.icon_sha256, t.colour, t.agency_profile,
       m.lon, m.lat, m.location_source
FROM messages m
LEFT JOIN aliases a ON a.id = m.alias_id
LEFT JOIN templates t ON t.id = a.template_id
WHERE (cardinality(sqlc.arg(only_ids)::bigint[]) = 0 OR m.id = ANY(sqlc.arg(only_ids)::bigint[]))
  -- R33a's text terms, each an ILIKE pattern found in the text or an extra
  -- field: every one of q_all, or any one of q_any (the or: terms). The
  -- trigram index on message_search_text() finds the candidates (R33b, ADR
  -- 0017): q_index1 to 4 are up to four of q_all it can serve, one condition
  -- each, and with q_any_index it serves the or: terms. Each term is then
  -- matched in the text or one extra field, as before. When a term is too
  -- short for a trigram, q_short are the terms the index on
  -- message_search_bigrams() serves instead, and q_index1 to 4 are unset
  -- (ADR 0017).
  AND ((cardinality(sqlc.arg(q_all)::text[]) = 0 AND cardinality(sqlc.arg(q_any)::text[]) = 0)
       OR (cardinality(sqlc.arg(q_all)::text[]) > 0
           AND (cardinality(sqlc.arg(q_short)::text[]) = 0
                OR message_search_bigrams(m.text, m.extra) @> search_terms_bigrams(sqlc.arg(q_short)::text[]))
           AND (sqlc.narg(q_index1)::text IS NULL OR message_search_text(m.text, m.extra) ILIKE sqlc.narg(q_index1)::text)
           AND (sqlc.narg(q_index2)::text IS NULL OR message_search_text(m.text, m.extra) ILIKE sqlc.narg(q_index2)::text)
           AND (sqlc.narg(q_index3)::text IS NULL OR message_search_text(m.text, m.extra) ILIKE sqlc.narg(q_index3)::text)
           AND (sqlc.narg(q_index4)::text IS NULL OR message_search_text(m.text, m.extra) ILIKE sqlc.narg(q_index4)::text)
           AND NOT EXISTS (SELECT 1 FROM unnest(sqlc.arg(q_all)::text[]) AS term (p)
                           WHERE NOT (m.text ILIKE term.p
                                      OR EXISTS (SELECT 1 FROM jsonb_each_text(m.extra) e WHERE e.value ILIKE term.p))))
       OR (cardinality(sqlc.arg(q_any)::text[]) > 0
           AND (NOT sqlc.arg(q_any_index)::bool OR message_search_text(m.text, m.extra) ILIKE ANY(sqlc.arg(q_any)::text[]))
           AND EXISTS (SELECT 1 FROM unnest(sqlc.arg(q_any)::text[]) AS term (p)
                       WHERE m.text ILIKE term.p
                          OR EXISTS (SELECT 1 FROM jsonb_each_text(m.extra) e WHERE e.value ILIKE term.p))))
  AND (cardinality(sqlc.arg(capcodes)::text[]) = 0 OR m.capcode = ANY(sqlc.arg(capcodes)::text[]))
  -- alias_names_by_id: the aliases those names match were looked up in this
  -- query's own snapshot (alias_name_ids), so they stand for the names. Then
  -- the planner can't start from the aliases the names match, guessing each
  -- holds few pages, and read every page of all of them (R33b, ADR 0017).
  AND (sqlc.arg(alias_names_by_id)::bool OR cardinality(sqlc.arg(aliases)::text[]) = 0 OR a.name ILIKE ANY(sqlc.arg(aliases)::text[]))
  AND (sqlc.arg(alias_names_by_id)::bool OR cardinality(sqlc.arg(alias_likes)::text[]) = 0 OR a.name ILIKE ANY(sqlc.arg(alias_likes)::text[]))
  -- The aliases those names match, looked up first (AliasIDsNamed; R33b,
  -- ADR 0017), so the planner can find their pages by alias_id.
  AND (NOT sqlc.arg(alias_names_resolved)::bool OR m.alias_id = ANY(sqlc.arg(alias_name_ids)::bigint[]))
  -- The one alias they name, or the one alias_ids holds, so the planner can
  -- read its newest pages in order (messages_alias_sent_idx).
  AND (sqlc.narg(only_alias_id)::bigint IS NULL OR m.alias_id = sqlc.narg(only_alias_id)::bigint)
  AND (cardinality(sqlc.arg(alias_ids)::bigint[]) = 0 OR m.alias_id = ANY(sqlc.arg(alias_ids)::bigint[]))
  AND (cardinality(sqlc.arg(agencies)::text[]) = 0 OR t.agency ILIKE ANY(sqlc.arg(agencies)::text[]))
  AND (cardinality(sqlc.arg(agency_likes)::text[]) = 0 OR t.agency ILIKE ANY(sqlc.arg(agency_likes)::text[]))
  AND (cardinality(sqlc.arg(call_signs)::text[]) = 0 OR upper(m.call_sign) = ANY(sqlc.arg(call_signs)::text[]))
  AND (cardinality(sqlc.arg(call_sign_likes)::text[]) = 0 OR m.call_sign ILIKE ANY(sqlc.arg(call_sign_likes)::text[]))
  AND (cardinality(sqlc.arg(priorities)::text[]) = 0
       OR ('none' = ANY(sqlc.arg(priorities)::text[]) AND m.priority IS NULL)
       OR m.priority = ANY(sqlc.arg(priorities)::text[]))
  AND (NOT sqlc.arg(cat_filter)::bool
       OR COALESCE(m.event_type = ANY(sqlc.arg(cat_codes)::text[])
                   OR m.event_type LIKE ANY(sqlc.arg(cat_prefixes)::text[]), false)
       OR (sqlc.arg(cat_other)::bool
           AND NOT COALESCE(m.event_type = ANY(sqlc.arg(all_cat_codes)::text[])
                            OR m.event_type LIKE ANY(sqlc.arg(all_cat_prefixes)::text[]), false)))
  AND (cardinality(sqlc.arg(job_numbers)::text[]) = 0 OR m.job_number = ANY(sqlc.arg(job_numbers)::text[]))
  AND (sqlc.narg(sent_from)::timestamptz IS NULL OR m.sent_at >= sqlc.narg(sent_from))
  AND (sqlc.narg(sent_to)::timestamptz IS NULL OR m.sent_at < sqlc.narg(sent_to))
  AND (NOT sqlc.arg(unaliased)::bool OR m.alias_id IS NULL)
  AND (sqlc.arg(include_ignored)::bool OR a.ignored IS NOT TRUE)
  AND (NOT sqlc.arg(parse_failed)::bool OR m.parse_failed)
  AND (NOT sqlc.arg(address_unsplit)::bool OR m.address_unsplit)
  AND (NOT sqlc.arg(location_mismatch)::bool OR m.location_mismatch)
  AND (cardinality(sqlc.arg(flags)::text[]) = 0
       OR ('parse_failed' = ANY(sqlc.arg(flags)::text[]) AND m.parse_failed)
       OR ('address_unsplit' = ANY(sqlc.arg(flags)::text[]) AND m.address_unsplit)
       OR ('location_mismatch' = ANY(sqlc.arg(flags)::text[]) AND m.location_mismatch))
  AND (NOT sqlc.arg(located)::bool OR m.location IS NOT NULL)
  -- R82: one album's messages. Only the album's owner is given it (ADR 0026).
  AND (sqlc.narg(album_id)::bigint IS NULL
       OR EXISTS (SELECT 1 FROM album_messages am WHERE am.album_id = sqlc.narg(album_id)::bigint AND am.message_id = m.id))
  -- R87c: one large event's pages (ADR 0027).
  AND (sqlc.narg(event_id)::bigint IS NULL
       OR EXISTS (SELECT 1 FROM large_event_messages lem WHERE lem.event_id = sqlc.narg(event_id)::bigint AND lem.message_id = m.id))
  AND (sqlc.narg(before_sent)::timestamptz IS NULL
       OR (m.sent_at, m.id) < (sqlc.narg(before_sent), sqlc.narg(before_id)::bigint))
  -- R81b: after a jump, the messages newer than the list's first, which
  -- Store.SearchNewer reads oldest first (ADR 0026).
  AND (sqlc.narg(after_sent)::timestamptz IS NULL
       OR (m.sent_at, m.id) > (sqlc.narg(after_sent), sqlc.narg(after_id)::bigint))
ORDER BY m.sent_at DESC, m.id DESC
LIMIT sqlc.arg(max_rows)::int;

-- name: LastMessageID :one
-- Where a live connection without after_id starts (ADR 0010 §2): the newest
-- message a client sent. An imported page isn't a live update (R83b, ADR 0018).
SELECT COALESCE(max(message_id), 0)::bigint FROM receptions;

-- name: MessageIDsAfter :many
-- A live connection's catch-up (ADR 0010 §3): messages clients sent after one,
-- oldest first. IDs are assigned in the order pages are stored. An imported
-- page has no reception until a client also hears it (R83b, ADR 0018).
SELECT DISTINCT message_id FROM receptions WHERE message_id > sqlc.arg(id) ORDER BY message_id LIMIT sqlc.arg(max_rows)::int;

-- name: InsertImportReception :exec
-- R83b: a copy read from an import file. Running a file again adds nothing.
INSERT INTO import_receptions (message_id, received_at, source_id, text) VALUES ($1, $2, $3, sqlc.narg(text))
ON CONFLICT DO NOTHING;

-- name: ImportedSourceIDs :many
-- R83c: which of these source IDs an earlier import stored.
SELECT source_id::text FROM import_receptions WHERE source_id = ANY(sqlc.arg(ids)::text[]);

-- name: ImportSourceIDsAfter :many
-- P1: the source IDs, in turn, for the nightly run to check against changed rules.
SELECT i.source_id::text FROM import_receptions i WHERE i.source_id > sqlc.narg(after)::text ORDER BY i.source_id LIMIT sqlc.arg(batch)::int;

-- name: ForgetImportSourceID :exec
-- P1: a source ID the rules now match. The copy stays, with no ID.
WITH gone AS (
    DELETE FROM import_receptions i WHERE i.source_id = sqlc.arg(source_id)::text RETURNING i.message_id, i.received_at
)
INSERT INTO import_receptions (message_id, received_at)
SELECT message_id, received_at FROM gone
ON CONFLICT DO NOTHING;

-- name: ImportedCopyExists :one
-- R83c: whether an earlier import stored this copy of a page with no source
-- ID, by its dedup key and receive time. A message's sent time is its earliest
-- copy's (R21), and copies are seconds apart (R19): a day bounds the search.
-- A near duplicate (R19a) is a copy kept with its own text: it was merged
-- within seconds of its message's sent time, so a minute bounds that search.
SELECT (EXISTS (
    SELECT 1
    FROM messages m
    JOIN import_receptions i ON i.message_id = m.id
    WHERE m.dedup_key = sqlc.arg(dedup_key)
      AND m.sent_at BETWEEN sqlc.arg(received_at)::timestamptz - interval '1 day' AND sqlc.arg(received_at)::timestamptz
      AND i.received_at = sqlc.arg(received_at)::timestamptz
      AND i.source_id IS NULL
      AND i.text IS NULL
) OR EXISTS (
    SELECT 1
    FROM messages m
    JOIN import_receptions i ON i.message_id = m.id
    WHERE m.capcode = sqlc.arg(capcode)::text
      AND m.sent_at BETWEEN sqlc.arg(received_at)::timestamptz - interval '1 minute' AND sqlc.arg(received_at)::timestamptz
      AND i.received_at = sqlc.arg(received_at)::timestamptz
      AND i.source_id IS NULL
      AND i.text = sqlc.arg(text)::text
))::boolean AS found;

-- name: UnaliasedCapcodes :many
-- R33, R78: capcodes with no alias, with message count (all time) and first-
-- and last-seen times; the most messages first, then the latest seen.
SELECT capcode, count(*) AS messages, min(sent_at)::timestamptz AS first_seen,
       max(sent_at)::timestamptz AS last_seen
FROM messages
WHERE alias_id IS NULL
GROUP BY capcode
ORDER BY count(*) DESC, max(sent_at) DESC, capcode;

-- name: MessageReceptions :many
-- An empty client_id is a copy from an import file (R83b, ADR 0018). text
-- is the copy's own when it isn't the message's (R19a).
SELECT r.client_id, c.name AS client_name, r.received_at, r.text
FROM receptions r
JOIN clients c ON c.id = r.client_id
WHERE r.message_id = $1
UNION ALL
SELECT DISTINCT '', 'import', i.received_at, i.text
FROM import_receptions i
WHERE i.message_id = $1
ORDER BY received_at, client_id;

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
SELECT a.id, a.pattern, a.pattern_key, a.name, a.ignored, a.template_id, t.name AS template_name, t.agency, t.icon_sha256, t.colour
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

-- name: SetUserLook :execrows
-- R82d: the look the user chose in the account menu.
UPDATE users SET look = $2 WHERE id = $1 AND status <> 'deleted';

-- name: SeeRelease :one
-- R77b: records the version as the last release the user was told of, and
-- answers with the one before (NULL: none was recorded yet). No row when it
-- was already recorded: they were told, here or in another tab or browser.
-- The row lock makes two asks at once take turns, so only one gets a row.
UPDATE users u SET seen_release = sqlc.arg(version)::text
FROM (SELECT id, seen_release FROM users WHERE id = sqlc.arg(id)::bigint FOR UPDATE) AS before
WHERE u.id = before.id AND u.status <> 'deleted' AND u.seen_release IS DISTINCT FROM sqlc.arg(version)::text
RETURNING before.seen_release;

-- name: ClearUserSubject :exec
UPDATE users SET subject = NULL WHERE id = $1;

-- name: ListUsers :many
SELECT u.id, u.email, u.name, u.role, u.status, u.created_at, u.deactivated_at, u.kind,
       u.invited_by, i.status AS inviter_status, i.email AS inviter_email, i.name AS inviter_name,
       -- R44b: how many integrations, and how many paused for failing.
       (SELECT count(*) FROM integrations g WHERE g.owner_id = u.id) AS integrations,
       (SELECT count(*) FROM integrations g WHERE g.owner_id = u.id AND g.paused_by = 'failing') AS integrations_failing,
       -- R42c: the trusted mark, who set it and when.
       u.trusted_at, t.status AS trusted_by_status, t.email AS trusted_by_email, t.name AS trusted_by_name
FROM users u
LEFT JOIN users i ON i.id = u.invited_by
LEFT JOIN users t ON t.id = u.trusted_by
WHERE u.status <> 'deleted'
ORDER BY u.created_at, u.id;

-- name: LockActiveAdmins :many
-- Taken before any change that could remove the last active admin (R44).
SELECT id FROM users WHERE role = 'admin' AND status = 'active' ORDER BY id FOR UPDATE;

-- name: CountActiveAdmins :one
SELECT count(*) FROM users WHERE role = 'admin' AND status = 'active';

-- name: ActiveAdminIDs :many
-- Read only, for a preview; changes lock them with LockActiveAdmins.
SELECT id FROM users WHERE role = 'admin' AND status = 'active' ORDER BY id;

-- name: InviteTree :many
-- A user and everyone they invited, recursively, through deactivated people
-- and deleted placeholders (R42, R42a). Each user has one inviter, so the
-- only cycle (an admin re-inviting the person who invited them) runs through
-- the root, which the recursion skips (ADR 0008 §1). Parents come before
-- their children; siblings by date joined.
WITH RECURSIVE tree AS (
    SELECT u.id, ARRAY[u.created_at] AS sort_path, ARRAY[u.id] AS id_path
    FROM users u WHERE u.id = sqlc.arg(root)::bigint
    UNION ALL
    SELECT c.id, t.sort_path || c.created_at, t.id_path || c.id
    FROM users c JOIN tree t ON c.invited_by = t.id
    WHERE c.id <> sqlc.arg(root)::bigint
)
SELECT u.id, u.invited_by, u.subject, u.email, u.name, u.role, u.status, u.kind,
       u.created_at, u.trusted_at,
       -- Unused invitations that still work (ADR 0008 §3).
       (SELECT count(*) FROM invitations i
        WHERE i.invited_by = u.id AND i.status = 'pending' AND i.expires_at > sqlc.arg(now)::timestamptz) AS invitations
FROM tree t JOIN users u ON u.id = t.id
ORDER BY t.sort_path, t.id_path;

-- name: LockUsers :exec
-- An invite tree's rows, before it's checked and changed (ADR 0008 §5).
-- Inserting a user or invitation that points at a locked row waits.
SELECT id FROM users WHERE id = ANY(sqlc.arg(ids)::bigint[]) ORDER BY id FOR UPDATE;

-- name: CountLiveInvitationsBy :one
SELECT count(*) FROM invitations
WHERE invited_by = ANY(sqlc.arg(ids)::bigint[]) AND status = 'pending' AND expires_at > sqlc.arg(now)::timestamptz;

-- name: SetUserTrusted :execrows
-- R42c: marks or unmarks a user, recording who marked them and when.
UPDATE users SET trusted_at = CASE WHEN sqlc.narg(by)::bigint IS NULL THEN NULL ELSE now() END,
    trusted_by = sqlc.narg(by)::bigint
WHERE id = sqlc.arg(id) AND status <> 'deleted';

-- name: CreateTreeDeactivation :one
INSERT INTO tree_deactivations (root_id, actor_id, reason, invitations_cancelled)
VALUES ($1, $2, $3, $4)
RETURNING id;

-- name: AddTreeDeactivationPeople :exec
INSERT INTO tree_deactivation_people (deactivation_id, user_id, outcome)
SELECT sqlc.arg(deactivation_id)::bigint, unnest(sqlc.arg(user_ids)::bigint[]), unnest(sqlc.arg(outcomes)::text[]);

-- name: TreeDeactivationsOf :many
-- R42d: the records that name a user (root, deactivated or spared), newest
-- first, with everyone named as they are now.
SELECT d.id, d.reason, d.invitations_cancelled, d.created_at,
       d.root_id, r.name AS root_name, r.email AS root_email, r.status AS root_status,
       a.name AS actor_name, a.email AS actor_email, a.status AS actor_status
FROM tree_deactivations d
JOIN users r ON r.id = d.root_id
JOIN users a ON a.id = d.actor_id
WHERE d.root_id = sqlc.arg(user_id)
   OR EXISTS (SELECT 1 FROM tree_deactivation_people p WHERE p.deactivation_id = d.id AND p.user_id = sqlc.arg(user_id))
ORDER BY d.created_at DESC, d.id DESC;

-- name: TreeDeactivationPeople :many
SELECT p.deactivation_id, p.user_id, p.outcome, u.name, u.email, u.status
FROM tree_deactivation_people p JOIN users u ON u.id = p.user_id
WHERE p.deactivation_id = ANY(sqlc.arg(ids)::bigint[])
ORDER BY p.deactivation_id, u.created_at, u.id;

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
    email_key = NULL, name = NULL, role = 'user', deactivated_at = NULL, trusted_at = NULL, trusted_by = NULL,
    look = 'harbour'
WHERE id = $1;

-- Service accounts (R62a, ADR 0048).

-- name: CreateServiceAccount :one
INSERT INTO users (kind, subject, name, role, invited_by, credential_expires_at)
VALUES ('service', sqlc.arg(subject), sqlc.arg(name)::text, 'user', sqlc.arg(owner)::bigint, sqlc.arg(expires_at))
RETURNING *;

-- name: ServiceAccountByName :one
-- The live one (active or deactivated) with that name.
SELECT * FROM users WHERE kind = 'service' AND name = sqlc.arg(name)::text AND status <> 'deleted';

-- name: ListServiceAccounts :many
-- Every service account, revoked ones too, with its owning admin.
SELECT u.id, u.name, u.status, u.created_at, u.credential_expires_at, u.deactivated_at, u.deleted_at,
       o.id AS owner_id, o.email AS owner_email, o.status AS owner_status
FROM users u JOIN users o ON o.id = u.invited_by
WHERE u.kind = 'service'
ORDER BY u.created_at, u.id;

-- name: RekeyServiceAccount :execrows
-- A new credential: a new Authentik user (so a new subject) and expiry.
UPDATE users SET subject = sqlc.arg(subject), credential_expires_at = sqlc.arg(expires_at)
WHERE id = sqlc.arg(id) AND kind = 'service' AND status = 'active';

-- name: RevokeServiceAccount :execrows
UPDATE users SET status = 'deleted', deleted_at = now(), subject = NULL, deactivated_at = NULL
WHERE id = sqlc.arg(id) AND kind = 'service' AND status <> 'deleted';

-- name: RevokeServiceAccountsOf :many
-- An owner deleting their account takes their service accounts with it.
UPDATE users s SET status = 'deleted', deleted_at = now(), deactivated_at = NULL, subject = NULL
FROM (SELECT id, subject FROM users WHERE kind = 'service' AND invited_by = sqlc.arg(owner)::bigint AND status <> 'deleted' FOR UPDATE) old
WHERE s.id = old.id
RETURNING s.id, old.subject;

-- name: ScrubInvitationsTo :exec
-- The invitation that enrolled a deleted user names them: forget the address.
UPDATE invitations SET email = NULL, email_key = NULL WHERE used_by = $1;

-- name: CreateSession :exec
INSERT INTO sessions (token_hash, user_id, expires_at, id_token) VALUES ($1, $2, $3, $4);

-- name: SessionUser :one
SELECT u.*, s.last_used_at AS session_used_at
FROM sessions s JOIN users u ON u.id = s.user_id
WHERE s.token_hash = $1 AND s.expires_at > now() AND u.status = 'active';

-- name: TouchSession :exec
UPDATE sessions SET last_used_at = now(), expires_at = $2 WHERE token_hash = $1;

-- name: DeleteSession :many
-- Signing out (R40b): the session's ID token, for Authentik's end-session
-- endpoint. No row when the session had already gone.
DELETE FROM sessions WHERE token_hash = $1 RETURNING id_token;

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

-- name: LockInvitation :one
SELECT * FROM invitations WHERE id = $1 FOR UPDATE;

-- name: ResendInvitation :exec
-- R43d: the same invitation, with a new link and a new 7 days.
UPDATE invitations SET authentik_id = $2, expires_at = $3, resent_at = now(), link_lost_at = NULL, cleared_at = NULL
WHERE id = $1 AND status = 'pending';

-- name: InvitationsToCheck :many
-- Waiting invitations whose links the app checks against Authentik (ADR 0021).
SELECT id, email_key, authentik_id FROM invitations
WHERE status = 'pending' AND expires_at > now() AND authentik_id IS NOT NULL AND link_lost_at IS NULL AND email_key IS NOT NULL
ORDER BY id;

-- name: MarkInvitationLinkLost :execrows
-- Only if nothing changed it since it was checked (a resend gives a new link).
UPDATE invitations SET link_lost_at = now()
WHERE id = $1 AND authentik_id = $2 AND status = 'pending' AND link_lost_at IS NULL;

-- name: InvitationsToClear :many
-- Used, cancelled or expired invitations whose unfinished Authentik user,
-- if an enrolment left one, the app hasn't yet removed (ADR 0021).
SELECT id, email_key FROM invitations
WHERE cleared_at IS NULL AND email_key IS NOT NULL AND NOT first_admin
  AND (status <> 'pending' OR expires_at <= now())
ORDER BY id;

-- name: MarkInvitationCleared :exec
-- Only while it still can't be enrolled with: a resend makes it waiting again.
UPDATE invitations SET cleared_at = now()
WHERE id = $1 AND (status <> 'pending' OR expires_at <= now());

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
SELECT i.id, i.email, i.status, i.created_at, i.expires_at, i.used_at, i.cancelled_at, i.resent_at, i.link_lost_at,
       i.first_admin, i.reactivates, i.invited_by, u.email AS inviter_email, u.name AS inviter_name, u.status AS inviter_status
FROM invitations i LEFT JOIN users u ON u.id = i.invited_by
WHERE (sqlc.narg(inviter)::bigint IS NULL OR i.invited_by = sqlc.narg(inviter)::bigint)
  AND i.email IS NOT NULL
ORDER BY i.created_at DESC, i.id DESC
LIMIT 200;

-- name: DeleteMessagesBefore :one
-- R45: one batch of messages older than the cut-off. Receptions go with them
-- (cascade); jobs and locations are columns of the message (section 14).
-- R82a: so do their places in albums, with their notes; the albums they
-- leave are marked, for FinishRetentionAlbums. The statement sees the album
-- rows as they were before it, so it finds them though the cascade removes them.
WITH gone AS (
    DELETE FROM messages
    WHERE id IN (SELECT id FROM messages WHERE sent_at < sqlc.arg(cutoff)::timestamptz LIMIT sqlc.arg(batch)::int)
    RETURNING id
), touched AS (
    UPDATE albums SET retention_touched = true
    WHERE NOT retention_touched AND id IN (SELECT am.album_id FROM album_messages am JOIN gone ON gone.id = am.message_id)
)
SELECT count(*)::bigint FROM gone;

-- name: DeleteEmptiedAlbums :execrows
-- R82a: an album retention left with no messages goes, unless it has a description.
DELETE FROM albums a
WHERE a.retention_touched AND a.description IS NULL
  AND NOT EXISTS (SELECT 1 FROM album_messages am WHERE am.album_id = a.id);

-- name: FinishRetentionAlbums :one
-- R82a: one kept, empty, for its description says its messages passed the
-- retention period (emptied_at). Returns how many were kept that way.
WITH done AS (
    UPDATE albums a SET retention_touched = false,
        emptied_at = CASE WHEN NOT EXISTS (SELECT 1 FROM album_messages am WHERE am.album_id = a.id)
                          THEN COALESCE(a.emptied_at, sqlc.arg(now)::timestamptz) ELSE a.emptied_at END
    WHERE a.retention_touched
    RETURNING NOT EXISTS (SELECT 1 FROM album_messages am WHERE am.album_id = a.id) AS kept
)
SELECT count(*)::bigint FROM done WHERE kept;

-- name: MessagesForRedaction :many
SELECT id, capcode, text FROM messages
WHERE id > sqlc.arg(after_id)::bigint
ORDER BY id
LIMIT sqlc.arg(batch)::int;

-- name: UpdateRedactedText :exec
-- P1: retroactive redaction replaces the text and its dedup hash, and
-- re-parses the message in the same transaction, so parsed fields follow (R45a).
UPDATE messages SET text = $2, dedup_key = $3 WHERE id = $1;

-- name: ReceptionVariantsBetween :many
-- P1, R19a: the copies with their own text (near duplicates) of the
-- messages a retroactive redaction batch checked, after after_id up to
-- last_id. An empty client_id is a copy from an import file.
SELECT r.message_id, r.client_id, r.received_at, r.text::text AS text
FROM receptions r
WHERE r.message_id > sqlc.arg(after_id)::bigint AND r.message_id <= sqlc.arg(last_id)::bigint AND r.text IS NOT NULL
UNION ALL
SELECT i.message_id, '', i.received_at, i.text::text
FROM import_receptions i
WHERE i.message_id > sqlc.arg(after_id)::bigint AND i.message_id <= sqlc.arg(last_id)::bigint AND i.text IS NOT NULL
ORDER BY message_id;

-- name: RedactReceptionVariant :exec
-- P1, R45a: a copy's own text, as the rules now redact it; none when that's
-- now its message's text.
UPDATE receptions r SET text = NULLIF(sqlc.arg(new_text)::text, (SELECT m.text FROM messages m WHERE m.id = r.message_id))
WHERE r.message_id = sqlc.arg(message_id) AND r.client_id = sqlc.arg(client_id) AND r.received_at = sqlc.arg(received_at)
  AND r.text = sqlc.arg(old_text)::text;

-- name: RedactImportVariant :exec
-- As RedactReceptionVariant, for a copy from an import file.
UPDATE import_receptions i SET text = NULLIF(sqlc.arg(new_text)::text, (SELECT m.text FROM messages m WHERE m.id = i.message_id))
WHERE i.message_id = sqlc.arg(message_id) AND i.received_at = sqlc.arg(received_at) AND i.text = sqlc.arg(old_text)::text;

-- name: DedupReportVariants :many
-- R19b: the near duplicates kept as copies with their own text, of the
-- messages sent since a time, with their messages' text, for the
-- dedup-report to count. Neither text leaves the command (P1).
SELECT m.capcode, m.text AS message_text, m.priority, m.heartbeat, r.text::text AS variant_text
FROM receptions r
JOIN messages m ON m.id = r.message_id
WHERE m.sent_at >= sqlc.arg(since)::timestamptz AND r.text IS NOT NULL
UNION ALL
SELECT m.capcode, m.text, m.priority, m.heartbeat, i.text::text
FROM import_receptions i
JOIN messages m ON m.id = i.message_id
WHERE m.sent_at >= sqlc.arg(since)::timestamptz AND i.text IS NOT NULL;

-- name: DedupReportCloseMessages :many
-- R19b: pairs of messages to one capcode sent within 10 s of each other
-- since a time: pages R19a kept apart, for the dedup-report to measure how
-- far apart they were. Neither text leaves the command (P1).
SELECT a.text AS first_text, b.text AS second_text, a.job_number AS first_job, b.job_number AS second_job
FROM messages a
JOIN messages b ON b.capcode = a.capcode AND b.id <> a.id
 AND b.sent_at BETWEEN a.sent_at AND a.sent_at + interval '10 seconds'
 AND (b.sent_at > a.sent_at OR b.id > a.id)
WHERE a.sent_at >= sqlc.arg(since)::timestamptz;

-- name: MessagesByIDs :many
-- As MessagesToReparse, for the given messages: one whose text changed while it was re-parsed.
SELECT m.id, m.capcode, m.text, m.priority, m.heartbeat, m.sent_at,
       EXISTS (SELECT 1 FROM large_event_messages lem WHERE lem.message_id = m.id) AS in_event,
       EXISTS (SELECT 1 FROM receptions r WHERE r.message_id = m.id) AS live
FROM messages m
WHERE m.id = ANY(sqlc.arg(ids)::bigint[])
ORDER BY m.id;

-- name: MessagesToRedactLocked :many
-- P1, R45a: the pages of a batch whose text the rules change, locked until
-- the batch commits, with what their re-parse needs (as MessagesByIDs).
SELECT m.id, m.capcode, m.text, m.priority, m.heartbeat, m.sent_at,
       EXISTS (SELECT 1 FROM large_event_messages lem WHERE lem.message_id = m.id) AS in_event,
       EXISTS (SELECT 1 FROM receptions r WHERE r.message_id = m.id) AS live
FROM messages m
WHERE m.id = ANY(sqlc.arg(ids)::bigint[])
ORDER BY m.id
FOR NO KEY UPDATE OF m;

-- name: CountMessagesToRedact :one
-- R45a: the pages a retroactive redaction run checks, counted when it starts.
SELECT count(*) FROM messages;

-- name: GetRedactionRun :one
-- R45a (ADR 0044): retroactive redaction's run, the latest; there's at most one.
SELECT id, state, reason, rules_hash, total, done, redacted, after_id, pages_done, source_after, sources_done, epoch,
       started_at, changed_at, finished_at FROM redaction_runs;

-- name: LockRedactionRun :one
-- As GetRedactionRun, locked until the transaction ends: a batch's end, an
-- admin's change or the nightly run's start, one at a time.
SELECT id, state, reason, rules_hash, total, done, redacted, after_id, pages_done, source_after, sources_done, epoch,
       started_at, changed_at, finished_at FROM redaction_runs FOR UPDATE;

-- name: DeleteRedactionRuns :exec
-- Before a new run: the last one, with its days, goes.
DELETE FROM redaction_runs;

-- name: CreateRedactionRun :one
INSERT INTO redaction_runs (state, reason, rules_hash, total) VALUES ('running', $1, $2, $3) RETURNING id;

-- name: RestartRedactionRun :exec
-- From the beginning again (Start again, the rules changed, or the nightly
-- run starting a stopped one): its days go with it.
UPDATE redaction_runs SET state = sqlc.arg(state)::text, reason = sqlc.arg(reason)::text, rules_hash = sqlc.arg(rules_hash)::text,
    total = sqlc.arg(total)::bigint, done = 0, redacted = 0, after_id = 0, pages_done = false, source_after = '', sources_done = false,
    epoch = epoch + 1, started_at = now(), changed_at = now(), finished_at = NULL
WHERE id = sqlc.arg(id)::bigint;

-- name: SetRedactionRunState :exec
UPDATE redaction_runs SET state = sqlc.arg(state)::text, changed_at = now() WHERE id = sqlc.arg(id)::bigint;

-- name: AdvanceRedactionRun :exec
-- Where the walk over the pages has got: written with the batch it ends.
UPDATE redaction_runs SET after_id = sqlc.arg(after_id)::bigint, done = done + sqlc.arg(checked)::bigint,
    redacted = redacted + sqlc.arg(changed)::bigint, pages_done = sqlc.arg(pages_done)::bool
WHERE id = sqlc.arg(id)::bigint;

-- name: AdvanceRedactionRunSources :exec
-- Where the walk over the imported source IDs has got: written with the batch it ends.
UPDATE redaction_runs SET source_after = sqlc.arg(source_after)::text, sources_done = sqlc.arg(sources_done)::bool
WHERE id = sqlc.arg(id)::bigint;

-- name: FinishRedactionRun :exec
-- Pages stored while it ran are checked by it too, so its total is then
-- what it did.
UPDATE redaction_runs SET state = 'finished', changed_at = now(), finished_at = now(), total = GREATEST(total, done)
WHERE id = $1;

-- name: AddRedactionRunDay :exec
INSERT INTO redaction_run_days (run_id, day, from_at, to_at) VALUES ($1, $2, $3, $4)
ON CONFLICT (run_id, day) DO UPDATE SET from_at = LEAST(redaction_run_days.from_at, EXCLUDED.from_at),
    to_at = GREATEST(redaction_run_days.to_at, EXCLUDED.to_at);

-- name: RedactionRunDays :many
SELECT from_at, to_at FROM redaction_run_days WHERE run_id = $1 ORDER BY day;

-- name: DeleteRedactionRunDays :exec
DELETE FROM redaction_run_days WHERE run_id = $1;

-- name: NotifyRedaction :exec
-- Wakes the API's retroactive redaction worker (R45a).
SELECT pg_notify('pagerarchive_redaction', '');

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

-- name: AliasReport :many
-- R38 for an alias: Emergency messages to every capcode the aliases cover
-- (the pages that resolved to them, R27; for a pattern, each capcode it
-- matches), in a date range, by event type. Heartbeats are excluded (R35b).
SELECT event_type, count(*) AS messages
FROM messages
WHERE alias_id = ANY(sqlc.arg(alias_ids)::bigint[])
  AND priority = 'Emergency'
  AND NOT heartbeat
  AND sent_at >= sqlc.arg(sent_from)::timestamptz
  AND sent_at < sqlc.arg(sent_to)::timestamptz
GROUP BY event_type
ORDER BY event_type NULLS LAST;

-- name: AliasesNamed :many
-- R38, R27b: the aliases with this name, ignoring case (names shared from
-- before names were unique, migration 00024, are all reported).
SELECT id, name, pattern FROM aliases
WHERE lower(name) = lower(sqlc.arg(name)::text)
ORDER BY pattern;

-- Integrations (R47-R51c, ADR 0006).

-- name: CreateIntegration :one
INSERT INTO integrations (uid, owner_id, name, kind, content, config, secrets, filter, large_events)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)
RETURNING *;

-- name: UpdateIntegration :exec
UPDATE integrations SET name = $2, content = $3, config = $4, secrets = $5, filter = $6, large_events = $7, updated_at = now()
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

-- name: SearchAliasNames :many
-- R33a: every alias name, for the search box's suggestions. The browser asks
-- once and matches what's typed itself, so no keystroke reaches the database.
SELECT DISTINCT name FROM aliases ORDER BY name;

-- name: SearchAgencies :many
-- R33a: every template agency, for the search box's suggestions.
SELECT DISTINCT agency FROM templates ORDER BY agency;

-- name: SearchCallSigns :many
-- R33a: every call sign, for the search box's suggestions. The call signs
-- are few and the messages many, so each distinct one is found with one
-- step down messages_call_sign_idx (a loose index scan), not by reading
-- every message.
WITH RECURSIVE c AS (
    (SELECT m.call_sign AS cs FROM messages m WHERE m.call_sign IS NOT NULL ORDER BY m.call_sign LIMIT 1)
    UNION ALL
    SELECT (SELECT m.call_sign FROM messages m WHERE m.call_sign > c.cs ORDER BY m.call_sign LIMIT 1)
    FROM c WHERE c.cs IS NOT NULL
)
SELECT cs::text AS call_sign FROM c
WHERE cs IS NOT NULL
ORDER BY cs;

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
WHERE i.status = 'active' AND u.status = 'active' AND NOT i.large_events
  AND (EXISTS (SELECT 1 FROM integration_capcodes c WHERE c.integration_id = i.id AND c.capcode = sqlc.arg(capcode)::text)
       OR (sqlc.narg(alias_id)::bigint IS NOT NULL
           AND EXISTS (SELECT 1 FROM integration_aliases a WHERE a.integration_id = i.id AND a.alias_id = sqlc.narg(alias_id)::bigint)));

-- name: InsertDelivery :exec
-- R51b: at most one row per page per integration, whatever the code path.
INSERT INTO deliveries (integration_id, message_id, status, next_attempt_at)
VALUES ($1, $2, $3, $4)
ON CONFLICT DO NOTHING;

-- name: DueDeliveries :many
SELECT integration_id, message_id, attempts, event_id
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
       m.sent_at, m.capcode, d.event_id
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
-- A renamed alias is no longer a name duplicate (R27b, migration 00024).
UPDATE aliases SET pattern = $2, name = $3, template_id = $4, ignored = $5,
    name_duplicate = name_duplicate AND name = $3
WHERE id = $1;

-- name: NameDuplicateAliases :many
-- R27b: aliases whose names match another's, ignoring letter case, from
-- before names were unique (migration 00024), in groups, oldest first.
SELECT id, pattern, name, lower(name) AS name_key FROM aliases a
WHERE EXISTS (SELECT 1 FROM aliases o WHERE o.id <> a.id AND lower(o.name) = lower(a.name))
ORDER BY lower(name), id;

-- name: SettleNameDuplicates :exec
-- R27b: after an alias is renamed or deleted, a group left with no alias
-- under the unique index (all marked name_duplicate) puts its oldest back
-- under it.
UPDATE aliases a SET name_duplicate = false
WHERE a.name_duplicate
  AND a.id = (SELECT min(o.id) FROM aliases o WHERE lower(o.name) = lower(a.name))
  AND NOT EXISTS (SELECT 1 FROM aliases o WHERE lower(o.name) = lower(a.name) AND NOT o.name_duplicate);

-- name: AliasNamesIgnoringCase :many
-- R27b, the import: every alias whose name is one of these, ignoring letter
-- case.
SELECT id, pattern, name, lower(name)::text AS name_key FROM aliases
WHERE lower(name) = ANY(SELECT lower(n) FROM unnest(sqlc.arg(names)::text[]) AS n)
ORDER BY id;

-- name: TemplateByID :one
SELECT id, name, agency, icon_sha256, colour, agency_profile FROM templates WHERE id = $1;

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
-- R53: aliases for Administration, filtered by name, capcode or pattern,
-- a page at a time after a pattern (the PowerShell module lists them all).
SELECT a.id, a.pattern, a.name, a.ignored, a.template_id, t.name AS template_name, t.colour,
       (SELECT count(*) FROM alias_requests r WHERE r.alias_id = a.id AND r.status = 'waiting')::bigint AS waiting
FROM aliases a
LEFT JOIN templates t ON t.id = a.template_id
WHERE (sqlc.arg(q)::text = '' OR strpos(lower(a.name), lower(sqlc.arg(q)::text)) > 0 OR strpos(a.pattern, sqlc.arg(q)::text) > 0)
  AND (sqlc.arg(after)::text = '' OR a.pattern COLLATE "C" > sqlc.arg(after)::text COLLATE "C")
ORDER BY a.pattern COLLATE "C"
LIMIT sqlc.arg(max_rows)::int;

-- name: ExportAliases :many
-- R58: every alias in the import format.
SELECT a.id, a.pattern, a.name, a.ignored, t.name AS template_name, t.colour
FROM aliases a
LEFT JOIN templates t ON t.id = a.template_id
ORDER BY a.pattern;

-- name: ReceptionsForMessages :many
-- R63a: each message's receive times, for a page of messages at once. An
-- empty client_id is a copy from an import file (R83b, ADR 0018). text is
-- the copy's own when it isn't the message's (R19a).
SELECT r.message_id, r.client_id, c.name AS client_name, r.received_at, r.text
FROM receptions r
JOIN clients c ON c.id = r.client_id
WHERE r.message_id = ANY(sqlc.arg(ids)::bigint[])
UNION ALL
SELECT DISTINCT i.message_id, '', 'import', i.received_at, i.text
FROM import_receptions i
WHERE i.message_id = ANY(sqlc.arg(ids)::bigint[])
ORDER BY message_id, received_at, client_id;

-- Albums (R82-R82b, R82f; ADR 0026). Every query names the album's owner,
-- so nobody else reaches an album, admins included.

-- name: CreateAlbum :one
INSERT INTO albums (owner_id, name, description) VALUES ($1, $2, $3)
RETURNING id;

-- name: MyAlbums :many
-- The signed-in user's albums, by name, with how many messages each holds.
SELECT a.id, a.name, a.description, a.created_at, a.updated_at, a.emptied_at,
       (SELECT count(*) FROM album_messages am WHERE am.album_id = a.id)::bigint AS messages
FROM albums a
WHERE a.owner_id = $1
ORDER BY lower(a.name), a.id;

-- name: MyAlbum :one
SELECT a.id, a.name, a.description, a.created_at, a.updated_at, a.emptied_at,
       (SELECT count(*) FROM album_messages am WHERE am.album_id = a.id)::bigint AS messages
FROM albums a
WHERE a.id = sqlc.arg(id) AND a.owner_id = sqlc.arg(owner_id);

-- name: UpdateAlbum :execrows
UPDATE albums SET name = sqlc.arg(name), description = sqlc.narg(description), updated_at = now()
WHERE id = sqlc.arg(id) AND owner_id = sqlc.arg(owner_id);

-- name: DeleteAlbum :execrows
DELETE FROM albums WHERE id = sqlc.arg(id) AND owner_id = sqlc.arg(owner_id);

-- name: AddToAlbum :execrows
-- A message added again keeps its place and its note. Adding one clears the
-- line that its messages had passed the retention period (R82a).
WITH album AS (
    UPDATE albums al SET emptied_at = NULL, updated_at = now()
    WHERE al.id = sqlc.arg(album_id) AND al.owner_id = sqlc.arg(owner_id)
    RETURNING al.id AS album_id
)
INSERT INTO album_messages (album_id, message_id)
SELECT album.album_id, m.id FROM album, messages m WHERE m.id = sqlc.arg(message_id)
ON CONFLICT (album_id, message_id) DO NOTHING;

-- name: SetAlbumNote :execrows
-- R82f: a message's note in one album; NULL for none.
UPDATE album_messages am SET note = sqlc.narg(note)
FROM albums a
WHERE a.id = am.album_id AND a.id = sqlc.arg(album_id) AND a.owner_id = sqlc.arg(owner_id)
  AND am.message_id = sqlc.arg(message_id);

-- name: RemoveFromAlbum :execrows
DELETE FROM album_messages am
USING albums a
WHERE a.id = am.album_id AND a.id = sqlc.arg(album_id) AND a.owner_id = sqlc.arg(owner_id)
  AND am.message_id = sqlc.arg(message_id);

-- name: AlbumNotes :many
-- The notes of these messages in one album (its owner's, checked by the caller).
SELECT message_id, note::text AS note FROM album_messages
WHERE album_id = sqlc.arg(album_id) AND message_id = ANY(sqlc.arg(ids)::bigint[]) AND note IS NOT NULL;

-- name: AlbumsHolding :many
-- Which of the signed-in user's albums hold a message, for "Add to album".
SELECT am.album_id FROM album_messages am JOIN albums a ON a.id = am.album_id
WHERE a.owner_id = sqlc.arg(owner_id) AND am.message_id = sqlc.arg(message_id)
ORDER BY am.album_id;

-- name: DeleteAlbumsOf :exec
-- R82b: deleting an account deletes its albums, notes and all.
DELETE FROM albums WHERE owner_id = $1;

-- Large events (R87-R87e; ADR 0027).

-- name: LockLargeEvents :exec
-- One page's event work at a time, to its transaction's end: two pages can't
-- both open an event, or join one as it's merged.
SELECT pg_advisory_xact_lock(4242008700);

-- name: JobEventPages :many
-- The pages of a job that count (R87: Emergency, to a capcode whose alias
-- isn't ignored (R27; owner, 2026-11-14), not a heartbeat, and heard by a
-- client: an imported page never counts, R83b), oldest first, with the event
-- each is in, if any. One row per stored message, so a copy from another
-- receiver (R19) counts once towards the job's number of Emergency pages.
-- RegroupPages counts the same pages.
SELECT m.id, m.sent_at, lem.event_id
FROM messages m
JOIN aliases a ON a.id = m.alias_id AND NOT a.ignored
LEFT JOIN large_event_messages lem ON lem.message_id = m.id
WHERE m.job_number = sqlc.arg(job_number)::text AND m.priority = 'Emergency' AND NOT m.heartbeat
  AND EXISTS (SELECT 1 FROM receptions r WHERE r.message_id = m.id)
ORDER BY m.sent_at, m.id
LIMIT 5000;

-- name: LargeEventsNear :many
-- R87a: the events a page sent at t is within 12 hours of: from 12 hours
-- before the event's first page (a page that arrived late) to 12 hours
-- after its last. Oldest first.
SELECT * FROM large_events
WHERE last_at >= sqlc.arg(t)::timestamptz - interval '12 hours'
  AND first_at <= sqlc.arg(t)::timestamptz + interval '12 hours'
ORDER BY id;

-- name: CreateLargeEvent :one
-- R87d: the only place an event begins, so the only time its integrations
-- are told.
INSERT INTO large_events (first_at, last_at) VALUES (sqlc.arg(at)::timestamptz, sqlc.arg(at)::timestamptz)
RETURNING *;

-- name: MergeLargeEvents :exec
-- R87a: a page within 12 hours of two events joins them into one, the
-- oldest. Their pages and deliveries move to it. Nothing is sent for the
-- merge: each was told of when it opened (R87d).
WITH others AS (
    SELECT * FROM large_events WHERE id = ANY(sqlc.arg(others)::bigint[]) AND id <> sqlc.arg(id)::bigint
), pages AS (
    UPDATE large_event_messages SET event_id = sqlc.arg(id)::bigint WHERE event_id IN (SELECT id FROM others)
), sent AS (
    UPDATE deliveries SET event_id = sqlc.arg(id)::bigint WHERE event_id IN (SELECT id FROM others)
), kept AS (
    UPDATE large_events e SET
        first_at = LEAST(e.first_at, (SELECT min(first_at) FROM others)),
        last_at = GREATEST(e.last_at, (SELECT max(last_at) FROM others))
    WHERE e.id = sqlc.arg(id)::bigint
)
SELECT 1;

-- name: DeleteLargeEvents :exec
-- The events merged into another, once their pages and deliveries moved.
DELETE FROM large_events WHERE id = ANY(sqlc.arg(ids)::bigint[]);

-- name: AddLargeEventPage :exec
INSERT INTO large_event_messages (message_id, event_id)
VALUES (sqlc.arg(message_id)::bigint, sqlc.arg(event_id)::bigint)
ON CONFLICT (message_id) DO NOTHING;

-- name: LargeEventByID :one
SELECT * FROM large_events WHERE id = $1;

-- name: SetLargeEventSpan :exec
-- An event's first and last pages' times after pages joined it.
UPDATE large_events SET
    first_at = LEAST(first_at, sqlc.arg(first_at)::timestamptz),
    last_at = GREATEST(last_at, sqlc.arg(last_at)::timestamptz)
WHERE id = sqlc.arg(id)::bigint;

-- name: LargeEventJobs :many
-- R87c, R87d: an event's jobs, from the pages it holds, by their first page.
SELECT m.job_number::text AS job_number, min(m.sent_at)::timestamptz AS first_at
FROM large_event_messages lem JOIN messages m ON m.id = lem.message_id
WHERE lem.event_id = $1 AND m.job_number IS NOT NULL
GROUP BY m.job_number
ORDER BY 2, 1;

-- name: ListLargeEvents :many
-- R87c: events, newest first, with their numbers counted from the pages they
-- still hold (R87e). Every page an event holds is an Emergency page that
-- counts (R87), so pages is its number of Emergency pages. before_* is the
-- page cursor.
SELECT e.*,
       (SELECT count(*) FROM large_event_messages lem WHERE lem.event_id = e.id)::bigint AS pages,
       (SELECT count(DISTINCT m.job_number) FROM large_event_messages lem JOIN messages m ON m.id = lem.message_id
        WHERE lem.event_id = e.id)::bigint AS jobs
FROM large_events e
WHERE sqlc.narg(before_first)::timestamptz IS NULL
   OR (e.first_at, e.id) < (sqlc.narg(before_first)::timestamptz, sqlc.narg(before_id)::bigint)
ORDER BY e.first_at DESC, e.id DESC
LIMIT sqlc.arg(max_rows)::int;

-- name: LargeEventIntegrations :many
-- R87d: active integrations of active owners that select large events.
SELECT i.id, i.kind, u.role AS owner_role
FROM integrations i JOIN users u ON u.id = i.owner_id
WHERE i.status = 'active' AND u.status = 'active' AND i.large_events;

-- name: InsertEventDelivery :exec
-- R87d: an event's delivery, carried by the page that opened it.
INSERT INTO deliveries (integration_id, message_id, status, next_attempt_at, event_id)
VALUES (sqlc.arg(integration_id), sqlc.arg(message_id), sqlc.arg(status), sqlc.narg(next_attempt_at), sqlc.arg(event_id)::bigint)
ON CONFLICT DO NOTHING;

-- name: DeleteEmptyLargeEvents :execrows
-- R87e: an event goes with its last page.
DELETE FROM large_events e
WHERE NOT EXISTS (SELECT 1 FROM large_event_messages lem WHERE lem.event_id = e.id);

-- name: RefreshLargeEventSpans :execrows
-- R87e: after retention, each event's start and end follow the pages it still holds.
UPDATE large_events e SET first_at = s.first_at, last_at = s.last_at
FROM (SELECT lem.event_id, min(m.sent_at) AS first_at, max(m.sent_at) AS last_at
      FROM large_event_messages lem JOIN messages m ON m.id = lem.message_id
      GROUP BY lem.event_id) s
WHERE s.event_id = e.id AND (e.first_at <> s.first_at OR e.last_at <> s.last_at);

-- name: LargeEventPages :one
-- R87c, R87d: how many Emergency pages an event holds (all its pages count, R87).
SELECT count(*)::bigint FROM large_event_messages WHERE event_id = $1;

-- Rebuilding large events after a re-parse (R23a, R87; owner, 2026-11-14;
-- ADR 0027 §9). Each runs in a rebuild's transaction, under LockLargeEvents.
-- The pages that count are those JobEventPages counts.

-- name: RegroupEventsNear :many
-- The events pages sent between lo and hi could join (R87a): from 12 hours
-- before an event's first page to 12 hours after its last. Oldest first.
SELECT id, first_at, last_at FROM large_events
WHERE last_at >= sqlc.arg(lo)::timestamptz - interval '12 hours'
  AND first_at <= sqlc.arg(hi)::timestamptz + interval '12 hours'
ORDER BY id;

-- name: RegroupJobsExtent :one
-- The first and last counting pages of every job with a counting page sent
-- between lo and hi: a job's pages join its event whenever they were sent,
-- so a rebuild takes them all. lo and hi themselves when there's none.
WITH jobs AS (
    SELECT DISTINCT m.job_number
    FROM messages m JOIN aliases a ON a.id = m.alias_id AND NOT a.ignored
    WHERE m.sent_at BETWEEN sqlc.arg(lo)::timestamptz AND sqlc.arg(hi)::timestamptz
      AND m.job_number IS NOT NULL AND m.priority = 'Emergency' AND NOT m.heartbeat
      AND EXISTS (SELECT 1 FROM receptions r WHERE r.message_id = m.id)
)
SELECT COALESCE(min(m.sent_at), sqlc.arg(lo)::timestamptz)::timestamptz AS first_at,
       COALESCE(max(m.sent_at), sqlc.arg(hi)::timestamptz)::timestamptz AS last_at
FROM messages m JOIN aliases a ON a.id = m.alias_id AND NOT a.ignored
WHERE m.job_number IN (SELECT job_number FROM jobs)
  AND m.priority = 'Emergency' AND NOT m.heartbeat
  AND EXISTS (SELECT 1 FROM receptions r WHERE r.message_id = m.id);

-- name: RegroupPages :many
-- The pages sent between lo and hi that count: the pages a rebuild groups.
-- text is read for codes (R5), never logged; large_event_field is a field
-- value its agency profile lists (R7), which opens an event as a code does.
SELECT m.id, m.sent_at, m.job_number, m.text, m.large_event_field
FROM messages m JOIN aliases a ON a.id = m.alias_id AND NOT a.ignored
WHERE m.sent_at BETWEEN sqlc.arg(lo)::timestamptz AND sqlc.arg(hi)::timestamptz
  AND m.priority = 'Emergency' AND NOT m.heartbeat
  AND EXISTS (SELECT 1 FROM receptions r WHERE r.message_id = m.id)
ORDER BY m.sent_at, m.id;

-- name: RegroupMembers :many
-- The pages that the events a rebuild replaces hold now.
SELECT message_id, event_id FROM large_event_messages
WHERE event_id = ANY(sqlc.arg(event_ids)::bigint[])
ORDER BY event_id, message_id;

-- name: PutLargeEventPages :exec
-- A rebuild's pages, each into its event, moved from any other.
INSERT INTO large_event_messages (message_id, event_id)
SELECT unnest(sqlc.arg(message_ids)::bigint[]), unnest(sqlc.arg(event_ids)::bigint[])
ON CONFLICT (message_id) DO UPDATE SET event_id = EXCLUDED.event_id;

-- name: RemoveLargeEventPages :exec
-- The pages a rebuild leaves in no event.
DELETE FROM large_event_messages WHERE message_id = ANY(sqlc.arg(message_ids)::bigint[]);

-- name: PutLargeEventSpan :exec
-- A rebuilt event's first and last pages' times.
UPDATE large_events SET first_at = sqlc.arg(first_at)::timestamptz, last_at = sqlc.arg(last_at)::timestamptz
WHERE id = sqlc.arg(id)::bigint;

-- name: RehomeEventDeliveries :execrows
-- R87d after a rebuild, before the replaced events go: a large-event
-- delivery stays with whatever event now holds the page that carried it.
-- That is one about a replaced event, or one about no event whose page the
-- rebuild put back into one (pages: those it put into events). If no event
-- holds the page, the delivery is kept, about no event, and one not yet
-- sent is cancelled: a rebuild never sends anything.
UPDATE deliveries d SET
    event_id = h.event_id,
    status = CASE WHEN h.event_id IS NULL AND d.status = 'pending' THEN 'cancelled' ELSE d.status END,
    next_attempt_at = CASE WHEN h.event_id IS NULL THEN NULL ELSE d.next_attempt_at END,
    updated_at = now()
FROM (SELECT d2.integration_id, d2.message_id, lem.event_id
      FROM deliveries d2 LEFT JOIN large_event_messages lem ON lem.message_id = d2.message_id
      JOIN integrations i ON i.id = d2.integration_id
      WHERE d2.event_id = ANY(sqlc.arg(event_ids)::bigint[])
         OR (d2.event_id IS NULL AND i.large_events AND d2.message_id = ANY(sqlc.arg(pages)::bigint[]))) h
WHERE d.integration_id = h.integration_id AND d.message_id = h.message_id
  AND d.event_id IS DISTINCT FROM h.event_id;
