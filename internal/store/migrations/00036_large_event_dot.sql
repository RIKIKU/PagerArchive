-- +goose Up
-- R77c (owner, 2026-11-23): a new large event shows a dot in the side menu
-- until its user opens the Large events page, or 4 hours after it started.
--
-- users.seen_large_events: when the user last opened the Large events page,
-- by the server's clock; NULL until they first do. Kept on the server, so
-- looking on one device clears the dot on the others.
--
-- large_events.opened_at: when the event opened in the archive, by the same
-- clock: the moment the page that opened it was stored. An event is new to a
-- user when it opened after they last looked. It isn't the row's created_at,
-- which a rebuild (R87f) renews when it gives an event a new row: a rebuilt
-- event keeps the newest opened_at of the events whose pages it took, and a
-- merge the newer of the two, so neither brings the dot back for an event
-- the user saw. Only an event a rebuild makes from pages in no event opens
-- then, as live ingest's do. NULL for an event from before this column,
-- which reads as its created_at, the time it was made.
--
-- R91a: nullable columns with no default change only the catalogue: no row
-- is rewritten, and nothing is backfilled.
ALTER TABLE users ADD COLUMN seen_large_events timestamptz;
ALTER TABLE large_events ADD COLUMN opened_at timestamptz;

-- +goose Down
ALTER TABLE large_events DROP COLUMN opened_at;
ALTER TABLE users DROP COLUMN seen_large_events;
