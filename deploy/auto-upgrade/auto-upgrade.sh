#!/usr/bin/env bash
# Automatic upgrades (SPEC.md R84d-R84h, ADR 0030; docs/deploy.md, "Automatic
# upgrades"). Off unless switched on. Part of the release's archive, so it's in
# deploy/auto-upgrade/ next to compose.yaml, and runs on the host, as the user
# who runs `docker compose` for this install, from cron (or a systemd user timer):
#
#   deploy/auto-upgrade/auto-upgrade.sh enable [--systemd]   switch it on: hourly, from
#                                    cron (or a systemd user timer; --no-schedule: neither)
#   deploy/auto-upgrade/auto-upgrade.sh disable              switch it off
#   deploy/auto-upgrade/auto-upgrade.sh run                  one check, now
#   deploy/auto-upgrade/auto-upgrade.sh status               what it last did
#   deploy/auto-upgrade/auto-upgrade.sh verify [vX.Y.Z]      check a release's files
#                                                            and images, nothing more
#   deploy/auto-upgrade/auto-upgrade.sh retry X.Y.Z          try a release marked failed again
#   deploy/auto-upgrade/auto-upgrade.sh clear                after a run that stopped part way
#
# Each run: the newest full release (not a draft, not a pre-release) from
# GitHub's releases API. If it's newer than what's running, in the same major
# version, not marked failed and its notes don't start with "Upgrade steps":
#   1. verify: its files' and our images' attestations, with a pinned GitHub CLI
#      this script fetches and checks by its checksum; refused if that can't run
#   2. back up: the backup service's `now` (the same checked backup as the
#      nightly one), kept aside, plus copies of compose.yaml, .env,
#      compose.override.yaml, deploy/ and the monitoring stack's files
#   3. upgrade: the verified files installed, the client's tag in
#      compose.override.yaml moved with the minor version, pull, up -d, and the
#      example monitoring stack too when AUTO_UPGRADE_MONITORING_DIR says where
#   4. health check: containers running, the API answering and running the new
#      version, a page stored (when pages were arriving), your own URLs
#   5. on failure: the previous release's files and images back, its database
#      restored from step 2's backup if the new version changed it, and the
#      release marked failed so it isn't tried again
#
# What it did goes to auto-upgrade/status/status.json, which the API reads (for
# Administration and /metrics), and one JSON line per step to
# auto-upgrade/status/auto-upgrade.log, which the example monitoring stack's
# Alloy can read. Neither ever holds page text: only versions, times, counts
# and fixed text.
#
# Never two runs at once (a lock), and never while a backup is running: the
# backup service's `now` refuses then, and the run tries again next hour.

PA_GH_VERSION=2.103.3
PA_GH_SHA256=4c806978ffce1cec70853302a56d70ac6da4f19bae7adca4e1a8ab00633dd91d
PA_JQ_VERSION=1.8.2
PA_JQ_SHA256=b1c22172dd303f3be49e935aa56aa48a8b7a46e0bc838b4997d3bb451495870f
PA_REPO=RIKIKU/PagerArchive

set -u
umask 022

# ── Settings ───────────────────────────────────────────────────────────────
# setting <NAME> <default>: the environment's value, else .env's, else the default.
setting() {
	local v
	if [ -n "${!1+x}" ]; then
		printf '%s' "${!1}"
		return
	fi
	v=$(sed -n "s/^$1=//p" "$PA_DIR/.env" 2>/dev/null | tail -n 1)
	v=${v%\"}
	v=${v#\"}
	printf '%s' "${v:-$2}"
}

load_settings() {
	HOME_AU=$(setting AUTO_UPGRADE_HOME "$PA_DIR/auto-upgrade")
	STATUS_DIR=$HOME_AU/status
	STATUS=$STATUS_DIR/status.json
	LOG=$STATUS_DIR/auto-upgrade.log
	API=$(setting AUTO_UPGRADE_API "https://api.github.com/repos/$PA_REPO")
	TOKEN=$(setting AUTO_UPGRADE_GITHUB_TOKEN "")
	PREFIX=$(setting AUTO_UPGRADE_IMAGE_PREFIX "ghcr.io/rikiku/")
	WINDOW=$(setting AUTO_UPGRADE_PAGE_WINDOW 30)
	START_TIMEOUT=$(setting AUTO_UPGRADE_START_TIMEOUT 600)
	CHECK_URLS=$(setting AUTO_UPGRADE_CHECK_URLS "")
	MON_DIR=$(setting AUTO_UPGRADE_MONITORING_DIR "")
	KEEP=$(setting AUTO_UPGRADE_KEEP 3)
	GH_OVERRIDE=$(setting AUTO_UPGRADE_GH "")
	case $MON_DIR in "" | /*) ;; *) MON_DIR=$PA_DIR/$MON_DIR ;; esac
	case $HOME_AU in /*) ;; *)
		HOME_AU=$PA_DIR/$HOME_AU
		STATUS_DIR=$HOME_AU/status STATUS=$HOME_AU/status/status.json LOG=$HOME_AU/status/auto-upgrade.log
		;;
	esac
	RELEASES=$HOME_AU/releases.json
	for n in WINDOW START_TIMEOUT KEEP; do
		case ${!n} in '' | *[!0-9]*) echo "auto-upgrade: a number setting ($n) must be a whole number" >&2 && exit 2 ;; esac
	done
}

# ── Output: the log for Loki, the status for the API ──────────────────────
now() { date -u +%s; }

json_str() { printf '%s' "$1" | sed 's/\\/\\\\/g; s/"/\\"/g' | tr -d '\000-\037'; }

# event <level> <step> <result> [reason]: one JSON line per step (R84g). The
# reason is fixed text, versions and counts: never page text.
event() {
	local line
	line=$(printf '{"time":"%s","level":"%s","step":"%s","result":"%s","from":"%s","to":"%s","reason":"%s","seconds":%s}' \
		"$(date -u +%Y-%m-%dT%H:%M:%SZ)" "$1" "$2" "$3" "$(json_str "${FROM:-}")" "$(json_str "${TO:-}")" \
		"$(json_str "${4:-}")" "$(($(now) - ${STEP_START:-$(now)}))")
	mkdir -p "$STATUS_DIR"
	printf '%s\n' "$line" >>"$LOG"
	echo "auto-upgrade: $2 $3${4:+: $4}"
	STEP_START=$(now)
}

# rotate_log: past 1 MB the log moves aside, once.
rotate_log() {
	[ -f "$LOG" ] || return 0
	[ "$(wc -c <"$LOG")" -lt 1048576 ] || mv -f "$LOG" "$LOG.1"
}

# status <jq filter> [jq args...]: changes the status file atomically.
status() {
	local f=$1
	shift
	mkdir -p "$STATUS_DIR"
	chmod 755 "$HOME_AU" "$STATUS_DIR" 2>/dev/null
	[ -s "$STATUS" ] || echo '{"enabled": false, "failures": 0, "rollbacks": 0, "failed_versions": [], "history": []}' >"$STATUS"
	if "$JQ" "$@" "$f" "$STATUS" >"$STATUS.partial"; then
		chmod 644 "$STATUS.partial" && mv -f "$STATUS.partial" "$STATUS"
	else
		rm -f "$STATUS.partial"
		echo "auto-upgrade: couldn't write the status" >&2
	fi
}

status_get() { "$JQ" -r "$1 // empty" "$STATUS" 2>/dev/null; }

# checked <result> <note>: the end of a run that changed nothing.
checked() {
	status '.state = "idle" | .running_since = 0 | .last_check_at = ($t|tonumber) | .last_check_result = $r | .last_check_note = $n
		| .running_version = $v | .latest_version = $l' \
		--arg t "$(now)" --arg r "$1" --arg n "$2" --arg v "${FROM:-}" --arg l "${TO:-}"
}

# ── Tools ──────────────────────────────────────────────────────────────────
dc() { (cd "$PA_DIR" && docker compose "$@"); }
mc() { (cd "$MON_DIR" && docker compose "$@"); }

sha() { sha256sum "$1" | cut -d' ' -f1; }

curl_api() {
	if [ -n "$TOKEN" ]; then
		curl -fsSL --max-time 60 -H "Accept: application/vnd.github+json" -H "Authorization: Bearer $TOKEN" "$API$1"
	else
		curl -fsSL --max-time 60 -H "Accept: application/vnd.github+json" "$API$1"
	fi
}

# fetch_pinned <url> <file> <sha256>: downloaded once, checked on every run.
fetch_pinned() {
	if [ -f "$2" ] && [ "$(sha "$2")" = "$3" ]; then
		return 0
	fi
	rm -f "$2"
	curl -fsSL --max-time 300 -o "$2.partial" "$1" || return 1
	if [ "$(sha "$2.partial")" != "$3" ]; then
		rm -f "$2.partial"
		return 1
	fi
	mv -f "$2.partial" "$2"
}

# tools: jq, and the GitHub CLI for `gh attestation verify`, each a pinned
# release checked against its published checksum (ADR 0030). The CLI needs no
# sign-in: the attestations come from GitHub's API, which needs no token for a
# public repository, and are verified offline against Sigstore's public roots.
tools() {
	local bin=$HOME_AU/bin
	mkdir -p "$bin"
	JQ=$bin/jq-$PA_JQ_VERSION
	fetch_pinned "https://github.com/jqlang/jq/releases/download/jq-$PA_JQ_VERSION/jq-linux-amd64" "$JQ" "$PA_JQ_SHA256" || return 1
	chmod 755 "$JQ"
	if [ -n "$GH_OVERRIDE" ]; then
		GH=$GH_OVERRIDE
		[ -x "$GH" ] || return 1
		return 0
	fi
	local tgz=$bin/gh_${PA_GH_VERSION}_linux_amd64.tar.gz
	fetch_pinned "https://github.com/cli/cli/releases/download/v$PA_GH_VERSION/gh_${PA_GH_VERSION}_linux_amd64.tar.gz" "$tgz" "$PA_GH_SHA256" || return 1
	# Unpacked from the checked archive on every run, so a changed binary is never used.
	rm -rf "$bin/gh"
	mkdir -p "$bin/gh"
	tar -xzf "$tgz" -C "$bin/gh" --strip-components=2 "gh_${PA_GH_VERSION}_linux_amd64/bin/gh" || return 1
	GH=$bin/gh/gh
	export GH_CONFIG_DIR=$bin/gh-config GH_NO_UPDATE_NOTIFIER=1 GH_PROMPT_DISABLED=1
	unset GH_TOKEN GITHUB_TOKEN
}

# ── Versions ───────────────────────────────────────────────────────────────
is_release() { printf '%s' "$1" | grep -Eq '^[0-9]+\.[0-9]+\.[0-9]+$'; }
newer() { [ "$1" != "$2" ] && [ "$(printf '%s\n%s\n' "$1" "$2" | sort -V | tail -n 1)" = "$1" ]; }
minor_of() { printf '%s' "${1%.*}"; }

running_version() {
	dc exec -T api /usr/local/bin/pagerarchive version 2>/dev/null | sed -n 's/^pagerarchive //p' | tr -d '\r'
}

# latest_release: the newest full release's tag, from the list, which it keeps
# in a file (it runs in a subshell) for release_field and asset_url.
latest_release() {
	curl_api "/releases?per_page=50" >"$RELEASES" || return 1
	"$JQ" -r '[.[] | select(.draft == false and .prerelease == false)
		| select(.tag_name | test("^v[0-9]+\\.[0-9]+\\.[0-9]+$"))]
		| sort_by(.tag_name | ltrimstr("v") | split(".") | map(tonumber)) | last // empty
		| .tag_name' "$RELEASES"
}

release_field() { "$JQ" -r --arg t "$1" ".[] | select(.tag_name == \$t) | $2" "$RELEASES"; }
asset_url() { release_field "$1" ".assets[] | select(.name == \"$2\") | .browser_download_url"; }

# ── Verify ─────────────────────────────────────────────────────────────────
# attested <subject> <digest>: the subject's attestations from GitHub's API,
# verified by the pinned CLI: built by this repository's workflows on main.
attested() {
	local bundles
	# gh wants the bundles file named .jsonl.
	bundles=$(mktemp --suffix=.jsonl)
	if ! curl_api "/attestations/$2" 2>/dev/null | "$JQ" -c '.attestations[].bundle' >"$bundles" 2>/dev/null || [ ! -s "$bundles" ]; then
		rm -f "$bundles"
		REASON="no attestation for $3"
		return 1
	fi
	if ! "$GH" attestation verify "$1" --repo "$PA_REPO" --source-ref refs/heads/main --bundle "$bundles" >/dev/null 2>&1; then
		rm -f "$bundles"
		REASON="the attestation of $3 doesn't verify"
		return 1
	fi
	rm -f "$bundles"
}

# verify_image <image:tag>: pulled, and its digest attested. Sets IMAGE_ID.
verify_image() {
	local digest id
	docker pull -q "$1" >/dev/null 2>&1 || {
		REASON="couldn't pull ${1##*/}"
		return 1
	}
	digest=$(docker image inspect -f '{{range .RepoDigests}}{{println .}}{{end}}' "$1" | grep "^${1%:*}@" | head -n 1)
	digest=${digest#*@}
	[ -n "$digest" ] || {
		REASON="no digest for ${1##*/}"
		return 1
	}
	attested "oci://${1%:*}@$digest" "$digest" "${1##*/}" || return 1
	IMAGE_ID=$(docker image inspect -f '{{.Id}}' "$1")
}

# verify <tag>: the release's files downloaded into auto-upgrade/releases/<version>
# and attested, then our images (the server's, and the client's when the override
# names it). Sets FILES_SHA (what's installed is checked against it) and VERIFIED.
verify() {
	local tag=$1 v=${1#v} dir url f d id
	dir=$HOME_AU/releases/$v
	rm -rf "$dir"
	mkdir -p "$dir"
	FILES="compose.yaml pagerarchive-config.tar.gz"
	[ -z "$MON_DIR" ] || FILES="$FILES monitoring-compose.yaml"
	FILES_SHA=""
	for f in $FILES; do
		url=$(asset_url "$tag" "$f")
		if [ -z "$url" ]; then
			REASON="the release has no $f yet"
			NOT_READY=1
			return 1
		fi
		curl -fsSL --max-time 300 -o "$dir/$f" "$url" || {
			REASON="couldn't download $f"
			NOT_READY=1
			return 1
		}
		d=$(sha "$dir/$f")
		attested "$dir/$f" "sha256:$d" "$f" || return 1
		FILES_SHA="$FILES_SHA $f=$d"
	done
	# The compose.yaml must name our image by the release's minor version.
	grep -q "image: ${PREFIX}pagerarchive:$(minor_of "$v")\$" "$dir/compose.yaml" || {
		REASON="compose.yaml doesn't name pagerarchive:$(minor_of "$v")"
		return 1
	}
	VERIFIED=""
	IMAGES="${PREFIX}pagerarchive:$v"
	if [ -f "$PA_DIR/compose.override.yaml" ] && grep -Eq "${PREFIX}pagerarchive-client:[0-9]+\.[0-9]+([^.0-9]|\$)" "$PA_DIR/compose.override.yaml"; then
		IMAGES="$IMAGES ${PREFIX}pagerarchive-client:$v"
	fi
	for f in $IMAGES; do
		verify_image "$f" || return 1
		id=$IMAGE_ID
		VERIFIED="$VERIFIED $id"
	done
}

# verified_file <file> <name>: the file installed is the one verified.
verified_file() {
	case " $FILES_SHA " in *" $2=$(sha "$1") "*) return 0 ;; esac
	return 1
}

# ── The database ───────────────────────────────────────────────────────────
dbq() { dc exec -T db psql -X -q -At -U postgres -d "$1" -c "$2" 2>/dev/null | tr -d '\r'; }

# fingerprint: what an upgrade can change in the databases: PagerArchive's
# migrations, Authentik's, and the PostGIS extensions' versions (R84c).
fingerprint() {
	local a b c e
	a=$(dbq postgres "SELECT coalesce(max(version_id), 0) FROM goose_db_version WHERE is_applied") || a=""
	b=$(dbq authentik "SELECT CASE WHEN to_regclass('public.django_migrations') IS NULL THEN 0 ELSE (SELECT count(*) FROM django_migrations) END") || b=""
	c=$(dbq postgres "SELECT coalesce(string_agg(extname || '=' || extversion, ',' ORDER BY extname), '') FROM pg_extension")
	e=$(dbq authentik "SELECT coalesce(string_agg(extname || '=' || extversion, ',' ORDER BY extname), '') FROM pg_extension")
	if [ -z "$a" ] || [ -z "$b" ]; then
		echo unreadable
	else
		echo "$a|$b|$c|$e"
	fi
}

# ── Health ─────────────────────────────────────────────────────────────────
# api_url: the API's port published on the host, else its container's address.
api_url() {
	local p ip
	p=$(dc port api 8080 2>/dev/null | head -n 1)
	case $p in
	0.0.0.0:* | "[::]:"*) echo "http://127.0.0.1:${p##*:}" && return ;;
	?*) echo "http://$p" && return ;;
	esac
	ip=$(docker inspect -f '{{range .NetworkSettings.Networks}}{{.IPAddress}} {{end}}' "$(dc ps -q api)" 2>/dev/null | awk '{print $1}')
	[ -n "$ip" ] && echo "http://$ip:8080"
}

# all_running: every service of the stack running, none restarting or exited.
all_running() {
	local out bad
	out=$(dc ps -a --format '{{.Service}} {{.State}}' 2>/dev/null) || return 1
	[ -n "$out" ] || return 1
	bad=$(printf '%s\n' "$out" | awk '$2 != "running" { print $1 }' | tr '\n' ' ')
	[ -z "$bad" ] || {
		REASON="not running: $bad"
		return 1
	}
}

# api_up <version>: the API answers /healthz (it listens only after its
# migrations have run, and exits if one fails) and runs <version>.
api_up() {
	local url got end=$(($(now) + START_TIMEOUT))
	while :; do
		url=$(api_url)
		got=$(running_version)
		if [ -n "$url" ] && curl -fsS --max-time 10 "$url/healthz" >/dev/null 2>&1 && [ "$got" = "$1" ]; then
			return 0
		fi
		if [ "$(now)" -ge "$end" ]; then
			REASON="the API didn't answer as $1 within $START_TIMEOUT s (it runs ${got:-nothing})"
			return 1
		fi
		sleep 5
	done
}

urls_up() {
	local u end
	for u in $CHECK_URLS; do
		end=$(($(now) + START_TIMEOUT))
		until curl -fsS -o /dev/null --max-time 20 "$u" 2>/dev/null; do
			if [ "$(now)" -ge "$end" ]; then
				REASON="$u didn't answer"
				return 1
			fi
			sleep 10
		done
	done
}

# health <version>: R84f's checks after an upgrade, the page rule included.
health() {
	local v=$1 n end
	api_up "$v" || return 1
	# A container that starts and then fails shows within a minute.
	sleep 20
	all_running || return 1
	[ "$(dc exec -T api /usr/local/bin/pagerarchive version 2>/dev/null | tr -d '\r')" = "pagerarchive $v" ] || {
		REASON="the API isn't running $v"
		return 1
	}
	local id
	id=$(docker inspect -f '{{.Image}}' "$(dc ps -q api)" 2>/dev/null)
	case " $VERIFIED " in *" $id "*) ;; *)
		REASON="the API runs an image that wasn't verified"
		return 1
		;;
	esac
	urls_up || return 1
	# The page rule (R84f): if pages were arriving before the upgrade (on average
	# at least 5 in each window over the 4 windows before it, so a whole window
	# with none would be very unusual), one must be stored within a window of
	# the restart. A quiet network is never a reason to roll back.
	if [ "$PAGES_BEFORE" -ge 20 ]; then
		end=$((RESTARTED + WINDOW * 60))
		while :; do
			n=$(dbq postgres "SELECT count(*) FROM messages WHERE created_at > to_timestamp($RESTARTED)")
			[ "${n:-0}" -gt 0 ] && break
			if [ "$(now)" -ge "$end" ]; then
				REASON="no page stored within $WINDOW minutes of the restart, with $PAGES_BEFORE in the $((WINDOW * 4)) minutes before"
				return 1
			fi
			sleep 30
		done
		PAGE_NOTE="a page was stored after the restart"
	else
		PAGE_NOTE="the page check was skipped: $PAGES_BEFORE pages in the $((WINDOW * 4)) minutes before, fewer than 20"
	fi
	api_up "$v" && all_running
}

# ── Backups ────────────────────────────────────────────────────────────────
# backup_busy: the backup service is running one, or one is asked for.
backup_busy() {
	local r
	r=$(dc exec -T backup sh -c 'cat /status/status.json 2>/dev/null' | "$JQ" -r '.running_since // 0' 2>/dev/null)
	[ "${r:-0}" != 0 ] && return 0
	dc exec -T backup sh -c 'test -e /requests/request.json' 2>/dev/null
}

# back_up: the backup service's `now`, kept aside in the backup folder's
# pre-upgrade/<set>/ by hard links (no extra space while the night's files
# stay), so a backup made later that day can't replace it. Then the files.
back_up() {
	local out
	out=$(dc exec -T backup pagerarchive-backup now 2>&1)
	case $out in *"another backup is running"*)
		BUSY=1
		REASON="a backup is running"
		return 1
		;;
	esac
	NIGHT=$(printf '%s\n' "$out" | sed -n 's/^backup: \([0-9-]*\) good:.*/\1/p' | tail -n 1)
	if [ -z "$NIGHT" ]; then
		REASON="the backup failed: $(printf '%s\n' "$out" | sed -n 's/^backup: failed: //p' | tail -n 1)"
		return 1
	fi
	dc exec -T backup sh -c "set -e; d=/backups/pre-upgrade/$SET; mkdir -p \$d
		for f in pagerarchive.dump authentik.dump counts.tsv check; do ln -f /backups/$NIGHT.\$f \$d/$NIGHT.\$f; done
		chown \${BACKUP_UID:-0}:\${BACKUP_GID:-0} /backups/pre-upgrade \$d; chmod 750 /backups/pre-upgrade \$d
		ls -1d /backups/pre-upgrade/*/ | sort | head -n -$KEEP | xargs -r rm -rf" || {
		REASON="couldn't keep the backup aside"
		return 1
	}
	FILES_DIR=$HOME_AU/files/$SET
	mkdir -p "$FILES_DIR"
	chmod 700 "$HOME_AU/files" "$FILES_DIR"
	cp -a "$PA_DIR/compose.yaml" "$PA_DIR/.env" "$PA_DIR/deploy" "$FILES_DIR/" || {
		REASON="couldn't copy the install's files"
		return 1
	}
	[ ! -f "$PA_DIR/compose.override.yaml" ] || cp -a "$PA_DIR/compose.override.yaml" "$FILES_DIR/"
	if [ -n "$MON_DIR" ]; then
		mkdir -p "$FILES_DIR/monitoring"
		for f in compose.yaml compose.override.yaml .env alloy grafana loki prometheus tempo; do
			[ ! -e "$MON_DIR/$f" ] || cp -a "$MON_DIR/$f" "$FILES_DIR/monitoring/"
		done
	fi
	ls -1d "$HOME_AU"/files/*/ | sort | head -n -"$KEEP" | xargs -r rm -rf
}

# images_of <compose function>: "image ID<TAB>image name" for each container,
# so a rollback can point the names back at the images that were running.
images_of() {
	local c
	for c in $("$1" ps -q 2>/dev/null); do
		docker inspect -f '{{.Image}}	{{.Config.Image}}' "$c"
	done | sort -u
}

# ── Upgrade ────────────────────────────────────────────────────────────────
install_files() {
	local dir=$HOME_AU/releases/$TO f
	for f in $FILES; do
		verified_file "$dir/$f" "$f" || {
			REASON="$f changed after it was verified"
			return 1
		}
	done
	cp "$dir/compose.yaml" "$PA_DIR/compose.yaml.partial" && mv -f "$PA_DIR/compose.yaml.partial" "$PA_DIR/compose.yaml" &&
		tar -xzf "$dir/pagerarchive-config.tar.gz" -C "$PA_DIR" || {
		REASON="couldn't install the release's files"
		return 1
	}
	# The client's major.minor tag follows the server's (an exact tag is left alone).
	if [ -f "$PA_DIR/compose.override.yaml" ]; then
		sed -i -E "s#(${PREFIX//./\\.}pagerarchive-client:)[0-9]+\.[0-9]+([^.0-9]|\$)#\1$(minor_of "$TO")\2#" "$PA_DIR/compose.override.yaml"
	fi
	if [ -n "$MON_DIR" ]; then
		cp "$dir/monitoring-compose.yaml" "$MON_DIR/compose.yaml" || return 1
		if [ "$(cd "$MON_DIR" && pwd)" != "$PA_DIR/deploy/monitoring" ]; then
			tar -xzf "$dir/pagerarchive-config.tar.gz" -C "$MON_DIR" --strip-components=2 deploy/monitoring || return 1
		fi
	fi
}

# our_images_verified: after the pull, the images Compose will run are the
# verified ones; one whose tag moved since (a patch release came out) is
# verified too, or refused.
our_images_verified() {
	local ref id
	for ref in $(cat "$PA_DIR/compose.yaml" "$PA_DIR/compose.override.yaml" 2>/dev/null |
		sed -n -E "s#^ *image: *(${PREFIX//./\\.}pagerarchive(-client)?:[0-9.]+) *\$#\1#p" | sort -u); do
		id=$(docker image inspect -f '{{.Id}}' "$ref" 2>/dev/null)
		case " $VERIFIED " in *" $id "*) continue ;; esac
		verify_image "$ref" || return 1
		id=$IMAGE_ID
		VERIFIED="$VERIFIED $id"
	done
}

upgrade() {
	install_files || return 1
	dc config -q >/dev/null 2>&1 || {
		REASON="the new compose files don't load"
		return 1
	}
	dc pull -q >/dev/null 2>&1 || {
		REASON="docker compose pull failed"
		return 1
	}
	our_images_verified || return 1
	dc up -d >/dev/null 2>&1 || {
		REASON="docker compose up -d failed"
		return 1
	}
	local i=0
	RESTARTED=""
	while [ -z "$RESTARTED" ] && [ $i -lt 30 ]; do
		RESTARTED=$(dbq postgres "SELECT extract(epoch FROM now())::bigint")
		[ -n "$RESTARTED" ] || sleep 2
		i=$((i + 1))
	done
	[ -n "$RESTARTED" ] || RESTARTED=$(now)
	if [ -n "$MON_DIR" ]; then
		{ mc config -q && mc pull -q && mc up -d; } >/dev/null 2>&1 || {
			REASON="the monitoring stack didn't upgrade"
			return 1
		}
	fi
}

# ── Rollback ───────────────────────────────────────────────────────────────
# rollback: the previous release's files and images, and its database from the
# pre-upgrade backup if the new version changed the database (R84g).
rollback() {
	local fp_after restore="" d vol=$DB_VOLUME
	status '.state = "rolling-back"'
	fp_after=$(fingerprint)
	[ "$fp_after" = "$FP_BEFORE" ] || restore=1
	event info rollback started "the database $([ -n "$restore" ] && echo changed: it will be restored || echo "is unchanged")"
	if [ -n "$restore" ]; then
		# The night's files back in the folder, from the copies kept aside. Ones
		# made since (Back up now) move into the set rather than being lost.
		d=/backups/pre-upgrade/$SET
		dc run --rm --no-deps -T --entrypoint sh backup -c "set -e
			for f in pagerarchive.dump authentik.dump counts.tsv check; do
				if [ -e /backups/$NIGHT.\$f ] && [ ! /backups/$NIGHT.\$f -ef $d/$NIGHT.\$f ]; then
					mkdir -p $d/after-upgrade; chown \${BACKUP_UID:-0}:\${BACKUP_GID:-0} $d/after-upgrade
					mv -f /backups/$NIGHT.\$f $d/after-upgrade/
				fi
				ln -f $d/$NIGHT.\$f /backups/$NIGHT.\$f
			done" >/dev/null 2>&1 || {
			ROLLBACK_FAILED="the pre-upgrade backup couldn't be put back in the backup folder"
			return 1
		}
		dc down >/dev/null 2>&1
	fi
	cp -a "$FILES_DIR/compose.yaml" "$FILES_DIR/.env" "$PA_DIR/" && cp -a "$FILES_DIR/deploy/." "$PA_DIR/deploy/" || {
		ROLLBACK_FAILED="the previous files couldn't be put back"
		return 1
	}
	if [ -f "$FILES_DIR/compose.override.yaml" ]; then
		cp -a "$FILES_DIR/compose.override.yaml" "$PA_DIR/"
	fi
	# The names point at the images that were running again, so nothing is pulled.
	printf '%s\n' "$IMAGES_BEFORE" | while IFS='	' read -r id ref; do
		[ -z "$id" ] || docker tag "$id" "$ref" 2>/dev/null
	done
	if [ -n "$restore" ]; then
		STEP_START=$(now)
		event info restore started "into a new database, the documented restore"
		docker volume rm "$vol" >/dev/null 2>&1 || {
			ROLLBACK_FAILED="the database's volume couldn't be removed for the restore"
			event error restore failed "$ROLLBACK_FAILED"
			return 1
		}
		dc up -d db >/dev/null 2>&1
		if ! dc run --rm -T backup restore "$NIGHT" >/dev/null 2>&1; then
			ROLLBACK_FAILED="the restore of the pre-upgrade backup ($NIGHT) failed: the stack is left stopped"
			event error restore failed "$ROLLBACK_FAILED"
			return 1
		fi
		RESTORED=1
		event info restore ok "the database is as it was before the upgrade"
	fi
	dc up -d --remove-orphans >/dev/null 2>&1
	if [ -n "$MON_DIR" ] && [ -d "$FILES_DIR/monitoring" ]; then
		cp -a "$FILES_DIR/monitoring/." "$MON_DIR/"
		printf '%s\n' "$MON_IMAGES_BEFORE" | while IFS='	' read -r id ref; do
			[ -z "$id" ] || docker tag "$id" "$ref" 2>/dev/null
		done
		mc up -d >/dev/null 2>&1
	fi
	VERIFIED=$(printf '%s\n' "$IMAGES_BEFORE" | cut -f1 | tr '\n' ' ')
	if ! api_up "$FROM" || ! all_running; then
		ROLLBACK_FAILED="after the rollback: $REASON"
		return 1
	fi
}

# ── A run ──────────────────────────────────────────────────────────────────
fail_upgrade() { # <step> <reason>
	local why=$2 rb=false rs=false outcome
	event error "$1" failed "$why"
	STEP_START=$(now)
	ROLLBACK_FAILED="" RESTORED=""
	if rollback; then
		rb=true
		[ -z "$RESTORED" ] || rs=true
		outcome=rolled-back
		event warn rollback ok "back on $FROM"
	else
		outcome=rollback-failed
		event error rollback failed "$ROLLBACK_FAILED"
	fi
	status '.state = "idle" | .running_since = 0 | .last_check_at = ($t|tonumber) | .last_check_result = $o | .last_check_note = $w
		| .failures += 1 | .rollbacks += (if $rb == "true" then 1 else 0 end)
		| .failed_versions = ((.failed_versions + [$to]) | unique)
		| .last_failure = {at: ($t|tonumber), from: $from, to: $to, step: $step, reason: $w, rolled_back: ($rb == "true"),
			restored: ($rs == "true"), rollback_error: $rf, backup: $set}
		| .running_version = (if $rb == "true" then $from else "" end) | .latest_version = $to
		| .history = (.history + [{at: ($t|tonumber), from: $from, to: $to, result: $o}])[-20:]' \
		--arg t "$(now)" --arg o "$outcome" --arg w "$why" --arg rb "$rb" --arg rs "$rs" --arg from "$FROM" --arg to "$TO" \
		--arg step "$1" --arg rf "${ROLLBACK_FAILED:-}" --arg set "$SET"
	return 1
}

run() {
	local latest major notes
	rotate_log
	STEP_START=$(now)
	FROM="" TO=""
	case $(status_get .state) in upgrading | health-check | rolling-back)
		event error check refused "a run stopped part way, while $(status_get .state): see docs/deploy.md, then run clear"
		status '.last_check_at = ($t|tonumber) | .last_check_result = "interrupted"' --arg t "$(now)"
		return 1
		;;
	esac
	status '.state = "checking" | .running_since = ($t|tonumber)' --arg t "$(now)"
	FROM=$(running_version)
	if [ -z "$FROM" ]; then
		event warn check skipped "the API isn't running"
		checked skipped "the API isn't running, so nothing was done"
		return 0
	fi
	if ! is_release "$FROM"; then
		event info check skipped "$FROM isn't a release"
		checked skipped "$FROM isn't a release, so it isn't upgraded"
		return 0
	fi
	if ! latest=$(latest_release) || [ -z "$latest" ]; then
		event warn check failed "couldn't read the releases from GitHub"
		checked error "couldn't read the releases from GitHub"
		return 1
	fi
	TO=${latest#v}
	if ! newer "$TO" "$FROM"; then
		event info check up-to-date
		checked up-to-date "$FROM is the latest release"
		return 0
	fi
	major=${FROM%%.*}
	if [ "${TO%%.*}" != "$major" ]; then
		event info check skipped "$TO is a new major version: upgrade by hand"
		checked skipped "$TO is a new major version: upgrade by hand (docs/deploy.md)"
		return 0
	fi
	if "$JQ" -e --arg v "$TO" '.failed_versions | index($v)' "$STATUS" >/dev/null 2>&1; then
		event info check skipped "$TO failed before"
		checked skipped "$TO failed before, so it isn't tried again (retry $TO to try it)"
		return 0
	fi
	notes=$(release_field "$latest" '.body // ""' | tr -d '\r' | sed -n '/[^[:space:]]/{p;q}')
	case $notes in *"Upgrade steps"*)
		event info check skipped "$TO has upgrade steps"
		checked skipped "$TO's release notes have Upgrade steps: upgrade by hand"
		return 0
		;;
	esac
	if backup_busy; then
		event info check skipped "a backup is running"
		checked skipped "a backup is running or asked for: trying again next hour"
		return 0
	fi
	event info check newer "$TO is out"

	status '.state = "verifying"'
	REASON="" NOT_READY=""
	if ! verify "$latest"; then
		event error verify refused "$REASON"
		checked refused "$TO wasn't installed: $REASON"
		return 1
	fi
	event info verify ok "files and images attested"

	status '.state = "backing-up"'
	SET="$(date -u +%Y%m%dT%H%M%SZ)-$FROM-to-$TO" BUSY=""
	if ! back_up; then
		if [ -n "$BUSY" ]; then
			event info backup skipped "$REASON"
			checked skipped "a backup is running: trying again next hour"
			return 0
		fi
		event error backup failed "$REASON"
		checked refused "$TO wasn't installed: $REASON"
		return 1
	fi
	event info backup ok "night $NIGHT kept aside, and the files"

	FP_BEFORE=$(fingerprint)
	IMAGES_BEFORE=$(images_of dc)
	MON_IMAGES_BEFORE=""
	[ -z "$MON_DIR" ] || MON_IMAGES_BEFORE=$(images_of mc)
	DB_VOLUME=$(docker inspect -f '{{range .Mounts}}{{if eq .Destination "/var/lib/postgresql"}}{{.Name}}{{end}}{{end}}' "$(dc ps -q db)")
	PAGES_BEFORE=$(dbq postgres "SELECT count(*) FROM messages WHERE created_at > now() - interval '$((WINDOW * 4)) minutes'")
	PAGES_BEFORE=${PAGES_BEFORE:-0}

	status '.state = "upgrading"'
	if ! upgrade; then
		fail_upgrade upgrade "$REASON"
		return 1
	fi
	event info upgrade ok "pulled and started"

	status '.state = "health-check"'
	PAGE_NOTE=""
	if ! health "$TO"; then
		fail_upgrade health "$REASON"
		return 1
	fi
	event info health ok "$PAGE_NOTE"
	status '.state = "idle" | .running_since = 0 | .last_check_at = ($t|tonumber) | .last_check_result = "upgraded"
		| .last_check_note = $n | .running_version = $to | .latest_version = $to
		| .last_upgrade = {at: ($t|tonumber), from: $from, to: $to}
		| .history = (.history + [{at: ($t|tonumber), from: $from, to: $to, result: "upgraded"}])[-20:]' \
		--arg t "$(now)" --arg n "Upgraded from $FROM; $PAGE_NOTE" --arg from "$FROM" --arg to "$TO"
	rm -rf "$HOME_AU/releases/$TO"
}

# ── Switching it on and off ────────────────────────────────────────────────
CRON_MARK="# pagerarchive-auto-upgrade"

enable() {
	local minute line systemd="" schedule=1
	case ${1:-} in
	--systemd) systemd=1 ;;
	--no-schedule) schedule="" ;;
	esac
	tools || {
		echo "couldn't fetch jq and the GitHub CLI from GitHub and check them (is the network up?), or AUTO_UPGRADE_GH isn't a program" >&2
		return 1
	}
	mkdir -p "$STATUS_DIR"
	chmod 755 "$HOME_AU" "$STATUS_DIR"
	if ! grep -q '^AUTO_UPGRADE_STATUS_DIR=' "$PA_DIR/.env"; then
		printf '\n# Automatic upgrades (docs/deploy.md): their status, which the API reads.\nAUTO_UPGRADE_STATUS_DIR=%s\n' \
			"./${STATUS_DIR#"$PA_DIR"/}" >>"$PA_DIR/.env"
		echo "added AUTO_UPGRADE_STATUS_DIR to .env"
	fi
	status '.enabled = true | .enabled_at = ($t|tonumber) | if (.state // "") == "" then .state = "idle" else . end' --arg t "$(now)"
	if [ -z "$schedule" ]; then
		echo "on, with no schedule: run deploy/auto-upgrade/auto-upgrade.sh run from your own scheduler, hourly"
		return 0
	fi
	minute=$(($(od -An -N1 -tu1 /dev/urandom | tr -d ' ') % 60))
	if [ -n "$systemd" ]; then
		local units=${XDG_CONFIG_HOME:-$HOME/.config}/systemd/user
		mkdir -p "$units"
		cat >"$units/pagerarchive-auto-upgrade.service" <<UNIT
[Unit]
Description=PagerArchive automatic upgrade check ($PA_DIR)

[Service]
Type=oneshot
WorkingDirectory=$PA_DIR
ExecStart=$PA_DIR/deploy/auto-upgrade/auto-upgrade.sh run
UNIT
		cat >"$units/pagerarchive-auto-upgrade.timer" <<UNIT
[Unit]
Description=PagerArchive automatic upgrade check, hourly

[Timer]
OnCalendar=*-*-* *:$(printf '%02d' "$minute"):00
Persistent=false

[Install]
WantedBy=timers.target
UNIT
		systemctl --user daemon-reload && systemctl --user enable --now pagerarchive-auto-upgrade.timer || return 1
		echo "on: a systemd user timer, hourly at :$(printf '%02d' "$minute") (it runs while you're logged out only with lingering: loginctl enable-linger)"
	else
		line="$minute * * * * cd '$PA_DIR' && PATH=/usr/local/bin:/usr/bin:/bin bash deploy/auto-upgrade/auto-upgrade.sh run >'$HOME_AU/last-run.txt' 2>&1 $CRON_MARK $PA_DIR"
		{ crontab -l 2>/dev/null | grep -vF "$CRON_MARK $PA_DIR"; echo "$line"; } | crontab - || return 1
		echo "on: your crontab runs it hourly at :$(printf '%02d' "$minute")"
	fi
	echo "now run: docker compose up -d   (so the API reads the status), then: deploy/auto-upgrade/auto-upgrade.sh run"
}

disable() {
	local units=${XDG_CONFIG_HOME:-$HOME/.config}/systemd/user
	if crontab -l 2>/dev/null | grep -qF "$CRON_MARK $PA_DIR"; then
		crontab -l 2>/dev/null | grep -vF "$CRON_MARK $PA_DIR" | crontab -
	fi
	if [ -f "$units/pagerarchive-auto-upgrade.timer" ] && grep -q "$PA_DIR" "$units/pagerarchive-auto-upgrade.service" 2>/dev/null; then
		systemctl --user disable --now pagerarchive-auto-upgrade.timer >/dev/null 2>&1
		rm -f "$units/pagerarchive-auto-upgrade.timer" "$units/pagerarchive-auto-upgrade.service"
		systemctl --user daemon-reload 2>/dev/null
	fi
	if [ -x "${JQ:-}" ]; then
		status '.enabled = false | .disabled_at = ($t|tonumber)' --arg t "$(now)"
	fi
	FROM="" TO="" event info check off "switched off"
	echo "off: no more checks. Administration says so. Upgrade by hand as docs/deploy.md says."
}

main() {
	PA_DIR=${AUTO_UPGRADE_INSTALL_DIR:-$(cd "$(dirname "$0")/../.." && pwd)}
	[ -f "$PA_DIR/compose.yaml" ] || {
		echo "auto-upgrade: no compose.yaml in $PA_DIR" >&2
		return 2
	}
	load_settings
	mkdir -p "$HOME_AU"
	local cmd=${1:-}
	[ $# -eq 0 ] || shift
	case $cmd in
	run | verify | retry | clear | status | enable | disable) ;;
	*)
		sed -n '2,15p' "$0" | sed 's/^# \{0,1\}//' >&2
		return 2
		;;
	esac
	for t in docker curl flock sha256sum; do
		command -v "$t" >/dev/null || {
			echo "auto-upgrade: needs $t" >&2
			return 2
		}
	done
	case $cmd in
	enable)
		enable "$@"
		return
		;;
	disable)
		tools >/dev/null 2>&1
		disable
		return
		;;
	esac
	exec 9>"$HOME_AU/lock"
	if ! flock -n 9; then
		echo "auto-upgrade: another run is going" >&2
		return 1
	fi
	if ! tools; then
		JQ=${JQ:-false}
		STEP_START=$(now)
		event error verify refused "the pinned jq or GitHub CLI couldn't be fetched or didn't match its checksum, so nothing can be verified"
		[ ! -x "$JQ" ] || checked refused "nothing can be verified: the pinned tools couldn't be fetched"
		return 1
	fi
	case $cmd in
	run)
		if [ "$(status_get .enabled)" != true ]; then
			echo "auto-upgrade is off: deploy/auto-upgrade/auto-upgrade.sh enable" >&2
			return 1
		fi
		run
		;;
	status) "$JQ" . "$STATUS" 2>/dev/null || echo "no status yet: auto-upgrade has never run here" ;;
	retry)
		[ -n "${1:-}" ] || {
			echo "usage: auto-upgrade.sh retry X.Y.Z" >&2
			return 2
		}
		status '.failed_versions -= [$v]' --arg v "${1#v}"
		echo "${1#v} will be tried at the next run"
		;;
	clear)
		status '.state = "idle" | .running_since = 0 | if .last_check_result == "interrupted" then .last_check_result = "cleared" else . end
			| if .last_failure then .last_failure.cleared_at = ($t|tonumber) else . end' --arg t "$(now)"
		echo "cleared: Administration's notice and the alert go, and the next run checks again. Check the install first: docs/deploy.md, \"Automatic upgrades\""
		;;
	verify)
		local tag=${1:-}
		latest_release >/dev/null || {
			echo "couldn't read the releases from GitHub" >&2
			return 1
		}
		[ -n "$tag" ] || tag=$(latest_release)
		REASON=""
		if verify "$tag"; then
			echo "verified: $tag's files ($FILES) and images ($IMAGES) were built by $PA_REPO's workflows on main"
		else
			echo "NOT verified: $tag: $REASON" >&2
			return 1
		fi
		;;
	esac
}

main "$@"
exit $?
