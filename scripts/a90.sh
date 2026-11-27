#!/bin/sh
# A90, automatic upgrades (SPEC.md R84d-R84h, ADR 0030). A folder made from the
# latest release's attached files, as A77's, with this checkout's
# deploy/auto-upgrade added as the next release's archive will bring it, and
# switched on (with no schedule: the test runs it). GitHub is stood in for by
# scripts/test-releases (the releases API, the attestations API and the files)
# and the GitHub CLI by scripts/test-releases/gh, as a release built here has
# no real attestation. Releases built from this checkout go to a throwaway
# registry, as A77's do:
#
#   N   the next minor version: this checkout
#   B   the one after: this checkout with an extra migration, whose upgrade
#       will fail its health check (no page arrives after the restart)
#   P   a pre-release, and D a draft, both higher: never installed
#
# The steps: N's archive unattested, and a GitHub CLI that can't run, are each
# refused; with everything attested, N is installed while pages arrive, and P
# and D are ignored; B is installed with pages arriving before but none after,
# so it's rolled back to N with the database restored from the pre-upgrade
# backup (B's migration gone, every page there); then B isn't tried again.
# The log has a line per step and the API's metrics have the result. Last,
# the real pinned GitHub CLI verifies the real latest release (auto-upgrade.sh
# verify), so the stand-in isn't the only thing that ever verified.
#
# A153 runs in the same stack: a receiving client on its own "host" (its own
# folder and compose project, this checkout's client files, the previous
# release's client image) with its automatic upgrades switched on. With the
# server still on the previous release, which can't say its version, nothing
# is installed; with the server on N, N's client (published first unable to
# start) fails its health check and is rolled back; republished working and
# retried, it's installed; with B out but the server on N, the client stays
# on N. The server's metrics have the client's version and each run.
#
# usage: scripts/a90.sh [-o OUT]
# needs: Docker with compose, Go, curl. Uses ports 8080, 9000, 15000, 15080.
set -eu
cd "$(dirname "$0")/.."
REPO=$PWD

OUT=a90-results
while getopts o: opt; do
	case $opt in
	o) OUT=$OPTARG ;; *) exit 2 ;;
	esac
done
mkdir -p "$OUT"
OUT=$(cd "$OUT" && pwd)

export COMPOSE_PROJECT_NAME="pa-a90-$$"
export ALLOWED_EMAIL_DOMAINS=example.com INITIAL_ADMIN_EMAIL=owner@example.org
. ./scripts/stack-lib.sh
. ./scripts/install-lib.sh
. ./scripts/ci/versions-lib.sh
IMAGE_TAG=$(compose_image_tag)
WORK=$(mktemp -d)
DIR=$WORK/deployment
REL=$WORK/releases
RELEASES_ADDR=127.0.0.1:15080
SERVER=""
POSTER=""
stack_compose() { in_folder "$DIR" "$@"; }
# A153: the receiving client's own folder and compose project, on its "host".
CDIR=$WORK/client
CLIENT_PROJECT="pa-a153-$$"
client_compose() { (cd "$CDIR" && COMPOSE_PROJECT_NAME="$CLIENT_PROJECT" docker compose "$@"); }

cleanup() {
	[ -z "$POSTER" ] || kill "$POSTER" 2>/dev/null || true
	[ -z "$SERVER" ] || kill "$SERVER" 2>/dev/null || true
	stack_compose logs --no-color api db backup >"$OUT/a90-logs.txt" 2>&1 || true
	cp "$DIR/auto-upgrade/status/auto-upgrade.log" "$OUT/auto-upgrade.log" 2>/dev/null || true
	cp "$DIR/auto-upgrade/status/status.json" "$OUT/status.json" 2>/dev/null || true
	cp "$CDIR/auto-upgrade/status/auto-upgrade.log" "$OUT/client-auto-upgrade.log" 2>/dev/null || true
	cp "$CDIR/auto-upgrade/status/status.json" "$OUT/client-status.json" 2>/dev/null || true
	[ ! -d "$CDIR" ] || client_compose down -v --remove-orphans >/dev/null 2>&1 || true
	docker rmi -f "pa-a153-client:${N:-none}" "pa-a153-client-bad:${N:-none}" >/dev/null 2>&1 || true
	stack_compose down -v --remove-orphans >/dev/null 2>&1 || true
	registry_stop
	rm -rf "$WORK" 2>/dev/null || true
}
trap cleanup EXIT

fail() {
	echo "FAIL: $*" >&2
	status=1
}

latest_release() {
	curl -fsS -o /dev/null -w '%{redirect_url}' https://github.com/RIKIKU/PagerArchive/releases/latest |
		sed -n 's#^https://github.com/RIKIKU/PagerArchive/releases/tag/\(v[0-9.]*\)$#\1#p'
}

# ── The previous release, installed as a deployment has it ──────────────────
previous=$(latest_release)
[ -n "$previous" ] || { echo "FAIL: couldn't find the release marked Latest on GitHub" >&2; exit 1; }
PREV=${previous#v}
major=${PREV%%.*} minor=${PREV#*.}
minor=${minor%%.*}
N=$major.$((minor + 1)).0 B=$major.$((minor + 2)).0 P=$major.$((minor + 3)).0 D=$major.$((minor + 4)).0
echo "=== A90: $PREV installed; then N $N and B $B built here, pre-release $P and draft $D"

registry_start
mkdir -p "$WORK/previous"
for asset in $(sh scripts/ci/release-assets.sh --list-server); do
	curl -fsSL -o "$WORK/previous/$asset" "https://github.com/RIKIKU/PagerArchive/releases/download/$previous/$asset"
done
install_folder "$DIR" "$WORK/previous"
sh scripts/ci/pull-images.sh "$DIR/compose.yaml" "ghcr.io/rikiku/pagerarchive:$(compose_image_tag "$DIR/compose.yaml")"
# What the next release's archive brings: the script.
mkdir -p "$DIR/deploy/auto-upgrade"
cp deploy/auto-upgrade/auto-upgrade.sh "$DIR/deploy/auto-upgrade/"
fill_env "$DIR/.env" deploy/test/stack.vars
cat >"$WORK/vars" <<VARS
ALLOWED_EMAIL_DOMAINS=$ALLOWED_EMAIL_DOMAINS
INITIAL_ADMIN_EMAIL=$INITIAL_ADMIN_EMAIL
BACKUP_DIR=./backups
BACKUP_UID=$(id -u)
BACKUP_GID=$(id -g)
VARS
fill_env "$DIR/.env" "$WORK/vars"
# As an install from before the API's own role (R84i), as A77's: the previous
# release's .env has neither setting, and the upgrade keeps the superuser.
sed -i -E '/^PAGERARCHIVE_DB_(USER|PASSWORD)=/d' "$DIR/.env"
chmod -R a+rX "$WORK"
stack_compose up -d
stack_wait_ready
echo "--- running: $(stack_compose exec -T api /usr/local/bin/pagerarchive version)"
# Backups refuse a database with no tables in it, so Authentik must have set up.
key=$(stack_compose exec -T api /usr/local/bin/pagerarchive client add a90-receiver "A90 receiver" | tail -n 1)

page() {
	curl -fsS -o /dev/null -X POST http://127.0.0.1:8080/api/v1/pages -H "Authorization: Bearer $key" -H 'Content-Type: application/json' \
		-d "{\"client_id\":\"a90-receiver\",\"capcode\":\"0100001\",\"text\":\"A90 PAGE $1 $(date +%s%N)\",\"received_at\":\"$(date -u +%Y-%m-%dT%H:%M:%S.%NZ)\",\"baud\":1200}"
}
pages() { # pages <n>: n pages, now
	_i=0
	while [ $_i -lt "$1" ]; do
		page "$_i" || true
		_i=$((_i + 1))
	done
}
poster_start() { # a page every 10 s, as a busy network sends them
	(while :; do
		page live || true
		sleep 10
	done) &
	POSTER=$!
}
poster_stop() {
	[ -z "$POSTER" ] || kill "$POSTER" 2>/dev/null || true
	POSTER=""
}
q() { stack_compose exec -T db psql -X -q -At -U postgres -d postgres -c "$1"; }
version_now() { stack_compose exec -T api /usr/local/bin/pagerarchive version | sed 's/^pagerarchive //'; }

# ── The stand-in GitHub ────────────────────────────────────────────────────
mkdir -p "$REL/files"
: >"$REL/attested"
go build -o "$WORK/test-releases" ./scripts/test-releases
"$WORK/test-releases" -dir "$REL" -addr "$RELEASES_ADDR" >"$OUT/test-releases.log" 2>&1 &
SERVER=$!

# release <version> <source folder>: its files under files/v<version>/, and its
# image in the registry by its version and its minor version. Prints the
# image's digest.
release() {
	_v=$1 _src=$2
	echo "$_v" >"$_src/VERSION"
	sed -i "s#ghcr.io/rikiku/pagerarchive:$IMAGE_TAG#ghcr.io/rikiku/pagerarchive:${_v%.*}#" "$_src/compose.yaml"
	sed -i -E "s#(image: ghcr\.io/rikiku/pagerarchive-client:).*#\1$_v#" "$_src/deploy/client/compose.yaml"
	sh "$_src/scripts/ci/release-assets.sh" "$REL/files/v$_v" >/dev/null
	use_registry "$REL/files/v$_v/compose.yaml"
	use_registry "$REL/files/v$_v/client-compose.yaml"
	docker build -q -t "$REGISTRY/rikiku/pagerarchive:$_v" -t "$REGISTRY/rikiku/pagerarchive:${_v%.*}" "$_src" >/dev/null
	docker push -q "$REGISTRY/rikiku/pagerarchive:$_v" >/dev/null
	docker push -q "$REGISTRY/rikiku/pagerarchive:${_v%.*}" >/dev/null
	_d=$(docker image inspect -f '{{range .RepoDigests}}{{println .}}{{end}}' "$REGISTRY/rikiku/pagerarchive:$_v" | head -n 1)
	docker rmi -f "$REGISTRY/rikiku/pagerarchive:$_v" "$REGISTRY/rikiku/pagerarchive:${_v%.*}" >/dev/null
	echo "${_d#*@}"
}
attest() { # attest <version> <file>...: the files' digests have attestations
	_v=$1
	shift
	for _f in "$@"; do
		echo "sha256:$(sha256sum "$REL/files/v$_v/$_f" | cut -d' ' -f1)" >>"$REL/attested"
	done
}
copy_checkout() {
	mkdir -p "$1"
	tar -cf - --exclude=.git --exclude=web/node_modules --exclude=web/test-results --exclude=web/playwright-report \
		--exclude=a77-results --exclude=a90-results . | (cd "$1" && tar -xf -)
}
# releases_json <version:kind>...: kind full, pre or draft; a full one built
# here lists its files.
releases_json() {
	_sep="" _id=1
	echo "["
	for _r in "$@"; do
		_v=${_r%%:*} _kind=${_r#*:} _assets=""
		if [ -d "$REL/files/v$_v" ]; then
			_asep=""
			for _f in $(ls "$REL/files/v$_v"); do
				_assets="$_assets$_asep{\"name\":\"$_f\",\"browser_download_url\":\"http://$RELEASES_ADDR/files/v$_v/$_f\"}"
				_asep=","
			done
		fi
		printf '%s{"id":%d,"tag_name":"v%s","draft":%s,"prerelease":%s,"body":"## What'"'"'s changed\\n- things","assets":[%s]}\n' \
			"$_sep" "$_id" "$_v" "$([ "$_kind" = draft ] && echo true || echo false)" "$([ "$_kind" = pre ] && echo true || echo false)" "$_assets"
		_sep="," _id=$((_id + 1))
	done
	echo "]"
}

# release_client <version> <source folder>: A153's client image for a
# release, built from the folder release() stamped. It's kept here as
# pa-a153-client:<version>; what's published first, and printed (its digest),
# is one that can't start: it prints its version and exits, so a client host
# that installs it restarts it over and over.
release_client() {
	_v=$1 _src=$2
	docker build -q -f "$_src/Dockerfile.client" -t "pa-a153-client:$_v" "$_src" >/dev/null
	printf 'FROM pa-a153-client:%s\nENTRYPOINT ["/usr/local/bin/pagerarchive-client", "version"]\n' "$_v" |
		docker build -q -t "pa-a153-client-bad:$_v" - >/dev/null
	docker tag "pa-a153-client-bad:$_v" "$REGISTRY/rikiku/pagerarchive-client:$_v"
	docker push -q "$REGISTRY/rikiku/pagerarchive-client:$_v" >/dev/null
	_d=$(docker image inspect -f '{{range .RepoDigests}}{{println .}}{{end}}' "$REGISTRY/rikiku/pagerarchive-client:$_v" | head -n 1)
	docker rmi -f "$REGISTRY/rikiku/pagerarchive-client:$_v" >/dev/null
	echo "${_d#*@}"
}

echo "--- building N ($N), B ($B, with an extra migration) and P ($P), and N's client"
copy_checkout "$WORK/n-src"
N_DIGEST=$(release "$N" "$WORK/n-src")
N_CLIENT_BAD=$(release_client "$N" "$WORK/n-src")
copy_checkout "$WORK/b-src"
cat >"$WORK/b-src/internal/store/migrations/99999_a90_bad.sql" <<'SQL'
-- +goose Up
CREATE TABLE a90_bad (id int);
SQL
B_DIGEST=$(release "$B" "$WORK/b-src")
copy_checkout "$WORK/p-src"
P_DIGEST=$(release "$P" "$WORK/p-src")
attest "$P" compose.yaml pagerarchive-config.tar.gz
echo "$P_DIGEST" >>"$REL/attested"
releases_json "$D:draft" "$P:pre" "$N:full" "$PREV:full" >"$REL/releases.json"

# ── Switched on ────────────────────────────────────────────────────────────
au() {
	(cd "$DIR" && env -i PATH="$PATH" HOME="$HOME" COMPOSE_PROJECT_NAME="$COMPOSE_PROJECT_NAME" ${DOCKER_HOST:+DOCKER_HOST="$DOCKER_HOST"} \
		AUTO_UPGRADE_API="http://$RELEASES_ADDR/repos/RIKIKU/PagerArchive" AUTO_UPGRADE_GH="${GH:-$REPO/scripts/test-releases/gh}" \
		AUTO_UPGRADE_IMAGE_PREFIX="$REGISTRY/rikiku/" AUTO_UPGRADE_PAGE_WINDOW=2 AUTO_UPGRADE_START_TIMEOUT=300 \
		bash deploy/auto-upgrade/auto-upgrade.sh "$@")
}
log_has() { grep -q "\"step\":\"$1\",\"result\":\"$2\"" "$DIR/auto-upgrade/status/auto-upgrade.log"; }
st() { "$DIR/auto-upgrade/bin/jq-1.8.2" -r "$1" "$DIR/auto-upgrade/status/status.json"; }
# A153: the client host's automatic upgrades, as its cron line runs them.
cau() {
	(cd "$CDIR" && env -i PATH="$PATH" HOME="$HOME" COMPOSE_PROJECT_NAME="$CLIENT_PROJECT" ${DOCKER_HOST:+DOCKER_HOST="$DOCKER_HOST"} \
		AUTO_UPGRADE_API="http://$RELEASES_ADDR/repos/RIKIKU/PagerArchive" AUTO_UPGRADE_GH="$REPO/scripts/test-releases/gh" \
		AUTO_UPGRADE_IMAGE_PREFIX="$REGISTRY/rikiku/" AUTO_UPGRADE_START_TIMEOUT=60 AUTO_UPGRADE_HEALTH_WAIT=20 \
		bash client-auto-upgrade.sh "$@")
}
clog_has() { grep -q "\"step\":\"$1\",\"result\":\"$2\"" "$CDIR/auto-upgrade/status/auto-upgrade.log"; }
cst() { "$CDIR/auto-upgrade/bin/jq-1.8.2" -r "$1" "$CDIR/auto-upgrade/status/status.json"; }
status=0

echo "=== A90: switched on"
au enable --no-schedule
grep -q '^AUTO_UPGRADE_STATUS_DIR=./auto-upgrade/status$' "$DIR/.env" || fail "enable didn't set AUTO_UPGRADE_STATUS_DIR"
stack_compose up -d
stack_wait_ready

# ── A153: a receiving client on its own host ───────────────────────────────
# The documented install (docs/client.md): this checkout's client files, as
# the next release attaches them, in a folder of their own, running the
# previous release's client image (from the registry, as GHCR has it). The
# test's own override only lets it run here: the host's network (to reach the
# API on 127.0.0.1), stdin held open (a client reading stdin, no radio) and no
# dongle.
echo "=== A153: a client at $PREV on its own host, its automatic upgrades switched on"
docker pull -q "ghcr.io/rikiku/pagerarchive-client:$PREV" >/dev/null
docker tag "ghcr.io/rikiku/pagerarchive-client:$PREV" "$REGISTRY/rikiku/pagerarchive-client:$PREV"
docker push -q "$REGISTRY/rikiku/pagerarchive-client:$PREV" >/dev/null
docker rmi -f "$REGISTRY/rikiku/pagerarchive-client:$PREV" >/dev/null
mkdir -p "$CDIR"
cp deploy/client/compose.yaml "$CDIR/compose.yaml"
sed -i -E "s#^( *image: ).*pagerarchive-client:.*#\1$REGISTRY/rikiku/pagerarchive-client:$PREV#" "$CDIR/compose.yaml"
cp deploy/client/.env.example "$CDIR/.env"
cp deploy/client/auto-upgrade.sh "$CDIR/client-auto-upgrade.sh"
cat >"$WORK/client-vars" <<VARS
SERVER_URL=http://127.0.0.1:8080
CLIENT_ID=a153-receiver
RADIO_FREQUENCY=
ALLOW_PLAIN_HTTP=true
VARS
fill_env "$CDIR/.env" "$WORK/client-vars"
cat >"$CDIR/compose.override.yaml" <<'YAML'
services:
  client:
    network_mode: host
    stdin_open: true
    devices: !reset []
YAML
stack_compose exec -T api /usr/local/bin/pagerarchive client add a153-receiver "A153 receiver" | tail -n 1 >"$CDIR/key"
chmod 600 "$CDIR/key"
client_compose up -d
client_version_now() { client_compose exec -T client /usr/local/bin/pagerarchive-client --version 2>/dev/null | sed 's/^pagerarchive-client //' | tr -d '\r'; }
_i=0
until [ "$(client_version_now)" = "$PREV" ] || [ $_i -gt 30 ]; do
	sleep 2
	_i=$((_i + 1))
done
[ "$(client_version_now)" = "$PREV" ] || fail "the client didn't start at $PREV"
cau enable --no-schedule || fail "the client's automatic upgrades didn't switch on"
[ "$(cst .enabled)" = true ] || fail "the client's status isn't enabled"

echo "=== A153: the server runs $PREV, from before client upgrades: the client isn't upgraded"
if cau run; then fail "a client was upgraded, or checked, without its server's version"; fi
[ "$(cst .last_check_result)" = error ] || fail "the client's check says $(cst .last_check_result)"
[ "$(client_version_now)" = "$PREV" ] || fail "the client is $(client_version_now) with a server that can't say its version"
cst .last_check_note

echo "=== A90: verification fails: refused"
attest "$N" compose.yaml
pages 25
if au run; then fail "a release whose archive has no attestation was installed"; fi
log_has verify refused || fail "no 'verify refused' in the log"
[ "$(version_now)" = "$PREV" ] || fail "the API is $(version_now) after a refusal"
tail -n 1 "$DIR/auto-upgrade/status/auto-upgrade.log"

echo "=== A90: verification can't run: refused"
if GH=/nonexistent/gh au run; then fail "an upgrade went ahead with no GitHub CLI"; fi
[ "$(version_now)" = "$PREV" ] || fail "the API is $(version_now) with no GitHub CLI"
tail -n 1 "$DIR/auto-upgrade/status/auto-upgrade.log"

echo "=== A90: N attested: installed while pages arrive; P and D ignored"
attest "$N" pagerarchive-config.tar.gz monitoring-compose.yaml client-compose.yaml client-auto-upgrade.sh
echo "$N_DIGEST" >>"$REL/attested"
poster_start
before=$(q "SELECT count(*) FROM messages")
# While it runs, the status as the API reads it, twice a second: a run in
# progress has its start and, once it knows, N as its target (R84h).
(while :; do st '[.state, .running_since, (.target_version // ""), .last_check_at] | @tsv' 2>/dev/null || true; sleep 0.5; done) >"$WORK/run-states.tsv" &
WATCHER=$!
au run || fail "the upgrade to $N failed"
kill "$WATCHER" 2>/dev/null || true
wait "$WATCHER" 2>/dev/null || true
backing=$(awk -F '\t' -v n="$N" '$1 == "backing-up" && $2 > 0 && $3 == n && $4 == $2 { print; exit }' "$WORK/run-states.tsv")
[ -n "$backing" ] || fail "the status never had the run backing up with its start and $N as its target: $(sort -u "$WORK/run-states.tsv" | tr '\t\n' ' ;')"
echo "--- while backing up: $backing"
[ "$(st .last_check_at)" = "$(printf '%s' "$backing" | cut -f2)" ] || fail "the last check ($(st .last_check_at)) isn't the run's start"
[ "$(st .running_since)" = 0 ] && [ -z "$(st '.target_version // empty')" ] || fail "the run's start or target is still there after it ended"
[ "$(version_now)" = "$N" ] || fail "the API is $(version_now), not $N"
for step in "check newer" "verify ok" "backup ok" "upgrade ok" "health ok"; do
	log_has $step || fail "no '$step' in the log"
done
[ "$(st .last_upgrade.to)" = "$N" ] && [ "$(st .last_check_result)" = upgraded ] || fail "the status says $(st .last_check_result) to $(st .last_upgrade.to)"
grep '"step":"health","result":"ok"' "$DIR/auto-upgrade/status/auto-upgrade.log" | tail -n 1
after=$(q "SELECT count(*) FROM messages")
[ "$after" -gt "$before" ] || fail "no page stored after the upgrade ($before then $after)"
metrics=$(curl -fsS -H "Authorization: Bearer $METRICS_TOKEN" http://127.0.0.1:8080/metrics)
printf '%s\n' "$metrics" | grep -q "pagerarchive_auto_upgrade_last_success_timestamp_seconds{from=\"$PREV\",to=\"$N\"}" ||
	fail "the API's metrics don't have the upgrade: $(printf '%s\n' "$metrics" | grep auto_upgrade)"
printf '%s\n' "$metrics" | grep -q '^pagerarchive_auto_upgrade_failing 0$' || fail "the API says failing after a good upgrade"
ls "$DIR/auto-upgrade/files/" | grep -q -- "-$PREV-to-$N$" || fail "no copy of the files from before"
stack_compose exec -T backup sh -c 'ls /backups/pre-upgrade/*/' | grep -q 'pagerarchive.dump' || fail "no pre-upgrade backup kept aside"
au run || fail "the run after the upgrade failed"
[ "$(st .last_check_result)" = up-to-date ] || fail "after the upgrade the check says $(st .last_check_result), not up-to-date: P or D was taken"
poster_stop

echo "=== A153: the server runs $N; the client's $N fails its health check: rolled back to $PREV"
# N's client image, as published, can't start (it exits at once), and is attested.
echo "$N_CLIENT_BAD" >>"$REL/attested"
started=$(date +%s)
if cau run; then fail "the client's upgrade to a client that can't start passed"; fi
echo "--- the run took $(($(date +%s) - started)) s"
[ "$(client_version_now)" = "$PREV" ] || fail "the client is $(client_version_now) after the rollback, not $PREV"
for step in "check newer" "verify ok" "backup ok" "upgrade ok" "health failed" "rollback started" "rollback ok"; do
	clog_has $step || fail "no '$step' in the client's log"
done
grep -E '"step":"(health|rollback)"' "$CDIR/auto-upgrade/status/auto-upgrade.log" | tail -n 3
[ "$(cst .last_failure.to)" = "$N" ] && [ "$(cst .last_failure.rolled_back)" = true ] || fail "the client's last failure: $(cst .last_failure)"
[ "$(cst '.failed_versions | index("'"$N"'")')" != null ] || fail "$N isn't marked failed on the client"
grep -q "pagerarchive-client:$PREV\$" "$CDIR/compose.yaml" || fail "the client's compose.yaml isn't $PREV's after the rollback"
metrics=$(curl -fsS -H "Authorization: Bearer $METRICS_TOKEN" http://127.0.0.1:8080/metrics)
for want in "pagerarchive_client_auto_upgrade_failing{client=\"a153-receiver\"} 1" "pagerarchive_client_version_info{client=\"a153-receiver\",version=\"$PREV\"} 1" \
	"pagerarchive_client_auto_upgrade_last_check_result{client=\"a153-receiver\",result=\"rolled-back\"} 1" "pagerarchive_client_behind_since_timestamp_seconds{client=\"a153-receiver\"}"; do
	printf '%s\n' "$metrics" | grep -qF "$want" || fail "the server's metrics after the client's rollback: no $want"
done
cau run || fail "the client's run after its rollback failed"
[ "$(cst .last_check_result)" = skipped ] && [ "$(client_version_now)" = "$PREV" ] || fail "$N was tried again on the client: $(cst .last_check_result)"

echo "=== A153: $N's client republished, working: retry, and the client is upgraded to $N"
docker tag "pa-a153-client:$N" "$REGISTRY/rikiku/pagerarchive-client:$N"
docker push -q "$REGISTRY/rikiku/pagerarchive-client:$N" >/dev/null
N_CLIENT=$(docker image inspect -f '{{range .RepoDigests}}{{println .}}{{end}}' "$REGISTRY/rikiku/pagerarchive-client:$N" | head -n 1)
docker rmi -f "$REGISTRY/rikiku/pagerarchive-client:$N" >/dev/null
echo "${N_CLIENT#*@}" >>"$REL/attested"
cau retry "$N"
# While it runs, the server's metrics, as the dashboard reads them: a run in
# progress, its step and its target (R84p).
(while :; do
	curl -fsS -H "Authorization: Bearer $METRICS_TOKEN" http://127.0.0.1:8080/metrics 2>/dev/null |
		grep -E '^pagerarchive_client_auto_upgrade_(run|target_info)\{client="a153-receiver"' || true
	sleep 0.5
done) >"$WORK/client-run-metrics.txt" &
WATCHER=$!
cau run || fail "the client's upgrade to $N failed"
kill "$WATCHER" 2>/dev/null || true
wait "$WATCHER" 2>/dev/null || true
[ "$(client_version_now)" = "$N" ] || fail "the client is $(client_version_now), not $N"
[ "$(cst .last_upgrade.to)" = "$N" ] && [ "$(cst .last_check_result)" = upgraded ] || fail "the client's status says $(cst .last_check_result) to $(cst .last_upgrade.to)"
grep -q "pagerarchive-client:$N\$" "$CDIR/compose.yaml" || fail "the client's compose.yaml isn't $N's"
cmp -s "$CDIR/client-auto-upgrade.sh" "$REL/files/v$N/client-auto-upgrade.sh" || fail "the client's script isn't $N's"
grep -q "step=\"health-check\"" "$WORK/client-run-metrics.txt" && grep -q "to=\"$N\"" "$WORK/client-run-metrics.txt" ||
	fail "the server's metrics never had the client's run going, with $N as its target: $(sort -u "$WORK/client-run-metrics.txt" | tr '\n' ' ')"
echo "--- during the run: $(sort -u "$WORK/client-run-metrics.txt" | tr '\n' ' ')"
metrics=$(curl -fsS -H "Authorization: Bearer $METRICS_TOKEN" http://127.0.0.1:8080/metrics)
for want in "pagerarchive_client_auto_upgrade_failing{client=\"a153-receiver\"} 0" "pagerarchive_client_version_info{client=\"a153-receiver\",version=\"$N\"} 1" \
	"pagerarchive_client_auto_upgrade_last_success_timestamp_seconds{client=\"a153-receiver\",from=\"$PREV\",to=\"$N\"}"; do
	printf '%s\n' "$metrics" | grep -qF "$want" || fail "the server's metrics after the client's upgrade: no $want"
done
for not in 'pagerarchive_client_behind_since_timestamp_seconds{client="a153-receiver"}' 'pagerarchive_client_auto_upgrade_run{client="a153-receiver"'; do
	if printf '%s\n' "$metrics" | grep -qF "$not"; then fail "the server's metrics after the client's upgrade still have $not"; fi
done

echo "=== A90: B fails its health check: rolled back, the database restored"
attest "$B" compose.yaml pagerarchive-config.tar.gz monitoring-compose.yaml
echo "$B_DIGEST" >>"$REL/attested"
releases_json "$D:draft" "$P:pre" "$B:full" "$N:full" "$PREV:full" >"$REL/releases.json"
goose_before=$(q "SELECT max(version_id) FROM goose_db_version WHERE is_applied")
pages 25
count_before=$(q "SELECT count(*) FROM messages")
started=$(date +%s)
if au run; then fail "B's upgrade passed with no page after the restart"; fi
echo "--- the run took $(($(date +%s) - started)) s"
[ "$(version_now)" = "$N" ] || fail "the API is $(version_now) after the rollback, not $N"
for step in "health failed" "rollback started" "restore ok" "rollback ok"; do
	log_has $step || fail "no '$step' in the log"
done
grep -E '"step":"(health|restore|rollback)"' "$DIR/auto-upgrade/status/auto-upgrade.log" | tail -n 4
[ "$(q "SELECT to_regclass('public.a90_bad') IS NULL")" = t ] || fail "B's migration's table is still there: the database wasn't restored"
[ "$(q "SELECT max(version_id) FROM goose_db_version WHERE is_applied")" = "$goose_before" ] || fail "the migrations aren't N's after the restore"
[ "$(q "SELECT count(*) FROM messages")" = "$count_before" ] || fail "pages from before the upgrade are missing after the restore"
[ "$(st .last_failure.to)" = "$B" ] && [ "$(st .last_failure.rolled_back)" = true ] && [ "$(st .last_failure.restored)" = true ] ||
	fail "the status's last failure: $(st .last_failure)"
[ "$(st '.failed_versions | index("'"$B"'")')" != null ] || fail "B isn't marked failed"
metrics=$(curl -fsS -H "Authorization: Bearer $METRICS_TOKEN" http://127.0.0.1:8080/metrics)
for want in '^pagerarchive_auto_upgrade_failing 1$' '^pagerarchive_auto_upgrade_rollbacks_total 1$' '^pagerarchive_auto_upgrade_failures_total 1$'; do
	printf '%s\n' "$metrics" | grep -q "$want" || fail "metrics after the rollback: no $want"
done
page after-rollback || fail "no page could be posted after the rollback"

echo "=== A90: B isn't tried again"
au run || fail "the run after the rollback failed"
[ "$(version_now)" = "$N" ] || fail "B was tried again"
[ "$(st .last_check_result)" = skipped ] || fail "the check says $(st .last_check_result)"
st .last_check_note

echo "=== A153: $B is out, but the server runs $N: the client stays at $N"
cau run || fail "the client's check with $B out failed"
[ "$(client_version_now)" = "$N" ] || fail "the client is $(client_version_now): it went past its server"
[ "$(cst .last_check_result)" = up-to-date ] || fail "the client's check says $(cst .last_check_result)"
cst .last_check_note | grep -q "$B is out: the client waits for its server ($N)" || fail "the client's note: $(cst .last_check_note)"
cst .last_check_note

echo "=== A90: no page text in the log or the status"
if grep -q 'A90 PAGE' "$DIR/auto-upgrade/status/auto-upgrade.log" "$DIR/auto-upgrade/status/status.json" \
	"$CDIR/auto-upgrade/status/auto-upgrade.log" "$CDIR/auto-upgrade/status/status.json"; then fail "page text in the log or status"; fi

echo "=== A90: the real GitHub CLI verifies the real latest release, $previous"
(cd "$DIR" && env -i PATH="$PATH" HOME="$HOME" COMPOSE_PROJECT_NAME="$COMPOSE_PROJECT_NAME" ${AUTO_UPGRADE_GITHUB_TOKEN:+AUTO_UPGRADE_GITHUB_TOKEN="$AUTO_UPGRADE_GITHUB_TOKEN"} \
	bash deploy/auto-upgrade/auto-upgrade.sh verify "$previous") || fail "the real release didn't verify"

if [ "$status" -eq 0 ]; then echo "=== A90 passed"; else echo "=== A90 FAILED"; fi
exit $status
