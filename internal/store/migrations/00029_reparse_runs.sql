-- +goose Up
-- R23c (ADR 0043): the re-parse's run is kept in the database, so a crash,
-- restart or upgrade carries on from the last batch it finished, and an
-- admin can pause, carry on, stop or start it again. One run at a time:
-- the latest, kept after it finishes so its numbers can still be shown.
-- Counts and codes only (P1-P3): reasons are R23b's codes, never a
-- request's text.
CREATE TABLE reparse_runs (
    id          bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    state       text NOT NULL CHECK (state IN ('running', 'paused', 'stopped', 'finished')),
    reasons     text NOT NULL,           -- R23b's reason codes, comma-separated
    requests    integer NOT NULL,        -- how many requests it does
    total       bigint NOT NULL,         -- messages to do, counted when it (last) started
    done        bigint NOT NULL DEFAULT 0,
    -- Raised each time it starts again from the beginning, so a batch
    -- finishing as it does doesn't write its place over the new start.
    epoch       integer NOT NULL DEFAULT 0,
    started_at  timestamptz NOT NULL DEFAULT now(),
    changed_at  timestamptz NOT NULL DEFAULT now(), -- its last change of state
    finished_at timestamptz
);
CREATE UNIQUE INDEX reparse_runs_one ON reparse_runs ((true));

-- Each capcode pattern the run walks, by message ID, and the last ID done:
-- written in the same transaction as the batch of pages it ends.
CREATE TABLE reparse_run_patterns (
    run_id          bigint NOT NULL REFERENCES reparse_runs (id) ON DELETE CASCADE,
    capcode_pattern text NOT NULL,
    after_id        bigint NOT NULL DEFAULT 0,
    done            boolean NOT NULL DEFAULT false,
    PRIMARY KEY (run_id, capcode_pattern)
);

-- The days (UTC) of the pages done that large events are rebuilt around
-- once the run finishes or is stopped (R87f): the first and last sent time
-- of each day's, written with each batch.
CREATE TABLE reparse_run_days (
    run_id  bigint NOT NULL REFERENCES reparse_runs (id) ON DELETE CASCADE,
    day     bigint NOT NULL,
    from_at timestamptz NOT NULL,
    to_at   timestamptz NOT NULL,
    PRIMARY KEY (run_id, day)
);

-- +goose Down
DROP TABLE reparse_run_days;
DROP TABLE reparse_run_patterns;
DROP TABLE reparse_runs;
