-- +goose Up
-- R40b (ADR 0038): the ID token Authentik issued when this session signed
-- in, kept with the session so that signing out can send it to Authentik's
-- end-session endpoint as its id_token_hint, which Authentik needs before it
-- returns the browser to the app's sign-in page. It never goes to the
-- browser's pages and is never logged; it goes with the session. Sessions
-- from before this have none.
ALTER TABLE sessions ADD COLUMN id_token text;

-- +goose Down
ALTER TABLE sessions DROP COLUMN id_token;
