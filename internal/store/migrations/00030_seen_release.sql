-- +goose Up
-- R77b (owner, 2026-11-18): the last release each user was told of, so the
-- "New release" notice shows once per user, on every device. NULL until the
-- first release they open the app on, which is recorded silently.
ALTER TABLE users ADD COLUMN seen_release text;

-- +goose Down
ALTER TABLE users DROP COLUMN seen_release;
