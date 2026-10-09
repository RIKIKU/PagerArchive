-- +goose Up
CREATE EXTENSION IF NOT EXISTS postgis;
CREATE EXTENSION IF NOT EXISTS pg_trgm;

-- Receivers. Each has its own API key; only a hash is stored (R14).
CREATE TABLE clients (
    id           text PRIMARY KEY CHECK (id ~ '^[a-z0-9][a-z0-9-]{0,62}$'),
    name         text NOT NULL,
    key_hash     bytea NOT NULL UNIQUE,
    created_at   timestamptz NOT NULL DEFAULT now(),
    revoked_at   timestamptz,
    last_seen_at timestamptz -- latest receive time of any page, ignored ones included (R16, R39)
);

-- Shared alias settings (R28).
CREATE TABLE templates (
    id             bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    name           text NOT NULL UNIQUE,
    agency         text NOT NULL,
    icon           text,
    colour         text,
    agency_profile text -- agency profile name; NULL means text only
);

-- Capcode or capcode pattern (% any run, _ one character) to a name (R27).
CREATE TABLE aliases (
    id          bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    pattern     text NOT NULL UNIQUE CHECK (pattern ~ '^[0-9%_]{1,7}$'),
    name        text NOT NULL,
    template_id bigint NOT NULL REFERENCES templates (id) ON DELETE RESTRICT
);

-- One row per transmission per capcode (R19, R20). text is stored after
-- control-token removal and redaction (R17, R18, R22): never the raw text.
CREATE TABLE messages (
    id              bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    capcode         text NOT NULL CHECK (capcode ~ '^[0-9]{7}$'),
    text            text NOT NULL,
    priority        text CHECK (priority IN ('Emergency', 'Non-emergency', 'Admin')),
    heartbeat       boolean NOT NULL DEFAULT false,
    sent_at         timestamptz NOT NULL, -- earliest receive time (R21)
    dedup_key       bytea NOT NULL,       -- sha256(capcode, text)
    format          text,
    job_number      text,
    event_type      text,
    description     text,
    address         text,
    locality        text,
    grid_ref        text,
    paging_group    text,
    units           text[],
    extra           jsonb NOT NULL DEFAULT '{}',
    parse_failed    boolean NOT NULL DEFAULT false,
    address_unsplit boolean NOT NULL DEFAULT false,
    function        smallint, -- POCSAG function code and baud rate: stored, unused (section 14)
    baud            integer,
    created_at      timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX messages_dedup_idx ON messages (dedup_key, sent_at);
CREATE INDEX messages_sent_idx ON messages (sent_at DESC);
CREATE INDEX messages_capcode_idx ON messages (capcode, sent_at DESC);
CREATE INDEX messages_job_idx ON messages (job_number) WHERE job_number IS NOT NULL;

-- Every client's receive time for every copy it heard (R19, R35c).
CREATE TABLE receptions (
    message_id  bigint NOT NULL REFERENCES messages (id) ON DELETE CASCADE,
    client_id   text NOT NULL REFERENCES clients (id),
    received_at timestamptz NOT NULL,
    PRIMARY KEY (message_id, client_id, received_at)
);

-- +goose Down
DROP TABLE receptions;
DROP TABLE messages;
DROP TABLE aliases;
DROP TABLE templates;
DROP TABLE clients;
