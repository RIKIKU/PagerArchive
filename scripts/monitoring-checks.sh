#!/bin/sh
# A65, A67, A80's alert and A90's Grafana part against the example
# monitoring stack itself (deploy/monitoring): the API, its database and the
# backup services from the compose file at the top (no Authentik: none of these
# needs it), joined to the
# stack's `monitoring` network by the override docs/monitoring.md gives (R76), as
# a deployment runs it, and a webhook contact point that the test receives on
# the host. A67's clients run in the client image on the API's network, with
# a stand-in for rtl_fm (internal/radio/standin). Memory of the monitoring
# stack's containers is sampled throughout, idle first and then during the
# checks.
#
# The monitoring stack is installed as a deployment does it (R84, A78): a folder
# with only the release's archive, compose file and filled-in settings, and
# only upstream's images. It's checked like that before the test's own
# overlay is added for the alert checks.
#
# usage: scripts/monitoring-checks.sh [-o OUT] [-s SCREENSHOTS]
#   -o OUT          where the memory samples and summary go (default monitoring-results)
#   -s SCREENSHOTS  also feed the stack test data and take screenshots of a
#                   page's trace, the three dashboards and a firing alert into
#                   this folder (needs Node with web/'s dependencies and
#                   Playwright's Chromium)
# PA_MON_RUN, if set, runs only the checks it matches (go test -run), e.g.
# TestA67; mutation-checks.sh uses it.
# needs: Docker with compose, Go, curl, and no network named monitoring (the
# stack owns that name, on the whole host). Uses 127.0.0.1:18091, :13001, :14319,
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

if docker network inspect monitoring >/dev/null 2>&1; then
	echo "a network named monitoring already exists here: the stack owns that name" >&2
	exit 1
fi

P=pa-mon-$$
BACKUPS=$(mktemp -d)
export BACKUP_DIR="$BACKUPS" BACKUP_UID="$(id -u)" BACKUP_GID="$(id -g)"
# The automatic upgrades' status folder (R84h), which the API and Alloy both
# mount read-only, as docs/deploy.md has a deployment set it. A90's Grafana
# check writes in it as the host's script does.
AUTOUP=$(mktemp -d)
chmod 755 "$AUTOUP"
export AUTO_UPGRADE_STATUS_DIR="$AUTOUP"
export PAGERARCHIVE_VERSION=montest API_BIND=127.0.0.1:18091 AUTHENTIK_BIND=127.0.0.1:19091 MAILPIT_BIND=127.0.0.1:18026 PA_MON_DB_BIND=127.0.0.1:55437
GRAFANA_ADMIN_PASSWORD=test-grafana-admin-password
. ./scripts/install-lib.sh
MONROOT=$(mktemp -d)
MONDIR=$MONROOT/deploy/monitoring
OVERRIDE=$MONROOT/pagerarchive-override.yaml
APP_FILES="-f $PWD/compose.yaml -f $PWD/deploy/test/compose.yaml -f $PWD/deploy/monitoring/test/pagerarchive.yaml -f $OVERRIDE"
app() { docker compose -p "$P" --project-directory "$PWD" $APP_FILES "$@"; }
mon() { in_folder "$MONDIR" -p "$P-mon" "$@"; }

SAMPLER=""
BIN=$(mktemp -d)
cleanup() {
	[ -n "$SAMPLER" ] && kill "$SAMPLER" 2>/dev/null || true
	docker ps -aq --filter "label=pagerarchive.montest=$P" | xargs -r docker rm -f >/dev/null 2>&1 || true
	rm -rf "$BIN"
	mon logs --no-color >"$OUT/monitoring-stack.log" 2>&1 || true
	app logs --no-color api >"$OUT/api.log" 2>&1 || true
	if [ -z "${MON_KEEP:-}" ]; then
		# PagerArchive first: the stack can't remove its network while it's joined.
		app down -v --remove-orphans >/dev/null 2>&1 || true
		mon down -v --remove-orphans >/dev/null 2>&1 || true
		rm -rf "$MONROOT" "$BACKUPS" "$AUTOUP"
	fi
}
trap cleanup EXIT

# The override from the docs, as it's written there, without the list item's indent.
awk '/<!-- monitoring:override -->/ { found = 1; next } found && /^ *```yaml/ { on = 1; next } on && /^ *```/ { exit } on { sub(/^   /, ""); print }' \
	docs/monitoring.md >"$OVERRIDE"
grep -q 'external: true' "$OVERRIDE" || { echo "no override example found in docs/monitoring.md" >&2; exit 1; }

echo "--- building the API and client images and rtl_fm's stand-in ($P)"
app build -q api
docker build -q -f Dockerfile.client -t "pagerarchive-client:montest" . >/dev/null
CGO_ENABLED=0 go build -o "$BIN/rtl_fm" ./internal/radio/standin
export PA_MON_CLIENT_IMAGE=pagerarchive-client:montest PA_MON_STANDIN="$BIN/rtl_fm" PA_MON_NETWORK=monitoring \
	PA_MON_API_ADDRESS=pagerarchive:8080 PA_MON_LABEL="pagerarchive.montest=$P"

# mon_up: `docker compose up -d` in the folder, and Grafana answering.
mon_up() {
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
}

echo "--- installing the monitoring stack from the release's files, with no checkout ($P-mon)"
sh scripts/ci/release-assets.sh "$MONROOT/assets"
tar -xzf "$MONROOT/assets/pagerarchive-config.tar.gz" -C "$MONROOT"
cp "$MONROOT/assets/monitoring-compose.yaml" "$MONDIR/compose.yaml"
cp "$MONROOT/assets/monitoring-env.example" "$MONDIR/.env"
cat >"$MONROOT/vars" <<VARS
GRAFANA_ADMIN_PASSWORD=$GRAFANA_ADMIN_PASSWORD
PA_METRICS_TOKEN=$METRICS_TOKEN
GRAFANA_BIND=127.0.0.1:13001
GRAFANA_ROOT_URL=http://127.0.0.1:13001
AUTO_UPGRADE_STATUS_DIR=$AUTOUP
VARS
fill_env "$MONDIR/.env" "$MONROOT/vars"
chmod -R a+rX "$MONROOT"
listing "$MONROOT" >"$MONROOT.before"
mon_up
status=0

# The stack first, as the docs say: PagerArchive's override needs its network.
echo "--- PagerArchive joins the stack's network through the docs' override"
cat "$OVERRIDE"
app up -d db api backup backup-check >/dev/null 2>&1 || { app up -d db api backup backup-check; exit 1; }
i=0
until curl -sf http://127.0.0.1:18091/healthz >/dev/null; do
	i=$((i + 1))
	[ $i -gt 120 ] && { echo "API didn't start" >&2; app logs api | tail -20 >&2; exit 1; }
	sleep 1
done
# There's no Authentik here, and a backup refuses a database with no tables.
app exec -T db psql -q -v ON_ERROR_STOP=1 -U postgres -d authentik -c 'CREATE TABLE a80_stand_in (id int)'
# Only Alloy: what else other apps on the network could reach (R76).
on_network=$(docker network inspect monitoring -f '{{range .Containers}}{{.Name}} {{end}}')
echo "--- on the monitoring network: $on_network"
case $on_network in *tempo* | *loki* | *prometheus* | *grafana*) echo "FAIL: more than Alloy is on the monitoring network" >&2; status=1 ;; esac
case $on_network in *-db-*) echo "FAIL: PagerArchive's database is on the monitoring network" >&2; status=1 ;; esac
if listing "$MONROOT" | diff "$MONROOT.before" - >&2; then
	echo "--- the folder holds what it did before up -d"
else
	echo "FAIL: the folder changed" >&2
	status=1
fi
rm -f "$MONROOT.before"
IMAGE_TAG=none check_images "$P-mon" grafana/alloy grafana/tempo grafana/loki prom/prometheus grafana/grafana || status=1
PA_MON_GRAFANA_URL=http://127.0.0.1:13001 PA_MON_GRAFANA_PASSWORD="$GRAFANA_ADMIN_PASSWORD" \
	go test -count=1 -v -run TestA78MonitoringInstalled ./internal/monitortest/ || status=1

# From here on, the test's own overlay: a webhook contact point and Alloy's OTLP port.
cp deploy/monitoring/test/compose.yaml "$MONDIR/compose.override.yaml"
mkdir -p "$MONDIR/test"
cp deploy/monitoring/test/alerting-test.yaml "$MONDIR/test/"
chmod -R a+rX "$MONDIR/test"
cat >"$MONROOT/vars" <<VARS
ALLOY_OTLP_BIND=127.0.0.1:14319
PA_ALERT_WEBHOOK_URL=http://host.docker.internal:18099/alert
VARS
fill_env "$MONDIR/.env" "$MONROOT/vars"
mon_up

# Loki refuses every write above 90% disk use (its WAL's disk_full_threshold),
# so A67 would find no client lines (ADR 0014): stop now if the disk is close.
disk_used() { df -P "$(docker info -f '{{.DockerRootDir}}')" | awk 'NR == 2 { sub("%", "", $5); print $5 }'; }
echo "--- disk use under Docker's data: $(disk_used)% (Loki refuses writes above 90%)"
if [ "$(disk_used)" -ge 85 ]; then
	echo "the disk is $(disk_used)% full: Loki would stop taking logs during the checks; free some space first" >&2
	exit 1
fi
export PA_MON_LOKI_CONTAINER="$P-mon-loki-1"

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

PA_MON_REQUIRE=1 PA_MON_API_URL=http://127.0.0.1:18091 PA_MON_GRAFANA_URL=http://127.0.0.1:13001 \
	PA_MON_GRAFANA_PASSWORD="$GRAFANA_ADMIN_PASSWORD" PA_MON_WEBHOOK_ADDR=0.0.0.0:18099 \
	PA_MON_DB_URL="postgres://postgres:$POSTGRES_PASSWORD@127.0.0.1:55437/postgres?sslmode=disable" \
	PA_MON_STOP_API="docker compose -p $P --project-directory $PWD $APP_FILES stop api" \
	PA_MON_START_API="docker compose -p $P --project-directory $PWD $APP_FILES start api" \
	PA_MON_STOP_DB="docker compose -p $P --project-directory $PWD $APP_FILES stop db" \
	PA_MON_START_DB="docker compose -p $P --project-directory $PWD $APP_FILES start db" \
	PA_MON_BACKUP_NOW="docker compose -p $P --project-directory $PWD $APP_FILES exec -T backup pagerarchive-backup now" \
	PA_MON_AUTO_UPGRADE_DIR="$AUTOUP" \
	go test -count=1 -timeout 45m -v -run "${PA_MON_RUN:-TestA65|TestA67|TestA80|TestA90}" ./internal/monitortest/ || status=1
echo "--- disk use under Docker's data after the checks: $(disk_used)%"

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
