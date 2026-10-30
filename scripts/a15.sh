#!/bin/sh
# A15, the resource budget (SPEC.md section 8 and 9; ADR 0001 "How each choice
# will be verified"): the full compose stack, Authentik included, with a
# made-up address dataset of real size, 20 live-update viewers and pages
# generated from the example formats.
#
#   1. load:  RATE pages/day for MINUTES, 20 viewers connected
#   2. idle:  IDLE minutes with no pages and no viewers
#
# docker stats is sampled every 10 s into OUT/a15-stats.csv; its memory
# figure is the cgroup's usage without inactive file cache. Pass: after
# WARMUP minutes of load, the API's peak is <= 256 MiB and api + db peaks
# sum to <= 1 GiB; mean CPU of api + db while idle is < 5 % of one core.
#
# usage: scripts/a15.sh [-m MINUTES] [-w WARMUP] [-i IDLE] [-r RATE] [-l LOCALITIES] [-o OUT] [-t]
#   -t  telemetry on: the API exports traces and logs to a test collector on
#       the host (scripts/test-collector, port 14320), as a deployment with
#       the monitoring stack does (R69: still within the budget)
#   full run (by hand, with evidence): scripts/a15.sh            (60/10/10, 1500/day)
#   CI:                                scripts/a15.sh -m 5 -w 1 -i 2 -r 30000
# needs: Docker with compose, Go, curl, awk. Uses 127.0.0.1:8080 and :9000.
set -eu
cd "$(dirname "$0")/.."

MINUTES=60 WARMUP=10 IDLE=10 RATE=1500 LOCALITIES=3000 OUT=a15-results TELEMETRY=""
while getopts m:w:i:r:l:o:t opt; do
	case $opt in
	m) MINUTES=$OPTARG ;; w) WARMUP=$OPTARG ;; i) IDLE=$OPTARG ;; r) RATE=$OPTARG ;;
	l) LOCALITIES=$OPTARG ;; o) OUT=$OPTARG ;; t) TELEMETRY=1 ;; *) exit 2 ;;
	esac
done
mkdir -p "$OUT"
OUT=$(cd "$OUT" && pwd)

export COMPOSE_PROJECT_NAME="pa-a15-$$"
export PAGERARCHIVE_VERSION=a15
# Loading the large dataset happens before the API listens (ADR 0005).
export STACK_API_WAIT=1800
COLLECTOR=""
if [ -n "$TELEMETRY" ]; then
	export STACK_OVERLAYS="-f deploy/test/telemetry.yaml" PA_TEST_COLLECTOR_PORT=14320
	go build -o "$OUT/test-collector" ./scripts/test-collector
	"$OUT/test-collector" -addr 0.0.0.0:14320 -summary "$OUT/a15-telemetry.txt" &
	COLLECTOR=$!
	echo "--- telemetry on: the API exports to the test collector on the host (port 14320)"
fi
. ./scripts/stack-lib.sh

WORK=$(mktemp -d)
mkdir -p "$WORK/profiles"
echo "--- making the dataset ($LOCALITIES localities)"
go run scripts/a15/dataset.go -o "$WORK/profiles/a15-addresses.csv.gz" -sample "$WORK/sample.txt" -localities "$LOCALITIES"
ls -l "$WORK/profiles/a15-addresses.csv.gz" | awk '{print "--- dataset file: " $5 " bytes"}'
cp profiles/example/example-fire.json "$WORK/profiles/"
sed 's/"path": "example-addresses.json"/"path": "a15-addresses.csv.gz"/' profiles/example/example-network.json >"$WORK/profiles/example-network.json"
chmod -R a+rX "$WORK"
export PROFILES_DIR="$WORK/profiles"

SAMPLER=""
cleanup() {
	[ -n "$SAMPLER" ] && kill "$SAMPLER" 2>/dev/null || true
	if [ -n "$COLLECTOR" ]; then
		kill "$COLLECTOR" 2>/dev/null && wait "$COLLECTOR" 2>/dev/null || true
		rm -f "$OUT/test-collector"
	fi
	stack_compose logs --no-color api >"$OUT/a15-api.log" 2>&1 || true
	[ -n "${STACK_KEEP:-}" ] || stack_down
	rm -rf "$WORK"
}
trap cleanup EXIT

# Samples: seconds since start, phase, container, memory MiB, CPU %.
PHASE_FILE="$WORK/phase"
echo start >"$PHASE_FILE"
T0=$(date +%s)
echo "t,phase,container,mem_mib,cpu_pct" >"$OUT/a15-stats.csv"
(
	while :; do
		t=$(($(date +%s) - T0))
		p=$(cat "$PHASE_FILE")
		ids=$(docker ps -q --filter "label=com.docker.compose.project=$COMPOSE_PROJECT_NAME")
		# With no IDs, docker stats would sample every container on the host.
		[ -n "$ids" ] && docker stats --no-stream --format '{{.Name}},{{.MemUsage}},{{.CPUPerc}}' $ids 2>/dev/null |
			awk -F, -v t="$t" -v p="$p" -v proj="$COMPOSE_PROJECT_NAME-" '{
				name = $1; sub(proj, "", name); sub(/-[0-9]+$/, "", name)
				split($2, m, " ")
				v = m[1]; mib = v + 0
				if (v ~ /GiB/) mib = mib * 1024; else if (v ~ /KiB/) mib = mib / 1024; else if (v ~ /[0-9]B$/ && v !~ /iB/) mib = mib / 1048576
				cpu = $3; sub(/%/, "", cpu)
				printf "%s,%s,%s,%.1f,%s\n", t, p, name, mib, cpu
			}' >>"$OUT/a15-stats.csv"
		sleep 10
	done
) &
SAMPLER=$!

started=$(date +%s)
stack_up
echo "--- API up $(($(date +%s) - started)) s after start (dataset loaded)"
grep -h '"address dataset loaded"' "$OUT/a15-api.log" 2>/dev/null || stack_compose logs --no-color api | grep 'address dataset loaded\|street index' || true

cli() { stack_compose exec -T api /usr/local/bin/pagerarchive "$@"; }
stack_compose exec -T db psql -q -v ON_ERROR_STOP=1 -U postgres >/dev/null <scripts/fixture.sql
KEY=$(cli client add a15 "A15 load" | tail -n 1)

# 20 viewers: 20 users, each with a session (as scripts/e2e.sh seeds them).
SESSIONS=""
for i in $(seq 1 20); do SESSIONS="$SESSIONS,a15-viewer-$i-$(od -An -N8 -tx1 /dev/urandom | tr -d ' \n')"; done
SESSIONS=${SESSIONS#,}
{
	echo "INSERT INTO users (subject, email, email_key, name, role) VALUES ('a15-admin', 'a15-admin@example.test', 'a15-admin@example.test', 'A15 admin', 'admin');"
	i=0
	for s in $(echo "$SESSIONS" | tr , ' '); do
		i=$((i + 1))
		echo "INSERT INTO users (subject, email, email_key, name, role, invited_by) SELECT 'a15-$i', 'v$i@example.test', 'v$i@example.test', 'Viewer $i', 'user', id FROM users WHERE subject = 'a15-admin';"
		echo "INSERT INTO sessions (token_hash, user_id, expires_at) SELECT sha256(convert_to('$s', 'UTF8')), id, now() + interval '2 days' FROM users WHERE subject = 'a15-$i';"
	done
} | stack_compose exec -T db psql -q -v ON_ERROR_STOP=1 -U postgres >/dev/null

echo "--- load: $RATE pages/day for $MINUTES min, 20 viewers (memory counts after $WARMUP min)"
echo load >"$PHASE_FILE"
LOAD_START=$(($(date +%s) - T0))
load_status=0
go run scripts/a15/load.go -url http://127.0.0.1:8080 -key "$KEY" -sessions "$SESSIONS" -rate "$RATE" -for "${MINUTES}m" -sample "$WORK/sample.txt" >"$OUT/a15-load.txt" 2>&1 || load_status=$?
cat "$OUT/a15-load.txt"
echo "--- idle: $IDLE min, no pages, no viewers"
echo idle >"$PHASE_FILE"
IDLE_START=$(($(date +%s) - T0))
sleep $((IDLE * 60))
kill "$SAMPLER" 2>/dev/null || true
SAMPLER=""
stack_compose exec -T db psql -U postgres -tAc "SELECT datname, count(*) FROM pg_stat_activity WHERE datname IS NOT NULL GROUP BY 1 ORDER BY 1" | sed 's/^/--- db connections: /'
stack_compose exec -T db psql -U postgres -tAc "SELECT 'messages ' || count(*) FROM messages; SELECT 'address points ' || count(*) FROM address_points; SELECT 'located ' || count(*) FROM messages WHERE location IS NOT NULL"

budget_status=0
if [ -n "$COLLECTOR" ]; then
	kill "$COLLECTOR" && wait "$COLLECTOR" || true
	COLLECTOR=""
	rm -f "$OUT/test-collector"
	cat "$OUT/a15-telemetry.txt"
	# Telemetry was really on: the collector got spans.
	grep -q 'received [1-9][0-9]* spans' "$OUT/a15-telemetry.txt" || { echo "A15 with telemetry: the collector got no spans" >&2; budget_status=1; }
fi
awk -F,-v warm=$((LOAD_START + WARMUP * 60)) -v idle0=$IDLE_START '
	NR == 1 { next }
	$2 == "load" && $1 >= warm { if ($4 > peak[$3]) peak[$3] = $4 }
	{ if ($4 > all[$3]) all[$3] = $4 }
	$2 == "idle" && ($3 == "api" || $3 == "db") { cpu[$3] += $5; n[$3]++ }
	END {
		for (c in all) printf "peak memory %-17s %7.1f MiB under load after warm-up, %7.1f MiB overall\n", c, peak[c], all[c]
		api = peak["api"]; sum = peak["api"] + peak["db"]
		idle = (n["api"] ? cpu["api"] / n["api"] : 0) + (n["db"] ? cpu["db"] / n["db"] : 0)
		printf "API peak:          %.1f MiB (limit 256)  %s\n", api, (api <= 256 ? "PASS" : "FAIL")
		printf "api + db peaks:    %.1f MiB (limit 1024) %s\n", sum, (sum <= 1024 ? "PASS" : "FAIL")
		printf "idle CPU api + db: %.2f %% of one core (limit 5) %s\n", idle, (idle < 5 ? "PASS" : "FAIL")
		if (api > 256 || sum > 1024 || idle >= 5 || api == 0) exit 1
	}' "$OUT/a15-stats.csv" >"$OUT/a15-summary.txt" || budget_status=$?
cat "$OUT/a15-summary.txt"
[ "$load_status" -eq 0 ] && [ "$budget_status" -eq 0 ] && echo "A15: PASS" || { echo "A15: FAIL"; exit 1; }
