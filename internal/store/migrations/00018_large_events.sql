-- +goose Up
-- R87-R87e (ADR 0027): large events. An event is a group of pages, derived
-- from them on ingest: Emergency pages to aliased capcodes that carry a
-- deployment code or belong to a big job, and the other pages of their jobs,
-- grouped by 12 hours. Its numbers (deployment codes, jobs, pages) are
-- counted from the pages it still holds, so retention thins them (R87e).
CREATE TABLE large_events (
    id         bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    -- The sent times of its earliest and latest pages: the 12-hour window
    -- that joins pages to it, and its start and end in the app.
    first_at   timestamptz NOT NULL,
    last_at    timestamptz NOT NULL,
    -- R87b: its levels, each with the sent time of the page that first met
    -- it; NULL until met. They're never unset: an integration fires when one
    -- is first set (R87d).
    deployment_at          timestamptz,
    several_deployments_at timestamptz,
    big_job_at             timestamptz,
    created_at timestamptz NOT NULL DEFAULT now(),
    CHECK (first_at <= last_at)
);
-- The events near a page's time, and the list, newest first.
CREATE INDEX large_events_last_idx ON large_events (last_at);
CREATE INDEX large_events_first_idx ON large_events (first_at DESC, id DESC);

-- The pages each event holds: a page is in one event at most. A page deleted
-- by retention leaves its event (R87e). deployment_codes are the codes the
-- network profile's pattern found in it (R5), literal (P3), which are part of
-- the page's text.
CREATE TABLE large_event_messages (
    message_id       bigint PRIMARY KEY REFERENCES messages (id) ON DELETE CASCADE,
    event_id         bigint NOT NULL REFERENCES large_events (id) ON DELETE CASCADE,
    deployment_codes text[] NOT NULL DEFAULT '{}'
);
CREATE INDEX large_event_messages_event_idx ON large_event_messages (event_id);

-- R87d: an integration may select large events, at one level, instead of
-- capcodes and aliases. NULL: it selects pages as before.
ALTER TABLE integrations ADD COLUMN large_events text
    CHECK (large_events IN ('deployment', 'several_deployments', 'big_job'));

-- A large-event delivery is the delivery of the page that took the event to
-- the integration's level: the same queue, timing and "never twice" (R51a,
-- R51b), with the event it's about.
ALTER TABLE deliveries ADD COLUMN event_id bigint REFERENCES large_events (id) ON DELETE CASCADE;
CREATE INDEX deliveries_event_idx ON deliveries (event_id) WHERE event_id IS NOT NULL;

-- +goose Down
DROP INDEX deliveries_event_idx;
ALTER TABLE deliveries DROP COLUMN event_id;
ALTER TABLE integrations DROP COLUMN large_events;
DROP TABLE large_event_messages;
DROP TABLE large_events;
