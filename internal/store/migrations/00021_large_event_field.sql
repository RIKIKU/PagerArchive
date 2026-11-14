-- +goose Up
-- R87 (owner, 2026-11-14; ADR 0029): whether one of a page's fields holds a
-- value its agency profile lists in large_event_fields, such as a high alarm
-- level, which opens a large event as a code does. Set by ingest and by
-- every re-parse (R23a), like the other parsed columns, so it follows the
-- profile. A constant default: adding it rewrites no rows, and pages stored
-- before it read false until they're re-parsed (a profile change re-parses
-- them all).
ALTER TABLE messages ADD COLUMN large_event_field boolean NOT NULL DEFAULT false;

-- +goose Down
ALTER TABLE messages DROP COLUMN large_event_field;
