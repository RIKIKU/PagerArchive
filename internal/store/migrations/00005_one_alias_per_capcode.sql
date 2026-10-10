-- +goose Up
-- R27a: at most one alias per capcode or pattern, compared after
-- zero-padding exact capcodes to 7 digits (240241 = 0240241). Patterns with
-- wildcards are compared as written. Enforced here, whatever wrote the row.
ALTER TABLE aliases ADD COLUMN pattern_key text GENERATED ALWAYS AS (
    CASE WHEN pattern ~ '^[0-9]+$' THEN lpad(pattern, 7, '0') ELSE pattern END
) STORED;
ALTER TABLE aliases ADD CONSTRAINT aliases_one_per_capcode UNIQUE (pattern_key);

-- +goose Down
ALTER TABLE aliases DROP CONSTRAINT aliases_one_per_capcode;
ALTER TABLE aliases DROP COLUMN pattern_key;
