#!/bin/sh
# What a backup costs at A75's size (ADR 0020): 4,562,500 made-up pages, a backup
# from the test-only trigger, then a restore into a new install. Memory of the
# db, backup and backup-check containers is sampled each second or so: what
# Docker reports, and the container's own memory without its file cache. By hand,
# not in CI: it takes the better part of an hour.
#
# usage: scripts/a80-size.sh [-o OUT] [-p PAGES]
# needs: Docker with compose, Go. Uses 127.0.0.1:55439, 18092, 19092 and 18027.
set -eu
cd "$(dirname "$0")/.."

OUT=a80-size-results PAGES=4562500
while getopts o:p: opt; do
	case $opt in
	o) OUT=$OPTARG ;; p) PAGES=$OPTARG ;; *) exit 2 ;;
	esac
done
mkdir -p "$OUT"
OUT=$(cd "$OUT" && pwd)

export COMPOSE_PROJECT_NAME="pa-a80s-$$"
export PAGERARCHIVE_VERSION=a80size STACK_OVERLAYS="-f deploy/test/import.yaml" API_BIND=127.0.0.1:18092 AUTHENTIK_BIND=127.0.0.1:19092 MAILPIT_BIND=127.0.0.1:18027
BACKUPS=$(mktemp -d)
export BACKUP_DIR="$BACKUPS" BACKUP_UID="$(id -u)" BACKUP_GID="$(id -g)"
. ./scripts/stack-lib.sh

SAMPLER=""
cleanup() {
	[ -z "$SAMPLER" ] || kill "$SAMPLER" 2>/dev/null || true
	stack_compose logs --no-color backup backup-check >"$OUT/a80-size-logs.txt" 2>&1 || true
	stack_down
	rm -rf "$BACKUPS"
}
trap cleanup EXIT

stack_compose build -q api
stack_compose up -d
i=0
until curl -sf http://127.0.0.1:18092/healthz >/dev/null; do
	i=$((i + 1))
	[ $i -lt 300 ] || { echo "API didn't start" >&2; exit 1; }
	sleep 1
done
# Authentik's tables, so its dump is a real one too.
i=0
until [ "$(stack_compose exec -T db psql -At -U postgres -d authentik -c "SELECT count(*) FROM pg_tables WHERE tablename = 'authentik_core_user'")" = 1 ]; do
	i=$((i + 1))
	[ $i -lt 300 ] || { echo "Authentik didn't set up its database" >&2; exit 1; }
	sleep 2
done
sleep 60
stack_compose exec -T db psql -q -v ON_ERROR_STOP=1 -U postgres >/dev/null <scripts/fixture.sql
go run scripts/a80/seed.go -db "postgres://postgres:$POSTGRES_PASSWORD@127.0.0.1:${PA_A76_DB_PORT:-55439}/postgres?sslmode=disable" -pages "$PAGES"
stack_compose exec -T db psql -At -U postgres -c "SELECT 'database: ' || pg_size_pretty(pg_database_size('postgres')) || ', messages: ' || count(*) FROM messages"

PHASE=$OUT/phase
echo dump >"$PHASE"
echo "t,phase,container,mem_mib,anon_mib" >"$OUT/a80-size-stats.csv"
(
	T0=$(date +%s)
	while :; do
		for s in db backup backup-check; do
			id=$(stack_compose ps -q "$s" 2>/dev/null) || continue
			[ -n "$id" ] || continue
			stat=/sys/fs/cgroup/system.slice/docker-$id.scope/memory.stat
			awk -v t="$(($(date +%s) - T0))" -v p="$(cat "$PHASE")" -v s="$s" -v cur="$(cat "${stat%memory.stat}memory.current" 2>/dev/null || echo 0)" \
				'$1 == "anon" { printf "%s,%s,%s,%.1f,%.1f\n", t, p, s, cur / 1048576, $2 / 1048576 }' "$stat" 2>/dev/null || true
		done >>"$OUT/a80-size-stats.csv"
		sleep 1
	done
) &
SAMPLER=$!

stack_compose exec -T backup pagerarchive-backup now
night=$(TZ=$TZ date +%F)
ls -l "$BACKUPS" | tee "$OUT/a80-size-folder.txt"
cat "$BACKUPS/$night.check"
du -sh "$BACKUPS"

echo restore >"$PHASE"
stack_compose stop api authentik-server authentik-worker backup backup-check
stack_compose rm -fsv db
docker volume rm "${COMPOSE_PROJECT_NAME}_db"
start=$(date +%s)
stack_compose run --rm -T backup restore "$night"
echo "--- restore took $(($(date +%s) - start)) s"
stack_compose exec -T db psql -At -U postgres -c "SELECT 'restored messages: ' || count(*) FROM messages"
kill "$SAMPLER" 2>/dev/null || true
SAMPLER=""

awk -F, 'NR > 1 {
		k = $2 " " $3
		if ($4 > mem[k]) mem[k] = $4
		if ($5 > anon[k]) anon[k] = $5
		first[$2] = first[$2] == "" ? $1 : first[$2]; last[$2] = $1
	}
	END { for (k in mem) printf "%-22s peak %7.1f MiB, without file cache %7.1f MiB\n", k, mem[k], anon[k] }' \
	"$OUT/a80-size-stats.csv" | sort | tee "$OUT/a80-size-memory.txt"
