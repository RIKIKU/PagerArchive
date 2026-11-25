#!/bin/sh
# Checks Back up now in deploy/backup/backup.sh (SPEC.md R86a) without Docker
# or a database: the backup service's loop, run in a folder (PA_TEST_ROOT),
# with stand-ins for psql, pg_dump, pg_isready and sleep, and this script
# playing backup-check (each new night's check says "ok").
#   - on start, the request folder is made, writable but not listable by
#     others (1733), and a run that was stopped part way isn't "running";
#   - a request runs the backup and check, then it's deleted, and the status
#     has the result; a good one counts as the day's (last_night), a failed
#     one doesn't, so the nightly backup still runs after it;
#   - while it runs, the status says so (running_since) and the request stays;
#   - a request waits while another backup (`now`) holds the lock;
#   - a request that's a link is deleted, and what it points to isn't touched.
#   - extra databases (R86e): dumped with the night, the named tables' rows
#     left out, each checked and its state written; one failing leaves
#     PagerArchive's backup good; a bad setting is refused, in the status.
# The API's side (refusing a second request, admins only) is internal/backup's
# and internal/acceptance's; the real thing is A80 (scripts/a80.sh).
#
# usage: scripts/backup-checks.sh
set -eu
repo=$(cd "$(dirname "$0")/.." && pwd)
work=$(mktemp -d)
serve_pid=
check_pid=
cleanup() {
	[ -z "$serve_pid" ] || kill "$serve_pid" 2>/dev/null || true
	[ -z "$check_pid" ] || kill "$check_pid" 2>/dev/null || true
	wait 2>/dev/null || true
	rm -rf "$work"
}
trap cleanup EXIT
failed=0
fail() {
	echo "FAIL: $*" >&2
	failed=1
}

root=$work/root ctl=$work/control bin=$work/bin
mkdir -p "$root/backups" "$root/status" "$root/tmp" "$ctl" "$bin"
export PA_TEST_ROOT="$root" PA_FAKE="$ctl" PATH="$bin:$PATH"
# Never by the schedule: only requests (and `now`) back up here.
export BACKUP_AT=24:00 BACKUP_UID="$(id -u)" BACKUP_GID="$(id -g)"
real_sleep=$(command -v sleep)

cat >"$bin/sleep" <<SH
#!/bin/sh
exec $real_sleep 0.05
SH
cat >"$bin/pg_isready" <<'SH'
#!/bin/sh
[ ! -e "$PA_FAKE/down" ]
SH
# The snapshot's holder gets the snapshot's id; the counts' query, one table.
cat >"$bin/psql" <<'SH'
#!/bin/sh
# -c: the check that the database is there, which it is.
for a; do
	if [ "$a" = -c ]; then
		echo 1
		exit 0
	fi
done
IFS= read -r first || exit 1
case $first in
*pg_export_snapshot*)
	echo 00000003-0000001B-1
	cat >/dev/null
	;;
*) printf 'public.things\t3\n' ;;
esac
SH
# pg_dump -d DB -Fc ... -f FILE: holds while $PA_FAKE/hold is there. Its
# arguments go to $PA_FAKE/pg_dump.args, a line a dump; a database named in
# $PA_FAKE/fail fails.
cat >"$bin/pg_dump" <<'SH'
#!/bin/sh
echo "$*" >>"$PA_FAKE/pg_dump.args"
while [ $# -gt 0 ]; do
	[ "$1" != -f ] || out=$2
	[ "$1" != -d ] || db=$2
	shift
done
if [ -e "$PA_FAKE/fail" ] && grep -qx "$db" "$PA_FAKE/fail"; then
	exit 1
fi
if [ -e "$PA_FAKE/hold" ]; then
	touch "$PA_FAKE/held"
	while [ -e "$PA_FAKE/hold" ]; do /bin/sleep 0.05; done
fi
echo dump >"$out"
SH
chmod +x "$bin"/*

status_file=$root/status/status.json
request=$root/requests/request.json
today=$(date +%F)
# field <key>: a value in the status file.
field() { sed -n "s/^ *\"$1\": \"\{0,1\}\([^\",]*\)\"\{0,1\},\{0,1\}\$/\1/p" "$status_file"; }
# until_true <what> <command...>: waits up to 20 s.
until_true() {
	what=$1
	shift
	i=0
	until "$@"; do
		i=$((i + 1))
		if [ $i -ge 400 ]; then
			fail "$what, after 20 s"
			return 1
		fi
		/bin/sleep 0.05
	done
}
gone() { [ ! -e "$request" ] && [ ! -L "$request" ]; }
running() { [ "$(field running_since)" != 0 ]; }
# answered: the request is deleted, and then the result written.
answered() { gone && ! running; }
# ask: what the API does: a new file, created only if there's none.
ask() { (set -C && echo '{"requested_at":"made up"}' >"$request"); }

# backup-check's part: each new night's check is "ok", an extra database's
# (NAME.check) first, as backup-check writes them.
(
	while :; do
		for c in "$root"/backups/*.counts.tsv; do
			[ -e "$c" ] || continue
			n=${c%.counts.tsv}
			[ ! -e "$n.check" ] || continue
			for d in "$n".*.dump; do
				[ -e "$d" ] || continue
				x=${d%.dump}
				case $x in *.pagerarchive | *.authentik) continue ;; esac
				echo ok >"$x.check"
			done
			echo ok >"$n.check"
		done
		/bin/sleep 0.05
	done
) &
check_pid=$!

# --- start: a run stopped part way, before this start
cat >"$status_file" <<'JSON'
{
  "since": 1767225600,
  "last_night": "",
  "last_run_at": 0,
  "last_run_ok": false,
  "running_since": 1767225700,
  "last_good_at": 0,
  "nights": 0,
  "error": ""
}
JSON
sh "$repo/deploy/backup/backup.sh" serve >"$work/serve.log" 2>&1 &
serve_pid=$!
until_true "the service didn't start" grep -q 'nightly at' "$work/serve.log"
mode=$(stat -c %a "$root/requests")
[ "$mode" = 1733 ] || fail "the request folder's mode is $mode, want 1733"
[ "$(field running_since)" = 0 ] || fail "a run stopped part way is still running: $(field running_since)"
echo "--- start: request folder $mode, a stopped run isn't running"

# --- a request with the database down: failed, deleted, and not the day's
touch "$ctl/down"
ask
until_true "a request wasn't answered" answered
rm -f "$ctl/down"
[ "$(field last_run_ok)" = false ] || fail "a backup with the database down was good"
[ "$(field error)" = "the database couldn't be reached" ] || fail "the error is $(field error)"
[ -z "$(field last_night)" ] || fail "a failed request counted as the day's backup: $(field last_night)"
[ "$(field running_since)" = 0 ] || fail "still running after a failure"
echo "--- a failed request: deleted, $(field error), the nightly backup still due"

# --- a good request, held part way: running, then the result
touch "$ctl/hold"
ask
until_true "the backup didn't start" test -e "$ctl/held"
running || fail "the status doesn't say it's running"
[ -e "$request" ] || fail "the request went before the backup finished"
ask 2>/dev/null && fail "a second request was written while one runs"
since=$(field running_since)
rm -f "$ctl/hold" "$ctl/held"
until_true "the request wasn't answered" answered
[ "$(field last_run_ok)" = true ] || fail "the requested backup wasn't good: $(field error)"
[ "$(field running_since)" = 0 ] || fail "still running after a good backup"
[ "$(field last_night)" = "$today" ] || fail "a good request isn't the day's backup: $(field last_night)"
for f in "$today.pagerarchive.dump" "$today.authentik.dump" "$today.counts.tsv" "$today.check"; do
	[ -s "$root/backups/$f" ] || fail "no $f"
done
[ "$(field last_good_night)" = "$today" ] || fail "the last good night is $(field last_good_night)"
echo "--- a good request: running since $since, then good, deleted, and the day's backup"
grep -q 'backing up now, as an admin asked' "$work/serve.log" || fail "the log doesn't say why it backed up"

# --- a request while another backup holds the lock waits for it
touch "$ctl/hold"
sh "$repo/deploy/backup/backup.sh" now --night 2030-01-01 >"$work/now.log" 2>&1 &
now_pid=$!
until_true "now didn't start" test -e "$ctl/held"
ask
/bin/sleep 0.5
[ -e "$request" ] || fail "a request was answered while another backup ran"
rm -f "$ctl/hold" "$ctl/held"
wait $now_pid || fail "now failed: $(cat "$work/now.log")"
until_true "the waiting request wasn't answered" answered
[ "$(field last_run_ok)" = true ] || fail "the waiting request's backup wasn't good"
grep -q 'another backup is running' "$work/serve.log" || fail "the log doesn't say the request waited"
echo "--- a request waits for a backup that's running, then runs"

# --- a request that's a link
echo keep >"$work/target"
ln -s "$work/target" "$request"
until_true "a link wasn't answered" answered
[ "$(cat "$work/target")" = keep ] || fail "what the link pointed to changed"
ln -s "$work/nothing-here" "$request"
until_true "a link to nothing wasn't answered" answered
echo "--- a link is answered and deleted, its target untouched"

# --- extra databases (R86e, A135): dumped with the night, rows left out of
# the tables named, checked on their own, and each one's state in its file
xfield() { sed -n "s/^ *\"$2\": \"\{0,1\}\([^\",]*\)\"\{0,1\},\{0,1\}\$/\1/p" "$root/status/databases/$1.json"; }
export BACKUP_EXTRA_DATABASES='companion:things;spare'
night=2030-02-01
: >"$ctl/pg_dump.args"
sh "$repo/deploy/backup/backup.sh" now --night $night >"$work/extra.log" 2>&1 || fail "a backup with extra databases failed: $(cat "$work/extra.log")"
for f in $night.companion.dump $night.spare.dump $night.companion.check $night.spare.check; do
	[ -s "$root/backups/$f" ] || fail "no $f"
done
grep -q "^companion	public.things	0\$" "$root/backups/$night.counts.tsv" || fail "the left-out table's count isn't 0: $(cat "$root/backups/$night.counts.tsv")"
grep -q "^spare	public.things	3\$" "$root/backups/$night.counts.tsv" || fail "spare's counts aren't in the night's"
grep -- '-d companion ' "$ctl/pg_dump.args" | grep -q -- '--exclude-table-data=public.things' || fail "companion's dump doesn't leave out public.things: $(cat "$ctl/pg_dump.args")"
grep -- '-d spare ' "$ctl/pg_dump.args" | grep -q -- '--exclude-table-data' && fail "spare's dump leaves rows out"
grep -- '-d postgres ' "$ctl/pg_dump.args" | grep -q -- '--exclude-table-data' && fail "PagerArchive's dump leaves rows out"
for x in companion spare; do
	[ "$(xfield $x last_run_ok)" = true ] || fail "$x's state isn't good: $(cat "$root/status/databases/$x.json")"
	[ "$(xfield $x last_good_night)" = $night ] || fail "$x's last good night is $(xfield $x last_good_night)"
	[ "$(xfield $x database)" = $x ] || fail "$x's state names $(xfield $x database)"
done
[ "$(field last_run_ok)" = true ] || fail "PagerArchive's backup with extras wasn't good"
echo "--- extra databases: dumped, checked, companion's things left out, each one's state good"

# One that fails doesn't fail PagerArchive's, and keeps its last good night.
echo spare >"$ctl/fail"
sh "$repo/deploy/backup/backup.sh" now --night 2030-02-02 >"$work/extra.log" 2>&1 || fail "an extra's failure failed the backup: $(cat "$work/extra.log")"
rm -f "$ctl/fail"
[ "$(field last_run_ok)" = true ] || fail "an extra's failure failed PagerArchive's backup: $(field error)"
[ "$(xfield spare last_run_ok)" = false ] || fail "spare's failed dump is good"
case $(xfield spare error) in *"dump failed"*) ;; *) fail "spare's error: $(xfield spare error)" ;; esac
[ "$(xfield spare last_good_night)" = $night ] || fail "spare's last good night went: $(xfield spare last_good_night)"
[ ! -e "$root/backups/2030-02-02.spare.dump" ] || fail "spare's failed dump is in the folder"
[ "$(xfield companion last_good_night)" = 2030-02-02 ] || fail "companion wasn't backed up with spare failing"
echo "--- an extra that fails: $(xfield spare error); PagerArchive's and companion's backups good"

# A bad setting: refused, and the status says why.
if BACKUP_EXTRA_DATABASES='companion:Calls' sh "$repo/deploy/backup/backup.sh" now --night 2030-02-03 >"$work/extra.log" 2>&1; then
	fail "a backup with a bad setting ran"
fi
grep -q "refusing to back up: BACKUP_EXTRA_DATABASES: Calls in companion isn't a table name" "$work/extra.log" || fail "a bad setting's message: $(cat "$work/extra.log")"
[ ! -e "$root/backups/2030-02-03.counts.tsv" ] || fail "a bad setting backed up"
[ "$(field last_run_ok)" = false ] || fail "a bad setting isn't a failure in the status"
case $(field error) in "the setting isn't valid: BACKUP_EXTRA_DATABASES: Calls in companion"*) ;; *) fail "a bad setting's status error: $(field error)" ;; esac
echo "--- a bad setting: refused, $(field error)"
unset BACKUP_EXTRA_DATABASES

if [ "$failed" -ne 0 ]; then
	echo "--- the service's log:" >&2
	cat "$work/serve.log" >&2
	exit 1
fi
echo "backup checks passed"
