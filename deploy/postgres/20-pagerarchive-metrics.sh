#!/bin/sh
# The "accounts locked" metric (SPEC.md R52, R44a; ADR 0006, owner decision
# Q1). Creates, in Authentik's database:
#   - pagerarchive_locked_accounts(): the number of accounts the guessing
#     protection has locked now, by the same rule as the
#     pagerarchive-not-locked policy in deploy/authentik/blueprints. It
#     returns only that count.
#   - the role pagerarchive_metrics, which may connect and call that
#     function, and read nothing else.
#
# Runs once when the data volume is new (after 10-authentik.sh). An existing
# install runs it by hand, once (docs/deploy.md, "Upgrading"):
#   docker compose exec db sh /docker-entrypoint-initdb.d/20-pagerarchive-metrics.sh
# It's safe to run again. Without AUTHENTIK_METRICS_DB_PASSWORD it does nothing.
set -eu
if [ -z "${AUTHENTIK_METRICS_DB_PASSWORD:-}" ]; then
	echo "20-pagerarchive-metrics: AUTHENTIK_METRICS_DB_PASSWORD isn't set; skipped"
	exit 0
fi
psql -v ON_ERROR_STOP=1 -v pw="$AUTHENTIK_METRICS_DB_PASSWORD" --username "${POSTGRES_USER:-postgres}" --dbname authentik <<'SQL'
SELECT 'CREATE ROLE pagerarchive_metrics' WHERE NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'pagerarchive_metrics') \gexec
ALTER ROLE pagerarchive_metrics LOGIN NOSUPERUSER NOCREATEDB NOCREATEROLE PASSWORD :'pw';
GRANT CONNECT ON DATABASE authentik TO pagerarchive_metrics;

-- The same rule as the pagerarchive-not-locked policy: 5 failures within 15
-- minutes lock an account until 15 minutes after the fifth; a successful
-- sign-in starts the count again; 5 wrong enrolment codes on the account's
-- email device lock it too. PL/pgSQL, so Authentik's tables needn't exist
-- yet when this runs on a new install.
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
SQL
echo "20-pagerarchive-metrics: role pagerarchive_metrics and pagerarchive_locked_accounts() ready"
