-- +goose Up
-- Accounts (R40-R44, ADR 0004). Authentik holds credentials; the app holds
-- roles, status, who invited whom, and its own sessions.
CREATE TABLE users (
    id             bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    -- Authentik's user id (the OIDC subject). NULL once the account is
    -- deleted, or while an admin's re-invitation waits to be used.
    subject        text UNIQUE,
    -- As entered, and normalised for comparison (case and +tag ignored,
    -- owner 2026-10-13). Both NULL once the account is deleted (R43c).
    email          text,
    email_key      text UNIQUE,
    name           text,
    role           text NOT NULL DEFAULT 'user' CHECK (role IN ('user', 'admin')),
    status         text NOT NULL DEFAULT 'active' CHECK (status IN ('active', 'deactivated', 'deleted')),
    invited_by     bigint REFERENCES users (id), -- NULL only for the first admin
    created_at     timestamptz NOT NULL DEFAULT now(),
    deactivated_at timestamptz,
    deleted_at     timestamptz,
    CHECK ((status = 'deleted') = (email IS NULL)),
    CHECK (status <> 'deleted' OR (email_key IS NULL AND name IS NULL AND subject IS NULL))
);
CREATE INDEX users_invited_by_idx ON users (invited_by);

-- Invitations made in the app (R43a). Authentik holds the matching
-- single-use invitation; authentik_id is its id, for cancelling it.
CREATE TABLE invitations (
    id           bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    email        text,          -- NULL once the person it enrolled deletes their account
    email_key    text,
    invited_by   bigint REFERENCES users (id), -- NULL: the first admin's set-up invitation
    first_admin  boolean NOT NULL DEFAULT false,
    reactivates  bigint REFERENCES users (id), -- an admin's re-invitation of a deactivated user
    authentik_id text,
    status       text NOT NULL DEFAULT 'pending' CHECK (status IN ('pending', 'used', 'cancelled')),
    created_at   timestamptz NOT NULL DEFAULT now(),
    expires_at   timestamptz NOT NULL,
    used_by      bigint REFERENCES users (id),
    used_at      timestamptz,
    cancelled_at timestamptz,
    CHECK (status = 'used' OR email IS NOT NULL)
);
CREATE INDEX invitations_invited_by_idx ON invitations (invited_by);
CREATE INDEX invitations_pending_idx ON invitations (email_key) WHERE status = 'pending';

-- Signed-in browsers. Only a hash of the cookie's token is stored.
CREATE TABLE sessions (
    token_hash   bytea PRIMARY KEY,
    user_id      bigint NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    created_at   timestamptz NOT NULL DEFAULT now(),
    last_used_at timestamptz NOT NULL DEFAULT now(),
    expires_at   timestamptz NOT NULL
);
CREATE INDEX sessions_user_idx ON sessions (user_id);

-- +goose Down
DROP TABLE sessions;
DROP TABLE invitations;
DROP TABLE users;
