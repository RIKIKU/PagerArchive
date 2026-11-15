-- +goose Up
-- R27b (owner, 2026-11-15): alias names are unique ignoring letter case, so
-- there can't be two aliases called "Station 1", or "Station 1" and
-- "station 1". An install may already have names like that, and the upgrade
-- mustn't fail on them: in each such group the oldest alias stays under the
-- new unique index, and the others are marked name_duplicate, outside it,
-- until one is renamed (which clears the mark) or deleted. Nothing is renamed
-- here: which name each gets is the admins' choice. The API logs every group
-- on each start, and Administration lists them (catalog.NameDuplicates).
ALTER TABLE aliases ADD COLUMN name_duplicate boolean NOT NULL DEFAULT false;

-- Finding the aliases with a name, ignoring case, marked or not: each new
-- name is checked against them all (catalog.aliasNameTaken), an import's too.
CREATE INDEX aliases_name_lower_idx ON aliases (lower(name));

UPDATE aliases a SET name_duplicate = true
WHERE EXISTS (SELECT 1 FROM aliases o WHERE lower(o.name) = lower(a.name) AND o.id < a.id);

CREATE UNIQUE INDEX aliases_name_lower_key ON aliases (lower(name)) WHERE NOT name_duplicate;

-- +goose Down
DROP INDEX aliases_name_lower_key;
DROP INDEX aliases_name_lower_idx;
ALTER TABLE aliases DROP COLUMN name_duplicate;
