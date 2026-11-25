#!/bin/sh
# Nightly backups (SPEC.md R86-R86c, ADR 0020), run by upstream's PostGIS image,
# so pg_dump always matches the database. Part of the release's archive.
#
#   serve         the `backup` service: dumps both databases each night at BACKUP_AT,
#                 and when an admin asks (Back up now: the API's request file)
#   databases     what's backed up, with BACKUP_EXTRA_DATABASES checked
#   check-serve   the `backup-check` service, which has no network: restores each
#                 new night's dumps into a throwaway database and compares them
#   now           one backup, at once: docker compose exec backup pagerarchive-backup now
#   restore NIGHT into an empty install:
#                 docker compose run --rm backup restore 2026-11-03
#
# A night in the folder (/backups):
#   2026-11-03.pagerarchive.dump, 2026-11-03.authentik.dump   pg_dump's custom format
#   2026-11-03.counts.tsv   each table's rows, from the snapshot the dump read
#   2026-11-03.check        the check's result: "ok", or "failed: why"
#   2026-11-03.NAME.dump, 2026-11-03.NAME.check   an extra database's (R86e)
# Each is written as <name>.partial and renamed when complete, and the check's
# trigger, counts.tsv, is renamed last. So a file with a final name is complete.
#
# Back up now (R86a): the API can't reach the dumps or Docker. It writes
# /requests/request.json, on a volume of its own (backup-request), and the
# serve loop sees it within a minute, runs the same backup and check, and then
# deletes it. Its contents are never read. While it's there, or a backup is
# running (running_since in the status), the API refuses another.
#
# Extra databases (R86e): another app on the host, a companion app, can have
# its own databases in this server backed up with PagerArchive's, by naming
# them in BACKUP_EXTRA_DATABASES:
#   NAME[:TABLE,TABLE...][;NAME...]     e.g. companion:calls,transmissions;other
# A TABLE is `table` (in the public schema) or `schema.table`: its rows are
# left out of the dump (pg_dump --exclude-table-data), so its count is 0. Each
# is dumped with the night's two, kept and pruned with them, restore-checked
# by backup-check on its own (NAME.check), and its state is written to
# status/databases/NAME.json for that app. A failed extra never fails
# PagerArchive's own backup. A bad setting refuses every backup, saying why in
# the log and the status.
#
# P1: what pg_dump, pg_restore and PostgreSQL say on failure can quote a row, so
# it's thrown away, and every error here is fixed text. Nothing reads a dump's
# contents into a message or a log line.
set -u

# PA_TEST_ROOT: the script's own checks (scripts/backup-checks.sh) run it in a
# folder rather than a container.
ROOT=${PA_TEST_ROOT:-}
DIR=$ROOT/backups
STATUS=$ROOT/status/status.json
# One file per extra database (R86e), for the app it belongs to.
EXTRA_STATUS_DIR=$ROOT/status/databases
REQUESTS=$ROOT/requests
REQUEST=$REQUESTS/request.json
LOCK=$ROOT/tmp/pagerarchive-backup.lock
KEEP=${BACKUP_KEEP_DAYS:-14}
AT=${BACKUP_AT:-04:00}
CHECK_TIMEOUT=${BACKUP_CHECK_TIMEOUT:-43200}
OWNER=${BACKUP_UID:-0}:${BACKUP_GID:-0}
SOCKET=/var/run/postgresql
# The SQL that gives the API's and the metric's roles their rights, which a
# restore gives back (R84i).
SQL_DIR=${PA_SQL_DIR:-/usr/local/share/pagerarchive}
THROWAWAY=/var/lib/postgresql/check
TAB=$(printf '\t')
# PostgreSQL's database name : the name in the folder.
DATABASES="postgres:pagerarchive authentik:authentik"

# Every ordinary table, as "SELECT name, rows" queries psql then runs (\gexec).
COUNT_SQL="SELECT format('SELECT %L, count(*) FROM %I.%I', n.nspname || '.' || c.relname, n.nspname, c.relname)
FROM pg_class c JOIN pg_namespace n ON n.oid = c.relnamespace
WHERE c.relkind = 'r' AND n.nspname <> 'information_schema' AND n.nspname !~ '^pg_'
ORDER BY 1 \\gexec"

say() { echo "backup: $*"; }

# place <file>: the owner and mode the folder's files have, before the rename.
place() {
	chown "$OWNER" "$1" && chmod 640 "$1"
}

# field <key> [file]: a value from the status file, or an extra database's,
# which only this script writes.
field() {
	sed -n "s/^ *\"$1\": \"\{0,1\}\([^\",]*\)\"\{0,1\},\{0,1\}\$/\1/p" "${2:-$STATUS}" 2>/dev/null
}

# ── Extra databases (R86e) ─────────────────────────────────────────────────
# A name or table as the setting may write it: lower case, so pg_dump's
# patterns and PostgreSQL's names agree, and nothing a shell or JSON would
# treat specially.
IDENT='[a-z_][a-z0-9_]{0,62}'

# shown <text>: an item from the setting, safe to print in a message.
shown() { printf '%s' "$1" | tr ',' ' ' | tr -cd 'A-Za-z0-9_.: -' | cut -c1-70; }

# parse_extra: BACKUP_EXTRA_DATABASES into EXTRA_NAMES ("name name") and
# EXTRA_TABLES_<name> ("schema.table,schema.table"), or EXTRA_ERROR saying
# what's wrong, and a non-zero return.
parse_extra() {
	EXTRA_NAMES="" EXTRA_ERROR=""
	_v=${BACKUP_EXTRA_DATABASES:-}
	[ -n "$_v" ] || return 0
	case $_v in *[[:space:]]*)
		EXTRA_ERROR="BACKUP_EXTRA_DATABASES has a space or line break in it"
		return 1
		;;
	esac
	_rest="$_v;"
	while [ -n "$_rest" ]; do
		_item=${_rest%%;*} _rest=${_rest#*;}
		if [ -z "$_item" ]; then
			EXTRA_ERROR="BACKUP_EXTRA_DATABASES has an empty entry (two semicolons together or one at an end)"
			return 1
		fi
		_name=${_item%%:*} _tables=""
		case $_item in *:*) _tables=${_item#*:} ;; esac
		if ! printf '%s' "$_name" | grep -Eqx "$IDENT"; then
			EXTRA_ERROR="BACKUP_EXTRA_DATABASES: $(shown "$_name") isn't a database name (lower-case letters and digits and _ only and at most 63 of them)"
			return 1
		fi
		case $_name in postgres | pagerarchive | authentik | template0 | template1)
			EXTRA_ERROR="BACKUP_EXTRA_DATABASES: $_name isn't an extra database: PagerArchive's own are always backed up"
			return 1
			;;
		esac
		case " $EXTRA_NAMES " in *" $_name "*)
			EXTRA_ERROR="BACKUP_EXTRA_DATABASES names $_name twice"
			return 1
			;;
		esac
		_list=""
		case $_item in *:*)
			case $_tables in *:*)
				EXTRA_ERROR="BACKUP_EXTRA_DATABASES: $(shown "$_item") has more than one colon: separate databases with a semicolon"
				return 1
				;;
			esac
			if [ -z "$_tables" ]; then
				EXTRA_ERROR="BACKUP_EXTRA_DATABASES: $_name has a colon but no tables after it"
				return 1
			fi
			_trest="$_tables,"
			while [ -n "$_trest" ]; do
				_t=${_trest%%,*} _trest=${_trest#*,}
				if [ -z "$_t" ]; then
					EXTRA_ERROR="BACKUP_EXTRA_DATABASES: $_name has an empty table name (two commas together or one at an end)"
					return 1
				fi
				if ! printf '%s' "$_t" | grep -Eqx "($IDENT\\.)?$IDENT"; then
					EXTRA_ERROR="BACKUP_EXTRA_DATABASES: $(shown "$_t") in $_name isn't a table name (table or schema.table in lower-case letters and digits and _)"
					return 1
				fi
				case $_t in *.*) ;; *) _t=public.$_t ;; esac
				_list=${_list:+$_list,}$_t
			done
			;;
		esac
		EXTRA_NAMES=${EXTRA_NAMES:+$EXTRA_NAMES }$_name
		eval "EXTRA_TABLES_$_name=\$_list"
	done
}

# extra_tables <name>: the tables whose rows that database's dump leaves out.
extra_tables() { eval "printf '%s' \"\${EXTRA_TABLES_$1:-}\""; }

# settings_ok: the setting is checked before any backup. A bad one refuses
# the backup, in the log and the status (so Administration and the alert
# show it), and the script stops.
settings_ok() {
	parse_extra && return 0
	say "refusing to back up: $EXTRA_ERROR" >&2
	read_status
	s_run_at=$(date +%s) s_ok=false s_running=0 s_error="the setting isn't valid: $EXTRA_ERROR"
	mkdir -p "${STATUS%/*}" 2>/dev/null
	write_status 2>/dev/null
	return 1
}

# write_extra <name> <run at> <ok> <error> [<night> <bytes> <dump seconds>]:
# that database's state, for the app it belongs to. Times are seconds since
# 1970. A good run sets the last good backup; a failed one keeps it.
write_extra() {
	_ef=$EXTRA_STATUS_DIR/$1.json
	mkdir -p "$EXTRA_STATUS_DIR" && chmod 755 "$EXTRA_STATUS_DIR"
	_esince=$(field since "$_ef")
	# Since its first state: its first run, or the service's start with it set.
	[ -n "$_esince" ] && [ "$_esince" != 0 ] || _esince=${2:-0}
	[ "$_esince" != 0 ] || _esince=$(date +%s)
	if [ "$3" = true ]; then
		_egat=$2 _egn=$5 _egb=$6 _egd=$7
	else
		_egat=$(field last_good_at "$_ef") _egn=$(field last_good_night "$_ef")
		_egb=$(field last_good_bytes "$_ef") _egd=$(field last_good_dump_seconds "$_ef")
	fi
	_enights=0
	for _f in "$DIR"/*."$1".dump; do
		[ ! -e "$_f" ] || _enights=$((_enights + 1))
	done
	cat >"$_ef.partial" <<JSON
{
  "database": "$1",
  "since": $_esince,
  "last_run_at": ${2:-0},
  "last_run_ok": $3,
  "last_good_at": ${_egat:-0},
  "last_good_night": "${_egn:-}",
  "last_good_bytes": ${_egb:-0},
  "last_good_dump_seconds": ${_egd:-0},
  "nights": $_enights,
  "error": "$4"
}
JSON
	chmod 644 "$_ef.partial" && mv -f "$_ef.partial" "$_ef"
}

# extras_started: a database newly in the setting has a state from now, so
# the app it belongs to sees "not yet" rather than nothing.
extras_started() {
	for _x in $EXTRA_NAMES; do
		[ -e "$EXTRA_STATUS_DIR/$_x.json" ] || write_extra "$_x" 0 false ""
	done
}

# write_status: for the API (R86a). Times are seconds since 1970.
write_status() {
	cat >"$STATUS.partial" <<JSON
{
  "since": ${s_since:-0},
  "last_night": "${s_night:-}",
  "last_run_at": ${s_run_at:-0},
  "last_run_ok": ${s_ok:-false},
  "running_since": ${s_running:-0},
  "last_good_at": ${s_good_at:-0},
  "last_good_night": "${s_good_night:-}",
  "last_good_bytes": ${s_good_bytes:-0},
  "last_good_dump_seconds": ${s_good_dump:-0},
  "last_good_check_seconds": ${s_good_check:-0},
  "nights": ${s_nights:-0},
  "error": "${s_error:-}"
}
JSON
	chmod 644 "$STATUS.partial" && mv -f "$STATUS.partial" "$STATUS"
}

read_status() {
	s_since=$(field since) s_night=$(field last_night) s_run_at=$(field last_run_at) s_ok=$(field last_run_ok)
	s_running=$(field running_since)
	s_good_at=$(field last_good_at) s_good_night=$(field last_good_night) s_good_bytes=$(field last_good_bytes)
	s_good_dump=$(field last_good_dump_seconds) s_good_check=$(field last_good_check_seconds)
	s_nights=$(field nights) s_error=$(field error)
}

# nights: the nights in the folder with their counts, oldest first.
nights() {
	for _nf in "$DIR"/*.counts.tsv; do
		[ -e "$_nf" ] || continue
		_nf=${_nf##*/}
		echo "${_nf%.counts.tsv}"
	done | sort
}

# dump_database <database> <name> <night> <counts file>: the dump and the table
# counts from one snapshot, so the check compares like with like while pages
# keep arriving. An open transaction holds the snapshot for both.
# [excluded]: an extra database's "schema.table,..." whose rows are left out
# (R86e): their counts are 0, as a restore of the dump has them.
dump_database() {
	_db=$1 _partial=$DIR/$3.$2.dump.partial _excluded=${5:-} _exargs=""
	# Checked names (parse_extra): no space, quote or wildcard in them.
	for _t in $(printf '%s' "$_excluded" | tr , ' '); do
		_exargs="$_exargs --exclude-table-data=$_t"
	done
	# A database that isn't there (an extra one, dropped) fails here: the
	# snapshot's holder below would have gone, and writing to it stop the script.
	psql -X -q -At -d "$_db" -c 'SELECT 1' >/dev/null 2>&1 || return 1
	_fifo=$(mktemp -u) _snap=$(mktemp)
	mkfifo "$_fifo"
	psql -X -q -At -d "$_db" <"$_fifo" >"$_snap" 2>/dev/null &
	_holder=$!
	exec 3>"$_fifo"
	echo "BEGIN ISOLATION LEVEL REPEATABLE READ READ ONLY; SELECT pg_export_snapshot();" >&3
	_i=0
	until [ -s "$_snap" ] || [ $_i -ge 300 ] || ! kill -0 "$_holder" 2>/dev/null; do
		sleep 0.1
		_i=$((_i + 1))
	done
	_id=$(head -n 1 "$_snap")
	_rc=1
	if [ -n "$_id" ]; then
		{
			echo "BEGIN ISOLATION LEVEL REPEATABLE READ READ ONLY; SET TRANSACTION SNAPSHOT '$_id';"
			echo "$COUNT_SQL"
			echo "COMMIT;"
		} | psql -X -q -At -F "$TAB" -v ON_ERROR_STOP=1 -d "$_db" 2>/dev/null |
			awk -F "$TAB" -v OFS="$TAB" -v ex=",$_excluded," 'index(ex, "," $1 ",") { $2 = 0 } { print }' | LC_ALL=C sort >"$4"
		# No tables at all counts as a failure: a backup with no users in it is no backup.
		# shellcheck disable=SC2086 # $_exargs: checked names, one argument each
		if [ -s "$4" ] && nice -n 10 pg_dump -d "$_db" -Fc --no-privileges --snapshot="$_id" $_exargs -f "$_partial" >/dev/null 2>&1; then
			_rc=0
		fi
	fi
	echo "COMMIT;" >&3
	exec 3>&-
	wait "$_holder" 2>/dev/null
	rm -f "$_fifo" "$_snap"
	[ $_rc -eq 0 ] && [ -s "$_partial" ] && place "$_partial"
}

# prune <night>: deletes nights older than the setting, by the date in their
# names (copying can change a file's time). Only called after a good check, so
# there's always a good night left, however long backups had been failing.
prune() {
	_cut=$(date -d "$1 -$((KEEP - 1)) days" +%Y%m%d)
	for _f in "$DIR"/[0-9][0-9][0-9][0-9]-[0-9][0-9]-[0-9][0-9].*; do
		[ -e "$_f" ] || continue
		_d=${_f##*/}
		_d=${_d%%.*}
		[ "$(echo "$_d" | tr -d -)" -ge "$_cut" ] || rm -f "$_f"
	done
}

# finished: the run is over, and a request it answered is done. The request
# goes first, so the API says "running" until the result is there.
finished() {
	s_running=0
	[ -z "$_requested" ] || rm -f "$REQUEST"
	write_status
}

# backup_failed <at> <why>: records a failed run. The last good backup stays.
# An extra database not yet recorded failed with it.
backup_failed() {
	s_run_at=$1 s_ok=false s_error=$2
	for _x in $EXTRA_NAMES; do
		[ -z "$(extra_var REC "$_x")" ] || continue
		_e=$(extra_var ERR "$_x")
		write_extra "$_x" "$1" false "${_e:-PagerArchive's backup failed: $2}"
	done
	finished
	say "failed: $2"
}

# extra_var <what> <name> / set_extra <what> <name> <value>: a run's notes on
# an extra database: DUMP (its seconds), ERR (why it failed), REC (recorded).
extra_var() { eval "printf '%s' \"\${EXTRA_$1_$2:-}\""; }
set_extra() { eval "EXTRA_$1_$2=\$3"; }

# extras_checked <at> <night>: each extra database's result, once the check
# has finished, in its state file and the log.
extras_checked() {
	for _x in $EXTRA_NAMES; do
		_e=$(extra_var ERR "$_x")
		if [ -z "$_e" ]; then
			_r=$(cat "$DIR/$2.$_x.check" 2>/dev/null || echo "failed: wasn't checked")
			if [ "$_r" = ok ]; then
				_b=$(wc -c <"$DIR/$2.$_x.dump")
				write_extra "$_x" "$1" true "" "$2" "$_b" "$(extra_var DUMP "$_x")"
				say "$2 $_x good: its dump took $(extra_var DUMP "$_x") s, $_b bytes"
				set_extra REC "$_x" 1
				continue
			fi
			_e="the check ${_r#failed: }"
		fi
		write_extra "$_x" "$1" false "$_e"
		say "$2 $_x failed: $_e"
		set_extra REC "$_x" 1
	done
}

remove_partials() {
	rm -f "$DIR"/*.dump.partial "$DIR"/*.counts.tsv.partial
}

# run_backup <night> <at> [requested]: one night: dump, rename, wait for the
# check, prune. "requested" is Back up now, answering the request file.
run_backup() {
	_night=$1 _at=$2 _requested=${3:-} _start=$(date +%s)
	for _x in $EXTRA_NAMES; do
		set_extra DUMP "$_x" ""
		set_extra ERR "$_x" ""
		set_extra REC "$_x" ""
	done
	read_status
	# What the schedule goes by. A test's clock (now --night) leaves it alone.
	# Back up now moves it only once it's good (below): then that night's
	# backup isn't replaced later the same day, and after a failure the
	# nightly one still runs.
	[ "$_night" != "$(date +%F)" ] || [ -n "$_requested" ] || s_night=$_night
	s_running=$_start
	write_status
	remove_partials
	chown "$OWNER" "$DIR" 2>/dev/null
	if ! pg_isready -q -t 10; then
		backup_failed "$_at" "the database couldn't be reached"
		return 1
	fi
	_counts=$(mktemp)
	for _pair in $DATABASES; do
		if ! dump_database "${_pair%%:*}" "${_pair##*:}" "$_night" "$_counts"; then
			remove_partials
			rm -f "$_counts"
			backup_failed "$_at" "the dump of ${_pair##*:} failed"
			return 1
		fi
		sed "s/^/${_pair##*:}$TAB/" "$_counts" >>"$DIR/$_night.counts.tsv.partial"
	done
	# Extra databases (R86e): one that fails is left out of the night, and
	# PagerArchive's backup goes on.
	for _x in $EXTRA_NAMES; do
		_xs=$(date +%s)
		if dump_database "$_x" "$_x" "$_night" "$_counts" "$(extra_tables "$_x")"; then
			sed "s/^/$_x$TAB/" "$_counts" >>"$DIR/$_night.counts.tsv.partial"
			set_extra DUMP "$_x" $(($(date +%s) - _xs))
		else
			rm -f "$DIR/$_night.$_x.dump.partial" "$DIR/$_night.$_x.dump"
			set_extra ERR "$_x" "the dump failed: is the database there and does it have tables?"
		fi
	done
	rm -f "$_counts"
	place "$DIR/$_night.counts.tsv.partial"
	# A night done again replaces the old one's files, its check's result first.
	rm -f "$DIR/$_night.check" "$DIR/$_night.counts.tsv"
	for _x in $EXTRA_NAMES; do
		rm -f "$DIR/$_night.$_x.check"
		[ -n "$(extra_var ERR "$_x")" ] || mv -f "$DIR/$_night.$_x.dump.partial" "$DIR/$_night.$_x.dump"
	done
	for _pair in $DATABASES; do
		mv -f "$DIR/$_night.${_pair##*:}.dump.partial" "$DIR/$_night.${_pair##*:}.dump"
	done
	mv -f "$DIR/$_night.counts.tsv.partial" "$DIR/$_night.counts.tsv"
	_dumped=$(date +%s)

	# The check's own container does it, with no network (R86a).
	_waited=0
	until [ -e "$DIR/$_night.check" ]; do
		if [ "$_waited" -ge "$CHECK_TIMEOUT" ]; then
			backup_failed "$_at" "the check didn't finish: is backup-check running?"
			return 1
		fi
		sleep 2
		_waited=$((_waited + 2))
	done
	_result=$(cat "$DIR/$_night.check")
	if [ "$_result" != ok ]; then
		extras_checked "$_at" "$_night"
		backup_failed "$_at" "the check ${_result#failed: }"
		return 1
	fi

	prune "$_night"
	extras_checked "$_at" "$_night"
	# R86d: the three numbers the dashboard shows for each night.
	s_run_at=$_at s_ok=true s_error="" s_good_at=$_at s_good_night=$_night
	s_good_dump=$((_dumped - _start)) s_good_check=$(($(date +%s) - _dumped))
	s_good_bytes=$(cat "$DIR/$_night.pagerarchive.dump" "$DIR/$_night.authentik.dump" | wc -c)
	s_nights=$(nights | wc -l)
	[ "$_night" != "$(date +%F)" ] || s_night=$_night
	finished
	say "$_night good: dumps took $s_good_dump s, the check $s_good_check s, $s_good_bytes bytes, $s_nights nights kept"
}

# locked <command...>: one backup at a time. The lock goes with the process.
locked() {
	(
		flock -n 9 || {
			say "another backup is running"
			exit 1
		}
		"$@"
	) 9>"$LOCK"
}

# requested: whether an admin has asked for a backup (Back up now). A link
# counts too, so whatever is there is answered and deleted, never followed.
requested() {
	[ -e "$REQUEST" ] || [ -L "$REQUEST" ]
}

serve() {
	_hhmm=$(echo "$AT" | tr -d :)
	# The API may create a file here and look it up by name, but not list the
	# folder or delete what isn't its own. Only this script deletes requests.
	mkdir -p "$REQUESTS" && chmod 1733 "$REQUESTS"
	read_status
	if [ -z "$s_since" ] || [ "$s_since" = 0 ]; then
		# A new install has nothing to catch up on. Its first backup is at the
		# next BACKUP_AT, and it's overdue 36 hours from here if that never comes.
		s_since=$(date +%s)
		[ "$(date +%H%M)" -lt "$_hhmm" ] || s_night=$(date +%F)
		write_status
	elif [ "${s_running:-0}" != 0 ]; then
		# A run stopped part way, with its container: it isn't running now. A
		# request it was answering is still there, so that runs again.
		s_running=0
		write_status
	fi
	say "nightly at $AT, keeping $KEEP days"
	while :; do
		# A minute's pause first: a start that's catching up on a night missed
		# while it was down leaves an upgrade's migrations to finish.
		sleep 60
		if requested; then
			say "backing up now, as an admin asked"
			locked run_backup "$(date +%F)" "$(date +%s)" requested || true
			continue
		fi
		[ "$(date +%H%M)" -ge "$_hhmm" ] || continue
		[ "$(field last_night)" != "$(date +%F)" ] || continue
		locked run_backup "$(date +%F)" "$(date +%s)" || true
	done
}

# throwaway_start: a new PostgreSQL in this container, listening on nothing but
# its socket, with its log thrown away.
throwaway_stop() {
	gosu postgres pg_ctl -D "$THROWAWAY" -m immediate -w stop >/dev/null 2>&1
	rm -rf "$THROWAWAY"
}
throwaway_start() {
	[ ! -d "$THROWAWAY" ] || throwaway_stop
	mkdir -p "$THROWAWAY" "$SOCKET"
	chown postgres "$THROWAWAY" "$SOCKET"
	gosu postgres initdb -D "$THROWAWAY" -U postgres --auth=trust >/dev/null 2>&1 || return 1
	gosu postgres pg_ctl -D "$THROWAWAY" -w -t 300 -l /dev/null -o "-c listen_addresses='' \
		-c unix_socket_directories=$SOCKET -c shared_buffers=64MB -c maintenance_work_mem=128MB \
		-c max_parallel_maintenance_workers=0 -c fsync=off -c full_page_writes=off -c synchronous_commit=off \
		-c wal_level=minimal -c max_wal_senders=0 -c autovacuum=off -c log_statement=none" start >/dev/null 2>&1
}

# differs <name> <counts of the restored database, on stdin>: the first table
# that differs from the night's counts, or nothing. Table names and numbers only.
differs() {
	_want=$(mktemp) _got=$(mktemp)
	sed -n "s/^$1$TAB//p" "$DIR/$2.counts.tsv" | LC_ALL=C sort >"$_want"
	LC_ALL=C sort >"$_got"
	if [ ! -s "$_want" ]; then
		echo "no counts"
	elif ! cmp -s "$_want" "$_got"; then
		diff "$_want" "$_got" | sed -n 's/^[<>] //p' | head -n 1 | tr '\t' ' ' | tr -cd 'a-zA-Z0-9_. '
	fi
	rm -f "$_want" "$_got"
}

# check_one <database> <name> <night>: restores one dump into the throwaway
# database and compares tables and row counts. Prints "ok", or "failed: why".
check_one() {
	if [ "$1" != postgres ] && ! gosu postgres createdb -h "$SOCKET" -U postgres "$1" >/dev/null 2>&1; then
		echo "failed: couldn't create a database to restore into"
		return
	fi
	# Opened here, as root: the folder's files aren't the postgres user's.
	if ! gosu postgres pg_restore -h "$SOCKET" -U postgres -d "$1" --exit-on-error --no-owner --no-privileges \
		<"$DIR/$3.$2.dump" >/dev/null 2>&1; then
		echo "failed: couldn't restore the $2 dump"
		return
	fi
	_diff=$(echo "$COUNT_SQL" | gosu postgres psql -h "$SOCKET" -U postgres -X -q -At -F "$TAB" -d "$1" 2>/dev/null | differs "$2" "$3")
	if [ -n "$_diff" ]; then
		echo "failed: found the $2 restore differs from the dump at $_diff"
		return
	fi
	echo ok
}

# extras_in <night>: the extra databases dumped that night (R86e), from their
# files' names. backup-check has no settings: the folder says what to check.
extras_in() {
	for _f in "$DIR/$1".*.dump; do
		[ -e "$_f" ] || continue
		_x=${_f#"$DIR/$1".}
		_x=${_x%.dump}
		case $_x in pagerarchive | authentik) continue ;; esac
		# Only a name the setting could have given reaches a command.
		printf '%s' "$_x" | grep -Eqx "$IDENT" && echo "$_x"
	done
}

# put <file> <text>: a result, under its final name only when complete.
put() {
	printf '%s\n' "$2" >"$1.partial"
	place "$1.partial"
	mv -f "$1.partial" "$1"
}

# check_night <night>: each extra database's dump checked on its own, its
# result in NAME.check, then PagerArchive's two. Prints PagerArchive's
# result: "ok", or "failed: why".
check_night() {
	if ! throwaway_start; then
		for _x in $(extras_in "$1"); do
			put "$DIR/$1.$_x.check" "failed: couldn't start its throwaway database"
		done
		echo "failed: couldn't start its throwaway database"
		return
	fi
	for _x in $(extras_in "$1"); do
		_xr=$(check_one "$_x" "$_x" "$1")
		put "$DIR/$1.$_x.check" "$_xr"
		# To stderr: what's printed is PagerArchive's result.
		say "$1 $_x checked: $_xr" >&2
	done
	for _pair in $DATABASES; do
		_r=$(check_one "${_pair%%:*}" "${_pair##*:}" "$1")
		if [ "$_r" != ok ]; then
			throwaway_stop
			echo "$_r"
			return
		fi
	done
	throwaway_stop
	echo ok
}

check_serve() {
	say "the check is waiting for new dumps"
	while :; do
		for _n in $(nights); do
			[ ! -e "$DIR/$_n.check" ] || continue
			_start=$(date +%s)
			_r=$(check_night "$_n")
			put "$DIR/$_n.check" "$_r"
			say "$_n checked in $(($(date +%s) - _start)) s: $_r"
		done
		sleep 10
	done
}

# restore <night> [--unchecked]: into the install's empty databases, then the
# restored counts compared with the night's. Refuses anything else, so it can't
# overwrite a running archive.
restore() {
	_night=${1:-}
	if ! printf '%s' "$_night" | grep -Eq '^[0-9]{4}-[0-9]{2}-[0-9]{2}$'; then
		echo "usage: docker compose run --rm backup restore YYYY-MM-DD [--unchecked]" >&2
		echo "nights in the backup folder:" >&2
		nights >&2
		return 2
	fi
	for _pair in $DATABASES; do
		if [ ! -s "$DIR/$_night.${_pair##*:}.dump" ] || [ ! -s "$DIR/$_night.counts.tsv" ]; then
			echo "$_night isn't complete: the backup folder has no ${_pair##*:} dump or counts for it" >&2
			return 1
		fi
	done
	_result=$(cat "$DIR/$_night.check" 2>/dev/null || echo "no result")
	if [ "$_result" != ok ] && [ "${2:-}" != --unchecked ]; then
		echo "$_night didn't pass its check ($_result). Choose another night, or add --unchecked to restore it anyway" >&2
		return 1
	fi
	_i=0
	until pg_isready -q -t 2; do
		_i=$((_i + 1))
		if [ $_i -ge 60 ]; then
			echo "the database didn't answer" >&2
			return 1
		fi
		sleep 2
	done
	_tables="SELECT count(*) FROM pg_class c JOIN pg_namespace n ON n.oid = c.relnamespace
		WHERE c.relkind = 'r' AND n.nspname = 'public' AND c.relname <> 'spatial_ref_sys'"
	for _pair in $DATABASES; do
		if [ "$(psql -X -At -d "${_pair%%:*}" -c "$_tables" 2>/dev/null)" != 0 ]; then
			echo "this install isn't empty: ${_pair##*:} has tables, so nothing was changed." >&2
			echo "To replace it: docker compose down -v (this deletes the database, not the backup folder), then run this again before up -d." >&2
			return 1
		fi
	done
	say "restoring $_night"
	if ! psql -X -q -v ON_ERROR_STOP=1 -d template1 >/dev/null 2>&1 <<'SQL'
DROP DATABASE postgres;
CREATE DATABASE postgres TEMPLATE template0;
DROP DATABASE authentik;
CREATE DATABASE authentik OWNER authentik TEMPLATE template0;
SQL
	then
		echo "couldn't empty the databases: is something connected to them? docker compose stop api authentik-server authentik-worker, and run this again" >&2
		return 1
	fi
	# A dump from an install with the API's own role (R84i) names it as its
	# tables' owner. Roles aren't in the dumps: a new install whose .env has
	# no PAGERARCHIVE_DB_PASSWORD hasn't made it, so it's made here, unable to
	# sign in until api-role.sh gives it a password.
	if pg_restore -l "$DIR/$_night.pagerarchive.dump" 2>/dev/null | awk '{print $NF}' | grep -qx pagerarchive &&
		! psql -X -q -At -d template1 -c "SELECT 1 FROM pg_roles WHERE rolname = 'pagerarchive'" 2>/dev/null | grep -q 1; then
		if ! psql -X -q -v ON_ERROR_STOP=1 -d template1 -c 'CREATE ROLE pagerarchive' >/dev/null 2>&1; then
			echo "couldn't make the role that owns the dump's tables" >&2
			return 1
		fi
	fi
	for _pair in $DATABASES; do
		_db=${_pair%%:*} _name=${_pair##*:}
		if ! pg_restore -d "$_db" --exit-on-error --single-transaction --no-privileges <"$DIR/$_night.$_name.dump" >/dev/null 2>&1; then
			echo "the $_name dump didn't restore" >&2
			return 1
		fi
		_diff=$(echo "$COUNT_SQL" | psql -X -q -At -F "$TAB" -d "$_db" 2>/dev/null | differs "$_name" "$_night")
		if [ -n "$_diff" ]; then
			echo "the $_name restore differs from the night's counts at $_diff" >&2
			return 1
		fi
		say "$_name restored, with the night's tables and row counts"
	done
	# The new databases have none of the rights the roles had: the dumps hold
	# no grants. So the API's role gets its rights back, and the
	# locked-accounts metric's role its function, as api-role.sh gives them.
	if psql -X -q -At -d template1 -c "SELECT 1 FROM pg_roles WHERE rolname = 'pagerarchive'" 2>/dev/null | grep -q 1 &&
		! psql -X -q -v ON_ERROR_STOP=1 -d postgres -f "$SQL_DIR/api-role.sql" >/dev/null 2>&1; then
		echo "couldn't give the API's role, pagerarchive, its rights again: run deploy/postgres/api-role.sh after starting the stack" >&2
		return 1
	fi
	if psql -X -q -At -d template1 -c "SELECT 1 FROM pg_roles WHERE rolname = 'pagerarchive_metrics'" 2>/dev/null | grep -q 1 &&
		! psql -X -q -v ON_ERROR_STOP=1 -d authentik -f "$SQL_DIR/metrics-role.sql" >/dev/null 2>&1; then
		echo "couldn't give the locked-accounts metric's role its function again: run deploy/postgres/api-role.sh after starting the stack" >&2
		return 1
	fi
	say "$_night is restored. Start the stack: docker compose up -d"
}

# refuse_bad_settings: a bad BACKUP_EXTRA_DATABASES stops here (R86e), with
# the reason in the log and, under the lock, the status.
refuse_bad_settings() {
	parse_extra && return 0
	mkdir -p "${LOCK%/*}" 2>/dev/null
	locked settings_ok
	exit 2
}

case ${1:-} in
serve)
	refuse_bad_settings
	extras_started
	serve
	;;
check-serve) check_serve ;;
databases)
	# What's backed up, for you to check the setting before a night.
	if ! parse_extra; then
		echo "$EXTRA_ERROR" >&2
		exit 2
	fi
	echo "pagerarchive (PostgreSQL's postgres)"
	echo "authentik"
	for _x in $EXTRA_NAMES; do
		_t=$(extra_tables "$_x")
		echo "$_x${_t:+ (rows left out: $_t)}"
	done
	;;
now)
	# A backup at once, for you or a test. A test sets the clock (A80):
	# --night YYYY-MM-DD names the night, and --at SECONDS when it happened.
	shift
	_n=$(date +%F) _a=$(date +%s)
	while [ $# -gt 0 ]; do
		case $1 in
		--night) _n=$2 ;;
		--at) _a=$2 ;;
		*)
			echo "usage: pagerarchive-backup now" >&2
			exit 2
			;;
		esac
		shift 2
	done
	refuse_bad_settings
	locked run_backup "$_n" "$_a"
	;;
restore)
	shift
	restore "$@"
	;;
*)
	echo "usage: pagerarchive-backup serve | check-serve | now | databases | restore YYYY-MM-DD" >&2
	exit 2
	;;
esac
