#!/bin/sh
# Switches an existing install's API from the PostgreSQL superuser to its own
# role, pagerarchive (SPEC.md R84i, ADR 0033; docs/deploy.md, "The API's
# database role"). Part of the release's archive. Run it on the host, from
# anywhere, as the user who runs `docker compose` for this install, with the
# stack running:
#
#   deploy/postgres/api-role.sh          switch over, or check and repair a
#                                        switched install: safe to run again
#   deploy/postgres/api-role.sh undo     back to the superuser
#
# What it does, stopping at the first thing that fails:
#   1. counts every table's rows in PagerArchive's database
#   2. in one transaction, as the superuser: makes the role, hands it
#      PagerArchive's tables, functions and types, takes away what it mustn't
#      have, and sets its password: PAGERARCHIVE_DB_PASSWORD from .env, or a
#      new random one (deploy/postgres/api-role.sql). And the locked-accounts
#      metric's role and function, when AUTHENTIK_METRICS_DB_PASSWORD is set,
#      which the API can no longer make itself (metrics-role.sql)
#   3. signs in as the role and checks it: not a superuser, can't connect to
#      Authentik's database, can't run a program (COPY TO PROGRAM), can read
#      and create PagerArchive's tables
#   4. writes PAGERARCHIVE_DB_USER and PAGERARCHIVE_DB_PASSWORD into .env, and
#      recreates the API only (`up -d --no-deps api`)
#   5. waits for the API to connect as the role, and counts the rows again
# If step 5 fails, .env is put back and the API recreated as it was. Steps 1-3
# change nothing the running API notices: it's the superuser, and objects it
# owned are the role's. The password is never printed or put on a command line.
#
# undo: takes the two lines out of .env and recreates the API, which then
# connects as the superuser again, with a warning on every start. The role and
# its ownership stay: a superuser needs neither. An older release's
# compose.yaml ignores the two lines, so a rollback needs no undo.
set -eu

DIR=$(cd "$(dirname "$0")/../.." && pwd)
cd "$DIR"
[ -f compose.yaml ] && [ -f .env ] || {
	echo "api-role: run this from an install's folder (compose.yaml and .env next to deploy/)" >&2
	exit 2
}

say() { echo "api-role: $*"; }
fail() {
	echo "api-role: $*" >&2
	exit 1
}

# setting <NAME>: .env's value, as auto-upgrade.sh reads it.
setting() {
	v=$(sed -n "s/^$1=//p" .env | tail -n 1)
	v=${v%\"}
	v=${v#\"}
	printf '%s' "$v"
}

# psql_su <database> [psql arguments]: as the superuser, in the db container.
psql_su() {
	_db=$1
	shift
	docker compose exec -T db psql -X -q -v ON_ERROR_STOP=1 -U postgres -d "$_db" "$@"
}

# psql_app <database> [psql arguments]: as the role, over TCP with its password,
# as the API connects.
psql_app() {
	_db=$1
	shift
	PGPASSWORD=$PW docker compose exec -T -e PGPASSWORD db psql -X -q -v ON_ERROR_STOP=1 -h 127.0.0.1 -U pagerarchive -d "$_db" "$@"
}

# Every table in PagerArchive's database and its rows, sorted.
counts() {
	_c=$(psql_su postgres -At -F " " <<'SQL'
SELECT format('SELECT %L, count(*) FROM %I.%I', n.nspname || '.' || c.relname, n.nspname, c.relname)
FROM pg_class c JOIN pg_namespace n ON n.oid = c.relnamespace
WHERE c.relkind = 'r' AND n.nspname <> 'information_schema' AND n.nspname !~ '^pg_'
ORDER BY 1 \gexec
SQL
	) || return 1
	printf '%s\n' "$_c" | tr -d '\r' | LC_ALL=C sort
}

# drop_env_lines: .env without either setting, its permissions kept.
drop_env_lines() {
	sed -i -E '/^#? ?PAGERARCHIVE_DB_(USER|PASSWORD)=/d' .env
}

# api_uses_role: within 180 s the API is running and connected as the role.
api_uses_role() {
	_i=0
	while [ $_i -lt 90 ]; do
		_state=$(docker inspect -f '{{.State.Status}}' "$(docker compose ps -q api)" 2>/dev/null || true)
		_n=$(psql_su postgres -At -c "SELECT count(*) FROM pg_stat_activity WHERE usename = 'pagerarchive' AND datname = 'postgres'" 2>/dev/null || echo 0)
		if [ "$_state" = running ] && [ "${_n:-0}" -gt 0 ]; then
			return 0
		fi
		_i=$((_i + 1))
		sleep 2
	done
	return 1
}

docker compose ps --status running -q db | grep -q . || fail "the database isn't running: docker compose up -d first"

if [ "${1:-}" = undo ]; then
	if [ -z "$(setting PAGERARCHIVE_DB_USER)$(setting PAGERARCHIVE_DB_PASSWORD)" ]; then
		say "the API already connects as the superuser: nothing to undo"
		exit 0
	fi
	drop_env_lines
	docker compose up -d --no-deps api
	say "PAGERARCHIVE_DB_USER and PAGERARCHIVE_DB_PASSWORD are out of .env, and the API connects as the superuser again, with a warning on every start"
	exit 0
fi
[ $# -eq 0 ] || {
	echo "usage: deploy/postgres/api-role.sh [undo]" >&2
	exit 2
}

# 1. The rows before.
before=$(counts) || fail "couldn't count the tables' rows: nothing was changed"
say "counted the rows of $(printf '%s\n' "$before" | grep -c .) tables"

# 2. The role, its objects, its password; the metric's role. A new password is
# 32 random bytes in hex, so it needs no escaping in DATABASE_URL. The SQL comes
# from this folder's archive, on standard input; the passwords reach psql
# through the environment (\getenv).
PW=$(setting PAGERARCHIVE_DB_PASSWORD)
new_pw=""
if [ -z "$PW" ]; then
	PW=$(od -An -tx1 -N32 /dev/urandom | tr -d ' \n')
	[ ${#PW} -eq 64 ] || fail "couldn't make a password: nothing was changed"
	new_pw=1
fi
# A busy table (a backup's dump, a long report) makes it wait 10 s at most,
# then give up with nothing changed, rather than hold the API up behind it.
if ! {
	echo "SET LOCAL lock_timeout = '10s';"
	cat deploy/postgres/api-role.sql
	printf '%s\n' '\getenv pw PAGERARCHIVE_DB_PASSWORD' "ALTER ROLE pagerarchive PASSWORD :'pw';"
} | PAGERARCHIVE_DB_PASSWORD=$PW docker compose exec -T -e PAGERARCHIVE_DB_PASSWORD db \
	psql -X -q -1 -v ON_ERROR_STOP=1 -v VERBOSITY=terse -U postgres -d postgres >/dev/null; then
	fail "couldn't set up the role (a table busy for over 10 s? try again later): nothing was changed"
fi
say "the role pagerarchive owns PagerArchive's tables, functions and types, with only the rights it needs"
MPW=$(setting AUTHENTIK_METRICS_DB_PASSWORD)
if [ -n "$MPW" ]; then
	if ! {
		grep -v '^\\ir ' deploy/postgres/metrics-role.sql
		cat deploy/postgres/locked-accounts.sql
		printf '%s\n' '\getenv pw AUTHENTIK_METRICS_DB_PASSWORD' "ALTER ROLE pagerarchive_metrics PASSWORD :'pw';"
	} | AUTHENTIK_METRICS_DB_PASSWORD=$MPW docker compose exec -T -e AUTHENTIK_METRICS_DB_PASSWORD db \
		psql -X -q -1 -v ON_ERROR_STOP=1 -v VERBOSITY=terse -U postgres -d authentik >/dev/null; then
		fail "the API's role is set up, but the locked-accounts metric's role couldn't be: the API still connects as the superuser, and .env is unchanged"
	fi
	say "the locked-accounts metric's role, pagerarchive_metrics, and its function are set up"
fi

# 3. The role, as the API will use it.
q() { psql_app postgres -At -c "$1" 2>/dev/null | tr -d '\r'; }
[ "$(q "SELECT rolsuper OR rolcreaterole OR rolcreatedb FROM pg_roles WHERE rolname = current_user")" = f ] ||
	fail "the role can't sign in, or has rights it mustn't: the API still connects as the superuser, and .env is unchanged"
if psql_app authentik -At -c "SELECT 1" >/dev/null 2>&1; then
	fail "the role can connect to Authentik's database: the API still connects as the superuser, and .env is unchanged"
fi
if psql_app postgres -At -c "COPY (SELECT 1) TO PROGRAM 'true'" >/dev/null 2>&1; then
	fail "the role can run programs: the API still connects as the superuser, and .env is unchanged"
fi
[ "$(q "SELECT count(*) >= 0 FROM messages")" = t ] && psql_app postgres -c "BEGIN; CREATE TABLE api_role_check (n int); ROLLBACK;" >/dev/null 2>&1 ||
	fail "the role can't read or create PagerArchive's tables: the API still connects as the superuser, and .env is unchanged"
say "checked: not a superuser, no connection to Authentik's database, no programs, reads and creates PagerArchive's tables"

# 4. .env, then the API alone.
if [ "$(setting PAGERARCHIVE_DB_USER)" != pagerarchive ] || [ -n "$new_pw" ]; then
	cp -p .env .env.api-role-before
	drop_env_lines
	printf '%s\n' "PAGERARCHIVE_DB_USER=pagerarchive" "PAGERARCHIVE_DB_PASSWORD=$PW" >>.env
	say "PAGERARCHIVE_DB_USER and PAGERARCHIVE_DB_PASSWORD are in .env$([ -n "$new_pw" ] && echo ", with a new random password")"
fi
docker compose up -d --no-deps api

# 5. Connected as the role, and the same rows.
if ! api_uses_role; then
	if [ -f .env.api-role-before ]; then
		cp -p .env.api-role-before .env
		docker compose up -d --no-deps api
		fail "the API didn't connect as the role within 3 minutes: .env is as it was and the API connects as the superuser again (docker compose logs api says why)"
	fi
	fail "the API didn't connect as the role within 3 minutes (docker compose logs api says why)"
fi
rm -f .env.api-role-before
after=$(counts) || fail "couldn't count the tables' rows again"
# Every table is still there. Rows can change meanwhile, as the API runs: new
# pages arrive, sessions expire. Each table whose count changed is named, with
# both counts (table names and numbers only, never a row).
gone=$(printf '%s\n' "$before" | awk -v after="$after" 'BEGIN { n = split(after, a, "\n"); for (i = 1; i <= n; i++) { split(a[i], f, " "); got[f[1]] = 1 } }
	NF && !($1 in got) { print $1; exit }')
[ -z "$gone" ] || fail "after the switch, the table $gone is gone: undo with deploy/postgres/api-role.sh undo, and look into it before trying again"
changed=$(printf '%s\n' "$before" | awk -v after="$after" 'BEGIN { n = split(after, a, "\n"); for (i = 1; i <= n; i++) { split(a[i], f, " "); got[f[1]] = f[2] } }
	NF && got[$1] != $2 { printf "%s %s -> %s; ", $1, $2, got[$1] }')
if [ -n "$changed" ]; then
	say "rows that changed while it ran (new pages, expired sessions): ${changed%; }"
fi
say "the API connects as pagerarchive, and every table is there with its rows (undo: deploy/postgres/api-role.sh undo)"
