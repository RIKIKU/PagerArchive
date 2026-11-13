-- +goose Up
-- R87-R87d (owner, 2026-11-13; ADR 0027): a large event is simply a large
-- event. It has no levels, and the app and its integrations show jobs, never
-- the codes that opened it, so what 00018 kept for them goes. An integration
-- selects large events or pages, and is told once, when an event opens.

-- The levels and when each was met.
ALTER TABLE large_events
    DROP COLUMN deployment_at,
    DROP COLUMN several_deployments_at,
    DROP COLUMN big_job_at;

-- The codes found in each page: they open an event as the page arrives and
-- are never counted or shown afterwards.
ALTER TABLE large_event_messages DROP COLUMN deployment_codes;

-- An integration's level becomes yes or no: one that selected large events
-- at any level selects large events.
ALTER TABLE integrations DROP CONSTRAINT integrations_large_events_check;
ALTER TABLE integrations ALTER COLUMN large_events TYPE boolean USING large_events IS NOT NULL;
ALTER TABLE integrations ALTER COLUMN large_events SET DEFAULT false;
ALTER TABLE integrations ALTER COLUMN large_events SET NOT NULL;

-- +goose Down
ALTER TABLE integrations ALTER COLUMN large_events DROP NOT NULL;
ALTER TABLE integrations ALTER COLUMN large_events DROP DEFAULT;
ALTER TABLE integrations ALTER COLUMN large_events TYPE text
    USING CASE WHEN large_events THEN 'deployment' END;
ALTER TABLE integrations ADD CONSTRAINT integrations_large_events_check
    CHECK (large_events IN ('deployment', 'several_deployments', 'big_job'));
ALTER TABLE large_event_messages ADD COLUMN deployment_codes text[] NOT NULL DEFAULT '{}';
ALTER TABLE large_events
    ADD COLUMN deployment_at timestamptz,
    ADD COLUMN several_deployments_at timestamptz,
    ADD COLUMN big_job_at timestamptz;
