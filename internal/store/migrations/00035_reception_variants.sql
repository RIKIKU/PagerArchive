-- +goose Up
-- R19a (ADR 0050): a near duplicate, a copy of a page whose text differs a
-- little from its message's, is kept as its client's reception with its own
-- text, so nothing heard is lost and receivers' disagreements can be seen.
-- NULL: the copy's text is its message's, as every copy's was before. The
-- text is stored after control-token removal and redaction, as a message's
-- is (P1), and retroactive redaction covers it (R45a).
--
-- R91a: adding a nullable column with no default changes only the catalogue:
-- no row is rewritten, and nothing is backfilled.
ALTER TABLE receptions ADD COLUMN text text;
ALTER TABLE import_receptions ADD COLUMN text text;

-- +goose Down
ALTER TABLE import_receptions DROP COLUMN text;
ALTER TABLE receptions DROP COLUMN text;
