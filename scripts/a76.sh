#!/bin/sh
# A76's speed (R83f; ADR 0018): an import through `docker compose exec`, as a
# deployment runs it, on the compose file's API and database, while user B
# searches and a client posts live pages. scripts/a76/speed.go runs it and
# checks the rate, the searches and live delivery. docker stats is sampled every
# 10 s into OUT/a76-stats.csv, by the same measure as A15. Pass: during the
# import, the API's peak is <= 256 MiB and api + db peaks sum to <= 1 GiB. The
# API's figure includes the import, which runs in its container.
#
# usage: scripts/a76.sh [-s SEED] [-n PAGES] [-l LOCALITIES] [-o OUT]
#   CI:       scripts/a76.sh                        (seed 4,562,500, import 200,000)
#   full run: scripts/a76.sh -s 0 -n 4562500 -l 3000 (a real-size address dataset)
# needs: Docker with compose, Go, curl, awk. Uses 127.0.0.1:8080 and :55439, and
# port 14321 on every address for the test webhook.
set -eu
cd "$(dirname "$0")/.."

SEED=4562500 PAGES=200000 LOCALITIES=0 OUT=a76-results
while getopts s:n:l:o: opt; do
	case $opt in
	s) SEED=$OPTARG ;; n) PAGES=$OPTARG ;; l) LOCALITIES=$OPTARG ;; o) OUT=$OPTARG ;; *) exit 2 ;;
	esac
done
mkdir -p "$OUT"
OUT=$(cd "$OUT" && pwd)

export COMPOSE_PROJECT_NAME="pa-a76-$$"
export PAGERARCHIVE_VERSION=a76
export STACK_API_WAIT=1800
export STACK_OVERLAYS="-f deploy/test/import.yaml"
. ./scripts/stack-lib.sh

WORK=$(mktemp -d)
SAMPLE=""
if [ "$LOCALITIES" -gt 0 ]; then
	mkdir -p "$WORK/profiles"
	echo "--- making the address dataset ($LOCALITIES localities)"
	go run scripts/a15/dataset.go -o "$WORK/profiles/a15-addresses.csv.gz" -sample "$WORK/sample.txt" -localities "$LOCALITIES"
	cp profiles/example/example-fire.json "$WORK/profiles/"
	sed 's/"path": "example-addresses.json"/"path": "a15-addresses.csv.gz"/' profiles/example/example-network.json >"$WORK/profiles/example-network.json"
	chmod -R a+rX "$WORK"
	export PROFILES_DIR="$WORK/profiles"
	SAMPLE="-sample $WORK/sample.txt"
fi

SAMPLER=""
cleanup() {
	[ -n "$SAMPLER" ] && kill "$SAMPLER" 2>/dev/null || true
	stack_compose logs --no-color api >"$OUT/a76-api.log" 2>&1 || true
	stack_compose logs --no-color db >"$OUT/a76-db.log" 2>&1 || true
	[ -n "${STACK_KEEP:-}" ] || stack_down
	rm -rf "$WORK"
}
trap cleanup EXIT

started=$(date +%s)
echo "--- building the API image and starting it with the database ($COMPOSE_PROJECT_NAME)"
stack_compose build -q api
stack_compose up -d db api
stack_wait_api
echo "--- API up $(($(date +%s) - started)) s after start"
stack_compose exec -T db psql -q -v ON_ERROR_STOP=1 -U postgres >/dev/null <scripts/fixture.sql
KEY=$(stack_compose exec -T api /usr/local/bin/pagerarchive client add a76 "A76 client" | tail -n 1)

# Samples: seconds since start, phase, container, memory MiB, CPU %, and the
# container's own memory without its file cache, from its cgroup.
T0=$(date +%s)
PHASE_FILE="$WORK/phase"
echo seed >"$PHASE_FILE"
echo "t,phase,container,mem_mib,cpu_pct,anon_mib" >"$OUT/a76-stats.csv"
(
	while :; do
		t=$(($(date +%s) - T0))
		p=$(cat "$PHASE_FILE")
		docker stats --no-stream --format '{{.ID}},{{.Name}},{{.MemUsage}},{{.CPUPerc}}' \
			$(docker ps -q --filter "label=com.docker.compose.project=$COMPOSE_PROJECT_NAME") 2>/dev/null |
			while IFS=, read -r id name mem cpu; do
				full=$(docker inspect -f '{{.Id}}' "$id" 2>/dev/null || true)
				anon=$(awk '$1 == "anon" { printf "%.1f", $2 / 1048576 }' "/sys/fs/cgroup/system.slice/docker-$full.scope/memory.stat" 2>/dev/null || true)
				printf '%s,%s,%s,%s,%s\n' "$t" "$name" "$mem" "$cpu" "${anon:-0}"
			done |
			awk -F, -v p="$p" -v proj="$COMPOSE_PROJECT_NAME-" '{
				name = $2; sub(proj, "", name); sub(/-[0-9]+$/, "", name)
				split($3, m, " ")
				v = m[1]; mib = v + 0
				if (v ~ /GiB/) mib = mib * 1024; else if (v ~ /KiB/) mib = mib / 1024; else if (v ~ /[0-9]B$/ && v !~ /iB/) mib = mib / 1048576
				cpu = $4; sub(/%/, "", cpu)
				printf "%s,%s,%s,%.1f,%s,%s\n", $1, p, name, mib, cpu, $5
			}' >>"$OUT/a76-stats.csv"
		sleep 10
	done
) &
SAMPLER=$!

status=0
# shellcheck disable=SC2086
go run scripts/a76/speed.go -db "postgres://postgres:$POSTGRES_PASSWORD@127.0.0.1:${PA_A76_DB_PORT:-55439}/postgres?sslmode=disable" \
	-key "$KEY" -seed "$SEED" -pages "$PAGES" -o "$OUT" -phase "$PHASE_FILE" $SAMPLE -- \
	docker compose -f compose.yaml -f deploy/test/compose.yaml $STACK_OVERLAYS exec -T api pagerarchive import-messages - 2>&1 | tee "$OUT/a76-speed.txt" || status=1
grep -q '^A76 speed: PASS$' "$OUT/a76-speed.txt" || status=1
kill "$SAMPLER" 2>/dev/null || true
SAMPLER=""

# Memory during the import only: the seed isn't part of it.
awk -F, '
	NR == 1 { next }
	$2 == "import" { if ($4 > peak[$3]) peak[$3] = $4; if ($6 > anon[$3]) anon[$3] = $6 }
	END {
		for (c in peak) printf "peak memory %-6s %7.1f MiB by docker stats, %7.1f MiB without file cache\n", c, peak[c], anon[c]
		api = peak["api"]; sum = peak["api"] + peak["db"]
		printf "API peak:          %.1f MiB (limit 256)  %s\n", api, (api <= 256 ? "PASS" : "FAIL")
		printf "api + db peaks:    %.1f MiB (limit 1024) %s\n", sum, (sum <= 1024 ? "PASS" : "FAIL")
		if (api > 256 || sum > 1024 || api == 0) exit 1
	}' "$OUT/a76-stats.csv" >"$OUT/a76-summary.txt" || status=1
cat "$OUT/a76-summary.txt"
stack_compose logs --no-color api db >"$OUT/a76-logs.txt" 2>&1 || true
if grep -q 'FICTION\|0412' "$OUT/a76-logs.txt"; then
	echo "page text found in the logs" >&2
	status=1
fi
[ "$status" -eq 0 ] && echo "A76 speed and budget: PASS" || { echo "A76 speed and budget: FAIL"; exit 1; }
