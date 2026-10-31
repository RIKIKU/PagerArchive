#!/bin/sh
# A65 and A67 against the example monitoring stack itself (deploy/monitoring):
# the API and its database from the compose file at the top (no Authentik:
# neither check needs it), the monitoring stack joined to their network as a
# deployment runs it, and a webhook contact point that the test receives on
# the host. A67's clients run in the client image on the API's network, with
# a stand-in for rtl_fm (internal/radio/standin). Memory of the monitoring
# stack's containers is sampled throughout, idle first and then during the
# checks.
#
# usage: scripts/monitoring-checks.sh [-o OUT] [-s SCREENSHOTS]
#   -o OUT          where the memory samples and summary go (default monitoring-results)
#   -s SCREENSHOTS  also feed the stack test data and take screenshots of a
#                   page's trace, the three dashboards and a firing alert into
#                   this folder (needs Node with web/'s dependencies and
#                   Playwright's Chromium)
# PA_MON_RUN, if set, runs only the checks it matches (go test -run), e.g.
# TestA67; mutation-checks.sh uses it.
# needs: Docker with compose, Go, curl. Uses 127.0.0.1:18091, :13001, :14319,
# :55437 and 0.0.0.0:18099 (the webhook, which Grafana reaches through the
# Docker host's gateway).
set -eu
cd "$(dirname "$0")/.."

OUT=monitoring-results SHOTS=""
while getopts o:s: opt; do
	case $opt in
	o) OUT=$OPTARG ;; s) SHOTS=$OPTARG ;; *) exit 2 ;;
	esac
done
mkdir -p "$OUT"
OUT=$(cd "$OUT" && pwd)

# Test-only settings, as the stack checks use (deploy/test/stack.vars).
while IFS= read -r line; do
	case $line in "" | "#"*) continue ;; esac
	eval "set_already=\${${line%%=*}+x}"
	[ -n "$set_already" ] || export "$line"
done <deploy/test/stack.vars

P=pa-mon-$$
export PAGERARCHIVE_VERSION=montest API_BIND=127.0.0.1:18091 AUTHENTIK_BIND=127.0.0.1:19091 MAILPIT_BIND=127.0.0.1:18026 PA_MON_DB_BIND=127.0.0.1:55437
export PAGERARCHIVE_NETWORK="${P}_default" PA_METRICS_TOKEN="$METRICS_TOKEN" GRAFANA_ADMIN_PASSWORD=test-grafana-admin-password \
	GRAFANA_BIND=127.0.0.1:13001 GRAFANA_ROOT_URL=http://127.0.0.1:13001 ALLOY_OTLP_BIND=127.0.0.1:14319 \
	PA_ALERT_WEBHOOK_URL=http://host.docker.internal:18099/alert
app() { docker compose -p "$P" -f compose.yaml -f deploy/test/compose.yaml -f deploy/monitoring/test/pagerarchive.yaml "$@"; }
mon() { docker compose -p "$P-mon" -f deploy/monitoring/compose.yaml -f deploy/monitoring/test/compose.yaml "$@"; }

SAMPLER=""
BIN=$(mktemp -d)
cleanup() {
	[ -n "$SAMPLER" ] && kill "$SAMPLER" 2>/dev/null || true
	docker ps -aq --filter "label=pagerarchive.montest=$P" | xargs -r docker rm -f >/dev/null 2>&1 || true
	rm -rf "$BIN"
	mon logs --no-color >"$OUT/monitoring-stack.log" 2>&1 || true
	app logs --no-color api >"$OUT/api.log" 2>&1 || true
	if [ -z "${MON_KEEP:-}" ]; then
		mon down -v --remove-orphans >/dev/null 2>&1 || true
		app down -v --remove-orphans >/dev/null 2>&1 || true
	fi
}
trap cleanup EXIT

echo "--- building the API and client images and rtl_fm's stand-in, starting the API and its database ($P)"
app build -q api
docker build -q -f Dockerfile.client -t "pagerarchive-client:montest" . >/dev/null
CGO_ENABLED=0 go build -o "$BIN/rtl_fm" ./internal/radio/standin
export PA_MON_CLIENT_IMAGE=pagerarchive-client:montest PA_MON_STANDIN="$BIN/rtl_fm" PA_MON_NETWORK="${P}_default" \
	PA_MON_API_ADDRESS=api:8080 PA_MON_LABEL="pagerarchive.montest=$P"
app up -d db api >/dev/null 2>&1 || { app up -d db api; exit 1; }
i=0
until curl -sf http://127.0.0.1:18091/healthz >/dev/null; do
	i=$((i + 1))
	[ $i -gt 120 ] && { echo "API didn't start" >&2; app logs api | tail -20 >&2; exit 1; }
	sleep 1
done

echo "--- starting the monitoring stack ($P-mon)"
start=$(date +%s)
if ! out=$(mon up -d 2>&1); then
	printf '%s\n' "$out" | tail -n 30 >&2
	exit 1
fi
until curl -sf http://127.0.0.1:13001/api/health >/dev/null; do
	[ $(($(date +%s) - start)) -gt 300 ] && { echo "Grafana didn't start" >&2; mon logs grafana | tail -20 >&2; exit 1; }
	sleep 2
done
echo "--- monitoring stack ready after $(($(date +%s) - start)) s: $(mon ps --format '{{.Service}}' | sort | tr '\n' ' ')"

# Memory of each monitoring container: seconds, phase, container, MiB.
PHASE_FILE=$(mktemp)
echo idle >"$PHASE_FILE"
T0=$(date +%s)
echo "t,phase,container,mem_mib" >"$OUT/monitoring-stats.csv"
(
	while :; do
		t=$(($(date +%s) - T0))
		p=$(cat "$PHASE_FILE")
		ids=$(docker ps -q --filter "label=com.docker.compose.project=$P-mon")
		[ -n "$ids" ] && docker stats --no-stream --format '{{.Name}},{{.MemUsage}}' $ids 2>/dev/null |
			awk -F, -v t="$t" -v p="$p" -v proj="$P-mon-" '{
				name = $1; sub(proj, "", name); sub(/-[0-9]+$/, "", name)
				split($2, m, " "); v = m[1]; mib = v + 0
				if (v ~ /GiB/) mib = mib * 1024; else if (v ~ /KiB/) mib = mib / 1024
				printf "%s,%s,%s,%.1f\n", t, p, name, mib
			}' >>"$OUT/monitoring-stats.csv"
		sleep 10
	done
) &
SAMPLER=$!

echo "--- idle for 2 minutes (memory)"
sleep 120
echo checks >"$PHASE_FILE"

status=0
PA_MON_REQUIRE=1 PA_MON_API_URL=http://127.0.0.1:18091 PA_MON_GRAFANA_URL=http://127.0.0.1:13001 \
	PA_MON_GRAFANA_PASSWORD="$GRAFANA_ADMIN_PASSWORD" PA_MON_WEBHOOK_ADDR=0.0.0.0:18099 \
	PA_MON_DB_URL="postgres://postgres:$POSTGRES_PASSWORD@127.0.0.1:55437/postgres?sslmode=disable" \
	PA_MON_STOP_API="docker compose -p $P -f $PWD/compose.yaml -f $PWD/deploy/test/compose.yaml -f $PWD/deploy/monitoring/test/pagerarchive.yaml stop api" \
	PA_MON_START_API="docker compose -p $P -f $PWD/compose.yaml -f $PWD/deploy/test/compose.yaml -f $PWD/deploy/monitoring/test/pagerarchive.yaml start api" \
	go test -count=1 -timeout 45m -v -run "${PA_MON_RUN:-TestA65|TestA67}" ./internal/monitortest/ || status=1

if [ -n "$SHOTS" ]; then
	echo shots >"$PHASE_FILE"
	sh scripts/monitoring-screenshots.sh "$SHOTS" "$P" || status=1
fi

kill "$SAMPLER" 2>/dev/null || true
SAMPLER=""
awk -F, '
	NR == 1 { next }
	{ k = $2 SUBSEP $3; if ($4 > peak[k]) peak[k] = $4; seen[$3] = 1; phases[$2] = 1 }
	END {
		for (p in phases) {
			total = 0
			for (c in seen) { total += peak[p SUBSEP c] }
			printf "%-6s total of the containers'"'"' peaks: %6.1f MiB (", p, total
			sep = ""
			for (c in seen) { printf "%s%s %.1f", sep, c, peak[p SUBSEP c]; sep = ", " }
			print ")"
		}
	}' "$OUT/monitoring-stats.csv" | sort >"$OUT/monitoring-memory.txt"
cat "$OUT/monitoring-memory.txt"
rm -f "$PHASE_FILE"
exit $status
