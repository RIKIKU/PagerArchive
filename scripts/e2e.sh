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

# Standard test fixture: template "Example Fire" and Stations 1-4.
"$BIN" template add "Example Fire" --agency "Example Fire Service" --profile example-fire --colour "#b3261e" >/dev/null
for n in 1 2 3 4; do
	"$BIN" alias add "010000$n" "Station $n" --template "Example Fire" >/dev/null
done
PA_CLIENT_KEY=$("$BIN" client add e2e "E2E client" | tail -n 1)
export PA_CLIENT_KEY

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
SQL

# Authentik isn't running: sign-in reports it unavailable, which one test checks.
PROFILES_DIR=profiles/example TZ=Australia/Melbourne LISTEN_ADDR=127.0.0.1:18080 \
	APP_URL=http://127.0.0.1:18080 AUTHENTIK_URL=http://127.0.0.1:1 OIDC_CLIENT_SECRET=unused AUTHENTIK_API_TOKEN=unused \
	ALLOWED_EMAIL_DOMAINS=example.test "$BIN" serve 2>>"$BIN.log" &
PIDS="$PIDS $!"
until curl -sf "http://127.0.0.1:18080/healthz" >/dev/null; do sleep 0.2; done
export PA_URL=http://127.0.0.1:18080

status=0
(cd web && npx playwright test tests/e2e.spec.ts "$@") || status=$?

# P1: no page text in the API log.
if grep -qE 'SMOKE|GARBLED|SHED FIRE|EXTREMELY_LONG|UNKNOWN PAGER' "$BIN.log"; then
	echo "page text found in the API log" >&2
	status=1
else
	echo "API log lines checked: $(wc -l <"$BIN.log"), page texts found: 0"
fi
exit $status
