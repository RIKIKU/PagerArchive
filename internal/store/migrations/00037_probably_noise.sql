-- +goose NO TRANSACTION
-- +goose Up
-- R35h (owner, 2026-11-25): a page is "probably noise" when exactly one
-- client has a copy of it and no import does (a), another client has a copy
-- of some page received within 10 minutes either side of its sent time (b),
-- it matched no format (c), and no other stored page is to its capcode (d).
--
-- messages.probably_noise keeps the answer with the page, so a list reads a
-- column and runs no query per row. Ingest, the importer and the re-parse
-- keep it (ingest.Save, UpdateParsed); pages stored before are worked out in
-- the background after the start (internal/noise), not here (R91a). A
-- boolean with a constant default changes only the catalogue: no row is
-- rewritten.
ALTER TABLE messages ADD COLUMN IF NOT EXISTS probably_noise boolean NOT NULL DEFAULT false;

-- The rule, in one place, for the live check, the background pass and the
-- tests. The legs are tried cheapest first (CASE), so a page with a format,
-- nearly every page, costs nothing more. (b) goes through the clients, a
-- handful, so each is one probe of the index below.
-- +goose StatementBegin
CREATE OR REPLACE FUNCTION message_probably_noise(m_id bigint, m_capcode text, m_format text, m_sent_at timestamptz) RETURNS boolean
    LANGUAGE sql STABLE PARALLEL SAFE
    RETURN CASE
        WHEN m_format IS NOT NULL THEN false
        WHEN EXISTS (SELECT 1 FROM import_receptions i WHERE i.message_id = m_id) THEN false
        WHEN (SELECT count(DISTINCT r.client_id) FROM receptions r WHERE r.message_id = m_id) <> 1 THEN false
        WHEN EXISTS (SELECT 1 FROM messages o WHERE o.capcode = m_capcode AND o.id <> m_id) THEN false
        ELSE EXISTS (
            SELECT 1 FROM clients c
            WHERE c.id <> (SELECT r.client_id FROM receptions r WHERE r.message_id = m_id LIMIT 1)
              AND EXISTS (SELECT 1 FROM receptions r2
                          WHERE r2.client_id = c.id
                            AND r2.received_at BETWEEN m_sent_at - interval '10 minutes' AND m_sent_at + interval '10 minutes'))
    END;
-- +goose StatementEnd

-- (b)'s lookup, and the background pass's start (each client's first copy).
-- Built without blocking reads or writes; a build that failed part-way
-- leaves an invalid index, dropped here when the migration runs again.
DROP INDEX CONCURRENTLY IF EXISTS receptions_client_received_idx;
CREATE INDEX CONCURRENTLY receptions_client_received_idx ON receptions (client_id, received_at);

-- +goose Down
DROP INDEX IF EXISTS receptions_client_received_idx;
DROP FUNCTION IF EXISTS message_probably_noise(bigint, text, text, timestamptz);
ALTER TABLE messages DROP COLUMN IF EXISTS probably_noise;
