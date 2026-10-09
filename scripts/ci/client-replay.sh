#!/bin/sh
# Smoke test for the server and client images: replays every raw line from the
# example golden tables through the client container (stdin, no radio) into
# the API container, then checks what was stored and that no container log
# contains page text (P1).
#
# usage: scripts/ci/client-replay.sh <api-image> <client-image>
set -eu

API_IMAGE=$1
CLIENT_IMAGE=$2
NET=pa-replay-$$
DB_URL="postgres://postgres:test@pa-db-$$:5432/postgres?sslmode=disable"

cleanup() {
	docker rm -f "pa-api-$$" "pa-db-$$" >/dev/null 2>&1 || true
	docker network rm "$NET" >/dev/null 2>&1 || true
}
trap cleanup EXIT

docker network create "$NET" >/dev/null
# PostgreSQL logging settings from ADR 0001 section 5 (P1).
docker run -d --name "pa-db-$$" --network "$NET" -e POSTGRES_PASSWORD=test postgis/postgis:18-3.6 \
	postgres -c log_statement=none -c log_min_error_statement=panic -c log_error_verbosity=terse >/dev/null
i=0
until docker exec "pa-db-$$" pg_isready -U postgres -h 127.0.0.1 >/dev/null 2>&1; do
	i=$((i + 1)); [ "$i" -lt 60 ] || { echo "database didn't start"; exit 1; }
	sleep 1
done
# The image's init scripts restart PostgreSQL once; wait for the final server.
sleep 3
until docker exec "pa-db-$$" pg_isready -U postgres -h 127.0.0.1 >/dev/null 2>&1; do sleep 1; done
docker exec "pa-db-$$" psql -U postgres -tAc 'SELECT postgis_full_version()' | cut -c1-60

docker run -d --name "pa-api-$$" --network "$NET" -e DATABASE_URL="$DB_URL" \
	-v "$PWD/profiles/example:/profiles:ro" "$API_IMAGE" >/dev/null
i=0
until docker run --rm --network "$NET" curlimages/curl:latest -sf "http://pa-api-$$:8080/healthz" >/dev/null 2>&1; do
	i=$((i + 1)); [ "$i" -lt 60 ] || { docker logs "pa-api-$$"; echo "API didn't start"; exit 1; }
	sleep 1
done

KEY=$(docker run --rm --network "$NET" -e DATABASE_URL="$DB_URL" "$API_IMAGE" client add replay "CI replay" | tail -n 1)

LINES=$(mktemp)
jq -r '.cases[].raw' profiles/example/golden-*.json >"$LINES"
echo "replaying $(wc -l <"$LINES") lines"
docker run -i --rm --network "$NET" -e SERVER_URL="http://pa-api-$$:8080" -e CLIENT_ID=replay -e CLIENT_KEY="$KEY" \
	"$CLIENT_IMAGE" <"$LINES"

psql() { docker exec "pa-db-$$" psql -U postgres -tAc "$1"; }
STORED=$(psql 'SELECT count(*) FROM messages')
IGNORED_STORED=$(psql "SELECT count(*) FROM messages WHERE capcode = '0000042'")
PARSED=$(psql "SELECT count(*) FROM messages WHERE format IS NOT NULL")
echo "messages stored: $STORED (want 10), on the ignored capcode: $IGNORED_STORED (want 0), parsed by a format: $PARSED (want 0: no aliases in this database)"
[ "$STORED" = 10 ] && [ "$IGNORED_STORED" = 0 ]

# P1: no page text in any container log.
LOGS=$(mktemp)
docker logs "pa-api-$$" >"$LOGS" 2>&1
docker logs "pa-db-$$" >>"$LOGS" 2>&1
LEAKS=0
N=0
while IFS= read -r line; do
	N=$((N + 1))
	text=${line#*Alpha:   }
	if grep -qF -- "$text" "$LOGS"; then
		echo "page text of replay line $N found in container logs" >&2
		LEAKS=$((LEAKS + 1))
	fi
done <"$LINES"
echo "container log lines checked: $(wc -l <"$LOGS"), page texts found in them: $LEAKS"
[ "$LEAKS" = 0 ]
echo "client replay OK"
