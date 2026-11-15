#!/bin/sh
# PagerArchive's own roles in the shared PostgreSQL container (SPEC.md R84i,
# R52; ADR 0032). Part of the release's archive; runs once, when the data
# volume is new, after 10-authentik.sh. The SQL files are mounted beside it,
# in pagerarchive/, which the image's start-up leaves alone.
#
# - PAGERARCHIVE_DB_PASSWORD set: the API's role, pagerarchive (api-role.sql),
#   with that password. Without it (and PAGERARCHIVE_DB_USER) the API connects
#   as the superuser, as an install from before the role does, and warns on
#   every start.
# - AUTHENTIK_METRICS_DB_PASSWORD set: the locked-accounts metric's role and
#   function in Authentik's database, which the API can't make once it isn't
#   the superuser.
#
# Passwords reach psql through the environment (\getenv), never its command line.
set -eu
dir=/docker-entrypoint-initdb.d/pagerarchive

# Half set, the API couldn't sign in: refused here, so the first start fails
# and says why.
if [ -n "${PAGERARCHIVE_DB_USER:-}" ] && { [ "$PAGERARCHIVE_DB_USER" != pagerarchive ] || [ -z "${PAGERARCHIVE_DB_PASSWORD:-}" ]; }; then
	echo "20-pagerarchive: PAGERARCHIVE_DB_USER must be pagerarchive, with PAGERARCHIVE_DB_PASSWORD set too, in .env" >&2
	exit 1
fi

if [ -n "${PAGERARCHIVE_DB_PASSWORD:-}" ]; then
	psql -X -q -v ON_ERROR_STOP=1 --username "$POSTGRES_USER" --dbname "$POSTGRES_DB" <<SQL
\i $dir/api-role.sql
\getenv pw PAGERARCHIVE_DB_PASSWORD
ALTER ROLE pagerarchive PASSWORD :'pw';
SQL
	echo "20-pagerarchive: the API's role, pagerarchive, is set up"
fi

if [ -n "${AUTHENTIK_METRICS_DB_PASSWORD:-}" ]; then
	psql -X -q -v ON_ERROR_STOP=1 --username "$POSTGRES_USER" --dbname authentik <<SQL
\i $dir/metrics-role.sql
\getenv pw AUTHENTIK_METRICS_DB_PASSWORD
ALTER ROLE pagerarchive_metrics PASSWORD :'pw';
SQL
	echo "20-pagerarchive: the locked-accounts metric's role, pagerarchive_metrics, is set up"
fi
