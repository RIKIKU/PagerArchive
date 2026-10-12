-- +goose Up
-- At most one pending invitation per address, enforced by the database
-- whatever writes the row (ADR 0004). Expired invitations are cancelled
-- before a new one is made, so they don't block it.
DROP INDEX invitations_pending_idx;
CREATE UNIQUE INDEX invitations_one_pending_idx ON invitations (email_key) WHERE status = 'pending';

-- +goose Down
DROP INDEX invitations_one_pending_idx;
CREATE INDEX invitations_pending_idx ON invitations (email_key) WHERE status = 'pending';
