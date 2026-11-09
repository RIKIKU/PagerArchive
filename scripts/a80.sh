#!/bin/sh
# A80, backups (SPEC.md R86-R86c, ADR 0020). A folder made only from what a
# release attaches, with made-up users, aliases, templates with icons,
# integrations and pages (A77's, internal/stacktest). Then the night's backup,
# first by the schedule, then by the test-only trigger with a controllable clock
# (`pagerarchive-backup now --night --at`, as A12 calls the nightly job), and
# what goes wrong. The last step removes the stack with its volumes and restores
# into a new install with the documented command. The alert firing is
# TestA80ToldWhenABackupFails, in the monitoring stack's check.
#
# The folder also holds a test-only compose.override.yaml (deploy/test/backup.yaml):
# a stand-in for pg_dump that runs the real one, and can hold it open or spoil
# what it wrote, so a half-written dump and a bad one can be seen.
#
# Images built here go to a throwaway registry on this machine, never to GHCR.
#
# usage: scripts/a80.sh [-o OUT] [playwright arguments]
# needs: Docker with compose, Go, Node with web/ dependencies, Playwright browsers.
# Uses ports 8080, 9000, 8025, 14323 and 15000 on 127.0.0.1.
set -eu
cd "$(dirname "$0")/.."

OUT=a80-results
if [ "${1:-}" = -o ]; then
	OUT=$2
	shift 2
fi
mkdir -p "$OUT"
OUT=$(cd "$OUT" && pwd)

export COMPOSE_PROJECT_NAME="pa-a80-$$"
# A23's first admin and allowed domain, which internal/stacktest signs in with.
export ALLOWED_EMAIL_DOMAINS=example.com INITIAL_ADMIN_EMAIL=owner@example.org
. ./scripts/stack-lib.sh
. ./scripts/install-lib.sh
VERSION=$(tr -d ' \r\n' <VERSION)
IMAGE_TAG=${VERSION%.*}
WORK=$(mktemp -d)
DIR=$WORK/deployment
CTL=$WORK/control
BACKUPS=$DIR/backups
MAILPIT=pa-a80-mailpit-$$
stack_compose() { in_folder "$DIR" "$@"; }

status=0
fail() {
	echo "FAIL: $*" >&2
	status=1
}

cleanup() {
	stack_compose logs --no-color api backup backup-check db >"$OUT/a80-logs.txt" 2>&1 || true
	docker rm -fv "$MAILPIT" >/dev/null 2>&1 || true
	stack_compose down -v --remove-orphans >/dev/null 2>&1 || true
	registry_stop
	rm -rf "$WORK"
}
trap cleanup EXIT

start_mailpit() {
	docker run -d --name "$MAILPIT" --network "${COMPOSE_PROJECT_NAME}_default" --network-alias mailpit \
		-e MP_SMTP_AUTH_ACCEPT_ANY=true -e MP_SMTP_AUTH_ALLOW_INSECURE=true \
		-p 127.0.0.1:8025:8025 axllent/mailpit:v1.31.5 >/dev/null
}

# The test-only trigger, with the clock's arguments.
backup_now() { stack_compose exec -T backup pagerarchive-backup now "$@"; }
scrape() { curl -sf -H "Authorization: Bearer $METRICS_TOKEN" http://127.0.0.1:8080/metrics; }
# metric <name>: its value, or nothing.
metric() { scrape | awk -v m="$1" '$1 == m { print $2 }'; }
# nights_kept: how many nights have their PagerArchive dump.
nights_kept() { find "$BACKUPS" -name '*.pagerarchive.dump' | wc -l; }
# wait_metric <name> <value>: the API reads the file at each scrape, so at once.
want_metric() {
	got=$(metric "$1")
	[ "$got" = "$2" ] || fail "$1 is ${got:-absent}, want $2"
}

echo "=== A80: an install from the release's files, in an empty folder"
sh scripts/ci/release-assets.sh "$WORK/assets"
registry_start
registry_publish . "$IMAGE_TAG"
install_folder "$DIR" "$WORK/assets"
use_registry "$DIR/compose.yaml"
mkdir -p "$CTL" "$DIR/test-backup"
cp scripts/test-backup/pg_dump "$DIR/test-backup/pg_dump"
cp deploy/test/backup.yaml "$DIR/compose.override.yaml"
chmod -R a+rX "$WORK"
cat >"$WORK/vars" <<VARS
ALLOWED_EMAIL_DOMAINS=$ALLOWED_EMAIL_DOMAINS
INITIAL_ADMIN_EMAIL=$INITIAL_ADMIN_EMAIL
BACKUP_DIR=./backups
BACKUP_UID=$(id -u)
BACKUP_GID=$(id -g)
PA_TEST_CONTROL=$CTL
VARS
fill_env "$DIR/.env" deploy/test/stack.vars "$WORK/vars"
chmod 777 "$CTL"

stack_compose up -d
start_mailpit
stack_wait_ready
gateway=$(docker network inspect "${COMPOSE_PROJECT_NAME}_default" -f '{{(index .IPAM.Config 0).Gateway}}')
key=$(stack_compose exec -T api /usr/local/bin/pagerarchive client add a80-receiver "A80 receiver" | tail -n 1)
export PA_STACK_APP_URL=http://localhost:8080 PA_STACK_AUTHENTIK_URL=http://localhost:9000 \
	PA_STACK_MAILPIT_URL=http://localhost:8025 PA_STACK_AUTHENTIK_TOKEN="$AUTHENTIK_API_TOKEN" \
	PA_STACK_METRICS_TOKEN="$METRICS_TOKEN" PA_STACK_CLIENT_KEY="$key" \
	PA_A77_STATE="$OUT/a80-state.json" PA_A77_HOOK_HOST="$gateway"
PA_STACK_PHASE=a77-seed go test -count=1 -v -run 'TestA77Seed' ./internal/stacktest/ || exit 1

echo "=== A80: nothing to do by hand: a new install hasn't failed, and has no backup yet"
want_metric pagerarchive_backup_failing 0
[ -z "$(metric pagerarchive_backup_last_good_timestamp_seconds)" ] || fail "a last good backup on a new install"
net=$(docker inspect -f '{{.HostConfig.NetworkMode}}' "$(stack_compose ps -q backup-check)")
[ "$net" = none ] || fail "backup-check's network is $net"
ifaces=$(stack_compose exec -T backup-check sh -c "tail -n +3 /proc/net/dev | cut -d: -f1 | tr -d ' '")
[ "$ifaces" = lo ] || fail "backup-check has interfaces: $ifaces"
echo "--- the check's container has no network: $net, interfaces: $ifaces"

echo "=== A80: by the schedule: BACKUP_AT 2 minutes from now, and nothing else"
# BACKUP_AT is a time of day: not across midnight.
while [ "$(TZ=$TZ date +%H%M)" -ge 2350 ] || [ "$(TZ=$TZ date +%H%M)" -lt 0005 ]; do sleep 30; done
stack_compose exec -T backup rm -f /status/status.json
printf 'BACKUP_AT=%s\n' "$(TZ=$TZ date -d '+2 minutes' +%H:%M)" >"$WORK/vars"
fill_env "$DIR/.env" "$WORK/vars"
stack_compose up -d backup
today=$(TZ=$TZ date +%F)
i=0
until [ -n "$(metric pagerarchive_backup_last_good_timestamp_seconds)" ]; do
	i=$((i + 1))
	if [ $i -gt 120 ]; then
		fail "no good backup 6 minutes after BACKUP_AT was set"
		stack_compose logs --tail 20 backup backup-check >&2
		exit 1
	fi
	sleep 3
done
stack_compose logs --no-color backup | grep 'good:' | tail -n 1
want_metric pagerarchive_backup_failing 0
# R86d: the night's dump size, dump time and check time, as the log says.
size=$(cat "$BACKUPS/$today.pagerarchive.dump" "$BACKUPS/$today.authentik.dump" | wc -c)
want_metric pagerarchive_backup_last_good_bytes "$size"
logged=$(stack_compose logs --no-color backup | grep 'good:' | tail -n 1)
for m in dump check; do
	[ -n "$(metric pagerarchive_backup_last_good_${m}_seconds)" ] || fail "no pagerarchive_backup_last_good_${m}_seconds"
done
dump_s=$(metric pagerarchive_backup_last_good_dump_seconds)
check_s=$(metric pagerarchive_backup_last_good_check_seconds)
case $logged in
*"dumps took $dump_s s, the check $check_s s, $size bytes"*) ;;
*) fail "the metrics say $dump_s s, $check_s s and $size bytes, the log: $logged" ;;
esac
echo "--- R86d: pagerarchive_backup_last_good_bytes $size, _dump_seconds $dump_s, _check_seconds $check_s"
for f in "$today.pagerarchive.dump" "$today.authentik.dump" "$today.counts.tsv" "$today.check"; do
	[ -s "$BACKUPS/$f" ] || fail "no $f in the folder"
done
[ "$(cat "$BACKUPS/$today.check")" = ok ] || fail "the check says $(cat "$BACKUPS/$today.check")"
[ -z "$(find "$BACKUPS" -name '*.partial')" ] || fail "a .partial file was left behind"
mode=$(stat -c '%a %u %g' "$BACKUPS/$today.authentik.dump")
[ "$mode" = "640 $(id -u) $(id -g)" ] || fail "the dump's mode and owner are $mode"
for table in public.messages public.aliases public.templates public.integrations; do
	grep -q "^pagerarchive	$table	" "$BACKUPS/$today.counts.tsv" || fail "$table isn't in the counts"
done
grep -q '^authentik	public.authentik_core_user	' "$BACKUPS/$today.counts.tsv" || fail "Authentik's users aren't in the counts"
ls -l "$BACKUPS"

echo "=== A80: a dump is under a temporary name until it's complete"
touch "$CTL/hold"
backup_now --night 2030-01-01 >"$WORK/held.txt" 2>&1 &
held=$!
i=0
until [ -e "$CTL/held" ]; do
	i=$((i + 1))
	[ $i -lt 300 ] || { fail "the stand-in was never reached"; break; }
	sleep 0.2
done
in_progress=$(ls "$BACKUPS" | grep '^2030-01-01' || true)
echo "--- in the folder during the dump: $in_progress"
case $in_progress in
*2030-01-01.pagerarchive.dump.partial*) ;;
*) fail "no temporary file during the dump" ;;
esac
if printf '%s\n' "$in_progress" | grep -qE '\.(dump|counts\.tsv|check)$'; then
	fail "a final name is there before the dump is complete: $in_progress"
fi
rm -f "$CTL/hold"
wait $held || fail "the held backup failed: $(cat "$WORK/held.txt")"
[ -s "$BACKUPS/2030-01-01.pagerarchive.dump" ] || fail "no final name once it's complete"

echo "=== A80: the check: a dump that won't restore"
good=$(metric pagerarchive_backup_last_good_timestamp_seconds)
touch "$CTL/truncate"
if backup_now --night 2030-01-02 >"$WORK/out.txt" 2>&1; then fail "a truncated dump passed: $(cat "$WORK/out.txt")"; fi
rm -f "$CTL/truncate"
echo "--- $(cat "$BACKUPS/2030-01-02.check")"
want_metric pagerarchive_backup_failing 1
want_metric pagerarchive_backup_last_good_timestamp_seconds "$good"

echo "=== A80: the check: a dump with a table's rows missing"
touch "$CTL/no-data"
if backup_now --night 2030-01-03 >"$WORK/out.txt" 2>&1; then fail "a dump with rows missing passed: $(cat "$WORK/out.txt")"; fi
rm -f "$CTL/no-data"
echo "--- $(cat "$BACKUPS/2030-01-03.check")"
grep -q 'public.goose_db_version' "$BACKUPS/2030-01-03.check" || fail "the check doesn't name the table that differs"
want_metric pagerarchive_backup_failing 1
if stack_compose run --rm -T backup restore 2030-01-03 >"$WORK/out.txt" 2>&1; then fail "a restore of a night that failed its check went ahead"; fi
grep -q "didn't pass its check" "$WORK/out.txt" || fail "restore of a failed night: $(cat "$WORK/out.txt")"
if stack_compose run --rm -T backup restore 2030-01-01 >"$WORK/out.txt" 2>&1; then fail "a restore into a stack in use went ahead"; fi
grep -q "isn't empty" "$WORK/out.txt" || fail "restore into a stack in use: $(cat "$WORK/out.txt")"
echo "--- restore refused a night that failed its check, and a stack in use"

echo "=== A80: more than 36 hours since the last good backup (the clock)"
backup_now --night 2030-01-04 --at $(($(date +%s) - 37 * 3600)) >/dev/null
want_metric pagerarchive_backup_failing 1

echo "=== A80: a backup moves the last-good metric"
before=$(metric pagerarchive_backup_last_good_timestamp_seconds)
backup_now --night 2030-01-05
want_metric pagerarchive_backup_failing 0
after=$(metric pagerarchive_backup_last_good_timestamp_seconds)
[ "$after" != "$before" ] || fail "the last-good metric didn't move"
echo "--- pagerarchive_backup_last_good_timestamp_seconds: $before to $after"

echo "=== A80: 15 nights pass: 14 are kept"
n=1
while [ $n -le 15 ]; do
	night=2031-01-$(printf '%02d' $n)
	backup_now --night "$night" >/dev/null || fail "night $night failed"
	n=$((n + 1))
done
kept=$(nights_kept)
echo "--- nights kept: $kept, oldest $(ls "$BACKUPS" | head -n 1 | cut -d. -f1), newest $(ls "$BACKUPS" | tail -n 1 | cut -d. -f1)"
[ "$kept" = 14 ] || fail "$kept nights kept after 15, want 14"
[ ! -e "$BACKUPS/2031-01-01.pagerarchive.dump" ] && [ ! -e "$BACKUPS/2031-01-01.authentik.dump" ] && [ ! -e "$BACKUPS/2031-01-01.counts.tsv" ] && [ ! -e "$BACKUPS/2031-01-01.check" ] ||
	fail "the 15th night back is still there"
[ -e "$BACKUPS/2031-01-02.pagerarchive.dump" ] || fail "the 14th night back is gone"

echo "=== A80: the setting"
stack_compose exec -T -e BACKUP_KEEP_DAYS=2 backup pagerarchive-backup now --night 2031-01-16 >/dev/null
kept=$(nights_kept)
[ "$kept" = 2 ] || fail "$kept nights kept with BACKUP_KEEP_DAYS=2"
echo "--- with BACKUP_KEEP_DAYS=2: $kept nights kept"

echo "=== A80: a backup fails, with the database stopped; Administration shows it"
stack_compose stop db
if backup_now --night 2031-01-17 >"$WORK/out.txt" 2>&1; then fail "a backup with the database stopped passed"; fi
cat "$WORK/out.txt"
stack_compose start db
stack_compose restart authentik-server authentik-worker
stack_wait_ready
want_metric pagerarchive_backup_failing 1
(cd web && PA_URL=http://localhost:8080 PA_OWNER_EMAIL="$INITIAL_ADMIN_EMAIL" PA_A80_EXPECT=failed \
	npx playwright test tests/a80.spec.ts "$@") || fail "Administration didn't show the failed backup"

echo "=== A80: the next good backup clears it"
night=2031-01-18
backup_now --night $night
want_metric pagerarchive_backup_failing 0
(cd web && PA_URL=http://localhost:8080 PA_OWNER_EMAIL="$INITIAL_ADMIN_EMAIL" PA_A80_EXPECT=ok \
	npx playwright test tests/a80.spec.ts "$@") || fail "Administration still shows a failure after a good backup"

echo "=== A80: P1: no page text and no address in the backup services' logs, with their failures in them"
logs=$(stack_compose logs --no-color backup backup-check)
if printf '%s' "$logs" | grep -qiE 'BEFORE UPGRADE|FICTION ST|@example\.(com|org|test)'; then
	fail "page text or an address in the backup services' logs"
fi
echo "--- backup log lines checked: $(printf '%s\n' "$logs" | wc -l)"

echo "=== A80: the stack is removed with its volumes, and a new install restores the night"
docker rm -fv "$MAILPIT" >/dev/null
stack_compose down -v --remove-orphans
[ -s "$BACKUPS/$night.pagerarchive.dump" ] || fail "the backup folder didn't survive down -v"
# The documented command, as docs/deploy.md has it, with this night's date.
command=$(awk '/<!-- a80:restore -->/ { found = 1; next } found && /^```sh/ { on = 1; next } on && /^```/ { exit } on' docs/deploy.md | sed "s/2026-11-03/$night/")
echo "--- $command"
[ -n "$command" ] || fail "no restore command found in docs/deploy.md"
(cd "$DIR" && env -i PATH="$PATH" HOME="$HOME" COMPOSE_PROJECT_NAME="$COMPOSE_PROJECT_NAME" sh -c "$command") || fail "the restore failed"
stack_compose up -d
start_mailpit
stack_wait_ready
echo "--- running: $(stack_compose exec -T api /usr/local/bin/pagerarchive version)"
PA_STACK_PHASE=a77-check go test -count=1 -v -run 'TestA77Check' ./internal/stacktest/ || fail "after the restore, the data isn't all there"

echo "=== A80: the restored install backs up too"
backup_now --night 2031-01-19
want_metric pagerarchive_backup_failing 0

stack_check_logs || status=1
if [ "$status" -eq 0 ]; then echo "=== A80 passed"; else echo "=== A80 FAILED"; fi
exit $status
