-- +goose Up
-- P1, R45a (ADR 0044): retroactive redaction is a long job (R91). Its run is
-- kept in the database, written in the same transaction as each batch of
-- pages (each changed page's new text and its re-parse together), so a
-- crash, restart or upgrade carries on from the last batch it finished, and
-- an admin can pause, carry on, stop or start it again. One run at a time:
-- the latest, kept after it finishes so its numbers can still be shown.
-- Counts and codes only (P1-P3): never a page's text or a rule's terms.
CREATE TABLE redaction_runs (
    id           bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    state        text NOT NULL CHECK (state IN ('running', 'paused', 'stopped', 'finished')),
    reason       text NOT NULL,           -- a code: rules_changed or started_again
    rules_hash   text NOT NULL,           -- the rules it applies; saved as applied when it finishes
    total        bigint NOT NULL,         -- pages to check, counted when it (last) started
    done         bigint NOT NULL DEFAULT 0, -- pages checked
    redacted     bigint NOT NULL DEFAULT 0, -- pages whose text it changed
    after_id     bigint NOT NULL DEFAULT 0, -- the last page checked
    pages_done   boolean NOT NULL DEFAULT false,
    -- Then the imported source IDs (ADR 0018), in turn: the last one checked.
    source_after text NOT NULL DEFAULT '',
    sources_done boolean NOT NULL DEFAULT false,
    -- Raised each time it starts again from the beginning, so a batch
    -- finishing as it does doesn't write its place over the new start.
    epoch        integer NOT NULL DEFAULT 0,
    started_at   timestamptz NOT NULL DEFAULT now(),
    changed_at   timestamptz NOT NULL DEFAULT now(), -- its last change of state
    finished_at  timestamptz
);
CREATE UNIQUE INDEX redaction_runs_one ON redaction_runs ((true));

-- The days (UTC) of the pages it re-parsed, that large events are rebuilt
-- around once it finishes or is stopped (R87f): written with each batch.
CREATE TABLE redaction_run_days (
    run_id  bigint NOT NULL REFERENCES redaction_runs (id) ON DELETE CASCADE,
    day     bigint NOT NULL,
    from_at timestamptz NOT NULL,
    to_at   timestamptz NOT NULL,
    PRIMARY KEY (run_id, day)
);

-- +goose Down
DROP TABLE redaction_run_days;
DROP TABLE redaction_runs;
