-- +goose Up
-- Template icons are images stored with the template (R65, ADR 0011): a
-- 64×64 PNG the API encoded, and its SHA-256 in hex, which names it in its
-- URL. The old text column held icon names from CSV imports, which nothing
-- used (R30), so they're dropped.
ALTER TABLE templates DROP COLUMN icon;
ALTER TABLE templates
    ADD COLUMN icon_png bytea CHECK (octet_length(icon_png) BETWEEN 1 AND 65536),
    ADD COLUMN icon_sha256 text CHECK (icon_sha256 ~ '^[0-9a-f]{64}$'),
    ADD CONSTRAINT templates_icon_check CHECK ((icon_png IS NULL) = (icon_sha256 IS NULL));
CREATE INDEX templates_icon_sha256_idx ON templates (icon_sha256);

-- +goose Down
DROP INDEX templates_icon_sha256_idx;
ALTER TABLE templates
    DROP CONSTRAINT templates_icon_check,
    DROP COLUMN icon_sha256,
    DROP COLUMN icon_png,
    ADD COLUMN icon text;
