-- +goose Up
-- R14c, R84p (owner, 2026-11-27: "I think we should have an automated
-- upgrade for this as well"; ADR 0054). What the server knows of each
-- receiving client's version and its automatic upgrades:
--
-- clients.version: the version the client last said it runs (its
-- User-Agent, or its host's automatic upgrades' report): only a version, as
-- the server checks it before storing it. NULL until a client says.
-- clients.behind_since: since when that version has been an older release
-- than the server's (R14b), worked out when the client's version changes and
-- when the server starts; NULL when it isn't behind.
-- clients.auto_upgrade: the client host's automatic upgrades' last report
-- (R84p), only the fields the server keeps (versions, known words, times
-- and counts), and auto_upgrade_at when it came.
--
-- R91a: new columns with no default change only the catalogue.
ALTER TABLE clients
    ADD COLUMN version text,
    ADD COLUMN behind_since timestamptz,
    ADD COLUMN auto_upgrade jsonb,
    ADD COLUMN auto_upgrade_at timestamptz;

-- +goose Down
ALTER TABLE clients
    DROP COLUMN auto_upgrade_at,
    DROP COLUMN auto_upgrade,
    DROP COLUMN behind_since,
    DROP COLUMN version;
