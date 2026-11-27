#!/usr/bin/env bash
# Automatic upgrades for a receiving client on its own host (SPEC.md R84n-R84p,
# ADR 0053; docs/client.md, "Automatic upgrades"). Off unless switched on.
# Attached to every release as client-auto-upgrade.sh, and kept beside the
# client's compose.yaml, .env and key; it runs on the client's host, as the
# user who runs `docker compose` for the client, from cron (or a systemd user
# timer):
#
#   ./client-auto-upgrade.sh enable [--systemd]   switch it on: hourly, from cron
#                                    (or a systemd user timer; --no-schedule: neither)
#   ./client-auto-upgrade.sh disable              switch it off
#   ./client-auto-upgrade.sh run                  one check, now
#   ./client-auto-upgrade.sh status               what it last did
#   ./client-auto-upgrade.sh verify [vX.Y.Z]      check a release's files and
#                                                 the client's image, nothing more
#   ./client-auto-upgrade.sh retry X.Y.Z          try a release marked failed again
#   ./client-auto-upgrade.sh clear                after a run that stopped part way
#
# Each run asks the server, with the client's own key, which version it runs,
# and finds the newest full release (not a draft, not a pre-release) on
# GitHub's releases API that isn't newer than the server's: a client is never
# upgraded past its server (R14b). If it's newer than the client, in the same
# major version, not marked failed and its notes don't start with "Upgrade
# steps":
#   1. verify: its client-compose.yaml's, client-auto-upgrade.sh's and the
#      client image's attestations, with a pinned GitHub CLI this script fetches
#      and checks by its checksum, as the server's automatic upgrades do (R84e)
#   2. back up: copies of compose.yaml, .env, compose.override.yaml and this script
#   3. upgrade: the verified files installed, pull, up -d
#   4. health check: the client running the new version, from the verified
#      image, and still running, not restarted, a minute later
#   5. on failure: the previous files and image back, and the release marked
#      failed so it isn't tried again
#
# What it did goes to auto-upgrade/status/status.json and one JSON line per
# step to auto-upgrade/status/auto-upgrade.log, and to the server, which shows
# it in its metrics (the Clients dashboard): the status at each step, never the
# log. Neither ever holds page text: only versions, times, counts and fixed text.
#
# Never two runs at once (a lock), nor while another app on the host upgrades
# (R84m): every run holds the host-wide lock /run/lock/archive-upgrade.lock
# (flock), as the server's automatic upgrades and a companion app's do.

PA_GH_VERSION=2.103.3
PA_GH_SHA256_amd64=4c806978ffce1cec70853302a56d70ac6da4f19bae7adca4e1a8ab00633dd91d
PA_GH_SHA256_arm64=ee32df2323c8847f5fac6cb6b4b69ca75828e9329a8f22f677021f7b3306ce63
PA_JQ_VERSION=1.8.2
PA_JQ_SHA256_amd64=b1c22172dd303f3be49e935aa56aa48a8b7a46e0bc838b4997d3bb451495870f
PA_JQ_SHA256_arm64=8b85c817833814ddca00a144c33705546355afccf0cf39b188f3cdb48b852309
PA_REPO=RIKIKU/PagerArchive
# The name this script has in the client's folder, as the release attaches it.
PA_SELF=client-auto-upgrade.sh

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
	case $HOME_AU in /*) ;; *) HOME_AU=$PA_DIR/$HOME_AU ;; esac
	STATUS_DIR=$HOME_AU/status
	STATUS=$STATUS_DIR/status.json
	LOG=$STATUS_DIR/auto-upgrade.log
	RELEASES=$HOME_AU/releases.json
	API=$(setting AUTO_UPGRADE_API "https://api.github.com/repos/$PA_REPO")
	TOKEN=$(setting AUTO_UPGRADE_GITHUB_TOKEN "")
	PREFIX=$(setting AUTO_UPGRADE_IMAGE_PREFIX "ghcr.io/rikiku/")
	START_TIMEOUT=$(setting AUTO_UPGRADE_START_TIMEOUT 300)
	HEALTH_WAIT=$(setting AUTO_UPGRADE_HEALTH_WAIT 60)
	KEEP=$(setting AUTO_UPGRADE_KEEP 3)
	GH_OVERRIDE=$(setting AUTO_UPGRADE_GH "")
	SERVICE=$(setting AUTO_UPGRADE_SERVICE client)
	SERVER_URL=$(setting SERVER_URL "")
	for n in START_TIMEOUT HEALTH_WAIT KEEP; do
		case ${!n} in '' | *[!0-9]*) echo "client-auto-upgrade: a number setting ($n) must be a whole number" >&2 && exit 2 ;; esac
	done
	case $SERVICE in '' | *[!a-z0-9_-]*) echo "client-auto-upgrade: AUTO_UPGRADE_SERVICE must be a compose service's name" >&2 && exit 2 ;; esac
}

# ── Output: the log, the status, and the server ────────────────────────────
now() { date -u +%s; }

json_str() { printf '%s' "$1" | sed 's/\\/\\\\/g; s/"/\\"/g' | tr -d '\000-\037'; }

# event <level> <step> <result> [reason]: one JSON line per step, as the
# server's automatic upgrades write them (R84g). The reason is fixed text,
# versions and counts: never page text.
event() {
	local line
	line=$(printf '{"time":"%s","level":"%s","step":"%s","result":"%s","from":"%s","to":"%s","reason":"%s","seconds":%s}' \
		"$(date -u +%Y-%m-%dT%H:%M:%SZ)" "$1" "$2" "$3" "$(json_str "${FROM:-}")" "$(json_str "${TO:-}")" \
		"$(json_str "${4:-}")" "$(($(now) - ${STEP_START:-$(now)}))")
	mkdir -p "$STATUS_DIR"
	printf '%s\n' "$line" >>"$LOG"
	echo "client-auto-upgrade: $2 $3${4:+: $4}"
	STEP_START=$(now)
}

rotate_log() {
	[ -f "$LOG" ] || return 0
	[ "$(wc -c <"$LOG")" -lt 1048576 ] || mv -f "$LOG" "$LOG.1"
}

# status <jq filter> [jq args...]: changes the status file atomically.
status() {
	local f=$1
	shift
	mkdir -p "$STATUS_DIR"
	[ -s "$STATUS" ] || echo '{"enabled": false, "failures": 0, "rollbacks": 0, "failed_versions": [], "history": []}' >"$STATUS"
	if "$JQ" "$@" "$f" "$STATUS" >"$STATUS.partial"; then
		mv -f "$STATUS.partial" "$STATUS"
	else
		rm -f "$STATUS.partial"
		echo "client-auto-upgrade: couldn't write the status" >&2
	fi
}

status_get() { "$JQ" -r "$1 // empty" "$STATUS" 2>/dev/null; }

# progress <jq filter> [jq args...]: a step of a run, in the status and told
# to the server at once, so its dashboard shows a run going now (R84p).
progress() {
	status "$@"
	report >/dev/null 2>&1 || true
}

CHECKED_AT='.last_check_at = (($ENV.AU_RUN_START // "") | if . == "" then ($t|tonumber) else tonumber end)'
DONE='.state = "idle" | .running_since = 0 | .target_version = ""'

# checked <result> <note>: the end of a run that changed nothing.
checked() {
	status "$CHECKED_AT | $DONE"' | .last_check_result = $r | .last_check_note = $n
		| .running_version = $v | .latest_version = $l | .server_version = $s' \
		--arg t "$(now)" --arg r "$1" --arg n "$2" --arg v "${FROM:-}" --arg l "${TO:-}" --arg s "${SERVER_VERSION:-}"
}

# client_key: the client's API key, from the file compose.yaml mounts (key),
# else CLIENT_KEY in .env. Never on a command line.
client_key() {
	if [ -s "$PA_DIR/key" ]; then
		tr -d ' \r\n' <"$PA_DIR/key"
	else
		setting CLIENT_KEY ""
	fi
}

# report: the status, and the client's version, to the server, with the
# client's own key (POST /api/v1/client/auto-upgrade, R84p). Its answer is
# the server's version: SERVER_SAYS. The key goes in a header file only this
# user can read, so it's never on a command line.
report() {
	local key hdr out rc
	SERVER_SAYS=""
	[ -n "$SERVER_URL" ] && [ -x "${JQ:-}" ] || return 1
	key=$(client_key)
	[ -n "$key" ] || return 1
	hdr=$(umask 077 && mktemp "$HOME_AU/.header.XXXXXX") || return 1
	printf 'Authorization: Bearer %s\n' "$key" >"$hdr"
	out=$("$JQ" -c --arg v "${RUNNING:-}" '{client_version: $v, status: .}' "$STATUS" 2>/dev/null |
		curl -fsS --max-time 20 -X POST -H @"$hdr" -H 'Content-Type: application/json' -A "pagerarchive-client-auto-upgrade" \
			--data-binary @- "${SERVER_URL%/}/api/v1/client/auto-upgrade" 2>/dev/null)
	rc=$?
	rm -f "$hdr"
	[ $rc -eq 0 ] || return 1
	SERVER_SAYS=$(printf '%s' "$out" | "$JQ" -r '.server_version // empty' 2>/dev/null | head -n 1)
}

# ── Tools ──────────────────────────────────────────────────────────────────
dc() { (cd "$PA_DIR" && docker compose "$@"); }

sha() { sha256sum "$1" | cut -d' ' -f1; }

curl_api() {
	if [ -n "$TOKEN" ]; then
		curl -fsSL --max-time 60 -H "Accept: application/vnd.github+json" -H "Authorization: Bearer $TOKEN" "$API$1"
	else
		curl -fsSL --max-time 60 -H "Accept: application/vnd.github+json" "$API$1"
	fi
}

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

# host_arch: the client hosts' two architectures (a Raspberry Pi's arm64, R13).
host_arch() {
	case $(uname -m) in
	x86_64 | amd64) echo amd64 ;;
	aarch64 | arm64) echo arm64 ;;
	*) return 1 ;;
	esac
}

# tools: jq, and the GitHub CLI for `gh attestation verify`, each a pinned
# release for this host's architecture, checked against its published
# checksum (ADR 0030, ADR 0053). No sign-in: the attestations come from
# GitHub's API and are verified offline against Sigstore's public roots.
tools() {
	local bin=$HOME_AU/bin arch gh_sha jq_sha
	arch=$(host_arch) || return 1
	gh_sha=PA_GH_SHA256_$arch jq_sha=PA_JQ_SHA256_$arch
	mkdir -p "$bin"
	JQ=$bin/jq-$PA_JQ_VERSION
	fetch_pinned "https://github.com/jqlang/jq/releases/download/jq-$PA_JQ_VERSION/jq-linux-$arch" "$JQ" "${!jq_sha}" || return 1
	chmod 755 "$JQ"
	if [ -n "$GH_OVERRIDE" ]; then
		GH=$GH_OVERRIDE
		[ -x "$GH" ] || return 1
		return 0
	fi
	local tgz=$bin/gh_${PA_GH_VERSION}_linux_$arch.tar.gz
	fetch_pinned "https://github.com/cli/cli/releases/download/v$PA_GH_VERSION/gh_${PA_GH_VERSION}_linux_$arch.tar.gz" "$tgz" "${!gh_sha}" || return 1
	rm -rf "$bin/gh"
	mkdir -p "$bin/gh"
	tar -xzf "$tgz" -C "$bin/gh" --strip-components=2 "gh_${PA_GH_VERSION}_linux_$arch/bin/gh" || return 1
	GH=$bin/gh/gh
	export GH_CONFIG_DIR=$bin/gh-config GH_NO_UPDATE_NOTIFIER=1 GH_PROMPT_DISABLED=1
	unset GH_TOKEN GITHUB_TOKEN
}

# ── Versions ───────────────────────────────────────────────────────────────
# Every version and tag taken in (the running client's, the server's, GitHub's
# answers, the arguments) is checked whole, as one line, before it reaches a
# path or a command: a release is X.Y.Z and its tag vX.Y.Z (R84k).
is_release() { [[ $1 =~ ^[0-9]{1,9}\.[0-9]{1,9}\.[0-9]{1,9}$ ]]; }
is_tag() { [[ $1 =~ ^v[0-9]{1,9}\.[0-9]{1,9}\.[0-9]{1,9}$ ]]; }
is_commit() { [[ $1 =~ ^[0-9a-f]{40}$ ]]; }
newer() { [ "$1" != "$2" ] && [ "$(printf '%s\n%s\n' "$1" "$2" | sort -V | tail -n 1)" = "$1" ]; }

# shown <value>: a value that isn't a release is repeated only if it's
# version-like, as it may be any text.
shown() {
	case $1 in *[!0-9A-Za-z.+-]* | ?????????????????????????????????*) printf 'not a version' ;; *) printf '%s' "${1:-nothing}" ;; esac
}

running_version() {
	dc exec -T "$SERVICE" /usr/local/bin/pagerarchive-client --version 2>/dev/null | sed -n 's/^pagerarchive-client //p' | tr -d '\r' | head -n 1
}

# pick_release <major>: the newest full release in that major version that
# isn't newer than the server (SERVER_VERSION), as TARGET_TAG, and the newest
# full release of all, as NEWEST_TAG, from the releases list, which it keeps
# for release_field and asset_url. An answer that isn't tags is refused.
pick_release() {
	local tags t v
	curl_api "/releases?per_page=50" >"$RELEASES" || return 1
	tags=$("$JQ" -r '[.[] | select(.draft == false and .prerelease == false)
		| select(.tag_name | test("\\Av[0-9]+\\.[0-9]+\\.[0-9]+\\z"))]
		| sort_by(.tag_name | ltrimstr("v") | split(".") | map(tonumber)) | .[].tag_name' "$RELEASES") || return 1
	TARGET_TAG="" NEWEST_TAG=""
	for t in $tags; do
		is_tag "$t" || return 1
		NEWEST_TAG=$t
		v=${t#v}
		[ "${v%%.*}" = "$1" ] || continue
		newer "$v" "$SERVER_VERSION" && continue
		TARGET_TAG=$t
	done
}

tag_commit() {
	local c
	c=$(curl_api "/commits/$1" 2>/dev/null | "$JQ" -r '.sha // empty' 2>/dev/null)
	is_commit "$c" || return 1
	printf '%s' "$c"
}

release_field() { "$JQ" -r --arg t "$1" ".[] | select(.tag_name == \$t) | $2" "$RELEASES"; }
asset_url() { release_field "$1" ".assets[] | select(.name == \"$2\") | .browser_download_url"; }

# ── Verify ─────────────────────────────────────────────────────────────────
# attested <subject> <digest> <name>: as the server's automatic upgrades
# verify (R84e): signed by this repository's ci.yml on main, on a
# GitHub-hosted runner and, when SOURCE_DIGEST is set, from that commit.
attested() {
	local bundles
	bundles=$(mktemp --suffix=.jsonl)
	if ! curl_api "/attestations/$2" 2>/dev/null | "$JQ" -c '.attestations[].bundle' >"$bundles" 2>/dev/null || [ ! -s "$bundles" ]; then
		rm -f "$bundles"
		REASON="no attestation for $3"
		return 1
	fi
	if ! "$GH" attestation verify "$1" --repo "$PA_REPO" --signer-workflow "$PA_REPO/.github/workflows/ci.yml" \
		--source-ref refs/heads/main ${SOURCE_DIGEST:+--source-digest "$SOURCE_DIGEST"} --deny-self-hosted-runners \
		--bundle "$bundles" >/dev/null 2>&1; then
		rm -f "$bundles"
		REASON="the attestation of $3 doesn't verify"
		return 1
	fi
	rm -f "$bundles"
}

# verify_image <image:tag>: pulled, and the digest its tag names (the image
# index, for both architectures) attested. Sets IMAGE_ID.
verify_image() {
	local digest
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

# verify <tag>: the release's client files downloaded into
# auto-upgrade/releases/<version> and attested, then the client's image.
# Sets FILES_SHA (what's installed is checked against it) and VERIFIED.
verify() {
	local tag=$1 v=${1#v} dir url f d
	SOURCE_DIGEST=""
	is_tag "$tag" || {
		REASON="that isn't a release's tag (vX.Y.Z)"
		return 1
	}
	SOURCE_DIGEST=$(tag_commit "$tag") || {
		SOURCE_DIGEST=""
		REASON="couldn't find the commit $tag names"
		return 1
	}
	dir=$HOME_AU/releases/$v
	rm -rf "$dir"
	mkdir -p "$dir"
	FILES="client-compose.yaml $PA_SELF"
	FILES_SHA=""
	for f in $FILES; do
		url=$(asset_url "$tag" "$f")
		if [ -z "$url" ]; then
			REASON="the release has no $f yet"
			return 1
		fi
		curl -fsSL --max-time 300 -o "$dir/$f" "$url" || {
			REASON="couldn't download $f"
			return 1
		}
		d=$(sha "$dir/$f")
		attested "$dir/$f" "sha256:$d" "$f" || return 1
		FILES_SHA="$FILES_SHA $f=$d"
	done
	# The client's compose.yaml names the client's image by the release's own version.
	grep -q "^ *image: ${PREFIX}pagerarchive-client:$v\$" "$dir/client-compose.yaml" || {
		REASON="client-compose.yaml doesn't name pagerarchive-client:$v"
		return 1
	}
	IMAGES="${PREFIX}pagerarchive-client:$v"
	verify_image "$IMAGES" || return 1
	VERIFIED=" $IMAGE_ID"
}

verified_file() {
	case " $FILES_SHA " in *" $2=$(sha "$1") "*) return 0 ;; esac
	return 1
}

# ── Health ─────────────────────────────────────────────────────────────────
container() { dc ps -q "$SERVICE" 2>/dev/null | head -n 1; }

# client_up <version>: the client's container runs <version>, within START_TIMEOUT.
client_up() {
	local got end=$(($(now) + START_TIMEOUT))
	while :; do
		got=$(running_version)
		[ "$got" = "$1" ] && return 0
		if [ "$(now)" -ge "$end" ]; then
			REASON="the client didn't run as $1 within $START_TIMEOUT s (it runs $(shown "$got"))"
			return 1
		fi
		sleep 3
	done
}

# all_running: every service of the client's project running.
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

# health <version>: the client runs <version>, from the verified image, and is
# still running HEALTH_WAIT seconds later, in the same container, not
# restarted: a client that can't start (its settings, its radio) restarts
# over and over, and that shows within a minute.
health() {
	local v=$1 c started restarts id
	REASON=""
	client_up "$v" || return 1
	c=$(container)
	id=$(docker inspect -f '{{.Image}}' "$c" 2>/dev/null)
	case "$VERIFIED " in *" $id "*) ;; *)
		REASON="the client runs an image that wasn't verified"
		return 1
		;;
	esac
	started=$(docker inspect -f '{{.State.StartedAt}} {{.RestartCount}}' "$c" 2>/dev/null)
	sleep "$HEALTH_WAIT"
	restarts=$(docker inspect -f '{{.State.StartedAt}} {{.RestartCount}}' "$c" 2>/dev/null)
	if [ "$(container)" != "$c" ] || [ "$restarts" != "$started" ] || [ "$(docker inspect -f '{{.State.Running}}' "$c" 2>/dev/null)" != true ]; then
		REASON="the client stopped or restarted within $HEALTH_WAIT s of starting"
		return 1
	fi
	all_running && [ "$(running_version)" = "$v" ] || {
		REASON=${REASON:-"the client isn't running $v"}
		return 1
	}
}

# ── Backup, upgrade, rollback ──────────────────────────────────────────────
back_up() {
	local f
	[[ $SET =~ ^[0-9]{8}T[0-9]{6}Z-[0-9.]+-to-[0-9.]+$ ]] || {
		REASON="the backup set's name isn't one this script makes"
		return 1
	}
	FILES_DIR=$HOME_AU/files/$SET
	mkdir -p "$FILES_DIR"
	# .env may hold the key (CLIENT_KEY): only this user reads the copies.
	chmod 700 "$HOME_AU/files" "$FILES_DIR"
	for f in compose.yaml .env compose.override.yaml "$PA_SELF"; do
		[ ! -e "$PA_DIR/$f" ] || cp -a "$PA_DIR/$f" "$FILES_DIR/" || {
			REASON="couldn't copy the client's files"
			return 1
		}
	done
	ls -1d "$HOME_AU"/files/*/ | sort | head -n -"$KEEP" | while IFS= read -r old; do rm -rf -- "$old"; done
}

images_of() {
	local c
	for c in $(dc ps -q 2>/dev/null); do
		docker inspect -f '{{.Image}}	{{.Config.Image}}' "$c"
	done | sort -u
}

# install_files: the verified compose.yaml and this script's successor, each
# moved into place whole. The running script is read in full before it runs
# (main is called on its last line), so replacing it changes nothing in this run.
install_files() {
	local dir=$HOME_AU/releases/$TO f
	for f in $FILES; do
		verified_file "$dir/$f" "$f" || {
			REASON="$f changed after it was verified"
			return 1
		}
	done
	cp "$dir/client-compose.yaml" "$PA_DIR/compose.yaml.partial" && mv -f "$PA_DIR/compose.yaml.partial" "$PA_DIR/compose.yaml" &&
		cp "$dir/$PA_SELF" "$PA_DIR/$PA_SELF.partial" && chmod 755 "$PA_DIR/$PA_SELF.partial" && mv -f "$PA_DIR/$PA_SELF.partial" "$PA_DIR/$PA_SELF" || {
		REASON="couldn't install the release's files"
		return 1
	}
}

# our_images_verified: after the pull, the client image Compose will run is
# the verified one. A release's version tag never moves, so another is refused.
our_images_verified() {
	local ref id
	for ref in $(cat "$PA_DIR/compose.yaml" "$PA_DIR/compose.override.yaml" 2>/dev/null |
		sed -n -E "s#^ *image: *(${PREFIX//./\\.}pagerarchive-client:[^ ]+) *\$#\1#p" | sort -u); do
		id=$(docker image inspect -f '{{.Id}}' "$ref" 2>/dev/null)
		case "$VERIFIED " in *" $id "*) continue ;; esac
		REASON="${ref##*/} isn't the image that was verified"
		return 1
	done
}

upgrade() {
	install_files || return 1
	dc config -q >/dev/null 2>&1 || {
		REASON="the new compose.yaml doesn't load"
		return 1
	}
	dc pull -q "$SERVICE" >/dev/null 2>&1 || {
		REASON="docker compose pull failed"
		return 1
	}
	our_images_verified || return 1
	dc up -d >/dev/null 2>&1 || {
		REASON="docker compose up -d failed"
		return 1
	}
}

# rollback: the previous files, and the image that was running, with nothing pulled.
rollback() {
	local f
	status '.state = "rolling-back"'
	report >/dev/null 2>&1 || true
	event info rollback started "the previous files and image back"
	for f in compose.yaml .env compose.override.yaml "$PA_SELF"; do
		[ ! -e "$FILES_DIR/$f" ] || cp -a "$FILES_DIR/$f" "$PA_DIR/" || {
			ROLLBACK_FAILED="the previous files couldn't be put back"
			return 1
		}
	done
	printf '%s\n' "$IMAGES_BEFORE" | while IFS='	' read -r id ref; do
		[ -z "$id" ] || docker tag "$id" "$ref" 2>/dev/null
	done
	dc up -d --remove-orphans >/dev/null 2>&1
	VERIFIED=" $(printf '%s\n' "$IMAGES_BEFORE" | cut -f1 | tr '\n' ' ')"
	if ! client_up "$FROM" || ! all_running; then
		ROLLBACK_FAILED="after the rollback: $REASON"
		return 1
	fi
}

fail_upgrade() { # <step> <reason>
	local why=$2 rb=false outcome
	event error "$1" failed "$why"
	STEP_START=$(now)
	ROLLBACK_FAILED=""
	if rollback; then
		rb=true
		outcome=rolled-back
		event warn rollback ok "back on $FROM"
	else
		outcome=rollback-failed
		event error rollback failed "$ROLLBACK_FAILED"
	fi
	status "$CHECKED_AT | $DONE"' | .last_check_result = $o | .last_check_note = $w
		| .failures += 1 | .rollbacks += (if $rb == "true" then 1 else 0 end)
		| .failed_versions = ((.failed_versions + [$to]) | unique)
		| .last_failure = {at: ($t|tonumber), from: $from, to: $to, step: $step, reason: $w, rolled_back: ($rb == "true"),
			restored: false, rollback_error: $rf, backup: $set}
		| .running_version = (if $rb == "true" then $from else "" end) | .latest_version = $to
		| .history = (.history + [{at: ($t|tonumber), from: $from, to: $to, result: $o}])[-20:]' \
		--arg t "$(now)" --arg o "$outcome" --arg w "$why" --arg rb "$rb" --arg from "$FROM" --arg to "$TO" \
		--arg step "$1" --arg rf "${ROLLBACK_FAILED:-}" --arg set "$SET"
	return 1
}

# ── The host-wide lock ─────────────────────────────────────────────────────
# The same lock as the server's automatic upgrades and a companion app's
# (R84m), so two upgrades never run at once on one host. PA_TEST_HOST_LOCK:
# tests only.
HOST_LOCK=${PA_TEST_HOST_LOCK:-/run/lock/archive-upgrade.lock}

host_locked_run() {
	local rc
	[ -e "$HOST_LOCK" ] || (umask 000 && : >>"$HOST_LOCK") 2>/dev/null
	if [ ! -r "$HOST_LOCK" ]; then
		STEP_START=$(now)
		event error check refused "the host-wide upgrade lock $HOST_LOCK can't be made or read: see docs/client.md"
		checked refused "the host-wide upgrade lock can't be made or read, so nothing was done"
		report >/dev/null 2>&1 || true
		return 1
	fi
	exec 8<"$HOST_LOCK"
	if ! flock -n 8; then
		FROM=$(status_get .running_version) TO=$(status_get .latest_version)
		STEP_START=$(now)
		event info check skipped "another upgrade is running"
		checked skipped "another upgrade is running on this host: trying again next hour"
		exec 8<&-
		report >/dev/null 2>&1 || true
		return 0
	fi
	run
	rc=$?
	# Whatever the run did, the server hears how it ended.
	RUNNING=$(running_version)
	is_release "$RUNNING" || RUNNING=""
	report >/dev/null 2>&1 || true
	return $rc
}

# ── A run ──────────────────────────────────────────────────────────────────
run() {
	local major notes held
	rotate_log
	STEP_START=$(now)
	FROM="" TO="" SERVER_VERSION=""
	case $(status_get .state) in upgrading | health-check | rolling-back)
		event error check refused "a run stopped part way, while $(status_get .state): see docs/client.md, then run clear"
		status '.last_check_at = ($t|tonumber) | .last_check_result = "interrupted"' --arg t "$(now)"
		return 1
		;;
	esac
	AU_RUN_START=$(now)
	export AU_RUN_START
	status '.state = "checking" | .running_since = ($t|tonumber) | .last_check_at = ($t|tonumber) | .target_version = ""
		| .last_check_result = "in-progress" | .last_check_note = "checking for a release now"' --arg t "$AU_RUN_START"
	FROM=$(running_version)
	if [ -z "$FROM" ]; then
		event warn check skipped "the client isn't running"
		checked skipped "the client isn't running, so nothing was done"
		return 0
	fi
	if ! is_release "$FROM"; then
		FROM=$(shown "$FROM")
		event info check skipped "the running version ($FROM) isn't a release"
		checked skipped "the running version ($FROM) isn't a release, so it isn't upgraded"
		return 0
	fi
	RUNNING=$FROM
	# The server's version, from the server itself, with the client's key: a
	# client is never upgraded past it (R14b).
	if ! report; then
		event warn check failed "couldn't ask the server its version"
		checked error "couldn't ask the server at SERVER_URL its version (a server from before client upgrades needs upgrading first)"
		return 1
	fi
	if ! is_release "$SERVER_SAYS"; then
		event info check skipped "the server runs $(shown "$SERVER_SAYS"), not a release"
		checked skipped "the server runs $(shown "$SERVER_SAYS"), not a release, so the client isn't upgraded"
		return 0
	fi
	SERVER_VERSION=$SERVER_SAYS
	if newer "$FROM" "$SERVER_VERSION"; then
		event info check skipped "the client is newer than its server ($SERVER_VERSION)"
		checked skipped "the client ($FROM) is newer than its server ($SERVER_VERSION): upgrade the server"
		return 0
	fi
	major=${FROM%%.*}
	if ! pick_release "$major"; then
		event warn check failed "couldn't read the releases from GitHub"
		checked error "couldn't read the releases from GitHub"
		return 1
	fi
	TO=${TARGET_TAG#v}
	if [ -z "$TO" ] || ! newer "$TO" "$FROM"; then
		TO=$FROM
		held="$FROM is the latest release"
		if [ -n "$NEWEST_TAG" ] && newer "${NEWEST_TAG#v}" "$FROM"; then
			if newer "${NEWEST_TAG#v}" "$SERVER_VERSION"; then
				held="${NEWEST_TAG#v} is out: the client waits for its server ($SERVER_VERSION)"
			else
				held="${NEWEST_TAG#v} is a new major version: upgrade by hand (docs/client.md)"
			fi
		fi
		event info check up-to-date "$held"
		checked up-to-date "$held"
		return 0
	fi
	if "$JQ" -e --arg v "$TO" '.failed_versions | index($v)' "$STATUS" >/dev/null 2>&1; then
		event info check skipped "$TO failed before"
		checked skipped "$TO failed before, so it isn't tried again (retry $TO to try it)"
		return 0
	fi
	notes=$(release_field "v$TO" '.body // ""' | tr -d '\r' | sed -n '/[^[:space:]]/{p;q}')
	case $notes in *"Upgrade steps"*)
		event info check skipped "$TO has upgrade steps"
		checked skipped "$TO's release notes have Upgrade steps: upgrade by hand"
		return 0
		;;
	esac
	event info check newer "$TO is out, and the server runs $SERVER_VERSION"

	progress '.state = "verifying" | .target_version = $to | .server_version = $s | .last_check_note = ($to + " is out: upgrading to it")' \
		--arg to "$TO" --arg s "$SERVER_VERSION"
	REASON=""
	if ! verify "v$TO"; then
		event error verify refused "$REASON"
		checked refused "$TO wasn't installed: $REASON"
		return 1
	fi
	event info verify ok "files and image attested"

	progress '.state = "backing-up"'
	SET="$(date -u +%Y%m%dT%H%M%SZ)-$FROM-to-$TO"
	if ! back_up; then
		event error backup failed "$REASON"
		checked refused "$TO wasn't installed: $REASON"
		return 1
	fi
	event info backup ok "the client's files"
	IMAGES_BEFORE=$(images_of)

	progress '.state = "upgrading"'
	if ! upgrade; then
		fail_upgrade upgrade "$REASON"
		return 1
	fi
	event info upgrade ok "pulled and started"

	RUNNING=$TO
	progress '.state = "health-check"'
	if ! health "$TO"; then
		RUNNING=$FROM
		fail_upgrade health "$REASON"
		return 1
	fi
	event info health ok "running $TO, and still running after $HEALTH_WAIT s"
	status "$CHECKED_AT | $DONE"' | .last_check_result = "upgraded"
		| .last_check_note = $n | .running_version = $to | .latest_version = $to | .server_version = $s
		| .last_upgrade = {at: ($t|tonumber), from: $from, to: $to}
		| .history = (.history + [{at: ($t|tonumber), from: $from, to: $to, result: "upgraded"}])[-20:]' \
		--arg t "$(now)" --arg n "Upgraded from $FROM" --arg from "$FROM" --arg to "$TO" --arg s "$SERVER_VERSION"
	rm -rf "$HOME_AU/releases/$TO"
}

# ── Switching it on and off ────────────────────────────────────────────────
CRON_MARK="# pagerarchive-client-auto-upgrade"

enable() {
	local minute line systemd="" schedule=1
	case ${1:-} in
	--systemd) systemd=1 ;;
	--no-schedule) schedule="" ;;
	esac
	tools || {
		echo "couldn't fetch jq and the GitHub CLI from GitHub and check them (is the network up? is this host amd64 or arm64?), or AUTO_UPGRADE_GH isn't a program" >&2
		return 1
	}
	[ -n "$SERVER_URL" ] && [ -n "$(client_key)" ] || {
		echo "set SERVER_URL in .env and put the client's key in the file named key first (docs/client.md)" >&2
		return 1
	}
	mkdir -p "$STATUS_DIR"
	status '.enabled = true | .enabled_at = ($t|tonumber) | if (.state // "") == "" then .state = "idle" else . end' --arg t "$(now)"
	RUNNING=$(running_version)
	is_release "$RUNNING" || RUNNING=""
	report >/dev/null 2>&1 || echo "warning: the server didn't answer at SERVER_URL; the checks will say so" >&2
	if [ -z "$schedule" ]; then
		echo "on, with no schedule: run ./$PA_SELF run from your own scheduler, hourly"
		return 0
	fi
	minute=$(($(od -An -N1 -tu1 /dev/urandom | tr -d ' ') % 60))
	if [ -n "$systemd" ]; then
		local units=${XDG_CONFIG_HOME:-$HOME/.config}/systemd/user
		mkdir -p "$units"
		cat >"$units/pagerarchive-client-auto-upgrade.service" <<UNIT
[Unit]
Description=PagerArchive client automatic upgrade check ($PA_DIR)

[Service]
Type=oneshot
WorkingDirectory=$PA_DIR
ExecStart=$PA_DIR/$PA_SELF run
UNIT
		cat >"$units/pagerarchive-client-auto-upgrade.timer" <<UNIT
[Unit]
Description=PagerArchive client automatic upgrade check, hourly

[Timer]
OnCalendar=*-*-* *:$(printf '%02d' "$minute"):00
Persistent=false

[Install]
WantedBy=timers.target
UNIT
		systemctl --user daemon-reload && systemctl --user enable --now pagerarchive-client-auto-upgrade.timer || return 1
		echo "on: a systemd user timer, hourly at :$(printf '%02d' "$minute") (it runs while you're logged out only with lingering: loginctl enable-linger)"
	else
		line="$minute * * * * cd '$PA_DIR' && PATH=/usr/local/bin:/usr/bin:/bin bash $PA_SELF run >'$HOME_AU/last-run.txt' 2>&1 $CRON_MARK $PA_DIR"
		{ crontab -l 2>/dev/null | grep -vF "$CRON_MARK $PA_DIR"; echo "$line"; } | crontab - || return 1
		echo "on: your crontab runs it hourly at :$(printf '%02d' "$minute")"
	fi
	echo "now run: ./$PA_SELF run"
}

disable() {
	local units=${XDG_CONFIG_HOME:-$HOME/.config}/systemd/user
	if crontab -l 2>/dev/null | grep -qF "$CRON_MARK $PA_DIR"; then
		crontab -l 2>/dev/null | grep -vF "$CRON_MARK $PA_DIR" | crontab -
	fi
	if [ -f "$units/pagerarchive-client-auto-upgrade.timer" ] && grep -q "$PA_DIR" "$units/pagerarchive-client-auto-upgrade.service" 2>/dev/null; then
		systemctl --user disable --now pagerarchive-client-auto-upgrade.timer >/dev/null 2>&1
		rm -f "$units/pagerarchive-client-auto-upgrade.timer" "$units/pagerarchive-client-auto-upgrade.service"
		systemctl --user daemon-reload 2>/dev/null
	fi
	if [ -x "${JQ:-}" ]; then
		status '.enabled = false | .disabled_at = ($t|tonumber)' --arg t "$(now)"
		RUNNING=$(running_version)
		is_release "$RUNNING" || RUNNING=""
		report >/dev/null 2>&1 || true
	fi
	FROM="" TO="" event info check off "switched off"
	echo "off: no more checks. Upgrade by hand as docs/client.md says."
}

main() {
	PA_DIR=${AUTO_UPGRADE_INSTALL_DIR:-$(cd "$(dirname "$0")" && pwd)}
	[ -f "$PA_DIR/compose.yaml" ] || {
		echo "client-auto-upgrade: no compose.yaml in $PA_DIR" >&2
		return 2
	}
	load_settings
	unset AU_RUN_START
	mkdir -p "$HOME_AU"
	local cmd=${1:-}
	[ $# -eq 0 ] || shift
	case $cmd in
	retry)
		is_release "${1#v}" || {
			echo "usage: $PA_SELF retry X.Y.Z" >&2
			return 2
		}
		;;
	verify)
		[ -z "${1:-}" ] || is_tag "$1" || {
			echo "usage: $PA_SELF verify [vX.Y.Z]" >&2
			return 2
		}
		;;
	esac
	case $cmd in
	run | verify | retry | clear | status | enable | disable) ;;
	*)
		sed -n '2,17p' "$0" | sed 's/^# \{0,1\}//' >&2
		return 2
		;;
	esac
	for t in docker curl flock sha256sum; do
		command -v "$t" >/dev/null || {
			echo "client-auto-upgrade: needs $t" >&2
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
		echo "client-auto-upgrade: another run is going" >&2
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
			echo "client automatic upgrades are off: ./$PA_SELF enable" >&2
			return 1
		fi
		host_locked_run
		;;
	status) "$JQ" . "$STATUS" 2>/dev/null || echo "no status yet: client automatic upgrades have never run here" ;;
	retry)
		status '.failed_versions -= [$v]' --arg v "${1#v}"
		echo "${1#v} will be tried at the next run"
		;;
	clear)
		status "$DONE"' | if .last_check_result == "interrupted" then .last_check_result = "cleared" else . end
			| if .last_failure then .last_failure.cleared_at = ($t|tonumber) else . end' --arg t "$(now)"
		report >/dev/null 2>&1 || true
		echo "cleared: the next run checks again. Check the client first: docs/client.md, \"Automatic upgrades\""
		;;
	verify)
		local tag=${1:-}
		# Any full release: the newest, unless one is named.
		SERVER_VERSION=999999999.999999999.999999999
		pick_release 0 >/dev/null 2>&1 || {
			echo "couldn't read the releases from GitHub" >&2
			return 1
		}
		[ -n "$tag" ] || tag=$NEWEST_TAG
		REASON=""
		if verify "$tag"; then
			echo "verified: $tag's files ($FILES) and image ($IMAGES) were signed by $PA_REPO's ci.yml on main, on a GitHub-hosted runner, from $tag's commit $SOURCE_DIGEST"
		else
			echo "NOT verified: $tag: $REASON" >&2
			return 1
		fi
		;;
	esac
}

# Sourced (by the root package's tests, R84k), it only defines the functions.
if [[ ${BASH_SOURCE[0]} == "$0" ]]; then
	main "$@"
	exit $?
fi
