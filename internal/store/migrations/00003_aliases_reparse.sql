-- +goose Up
-- R27: an ignored alias hides its pages from the default list and live
-- updates; they are still stored and searchable.
ALTER TABLE aliases ADD COLUMN ignored boolean NOT NULL DEFAULT false;

-- The alias that matched when the message was last parsed. Names are read
-- through the join, so a renamed alias shows its current name (R23a).
ALTER TABLE messages ADD COLUMN alias_id bigint REFERENCES aliases (id) ON DELETE SET NULL;
CREATE INDEX messages_alias_idx ON messages (alias_id);
CREATE INDEX messages_group_idx ON messages (paging_group, sent_at DESC);
CREATE INDEX messages_text_trgm_idx ON messages USING gin (text gin_trgm_ops);

-- R26 (computed from milestone M4); the admin flag filter (R33) needs it now.
ALTER TABLE messages ADD COLUMN location_mismatch boolean NOT NULL DEFAULT false;

-- R23a: re-parse requests. capcode_pattern uses LIKE syntax; '%' means every
-- message. Written by the API, the CLI or start-up, consumed by the API's
-- background worker.
CREATE TABLE reparse_requests (
    id              bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    capcode_pattern text NOT NULL,
    reason          text NOT NULL,
    requested_at    timestamptz NOT NULL DEFAULT now()
);

-- Small key/value settings, e.g. the profile hashes last parsed with.
CREATE TABLE settings (
    key   text PRIMARY KEY,
    value text NOT NULL
);

-- +goose Down
DROP TABLE settings;
DROP TABLE reparse_requests;
ALTER TABLE messages DROP COLUMN location_mismatch;
DROP INDEX messages_text_trgm_idx;
DROP INDEX messages_group_idx;
ALTER TABLE messages DROP COLUMN alias_id;
ALTER TABLE aliases DROP COLUMN ignored;
