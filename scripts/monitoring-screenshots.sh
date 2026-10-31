#!/bin/sh
# Feeds the example monitoring stack made-up test data (a receiver reading
# stdin, one running the radio with rtl_fm's stand-in, and an integration
# whose deliveries fail) and takes the screenshots in docs/monitoring.md: a
# page's trace in Tempo, the three dashboards and a firing alert. Run by
# scripts/monitoring-checks.sh -s DIR, with its stacks up (project P); not on
# its own.
#
# usage: scripts/monitoring-screenshots.sh DIR P
set -eu
DIR=$1 P=$2
mkdir -p "$DIR"
DIR=$(cd "$DIR" && pwd)
API=http://127.0.0.1:18091
app() { docker compose -p "$P" -f compose.yaml -f deploy/test/compose.yaml -f deploy/monitoring/test/pagerarchive.yaml "$@"; }
psql_db() { app exec -T db psql -q -v ON_ERROR_STOP=1 -U postgres "$@"; }

BIN=$(mktemp -d)
trap 'docker rm -f "$P-mast-receiver" >/dev/null 2>&1; rm -rf "$BIN"' EXIT
go build -o "$BIN/client" ./cmd/pagerarchive-client

echo "--- test data: the fixture's template and aliases, a user, two receivers"
psql_db <scripts/fixture.sql >/dev/null
SESSION=shots-$(od -An -N12 -tx1 /dev/urandom | tr -d ' \n')
psql_db >/dev/null <<SQL
INSERT INTO users (subject, email, email_key, name, role) VALUES ('shots-user', 'viewer@example.test', 'viewer@example.test', 'Test Viewer', 'user')
  ON CONFLICT DO NOTHING;
INSERT INTO sessions (token_hash, user_id, expires_at)
  SELECT sha256(convert_to('$SESSION', 'UTF8')), id, now() + interval '1 day' FROM users WHERE subject = 'shots-user';
SQL
KEY=$(app exec -T api /usr/local/bin/pagerarchive client add hill-receiver "Hill receiver" | tail -n 1)
app exec -T api /usr/local/bin/pagerarchive client add shed-receiver "Shed receiver" >/dev/null
# The second receiver was last heard 31 minutes ago: its alert fires.
psql_db -c "UPDATE clients SET last_seen_at = now() - interval '31 minutes' WHERE id = 'shed-receiver'" >/dev/null

echo "--- a receiver running the radio, with rtl_fm's stand-in (A67), for radio health"
RKEY=$(app exec -T api /usr/local/bin/pagerarchive client add mast-receiver "Mast receiver" | tail -n 1)
mkdir -p "$BIN/standin"
docker run -d --name "$P-mast-receiver" --label "$PA_MON_LABEL" --network "$PA_MON_NETWORK" --user "$(id -u):$(id -g)" \
	-e SERVER_URL=http://api:8080 -e CLIENT_ID=mast-receiver -e CLIENT_KEY="$RKEY" -e RADIO_FREQUENCY=148.8125M -e STANDIN_EVERY=15s \
	-e OTEL_EXPORTER_OTLP_ENDPOINT=http://alloy:4318 -e OTEL_METRIC_EXPORT_INTERVAL=10000 \
	-v "$PA_MON_STANDIN:/usr/local/bin/rtl_fm:ro" -v "$BIN/standin:/run/rtl_fm-standin" "$PA_MON_CLIENT_IMAGE" >/dev/null

echo "--- an integration whose deliveries fail (nothing listens at db:9), for failures by cause"
psql_db -c "INSERT INTO settings (key, value) VALUES ('integration_allow_list', '[\"db\"]') ON CONFLICT (key) DO UPDATE SET value = EXCLUDED.value" >/dev/null
curl -sf -o /dev/null -b "pa_session=$SESSION" -H 'Content-Type: application/json' \
	-d '{"name":"Station 1 fires","kind":"webhook","content":"summary","capcodes":["0100001"],"secrets":{"url":"http://db:9/hook"}}' \
	"$API/api/v1/integrations" || { echo "couldn't create the integration" >&2; exit 1; }

line() { printf 'POCSAG1200: Address: %7d  Function: 3  Alpha:   %s\n' "$1" "$2"; }
echo "--- a receiver decodes pages, the API is unreachable for 20 s, then it's back"
FIFO=$BIN/in
mkfifo "$FIFO"
SERVER_URL=$API CLIENT_ID=hill-receiver CLIENT_KEY=$KEY OTEL_EXPORTER_OTLP_ENDPOINT=http://127.0.0.1:14319 \
	OTEL_METRIC_EXPORT_INTERVAL=5000 OTEL_BSP_SCHEDULE_DELAY=1000 OTEL_BLRP_SCHEDULE_DELAY=1000 \
	"$BIN/client" <"$FIFO" >"$BIN/client.out" 2>"$BIN/client.err" &
CLIENT=$!
exec 3>"$FIFO"
for i in 1 2 3 4 5 6; do
	line 100001 "@@EX000010$i HSF1 SMOKE FROM ROOF @ 12 FICTION ST, MADEUPTON GRID 123456 UNITS T1 T2 #STN1" >&3
	line 100002 "HbRE EX000010$i CREWS RETURNING #STN2" >&3
	line 50 "QDHEARTBEAT $i" >&3
	echo "Enabled demodulators: POCSAG1200" >&3
	sleep 5
done
app stop api >/dev/null 2>&1
line 100003 "@@EX0000120 GRS3 GRASS FIRE @ SAMPLE RD, MADEUPTON #STN3" >&3
sleep 20
app start api >/dev/null 2>&1
until curl -sf "$API/healthz" >/dev/null; do sleep 1; done
for i in 1 2 3 4 5 6 7 8 9 10; do
	curl -sf -b "pa_session=$SESSION" "$API/api/v1/messages?q=FIRE" >/dev/null || true
	curl -sf -b "pa_session=$SESSION" "$API/api/v1/messages" >/dev/null || true
	curl -sf -b "pa_session=$SESSION" "$API/api/v1/messages/1" >/dev/null || true
	line 100004 "QDTraining Tuesday 7pm at the station $i" >&3
	sleep 3
done
exec 3>&-
wait $CLIENT || true

echo "--- waiting a minute for traces, logs and metrics to settle"
sleep 60
echo "--- screenshots into $DIR"
GRAFANA_URL=http://127.0.0.1:13001 GRAFANA_PASSWORD="$GRAFANA_ADMIN_PASSWORD" node scripts/monitoring-screenshots.cjs "$DIR"
