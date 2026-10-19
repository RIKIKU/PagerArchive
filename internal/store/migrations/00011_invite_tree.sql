-- +goose Up
-- Trusted users (R42c, ADR 0008 §2): who set the mark and when. Removing it
-- clears both; a deleted account keeps neither (R43c).
ALTER TABLE users
    ADD COLUMN trusted_at timestamptz,
    ADD COLUMN trusted_by bigint REFERENCES users (id),
    ADD CONSTRAINT users_trusted_check CHECK ((trusted_at IS NULL) = (trusted_by IS NULL)),
    ADD CONSTRAINT users_deleted_untrusted_check CHECK (status <> 'deleted' OR trusted_at IS NULL);

-- Deactivation records (R42d, ADR 0008 §2): one per confirmed tree
-- deactivation, kept with no time limit. People are named as they are now.
CREATE TABLE tree_deactivations (
    id                    bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    root_id               bigint NOT NULL REFERENCES users (id),
    actor_id              bigint NOT NULL REFERENCES users (id),
    reason                text CHECK (char_length(reason) <= 500),
    invitations_cancelled integer NOT NULL,
    created_at            timestamptz NOT NULL DEFAULT now()
);

-- Who was deactivated and who was spared. Already-deactivated people and
-- deleted placeholders weren't affected, so they aren't listed.
CREATE TABLE tree_deactivation_people (
    deactivation_id bigint NOT NULL REFERENCES tree_deactivations (id) ON DELETE CASCADE,
    user_id         bigint NOT NULL REFERENCES users (id),
    outcome         text NOT NULL CHECK (outcome IN ('deactivated', 'spared')),
    PRIMARY KEY (deactivation_id, user_id)
);
CREATE INDEX tree_deactivation_people_user_idx ON tree_deactivation_people (user_id);

-- +goose Down
DROP TABLE tree_deactivation_people;
DROP TABLE tree_deactivations;
ALTER TABLE users
    DROP CONSTRAINT users_deleted_untrusted_check,
    DROP CONSTRAINT users_trusted_check,
    DROP COLUMN trusted_by,
    DROP COLUMN trusted_at;
