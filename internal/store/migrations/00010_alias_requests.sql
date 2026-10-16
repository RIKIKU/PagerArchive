-- +goose Up
-- Aliases in the browser (R53-R58, ADR 0007).

-- R54-R56: requests for a new alias, a change or a deletion. A change holds
-- only the fields it changes (NULL, or set_template false: unchanged).
CREATE TABLE alias_requests (
    id            bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    kind          text NOT NULL CHECK (kind IN ('add', 'change', 'delete')),
    requested_by  bigint NOT NULL REFERENCES users (id),
    -- change and delete: the alias. add: the alias it created, once approved.
    alias_id      bigint REFERENCES aliases (id) ON DELETE SET NULL,
    -- The normalised capcode or pattern asked for, or the alias's when the
    -- request was submitted; alias_name likewise. Kept so a request still
    -- reads well after its alias is deleted.
    pattern       text NOT NULL CHECK (pattern ~ '^[0-9%_]{1,7}$'),
    alias_name    text,
    name          text CHECK (char_length(name) BETWEEN 1 AND 100),
    set_template  boolean NOT NULL DEFAULT false,
    template_id   bigint REFERENCES templates (id) ON DELETE SET NULL, -- NULL with set_template: none
    template_name text,
    ignored       boolean,
    note          text CHECK (char_length(note) BETWEEN 1 AND 500),
    -- The alias's latest history entry when submitted or last edited (0: none);
    -- a newer one means "changed since requested" (ADR 0007 §3).
    base_change   bigint NOT NULL DEFAULT 0,
    status        text NOT NULL DEFAULT 'waiting'
                  CHECK (status IN ('waiting', 'approved', 'rejected', 'withdrawn', 'alias_deleted')),
    feedback      text CHECK (char_length(feedback) BETWEEN 1 AND 500),
    reviewed_by   bigint REFERENCES users (id),
    created_at    timestamptz NOT NULL DEFAULT now(),
    updated_at    timestamptz NOT NULL DEFAULT now(),
    closed_at     timestamptz,
    CHECK (kind <> 'add' OR (name IS NOT NULL AND set_template AND ignored IS NOT NULL)),
    CHECK (kind <> 'change' OR (alias_name IS NOT NULL AND (name IS NOT NULL OR set_template OR ignored IS NOT NULL))),
    CHECK (kind <> 'delete' OR (alias_name IS NOT NULL AND name IS NULL AND NOT set_template AND ignored IS NULL)),
    CHECK ((status = 'rejected') = (feedback IS NOT NULL)),
    CHECK ((status = 'waiting') = (closed_at IS NULL))
);
CREATE INDEX alias_requests_waiting_idx ON alias_requests (id) WHERE status = 'waiting';
CREATE INDEX alias_requests_requested_by_idx ON alias_requests (requested_by);
CREATE INDEX alias_requests_alias_idx ON alias_requests (alias_id);

-- R57: every change to an alias, whoever made it. alias_id has no foreign
-- key on purpose: the history outlives the alias (owner, 2026-10-16), and a
-- deleted alias's entries are found through its "deleted" entry's
-- pattern_key. Identity ids are never reused.
CREATE TABLE alias_changes (
    id           bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    alias_id     bigint NOT NULL,
    pattern_key  text NOT NULL,  -- the alias's normalised capcode or pattern at the time
    alias_name   text NOT NULL,  -- its name at the time
    action       text NOT NULL CHECK (action IN ('added', 'changed', 'deleted')),
    -- field -> [before, after]; templates by name.
    changes      jsonb NOT NULL DEFAULT '{}',
    source       text NOT NULL CHECK (source IN ('admin', 'request', 'cli', 'import')),
    made_by      bigint REFERENCES users (id), -- made or approved it; NULL for cli and import
    requested_by bigint REFERENCES users (id),
    request_id   bigint REFERENCES alias_requests (id),
    at           timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX alias_changes_alias_idx ON alias_changes (alias_id, id);
CREATE INDEX alias_changes_deleted_idx ON alias_changes (pattern_key) WHERE action = 'deleted';

-- +goose Down
DROP TABLE alias_changes;
DROP TABLE alias_requests;
