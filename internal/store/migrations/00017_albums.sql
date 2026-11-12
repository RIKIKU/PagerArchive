-- +goose Up
-- R82-R82b, R82f (ADR 0025): albums. An album is one user's named list of
-- messages, private to them: every query reads it by its owner. Its
-- description and each message's note are the user's own words, not page
-- text: kept as written (the redaction rules don't apply to them), never
-- searched, never logged.
CREATE TABLE albums (
    id          bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    owner_id    bigint NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    name        text NOT NULL CHECK (char_length(name) BETWEEN 1 AND 100),
    -- NULL for none: an empty description is stored as none.
    description text CHECK (char_length(description) BETWEEN 1 AND 2000),
    created_at  timestamptz NOT NULL DEFAULT now(),
    updated_at  timestamptz NOT NULL DEFAULT now(),
    -- R82a: when the nightly run's retention left it empty and kept it for its
    -- description; cleared when a message is added again.
    emptied_at  timestamptz,
    -- The nightly run's mark on an album that retention took a message from,
    -- until the run has decided whether it's now empty (kept across a run
    -- that stopped part-way, so the next one finishes it).
    retention_touched boolean NOT NULL DEFAULT false
);
-- One name per person, case aside, so the menu's entries can be told apart.
CREATE UNIQUE INDEX albums_owner_name_idx ON albums (owner_id, lower(name));

-- The messages in each album, each with its own note (R82f): the same message
-- in two albums has a note in each. A message deleted by retention leaves its
-- albums, and its notes go with it (R82a).
CREATE TABLE album_messages (
    album_id   bigint NOT NULL REFERENCES albums (id) ON DELETE CASCADE,
    message_id bigint NOT NULL REFERENCES messages (id) ON DELETE CASCADE,
    note       text CHECK (char_length(note) BETWEEN 1 AND 500),
    added_at   timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (album_id, message_id)
);
-- Retention deletes messages by the thousand: finding their albums, and the
-- cascade, look up each message here.
CREATE INDEX album_messages_message_idx ON album_messages (message_id);

-- +goose Down
DROP TABLE album_messages;
DROP TABLE albums;
