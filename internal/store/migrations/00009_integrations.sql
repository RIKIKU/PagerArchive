-- +goose Up
-- Per-user integrations (R47-R51c, ADR 0006). An integration is a selection
-- (capcodes and aliases), a filter and a destination. Secrets are one
-- AES-GCM blob per integration, keyed outside the database (ADR 0006 §4).
CREATE TABLE integrations (
    id                bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    uid               text NOT NULL UNIQUE CHECK (uid ~ '^[a-z0-9]{12}$'), -- MQTT topics, Home Assistant IDs, the secrets' associated data
    owner_id          bigint NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    name              text NOT NULL,
    kind              text NOT NULL CHECK (kind IN ('webhook', 'mqtt', 'discord', 'telegram', 'pushover', 'ntfy')),
    content           text NOT NULL CHECK (content IN ('full', 'summary')),
    config            jsonb NOT NULL DEFAULT '{}', -- non-secret settings
    secrets           bytea,                       -- encrypted (ADR 0006 §4)
    filter            jsonb NOT NULL DEFAULT '{}',
    status            text NOT NULL DEFAULT 'active' CHECK (status IN ('active', 'paused')),
    paused_by         text CHECK (paused_by IN ('failing', 'owner', 'admin')),
    paused_at         timestamptz,
    failed_pages      integer NOT NULL DEFAULT 0, -- undelivered pages in a row (ADR 0006 §3)
    last_error        text,                       -- never page text (P1)
    last_error_at     timestamptz,
    last_delivered_at timestamptz,
    created_at        timestamptz NOT NULL DEFAULT now(),
    updated_at        timestamptz NOT NULL DEFAULT now(),
    CHECK ((status = 'paused') = (paused_by IS NOT NULL))
);
CREATE INDEX integrations_owner_idx ON integrations (owner_id);

-- R48: the selection. Capcodes are exact and zero-padded; an alias selects
-- the messages that resolved to it.
CREATE TABLE integration_capcodes (
    integration_id bigint NOT NULL REFERENCES integrations (id) ON DELETE CASCADE,
    capcode        text NOT NULL CHECK (capcode ~ '^[0-9]{7}$'),
    PRIMARY KEY (integration_id, capcode)
);
CREATE INDEX integration_capcodes_capcode_idx ON integration_capcodes (capcode);

CREATE TABLE integration_aliases (
    integration_id bigint NOT NULL REFERENCES integrations (id) ON DELETE CASCADE,
    alias_id       bigint NOT NULL REFERENCES aliases (id) ON DELETE CASCADE,
    PRIMARY KEY (integration_id, alias_id)
);
CREATE INDEX integration_aliases_alias_idx ON integration_aliases (alias_id);

-- The delivery queue, which survives a restart. One row per page per
-- integration: the primary key is R51b's "never sent twice". No payload is
-- stored; it's built from the message when sent.
CREATE TABLE deliveries (
    integration_id  bigint NOT NULL REFERENCES integrations (id) ON DELETE CASCADE,
    message_id      bigint NOT NULL REFERENCES messages (id) ON DELETE CASCADE,
    status          text NOT NULL CHECK (status IN ('pending', 'delivered', 'failed', 'too_old', 'cancelled')),
    attempts        integer NOT NULL DEFAULT 0,
    next_attempt_at timestamptz,
    last_result     text, -- HTTP status or MQTT result of the last attempt
    last_error      text, -- never page text (P1)
    delivered_at    timestamptz,
    created_at      timestamptz NOT NULL DEFAULT now(),
    updated_at      timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (integration_id, message_id),
    CHECK ((status = 'pending') = (next_attempt_at IS NOT NULL))
);
CREATE INDEX deliveries_due_idx ON deliveries (next_attempt_at) WHERE status = 'pending';
CREATE INDEX deliveries_recent_idx ON deliveries (integration_id, created_at DESC);
CREATE INDEX deliveries_message_idx ON deliveries (message_id);

-- R52: flag counts stay cheap on a large archive.
CREATE INDEX messages_parse_failed_idx ON messages (id) WHERE parse_failed;
CREATE INDEX messages_address_unsplit_idx ON messages (id) WHERE address_unsplit;
CREATE INDEX messages_location_mismatch_idx ON messages (id) WHERE location_mismatch;

-- +goose Down
DROP INDEX messages_location_mismatch_idx;
DROP INDEX messages_address_unsplit_idx;
DROP INDEX messages_parse_failed_idx;
DROP TABLE deliveries;
DROP TABLE integration_aliases;
DROP TABLE integration_capcodes;
DROP TABLE integrations;
