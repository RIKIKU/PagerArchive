-- +goose Up
-- R82d (ADR 0028): each user's look, saved on the account so it follows
-- them to every browser and device. Harbour is the default, so every
-- account that exists before this migration keeps the look it had.
ALTER TABLE users ADD COLUMN look text NOT NULL DEFAULT 'harbour'
    CHECK (look IN ('harbour', 'console'));

-- +goose Down
ALTER TABLE users DROP COLUMN look;
