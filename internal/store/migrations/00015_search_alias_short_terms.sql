-- +goose NO TRANSACTION
-- +goose Up
-- R33b (ADR 0017, "A busy alias and short terms"): the two-character
-- substrings of what the search index reads (message_search_text), lower-cased
-- as ILIKE compares them, without repeats. A term too short for a trigram is
-- found, case aside, only in a page whose list holds every substring of its
-- own, so SearchMessages uses the list to find candidates for such terms and
-- still matches each term field by field. Derived from the text and extra
-- fields alone, its index follows retroactive redaction (P1) in the same
-- statement that changes them.
-- +goose StatementBegin
CREATE OR REPLACE FUNCTION search_bigrams(s text) RETURNS text[]
    LANGUAGE sql IMMUTABLE PARALLEL SAFE
    RETURN ARRAY(SELECT DISTINCT substr(l, i, 2) FROM lower(s) AS l, generate_series(1, length(l) - 1) AS i);
-- +goose StatementEnd

-- A high cost, so a plan that reads pages in order checks it last, on the
-- few that pass every other condition.
-- +goose StatementBegin
CREATE OR REPLACE FUNCTION message_search_bigrams(text text, extra jsonb) RETURNS text[]
    LANGUAGE sql IMMUTABLE PARALLEL SAFE COST 10000
    RETURN search_bigrams(message_search_text(text, extra));
-- +goose StatementEnd

-- The substrings a search's short terms need: every one of each term's.
-- +goose StatementBegin
CREATE OR REPLACE FUNCTION search_terms_bigrams(terms text[]) RETURNS text[]
    LANGUAGE sql IMMUTABLE PARALLEL SAFE
    RETURN ARRAY(SELECT DISTINCT b FROM unnest(terms) AS t, unnest(search_bigrams(t)) AS b);
-- +goose StatementEnd

-- Built without blocking reads or writes, as 00013's index; a build that
-- failed part-way leaves an invalid index, dropped here when it runs again.
DROP INDEX CONCURRENTLY IF EXISTS messages_search_bigrams_idx;
CREATE INDEX CONCURRENTLY messages_search_bigrams_idx ON messages USING gin (message_search_bigrams(text, extra));

-- An alias's pages, newest first: a search with one alias reads its newest
-- pages in order and stops at a page of results, rather than reading them
-- all to sort. messages_alias_idx stays: it holds each alias once, with its
-- pages, so it's a fifth of the size, and the tries that combine an alias's
-- pages with the text index's read it faster from a cold disk.
DROP INDEX CONCURRENTLY IF EXISTS messages_alias_sent_idx;
CREATE INDEX CONCURRENTLY messages_alias_sent_idx ON messages (alias_id, sent_at DESC);

-- Statistics on the new expression, for the planner to know a short term's
-- share of the pages.
ANALYZE messages;

-- +goose Down
DROP INDEX CONCURRENTLY IF EXISTS messages_alias_sent_idx;
DROP INDEX CONCURRENTLY IF EXISTS messages_search_bigrams_idx;
DROP FUNCTION IF EXISTS search_terms_bigrams(text[]);
DROP FUNCTION IF EXISTS message_search_bigrams(text, jsonb);
DROP FUNCTION IF EXISTS search_bigrams(text);
