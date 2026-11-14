-- +goose Up
-- R64 (owner, 2026-11-14): template names are unique ignoring letter case, so
-- there can't be both "EMV" and "emv". An install may already have names
-- like that, and the upgrade mustn't fail on them: in each such group the
-- oldest template stays under the new unique index, and the others are marked
-- case_duplicate, outside it, until one is renamed (which clears the mark) or
-- deleted. Nothing is renamed or merged here: which to keep is the admins'
-- choice. The API logs every group on each start, and Administration lists
-- them (catalog.CaseDuplicates).
ALTER TABLE templates ADD COLUMN case_duplicate boolean NOT NULL DEFAULT false;

UPDATE templates t SET case_duplicate = true
WHERE EXISTS (SELECT 1 FROM templates o WHERE lower(o.name) = lower(t.name) AND o.id < t.id);

CREATE UNIQUE INDEX templates_name_lower_key ON templates (lower(name)) WHERE NOT case_duplicate;

-- +goose Down
DROP INDEX templates_name_lower_key;
ALTER TABLE templates DROP COLUMN case_duplicate;
