-- +goose Up
-- R77f (owner, 2026-11-25): the soft launch of RadioArchive. "We should use
-- a feature toggle so that only selected users can see the new RadioArchive
-- button in PagerArchive. It should be hidden for the rest."
--
-- users.sees_switcher: an admin marked them to see the app switcher (R77e).
-- Nobody is marked to start with. It only hides the switcher; it isn't
-- access control. "Show the switcher to everyone" is the settings row
-- 'switcher_everyone' ('true' or 'false'; none is false).
--
-- R91a: a constant default changes only the catalogue: no row is
-- rewritten, and nothing is backfilled.
ALTER TABLE users ADD COLUMN sees_switcher boolean NOT NULL DEFAULT false;

-- +goose Down
DELETE FROM settings WHERE key = 'switcher_everyone';
ALTER TABLE users DROP COLUMN sees_switcher;
