-- +goose Up
-- R92a (owner, 2026-11-24; #106 item 3): the fireground channel numbers a
-- page names, found by the network profile's fireground_channel_pattern
-- when it's parsed. Nullable with no default, so adding it rewrites no rows
-- (R91a), and nothing is copied in: every page's is NULL until the profile's
-- pattern re-parses it, as a long job (R23a, R91).
ALTER TABLE messages ADD COLUMN fireground_channels integer[];

-- +goose Down
ALTER TABLE messages DROP COLUMN fireground_channels;
