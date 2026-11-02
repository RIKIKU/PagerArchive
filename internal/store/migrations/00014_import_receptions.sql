-- +goose Up
-- R83b, R83c (ADR 0018): a copy of a page read from an import file, kept
-- apart from receptions, whose rows belong to receiving clients. "import" is
-- not a client: it has no row in clients, so the clients list, liveness (R39)
-- and metrics can't see it. source_id is the page's ID in the archive it came
-- from, and never holds page text; a line with none is known by its message's
-- dedup key and its receive time.
CREATE TABLE import_receptions (
    message_id  bigint NOT NULL REFERENCES messages (id) ON DELETE CASCADE,
    received_at timestamptz NOT NULL,
    source_id   text,
    UNIQUE NULLS NOT DISTINCT (message_id, received_at, source_id)
);
CREATE UNIQUE INDEX import_receptions_source_idx ON import_receptions (source_id) WHERE source_id IS NOT NULL;

-- +goose Down
DROP TABLE import_receptions;
