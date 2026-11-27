-- +goose Up
-- R77f (owner, 2026-11-28; ADR 0055): who sees the app switcher is the
-- Authentik group RADIOARCHIVE_GROUP, the one RadioArchive's gate admits, so
-- the mark kept on each user (migration 38) goes.
--
-- switcher_marks_to_move: the users an admin had marked, for the API to add
-- to the group once, in the background after a start with RADIOARCHIVE_URL
-- set. A row goes once Authentik has them in the group; one whose user has
-- never signed in waits for a later start.
--
-- R91a: the copy is the few marked users, and dropping a column changes only
-- the catalogue.
CREATE TABLE switcher_marks_to_move (
    user_id bigint PRIMARY KEY REFERENCES users (id) ON DELETE CASCADE
);
INSERT INTO switcher_marks_to_move (user_id) SELECT id FROM users WHERE sees_switcher;
ALTER TABLE users DROP COLUMN sees_switcher;

-- +goose Down
-- The marks still waiting come back; one already moved is in the group.
ALTER TABLE users ADD COLUMN sees_switcher boolean NOT NULL DEFAULT false;
UPDATE users SET sees_switcher = true WHERE id IN (SELECT user_id FROM switcher_marks_to_move);
DROP TABLE switcher_marks_to_move;
