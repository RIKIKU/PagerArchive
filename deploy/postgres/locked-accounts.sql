-- The "accounts locked" metric (SPEC.md R52, R44a; ADR 0006, owner decision
-- Q1), in Authentik's database. pagerarchive_locked_accounts() is the number
-- of accounts the guessing protection has locked now, by the same rule as the
-- pagerarchive-not-locked policy in deploy/authentik/blueprints. It returns
-- only that count. The role pagerarchive_metrics may call it, and
-- pagerarchive_authentik_events() below, and read nothing
-- else. Run by metrics-role.sql on a new install and by api-role.sh, and by
-- the API on every start while it connects as the superuser (EnsureLocksRole).
--
-- The rule: 5 failures within 15 minutes lock an account until 15 minutes after
-- the fifth; a successful sign-in starts the count again; 5 wrong enrolment
-- codes on the account's email device lock it too. PL/pgSQL, so Authentik's
-- tables needn't exist yet when this runs on a new install.
CREATE OR REPLACE FUNCTION public.pagerarchive_locked_accounts() RETURNS integer
LANGUAGE plpgsql STABLE SECURITY DEFINER SET search_path = pg_catalog, public AS $fn$
DECLARE
	lim CONSTANT integer := 5;
	win CONSTANT interval := interval '15 minutes';
	t timestamptz := now();
	u record;
	f timestamptz;
	since timestamptz;
	last_login timestamptz;
	locked_until timestamptz;
	dev_until timestamptz;
	run timestamptz[];
	n integer := 0;
BEGIN
	FOR u IN
		SELECT cu.id, cu.username FROM public.authentik_core_user cu
		WHERE cu.username IN (SELECT e.context ->> 'username' FROM public.authentik_events_event e
		                      WHERE e.action = 'login_failed' AND e.created > t - 2 * win)
		   OR cu.id IN (SELECT d.user_id FROM public.authentik_stages_authenticator_email_emaildevice d
		                WHERE d.throttling_failure_count >= lim AND d.throttling_failure_timestamp > t - win)
	LOOP
		since := t - 2 * win;
		SELECT max(e.created) INTO last_login FROM public.authentik_events_event e
		WHERE e.action = 'login' AND e."user" ->> 'pk' = u.id::text AND e.created > since;
		IF last_login IS NOT NULL THEN
			since := last_login;
		END IF;
		locked_until := NULL;
		run := '{}';
		FOR f IN SELECT e.created FROM public.authentik_events_event e
		         WHERE e.action = 'login_failed' AND e.context ->> 'username' = u.username AND e.created > since
		         ORDER BY e.created
		LOOP
			run := ARRAY(SELECT x FROM unnest(run) x WHERE f - x < win) || f;
			IF cardinality(run) >= lim THEN
				locked_until := f + win;
				run := '{}';
			END IF;
		END LOOP;
		SELECT max(d.throttling_failure_timestamp) + win INTO dev_until
		FROM public.authentik_stages_authenticator_email_emaildevice d
		WHERE d.user_id = u.id AND d.throttling_failure_count >= lim AND d.throttling_failure_timestamp IS NOT NULL;
		IF dev_until IS NOT NULL AND (locked_until IS NULL OR dev_until > locked_until) THEN
			locked_until := dev_until;
		END IF;
		IF locked_until > t THEN
			n := n + 1;
		END IF;
	END LOOP;
	RETURN n;
END
$fn$;
ALTER FUNCTION public.pagerarchive_locked_accounts() OWNER TO authentik;
REVOKE ALL ON FUNCTION public.pagerarchive_locked_accounts() FROM PUBLIC;
GRANT EXECUTE ON FUNCTION public.pagerarchive_locked_accounts() TO pagerarchive_metrics;

-- The "Authentik events" metric (SPEC.md R52, R89b), in the same way:
-- pagerarchive_authentik_events() counts Authentik's events of a fixed list of
-- kinds in the last 5 minutes, and a failed sign-in's reason as the kind of
-- stage it failed at. It returns one row for every kind and reason, zero
-- included, and nothing else: never a username, an address or an event's
-- text (P1, R70). Authentik keeps no such counter itself.
CREATE OR REPLACE FUNCTION public.pagerarchive_authentik_events() RETURNS TABLE (action text, reason text, n integer)
LANGUAGE plpgsql STABLE SECURITY DEFINER SET search_path = pg_catalog, public AS $fn$
BEGIN
	RETURN QUERY
	WITH kinds (action, reason) AS (VALUES
		('login', ''), ('login_failed', 'password'), ('login_failed', 'second_factor'), ('login_failed', 'other'),
		('system_exception', ''), ('system_task_exception', ''), ('configuration_error', ''),
		('policy_exception', ''), ('property_mapping_exception', '')),
	recent AS (
		SELECT e.action, CASE
			WHEN e.action <> 'login_failed' THEN ''
			WHEN e.context -> 'stage' ->> 'model_name' = 'passwordstage' THEN 'password'
			WHEN e.context -> 'stage' ->> 'model_name' = 'authenticatorvalidatestage' THEN 'second_factor'
			ELSE 'other' END AS reason
		FROM public.authentik_events_event e
		WHERE e.created > now() - interval '5 minutes'
		  AND e.action IN (SELECT k.action FROM kinds k))
	SELECT k.action, k.reason, (SELECT count(*) FROM recent r WHERE r.action = k.action AND r.reason = k.reason)::integer
	FROM kinds k
	ORDER BY k.action, k.reason;
END
$fn$;
ALTER FUNCTION public.pagerarchive_authentik_events() OWNER TO authentik;
REVOKE ALL ON FUNCTION public.pagerarchive_authentik_events() FROM PUBLIC;
GRANT EXECUTE ON FUNCTION public.pagerarchive_authentik_events() TO pagerarchive_metrics;
