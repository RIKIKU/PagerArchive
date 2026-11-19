-- +goose Up
-- R62a (owner, 2026-11-19; ADR 0048): service accounts, read-only accounts
-- for tools, not people. One is a user row of kind 'service': it has a name
-- but no email, sits in its owning admin's invite tree (invited_by), and its
-- credential (an Authentik app password) expires at credential_expires_at.
-- Revoking it deletes it, as deleting an account does, but keeps its name,
-- so the list says which one was revoked.
ALTER TABLE users
    ADD COLUMN kind text NOT NULL DEFAULT 'person' CHECK (kind IN ('person', 'service')),
    ADD COLUMN credential_expires_at timestamptz,
    ADD CONSTRAINT users_service_check CHECK (
        kind = 'person' AND credential_expires_at IS NULL
        OR kind = 'service' AND email IS NULL AND email_key IS NULL AND role = 'user' AND name IS NOT NULL
           AND invited_by IS NOT NULL AND (status = 'deleted' OR credential_expires_at IS NOT NULL)),
    -- 00006's checks, which assumed every account that isn't deleted has an
    -- email: a person's still do.
    DROP CONSTRAINT users_check,
    DROP CONSTRAINT users_check1,
    ADD CONSTRAINT users_person_email_check CHECK (kind = 'service' OR (status = 'deleted') = (email IS NULL)),
    ADD CONSTRAINT users_deleted_check CHECK (
        status <> 'deleted' OR (email_key IS NULL AND subject IS NULL AND (name IS NULL OR kind = 'service')));
CREATE UNIQUE INDEX users_service_name_idx ON users (name) WHERE kind = 'service' AND status <> 'deleted';

-- +goose Down
DROP INDEX users_service_name_idx;
DELETE FROM users WHERE kind = 'service';
ALTER TABLE users
    DROP CONSTRAINT users_deleted_check,
    DROP CONSTRAINT users_person_email_check,
    DROP CONSTRAINT users_service_check,
    DROP COLUMN credential_expires_at,
    DROP COLUMN kind,
    ADD CONSTRAINT users_check CHECK ((status = 'deleted') = (email IS NULL)),
    ADD CONSTRAINT users_check1 CHECK (status <> 'deleted' OR (email_key IS NULL AND name IS NULL AND subject IS NULL));
