-- +goose Up
-- R8 (owner, 2026-11-18; ADR 0047): the call signs a page dispatches and
-- the roles it names, apart from its appliances (units), when its agency
-- profile moves them there (into). Nullable with no default, so adding it
-- rewrites no rows (R91a), and nothing is copied in: every page's is NULL
-- until a profile with into re-parses it, as a long job (R23a, R91).
ALTER TABLE messages ADD COLUMN call_signs text[];

-- +goose Down
ALTER TABLE messages DROP COLUMN call_signs;
