-- The locked-accounts metric's role, pagerarchive_metrics (SPEC.md R52, R84i;
-- ADR 0006, ADR 0033), in Authentik's database. Run as the superuser, in the
-- database authentik, by 20-pagerarchive.sh on a new install and by
-- api-role.sh. Safe to run again. The role may connect to Authentik's database
-- and call pagerarchive_locked_accounts() and pagerarchive_authentik_events(),
-- and nothing else. The password is set
-- by whoever runs this. While the API connects as the superuser, it does the
-- same on every start (EnsureLocksRole).
SELECT 'CREATE ROLE pagerarchive_metrics' WHERE NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'pagerarchive_metrics')
\gexec
ALTER ROLE pagerarchive_metrics LOGIN NOSUPERUSER NOCREATEDB NOCREATEROLE;
GRANT CONNECT ON DATABASE authentik TO pagerarchive_metrics;
\ir locked-accounts.sql
