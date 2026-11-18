-- +goose Up
-- R52, R91a: messages is vacuumed and analysed after a set number of changed
-- rows, not a fifth of the table. With PostgreSQL's defaults, a large archive
-- isn't vacuumed until a fifth of its rows are dead, so after a mass rewrite
-- (a re-parse of the whole archive) the dead rows and the stale visibility
-- map can stay until the nightly VACUUM (R45), and index-only scans read the
-- table for every row they count. These are storage settings only: no rows
-- are read or rewritten, and the lock taken doesn't stop reads or writes.
-- Vacuum: after 50 + 20 % of the rows as before (a small archive is
-- unchanged), but never more than 50,000 dead rows (PostgreSQL 18). Analyse:
-- after 2 % of the rows have changed, not 10 %.
ALTER TABLE messages SET (
    autovacuum_vacuum_max_threshold = 50000,
    autovacuum_analyze_scale_factor = 0.02
);

-- +goose Down
ALTER TABLE messages RESET (autovacuum_vacuum_max_threshold, autovacuum_analyze_scale_factor);
