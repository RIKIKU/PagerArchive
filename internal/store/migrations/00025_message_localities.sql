-- +goose Up
-- A page's derived locality (R24, R38a, ADR 0033): the address dataset's
-- locality the geocoder settled on, kept for a page whose profile gives no
-- locality field. Derived, so it's beside the page rather than in
-- messages.locality, which holds only what the page's text said (P3): search,
-- the message view, the API's fields, integrations and exports go on showing
-- the parsed locality, and reports count a job's locality as its pages'
-- locality field, else this. One row per page that has one; it goes with the
-- page (retention, R45). The rows for pages stored before this migration are
-- filled in by the API in the background (geocode.BackfillLocalities), so the
-- upgrade itself takes no longer.
CREATE TABLE message_localities (
    message_id bigint PRIMARY KEY REFERENCES messages (id) ON DELETE CASCADE,
    locality   text NOT NULL
);

-- A page's derived locality changing changes its job's locality (R38a), so
-- the job is counted again (ADR 0031 §3). Only for a page with no locality
-- field: one with a field counts that. A row deleted with its page needs
-- nothing here: the page's own delete queued its job. The backfill's
-- transactions (pagerarchive.backfill on) queue nothing: it adds a row for
-- most stored pages, and one rebuild of the counts after it is quicker than
-- counting each job again (ADR 0033 §5).
-- +goose StatementBegin
CREATE FUNCTION report_dirty_locality_new() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    IF current_setting('pagerarchive.backfill', true) = 'on' THEN
        RETURN NULL; -- the backfill's rows: the counts are rebuilt after it (ADR 0033 §5)
    END IF;
    INSERT INTO report_dirty (job_key)
    SELECT DISTINCT 'j' || m.job_number FROM new_rows n JOIN messages m ON m.id = n.message_id
    WHERE m.job_number IS NOT NULL AND m.locality IS NULL;
    RETURN NULL;
END $$;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE FUNCTION report_dirty_locality_old() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    IF current_setting('pagerarchive.backfill', true) = 'on' THEN
        RETURN NULL; -- the backfill's rows: the counts are rebuilt after it (ADR 0033 §5)
    END IF;
    INSERT INTO report_dirty (job_key)
    SELECT DISTINCT 'j' || m.job_number FROM old_rows o JOIN messages m ON m.id = o.message_id
    WHERE m.job_number IS NOT NULL AND m.locality IS NULL;
    RETURN NULL;
END $$;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE FUNCTION report_dirty_locality_changed() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    IF current_setting('pagerarchive.backfill', true) = 'on' THEN
        RETURN NULL; -- the backfill's rows: the counts are rebuilt after it (ADR 0033 §5)
    END IF;
    INSERT INTO report_dirty (job_key)
    SELECT DISTINCT 'j' || m.job_number FROM old_rows o JOIN new_rows n ON n.message_id = o.message_id
    JOIN messages m ON m.id = n.message_id
    WHERE m.job_number IS NOT NULL AND m.locality IS NULL AND o.locality IS DISTINCT FROM n.locality;
    RETURN NULL;
END $$;
-- +goose StatementEnd

CREATE TRIGGER report_message_localities_insert AFTER INSERT ON message_localities
    REFERENCING NEW TABLE AS new_rows FOR EACH STATEMENT EXECUTE FUNCTION report_dirty_locality_new();
CREATE TRIGGER report_message_localities_delete AFTER DELETE ON message_localities
    REFERENCING OLD TABLE AS old_rows FOR EACH STATEMENT EXECUTE FUNCTION report_dirty_locality_old();
CREATE TRIGGER report_message_localities_update AFTER UPDATE ON message_localities
    REFERENCING OLD TABLE AS old_rows NEW TABLE AS new_rows FOR EACH STATEMENT EXECUTE FUNCTION report_dirty_locality_changed();

-- +goose Down
DROP TRIGGER report_message_localities_update ON message_localities;
DROP TRIGGER report_message_localities_delete ON message_localities;
DROP TRIGGER report_message_localities_insert ON message_localities;
DROP FUNCTION report_dirty_locality_changed();
DROP FUNCTION report_dirty_locality_old();
DROP FUNCTION report_dirty_locality_new();
DROP TABLE message_localities;
DELETE FROM settings WHERE key = 'message_localities_backfill';
