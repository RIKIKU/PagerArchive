-- +goose Up
-- An alias may have no template (owner decision, 2026-10-11): its name is
-- shown, and its pages are text only.
ALTER TABLE aliases ALTER COLUMN template_id DROP NOT NULL;

-- +goose Down
DELETE FROM aliases WHERE template_id IS NULL;
ALTER TABLE aliases ALTER COLUMN template_id SET NOT NULL;
