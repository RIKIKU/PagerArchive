-- +goose NO TRANSACTION
-- +goose Up
-- R33b (ADR 0017): what the search index reads, the text, a unit separator
-- (U+001F) and every extra field's value as jsonb writes them (an array of
-- JSON values, without the keys). A term found in the text or one extra field
-- is found here too, unless it holds a character JSON escapes (", \ or a
-- control character), so SearchMessages uses it to find candidates for the
-- other terms and still matches each term field by field. A plain
-- expression, so PostgreSQL inlines it. Derived from the text and extra
-- fields alone, its index follows retroactive redaction (P1) in the same
-- statement that changes them.
-- +goose StatementBegin
CREATE OR REPLACE FUNCTION message_search_text(text text, extra jsonb) RETURNS text
    LANGUAGE sql IMMUTABLE PARALLEL SAFE
    RETURN text || chr(31) || COALESCE(jsonb_path_query_array(extra, '$.*')::text, '');
-- +goose StatementEnd

-- Built without blocking reads or writes (CONCURRENTLY can't run in a
-- transaction, hence NO TRANSACTION); a build that failed part-way leaves an
-- invalid index, dropped here when the migration runs again.
DROP INDEX CONCURRENTLY IF EXISTS messages_search_trgm_idx;
CREATE INDEX CONCURRENTLY messages_search_trgm_idx ON messages USING gin (message_search_text(text, extra) gin_trgm_ops);

-- The new index holds the text too; nothing else searched the text's own.
DROP INDEX CONCURRENTLY IF EXISTS messages_text_trgm_idx;

-- The planner knows how common a term is from statistics on the index's
-- expression, which only ANALYZE gathers (a sample; it doesn't block reads
-- or writes). Without them it would use the index for common terms too.
ANALYZE messages;

-- +goose Down
CREATE INDEX CONCURRENTLY IF NOT EXISTS messages_text_trgm_idx ON messages USING gin (text gin_trgm_ops);
DROP INDEX CONCURRENTLY IF EXISTS messages_search_trgm_idx;
DROP FUNCTION IF EXISTS message_search_text(text, jsonb);
