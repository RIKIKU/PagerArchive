-- +goose Up
-- M24's reports (R38a-R38i, ADR 0031): counts of jobs, kept beside the pages
-- so a report never reads the pages themselves. Everything here is derived
-- from messages: the API rebuilds it in the background on its first start
-- with this migration (and when the deployment's time zone changes), and
-- keeps it in step from report_dirty, which the triggers below fill. Counts
-- and the fields reports group by only, never page text (P1): when a page
-- goes (retention) or its fields change (re-parse, retroactive redaction),
-- its job is counted again from what's left.

-- One row per job (R23): a job number's pages, or a page with no job number
-- on its own. Heartbeats (R35b) and pages to ignored aliases (R27) aren't in
-- any job here. Each value is the job's as R38a defines it: its first page's
-- day and hour in the deployment's time zone, the agency (template) of its
-- first page to an alias (0: none), the first event type and locality any
-- of its pages has ('': none), its highest priority ('': none), and its
-- appliances (R8's units), each once.
CREATE TABLE report_jobs (
    job_key     text PRIMARY KEY, -- 'j' || job_number, or 'm' || message id
    job_number  text,
    first_at    timestamptz NOT NULL,
    last_at     timestamptz NOT NULL,
    day         date NOT NULL,
    hour        smallint NOT NULL,
    template_id bigint NOT NULL,
    event_type  text NOT NULL,
    locality    text NOT NULL,
    priority    text NOT NULL,
    pages       integer NOT NULL,
    capcodes    integer NOT NULL,
    appliances  text[] NOT NULL
);
-- By day, holding what R38g needs to pick the longest jobs of a range of up
-- to two years in an index-only scan.
CREATE INDEX report_jobs_day_idx ON report_jobs (day) INCLUDE (first_at, last_at, template_id, event_type, priority, job_key);
-- R38g: the longest jobs first, for longer ranges.
CREATE INDEX report_jobs_span_idx ON report_jobs ((last_at - first_at) DESC, first_at) WHERE last_at > first_at;

-- Each alias (or capcode with none) a job paged, with the job's values
-- beside it (R38b, R38d).
CREATE TABLE report_job_whos (
    job_key     text NOT NULL REFERENCES report_jobs (job_key) ON DELETE CASCADE,
    who         text NOT NULL, -- 'a' || alias id, or 'c' || capcode
    template_id bigint NOT NULL, -- the alias's template; 0 for a capcode with no alias
    day         date NOT NULL,
    hour        smallint NOT NULL,
    event_type  text NOT NULL,
    priority    text NOT NULL,
    PRIMARY KEY (job_key, who)
);
CREATE INDEX report_job_whos_who_idx ON report_job_whos (who, day) INCLUDE (hour, event_type, priority, job_key);
CREATE INDEX report_job_whos_day_idx ON report_job_whos (day);

-- Jobs per day, by what reports group and filter by. kind: 't' every job
-- (key ''), 'h' the hour a job started (key '0'-'23'), 'l' its locality, 'p' its number of pages, 'c'
-- of capcodes, 'n' of appliances, 'a' one of its appliances, 'w' an alias
-- ('a' || id) or capcode ('c' || capcode) it paged (template_id is then that
-- alias's). A day's rows are counted again whenever one of its jobs changes.
CREATE TABLE report_days (
    kind        text NOT NULL,
    day         date NOT NULL,
    template_id bigint NOT NULL,
    event_type  text NOT NULL,
    priority    text NOT NULL,
    key         text NOT NULL,
    jobs        integer NOT NULL,
    PRIMARY KEY (kind, day, template_id, event_type, priority, key) INCLUDE (jobs)
);

-- Jobs to count again: a queue that only grows by plain inserts, so a page
-- being stored never waits on the writer counting its job again. A job may
-- be in it more than once.
CREATE TABLE report_dirty (
    id      bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    job_key text NOT NULL
);

-- +goose StatementBegin
CREATE FUNCTION report_dirty_new() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    INSERT INTO report_dirty (job_key)
    SELECT DISTINCT COALESCE('j' || n.job_number, 'm' || n.id) FROM new_rows n;
    RETURN NULL;
END $$;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE FUNCTION report_dirty_old() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    INSERT INTO report_dirty (job_key)
    SELECT DISTINCT COALESCE('j' || o.job_number, 'm' || o.id) FROM old_rows o;
    RETURN NULL;
END $$;
-- +goose StatementEnd

-- An update counts its jobs again, old and new, only when it changes what a
-- report reads (a new location or text alone doesn't; the re-parse after a
-- redaction changes the fields, if any).
-- +goose StatementBegin
CREATE FUNCTION report_dirty_changed() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    INSERT INTO report_dirty (job_key)
    SELECT DISTINCT k FROM old_rows o JOIN new_rows n ON n.id = o.id,
         LATERAL (VALUES (COALESCE('j' || o.job_number, 'm' || o.id)), (COALESCE('j' || n.job_number, 'm' || n.id))) AS v (k)
    WHERE (o.job_number, o.sent_at, o.priority, o.heartbeat, o.alias_id, o.capcode, o.event_type, o.locality, o.units)
          IS DISTINCT FROM
          (n.job_number, n.sent_at, n.priority, n.heartbeat, n.alias_id, n.capcode, n.event_type, n.locality, n.units);
    -- A page in a large event with another job number changes its event's
    -- number of jobs (R38i).
    PERFORM report_event_jobs_count(ARRAY(SELECT DISTINCT lem.event_id FROM old_rows o JOIN new_rows n ON n.id = o.id
        JOIN large_event_messages lem ON lem.message_id = n.id WHERE o.job_number IS DISTINCT FROM n.job_number));
    RETURN NULL;
END $$;
-- +goose StatementEnd

-- An alias marked ignored or not, or moved to another template, changes its
-- pages' jobs without changing the pages.
-- +goose StatementBegin
CREATE FUNCTION report_dirty_alias() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    IF (OLD.ignored, OLD.template_id) IS DISTINCT FROM (NEW.ignored, NEW.template_id) THEN
        INSERT INTO report_dirty (job_key)
        SELECT DISTINCT COALESCE('j' || m.job_number, 'm' || m.id) FROM messages m WHERE m.alias_id = NEW.id;
    END IF;
    RETURN NULL;
END $$;
-- +goose StatementEnd

CREATE TRIGGER report_messages_insert AFTER INSERT ON messages
    REFERENCING NEW TABLE AS new_rows FOR EACH STATEMENT EXECUTE FUNCTION report_dirty_new();
CREATE TRIGGER report_messages_delete AFTER DELETE ON messages
    REFERENCING OLD TABLE AS old_rows FOR EACH STATEMENT EXECUTE FUNCTION report_dirty_old();
CREATE TRIGGER report_messages_update AFTER UPDATE ON messages
    REFERENCING OLD TABLE AS old_rows NEW TABLE AS new_rows FOR EACH STATEMENT EXECUTE FUNCTION report_dirty_changed();
CREATE TRIGGER report_aliases_update AFTER UPDATE ON aliases
    FOR EACH ROW EXECUTE FUNCTION report_dirty_alias();

-- R38i: each large event's number of jobs (its pages' job numbers, as R87c
-- counts them), kept as its pages change, so a report over years of events
-- reads one row per event rather than every event's pages.
CREATE TABLE report_event_jobs (
    event_id bigint PRIMARY KEY REFERENCES large_events (id) ON DELETE CASCADE,
    jobs     integer NOT NULL
);

-- +goose StatementBegin
CREATE FUNCTION report_event_jobs_count(ids bigint[]) RETURNS void LANGUAGE sql AS $$
    INSERT INTO report_event_jobs (event_id, jobs)
    SELECT e.id, (SELECT count(DISTINCT m.job_number) FROM large_event_messages lem JOIN messages m ON m.id = lem.message_id WHERE lem.event_id = e.id)
    FROM large_events e WHERE e.id = ANY(ids)
    ON CONFLICT (event_id) DO UPDATE SET jobs = EXCLUDED.jobs
$$;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE FUNCTION report_event_jobs_new() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    PERFORM report_event_jobs_count(ARRAY(SELECT DISTINCT event_id FROM new_rows));
    RETURN NULL;
END $$;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE FUNCTION report_event_jobs_old() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    PERFORM report_event_jobs_count(ARRAY(SELECT DISTINCT event_id FROM old_rows));
    RETURN NULL;
END $$;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE FUNCTION report_event_jobs_moved() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    PERFORM report_event_jobs_count(ARRAY(SELECT event_id FROM old_rows UNION SELECT event_id FROM new_rows));
    RETURN NULL;
END $$;
-- +goose StatementEnd

CREATE TRIGGER report_event_messages_insert AFTER INSERT ON large_event_messages
    REFERENCING NEW TABLE AS new_rows FOR EACH STATEMENT EXECUTE FUNCTION report_event_jobs_new();
CREATE TRIGGER report_event_messages_delete AFTER DELETE ON large_event_messages
    REFERENCING OLD TABLE AS old_rows FOR EACH STATEMENT EXECUTE FUNCTION report_event_jobs_old();
CREATE TRIGGER report_event_messages_update AFTER UPDATE ON large_event_messages
    REFERENCING OLD TABLE AS old_rows NEW TABLE AS new_rows FOR EACH STATEMENT EXECUTE FUNCTION report_event_jobs_moved();

-- The events there are now.
INSERT INTO report_event_jobs (event_id, jobs)
SELECT lem.event_id, count(DISTINCT m.job_number) FROM large_event_messages lem JOIN messages m ON m.id = lem.message_id GROUP BY lem.event_id;

-- +goose Down
DROP TRIGGER report_event_messages_update ON large_event_messages;
DROP TRIGGER report_event_messages_delete ON large_event_messages;
DROP TRIGGER report_event_messages_insert ON large_event_messages;
DROP FUNCTION report_event_jobs_moved();
DROP FUNCTION report_event_jobs_old();
DROP FUNCTION report_event_jobs_new();
DROP FUNCTION report_event_jobs_count(bigint[]);
DROP TABLE report_event_jobs;
DROP TRIGGER report_aliases_update ON aliases;
DROP TRIGGER report_messages_update ON messages;
DROP TRIGGER report_messages_delete ON messages;
DROP TRIGGER report_messages_insert ON messages;
DROP FUNCTION report_dirty_alias();
DROP FUNCTION report_dirty_changed();
DROP FUNCTION report_dirty_old();
DROP FUNCTION report_dirty_new();
DROP TABLE report_dirty;
DROP TABLE report_days;
DROP TABLE report_job_whos;
DROP TABLE report_jobs;
DELETE FROM settings WHERE key = 'reports_built';
