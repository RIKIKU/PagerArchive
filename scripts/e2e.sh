#!/bin/sh
# Runs the browser acceptance tests (web/tests/e2e.spec.ts) against a real
# API and a fresh PostGIS database with the standard test fixture (SPEC.md
# section 10).
#
# There's no Authentik here: a user and an admin are seeded straight into
# the database with a session each, and the tests carry those session
# cookies. Signing in through Authentik is tested by scripts/stack-checks.sh
# and scripts/stack-e2e.sh.
#
# usage: scripts/e2e.sh [playwright arguments]
# needs: Docker, Go, Node with web/ dependencies installed, Playwright browsers
set -eu
cd "$(dirname "$0")/.."

PORT_DB=${PA_E2E_DB_PORT:-55433}
DB=pa-e2e-db-$$
BIN=$(mktemp -d)/pagerarchive
export DATABASE_URL="postgres://postgres:test@127.0.0.1:$PORT_DB/postgres?sslmode=disable"
PIDS=""

cleanup() {
	for p in $PIDS; do kill "$p" 2>/dev/null || true; done
	docker rm -f "$DB" >/dev/null 2>&1 || true
}
trap cleanup EXIT

docker run -d --platform linux/amd64 --name "$DB" -e POSTGRES_PASSWORD=test -p "127.0.0.1:$PORT_DB:5432" postgis/postgis:18-3.6 \
	postgres -c log_statement=none -c log_min_error_statement=panic -c log_error_verbosity=terse >/dev/null
until docker exec "$DB" pg_isready -U postgres -h 127.0.0.1 >/dev/null 2>&1; do sleep 1; done
sleep 3 # the image restarts PostgreSQL once after its init scripts
until docker exec "$DB" pg_isready -U postgres -h 127.0.0.1 >/dev/null 2>&1; do sleep 1; done

(cd web && npm run build >/dev/null)
go build -o "$BIN" ./cmd/pagerarchive

# Adding a client creates the tables, then the standard test fixture goes in
# with SQL (template "Example Fire" and Stations 1-4; R59 took the template
# and alias commands out of pagerarchive).
PA_CLIENT_KEY=$("$BIN" client add e2e "E2E client" | tail -n 1)
export PA_CLIENT_KEY
{
	cat scripts/fixture.sql
	cat <<'SQL'
-- A40: a range alias over the stations, which typing one capcode never selects.
-- The capcode report test (R38) uses one capcode per browser project.
-- M6 (A42-A45): each browser project edits and requests changes to its own
-- aliases (n = 1, 2, 3 for chromium, firefox, webkit): 04001n1-04001n3.
INSERT INTO aliases (pattern, name, template_id)
SELECT p, name, (SELECT id FROM templates WHERE name = 'Example Fire')
FROM (VALUES ('0100%', 'Stations block')) AS v (p, name)
UNION ALL
SELECT '030010' || n, 'Report test ' || n, (SELECT id FROM templates WHERE name = 'Example Fire') FROM generate_series(1, 3) AS n
UNION ALL
SELECT '04001' || n || k, 'Edit test ' || n || k, (SELECT id FROM templates WHERE name = 'Example Fire')
FROM generate_series(1, 3) AS n, generate_series(1, 3) AS k;
SQL
} | docker exec -i "$DB" psql -q -v ON_ERROR_STOP=1 -U postgres >/dev/null
# R39: a receiver last heard 31 minutes ago (set below).
"$BIN" client add quiet "Quiet receiver" >/dev/null

# A user and an admin, each with a session (the cookie holds the token; the
# database holds its SHA-256).
PA_USER_SESSION=e2e-user-$(od -An -N16 -tx1 /dev/urandom | tr -d ' \n')
PA_ADMIN_SESSION=e2e-admin-$(od -An -N16 -tx1 /dev/urandom | tr -d ' \n')
export PA_USER_SESSION PA_ADMIN_SESSION
docker exec -i "$DB" psql -q -v ON_ERROR_STOP=1 -U postgres >/dev/null <<SQL
INSERT INTO users (subject, email, email_key, name, role) VALUES
  ('e2e-admin', 'admin@example.test', 'admin@example.test', 'Test Admin', 'admin');
INSERT INTO users (subject, email, email_key, name, role, invited_by)
  SELECT 'e2e-user', 'user@example.test', 'user@example.test', 'Test User', 'user', id FROM users WHERE subject = 'e2e-admin';
INSERT INTO sessions (token_hash, user_id, expires_at)
  SELECT sha256(convert_to('$PA_USER_SESSION', 'UTF8')), id, now() + interval '1 day' FROM users WHERE subject = 'e2e-user';
INSERT INTO sessions (token_hash, user_id, expires_at)
  SELECT sha256(convert_to('$PA_ADMIN_SESSION', 'UTF8')), id, now() + interval '1 day' FROM users WHERE subject = 'e2e-admin';
UPDATE clients SET last_seen_at = now() - interval '31 minutes' WHERE id = 'quiet';
SQL

# M7 (A48-A50): each browser project n (1-3) gets its own copies of A48's
# tree: the admin invited A, A invited B and C, B invited D, and D has an
# unused invitation. They have no Authentik user (subject NULL), so
# deactivating them calls nothing that isn't running here.
for n in 1 2 3; do
	for t in 48 49a 49b 50a 50b; do
		k=$t-$n
		cat <<SQL
INSERT INTO users (email, email_key, name, role, invited_by, created_at)
  SELECT 'a-$k@example.test', 'a-$k@example.test', 'A $k', 'user', id, clock_timestamp() FROM users WHERE subject = 'e2e-admin';
INSERT INTO users (email, email_key, name, role, invited_by, created_at)
  SELECT 'b-$k@example.test', 'b-$k@example.test', 'B $k', 'user', id, clock_timestamp() FROM users WHERE email_key = 'a-$k@example.test';
INSERT INTO users (email, email_key, name, role, invited_by, created_at)
  SELECT 'c-$k@example.test', 'c-$k@example.test', 'C $k', 'user', id, clock_timestamp() FROM users WHERE email_key = 'a-$k@example.test';
INSERT INTO users (email, email_key, name, role, invited_by, created_at)
  SELECT 'd-$k@example.test', 'd-$k@example.test', 'D $k', 'user', id, clock_timestamp() FROM users WHERE email_key = 'b-$k@example.test';
INSERT INTO invitations (email, email_key, invited_by, expires_at)
  SELECT 'x-$k@example.test', 'x-$k@example.test', id, now() + interval '7 days' FROM users WHERE email_key = 'd-$k@example.test';
SQL
	done
done | docker exec -i "$DB" psql -q -v ON_ERROR_STOP=1 -U postgres >/dev/null
# A50 adds someone to a tree while its preview is open.
export PA_E2E_DB="$DB"

# Authentik isn't running: sign-in reports it unavailable, which one test checks.
PROFILES_DIR=profiles/example TZ=Australia/Melbourne LISTEN_ADDR=127.0.0.1:18080 MAP_TILES=deploy/example-map/example-map.pmtiles \
	APP_URL=http://127.0.0.1:18080 AUTHENTIK_URL=http://127.0.0.1:1 OIDC_CLIENT_SECRET=unused AUTHENTIK_API_TOKEN=unused \
	INTEGRATION_SECRETS_KEY=$(head -c 32 /dev/urandom | base64) \
	ALLOWED_EMAIL_DOMAINS=example.test "$BIN" serve 2>>"$BIN.log" &
PIDS="$PIDS $!"
until curl -sf "http://127.0.0.1:18080/healthz" >/dev/null; do sleep 0.2; done
export PA_URL=http://127.0.0.1:18080

status=0
(cd web && npx playwright test tests/e2e.spec.ts "$@") || status=$?

# P1: no page text in the API log, and no alias request notes, feedback or
# names (ADR 0007 §8), or tree deactivation reasons (ADR 0008 §10).
if grep -qE 'SMOKE|GARBLED|SHED FIRE|EXTREMELY_LONG|UNKNOWN PAGER|NOTE-MARKER|Wrong station|Renamed by admin|REASON-MARKER' "$BIN.log"; then
	echo "page text, a note, feedback or a name found in the API log" >&2
	status=1
else
	echo "API log lines checked: $(wc -l <"$BIN.log"), page texts found: 0"
fi
exit $status
