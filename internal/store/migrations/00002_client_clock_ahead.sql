-- +goose Up
-- Set when the server rejects a page because its receive time is more than
-- 60 s ahead of the server's clock; cleared by the next accepted page
-- (ADR 0002).
ALTER TABLE clients ADD COLUMN clock_ahead_since timestamptz;

-- +goose Down
ALTER TABLE clients DROP COLUMN clock_ahead_since;
