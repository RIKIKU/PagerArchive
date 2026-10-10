#!/bin/sh
# Runs the browser acceptance tests (web/tests) against a real API and a
# fresh PostGIS database with the standard test fixture (SPEC.md section 10).
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

start() { # port role
	PROFILES_DIR=profiles/example TZ=Australia/Melbourne LISTEN_ADDR="127.0.0.1:$1" DEV_VIEWER_ROLE="$2" \
		"$BIN" serve 2>>"$BIN-$1.log" &
	PIDS="$PIDS $!"
	until curl -sf "http://127.0.0.1:$1/healthz" >/dev/null; do sleep 0.2; done
}
start 18080 user
start 18081 admin
export PA_USER_URL=http://127.0.0.1:18080 PA_ADMIN_URL=http://127.0.0.1:18081

status=0
(cd web && npx playwright test "$@") || status=$?

# P1: no page text in the API logs.
if grep -qE 'SMOKE|GARBLED|SHED FIRE|EXTREMELY_LONG' "$BIN"-*.log; then
	echo "page text found in the API logs" >&2
	status=1
else
	echo "API log lines checked: $(cat "$BIN"-*.log | wc -l), page texts found: 0"
fi
exit $status
