#!/bin/sh
# A77, upgrade from the previous release (SPEC.md R84a, ADR 0019). A folder made
# from the previous release's attached files, started, and given made-up users,
# aliases, templates with icons, integrations and pages (internal/stacktest).
# Then this checkout's compose.yaml and archive are put in the folder, and
# `docker compose pull && docker compose up -d` is all that's run. The checks
# then find everything still there, the migrations run and a new page stored,
# found and delivered.
#
# The previous release is the one GitHub marks Latest, which the Release
# workflow does last, after the images and files are published: the release
# deployments run. So every pull request and every build of main upgrades
# from it, and a change that breaks the upgrade is caught before a release is
# cut (ADR 0024). One from before M17 has nothing attached (A77 starts with
# the release after M17's), so a build that has only those makes a release of
# its own to start from, in the throwaway registry: this checkout's code with
# a lower VERSION, without its newest migration, and with a changed Authentik
# file.
#
# This checkout's image goes to a throwaway registry on this machine, never to
# GHCR. Before the upgrade the metrics role is dropped, so the API has to
# create it again: nothing may be left for a hand step.
#
# Then deploy/postgres/api-role.sh switches the upgraded install, which still
# connects as the superuser with a warning, to the API's own role (R84i), and
# the checks run again: the role is all the API connects as, it can't reach
# Authentik's database or run a program, and nothing is lost. A second run
# changes nothing.
#
# Last comes a patch release (R84b): a newer image under the same tag, with
# nothing in the folder changed, and pull and up again.
#
# usage: scripts/a77.sh [-o OUT]
# needs: Docker with compose, Go, curl, git with the v* tags (for a real previous
# release). Uses ports 8080, 9000, 8025, 14323 and 15000.
set -eu
cd "$(dirname "$0")/.."

OUT=a77-results
while getopts o: opt; do
	case $opt in
	o) OUT=$OPTARG ;; *) exit 2 ;;
	esac
done
mkdir -p "$OUT"
OUT=$(cd "$OUT" && pwd)

export COMPOSE_PROJECT_NAME="pa-a77-$$"
# A23's first admin and allowed domain, which internal/stacktest signs in with.
export ALLOWED_EMAIL_DOMAINS=example.com INITIAL_ADMIN_EMAIL=owner@example.org
. ./scripts/stack-lib.sh
. ./scripts/install-lib.sh
. ./scripts/ci/versions-lib.sh
VERSION=$(tr -d ' \r\n' <VERSION)
# The tag compose.yaml names our image by: the build's MAJOR.MINOR, or main
# in the repository's own, unstamped files (ADR 0024).
IMAGE_TAG=$(compose_image_tag)
WORK=$(mktemp -d)
DIR=$WORK/deployment
MAILPIT=pa-a77-mailpit-$$
MARKER=a77-previous-release
stack_compose() { in_folder "$DIR" "$@"; }

cleanup() {
	stack_compose logs --no-color api db >"$OUT/a77-logs.txt" 2>&1 || true
	docker rm -fv "$MAILPIT" >/dev/null 2>&1 || true
	stack_compose down -v --remove-orphans >/dev/null 2>&1 || true
	registry_stop
	rm -rf "$WORK"
}
trap cleanup EXIT

registry_start

copy_checkout() {
	mkdir -p "$1"
	tar -cf - --exclude=.git --exclude=web/node_modules --exclude=web/test-results --exclude=web/playwright-report \
		--exclude=a77-results . | (cd "$1" && tar -xf -)
}

# previous_from_release <tag>: its attached files, its images on GHCR. A
# release whose files were never attached (v0.15.0: the attach job was
# skipped) gets them made from its tag by its own release-assets.sh, which is
# what the attach job would have uploaded.
previous_from_release() {
	mkdir -p "$WORK/previous"
	url=https://github.com/RIKIKU/PagerArchive/releases/download/$1
	if curl -fsIL -o /dev/null "$url/compose.yaml"; then
		echo "=== A77: starting from $1's attached files"
		for asset in $(sh scripts/ci/release-assets.sh --list); do
			curl -fsSL -o "$WORK/previous/$asset" "$url/$asset"
		done
	else
		echo "=== A77: $1 has no attached files: making them from its tag"
		mkdir -p "$WORK/previous-tag"
		git archive "$1" | (cd "$WORK/previous-tag" && tar -xf -)
		sh "$WORK/previous-tag/scripts/ci/release-assets.sh" "$WORK/previous"
	fi
	install_folder "$DIR" "$WORK/previous"
	# Its images, from GHCR and upstream, each pull retried (ADR 0025), so
	# `up` finds them here.
	sh scripts/ci/pull-images.sh "$DIR/compose.yaml" "ghcr.io/rikiku/pagerarchive:$(compose_image_tag "$DIR/compose.yaml")"
}

# previous_from_here: a release made from this checkout, as the header says.
previous_from_here() {
	old=0.14.99
	echo "=== A77: no earlier release has attached files: starting from one made from this checkout, $old"
	src=$WORK/previous-src
	copy_checkout "$src"
	echo "$old" >"$src/VERSION"
	sed -i "s#ghcr.io/rikiku/pagerarchive:$IMAGE_TAG#ghcr.io/rikiku/pagerarchive:${old%.*}#" "$src/compose.yaml"
	newest=$(ls "$src"/internal/store/migrations/*.sql | sort | tail -n 1)
	rm "$newest"
	echo "--- without the migration $(basename "$newest")"
	# A changed file, and the label that went with it.
	echo "# $MARKER" >>"$src/deploy/authentik/blueprints/pagerarchive.yaml"
	sed -i -E 's/pagerarchive\.config: "[0-9a-f]+"/pagerarchive.config: "0000000000000001"/' "$src/compose.yaml"
	sh "$src/scripts/ci/release-assets.sh" "$WORK/previous"
	registry_publish "$src" "${old%.*}"
	install_folder "$DIR" "$WORK/previous"
	use_registry "$DIR/compose.yaml"
	MADE_HERE=1
}

# latest_release: the tag of the release GitHub marks Latest. The release
# workflow marks it only at the end, once its images and files are all
# published, so it's never one still being published (or one that failed).
latest_release() {
	curl -fsS -o /dev/null -w '%{redirect_url}' https://github.com/RIKIKU/PagerArchive/releases/latest |
		sed -n 's#^https://github.com/RIKIKU/PagerArchive/releases/tag/\(v[0-9.]*\)$#\1#p'
}

MADE_HERE=""
previous=$(latest_release || true)
[ -n "$previous" ] || { echo "FAIL: couldn't find the release marked Latest on GitHub" >&2; exit 1; }
echo "=== A77: upgrading this build, $VERSION, from the latest release, $previous"
if [ -n "$previous" ] && higher "${previous#v}" 0.14.1; then
	previous_from_release "$previous"
else
	previous_from_here
fi
fill_env "$DIR/.env" deploy/test/stack.vars
cat >"$WORK/vars" <<VARS
ALLOWED_EMAIL_DOMAINS=$ALLOWED_EMAIL_DOMAINS
INITIAL_ADMIN_EMAIL=$INITIAL_ADMIN_EMAIL
VARS
fill_env "$DIR/.env" "$WORK/vars"
# As an install from before the API's own role (R84i): it connects as the
# superuser, and its .env has neither setting.
sed -i -E '/^PAGERARCHIVE_DB_(USER|PASSWORD)=/d' "$DIR/.env"
chmod -R a+rX "$WORK"

stack_compose up -d
docker run -d --name "$MAILPIT" --network "${COMPOSE_PROJECT_NAME}_default" --network-alias mailpit \
	-e MP_SMTP_AUTH_ACCEPT_ANY=true -e MP_SMTP_AUTH_ALLOW_INSECURE=true \
	-p 127.0.0.1:8025:8025 axllent/mailpit:v1.31.5 >/dev/null
stack_wait_ready
echo "--- running: $(stack_compose exec -T api /usr/local/bin/pagerarchive version)"

if [ -n "$MADE_HERE" ] && ! stack_compose exec -T authentik-worker grep -q "$MARKER" /blueprints/pagerarchive/pagerarchive.yaml; then
	echo "FAIL: the previous release's changed file isn't in its container" >&2
	exit 1
fi

gateway=$(docker network inspect "${COMPOSE_PROJECT_NAME}_default" -f '{{(index .IPAM.Config 0).Gateway}}')
key=$(stack_compose exec -T api /usr/local/bin/pagerarchive client add a77-receiver "A77 receiver" | tail -n 1)
export PA_STACK_APP_URL=http://localhost:8080 PA_STACK_AUTHENTIK_URL=http://localhost:9000 \
	PA_STACK_MAILPIT_URL=http://localhost:8025 PA_STACK_AUTHENTIK_TOKEN="$AUTHENTIK_API_TOKEN" \
	PA_STACK_METRICS_TOKEN="$METRICS_TOKEN" PA_STACK_CLIENT_KEY="$key" \
	PA_A77_STATE="$OUT/a77-state.json" PA_A77_HOOK_HOST="$gateway"

status=0
echo "=== A77: made-up data on the previous release"
PA_STACK_PHASE=a77-seed go test -count=1 -v -run 'TestA77Seed' ./internal/stacktest/ || exit 1

# As an install from before the role existed would be.
stack_compose exec -T db psql -q -v ON_ERROR_STOP=1 -U postgres -d authentik <<'SQL'
DROP FUNCTION IF EXISTS public.pagerarchive_locked_accounts();
DO $$ BEGIN
	IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'pagerarchive_metrics') THEN
		REVOKE CONNECT ON DATABASE authentik FROM pagerarchive_metrics;
		DROP ROLE pagerarchive_metrics;
	END IF;
END $$;
SQL

echo "=== A77: upgrading: this checkout's compose.yaml and archive, then pull and up"
sh scripts/ci/release-assets.sh "$WORK/assets"
registry_publish . "$IMAGE_TAG"
cp "$WORK/assets/compose.yaml" "$DIR/compose.yaml"
use_registry "$DIR/compose.yaml"
tar -xzf "$WORK/assets/pagerarchive-config.tar.gz" -C "$DIR"
chmod -R a+rX "$DIR"
(in_folder "$DIR" pull && in_folder "$DIR" up -d) 2>&1 | tee "$OUT/a77-upgrade.txt"
stack_wait_ready

got=$(stack_compose exec -T api /usr/local/bin/pagerarchive version)
echo "--- running: $got"
[ "$got" = "pagerarchive $VERSION" ] || { echo "FAIL: the API is $got after the upgrade" >&2; status=1; }

# R84a: every file the new release changed is what the containers now see.
for service in authentik-server authentik-worker; do
	for file in blueprints/pagerarchive.yaml:/blueprints/pagerarchive/pagerarchive.yaml \
		templates/pagerarchive/code.html:/templates/pagerarchive/code.html \
		templates/pagerarchive/code.txt:/templates/pagerarchive/code.txt \
		templates/pagerarchive/invitation.html:/templates/pagerarchive/invitation.html \
		templates/pagerarchive/invitation.txt:/templates/pagerarchive/invitation.txt; do
		if ! stack_compose exec -T "$service" cat "${file#*:}" | cmp -s - "$DIR/deploy/authentik/${file%%:*}"; then
			echo "FAIL: $service doesn't see the new ${file%%:*}" >&2
			status=1
		fi
	done
done
echo "--- Authentik's containers see the new release's files"

echo "=== A77: the checks after the upgrade"
PA_STACK_PHASE=a77-check go test -count=1 -v -run 'TestA77Check' ./internal/stacktest/ || status=1

# R84: no profile mounted, so the example, and the warning on every start.
n=$(stack_compose logs --no-color api | grep -c 'made-up example profile' || true)
if [ "$n" -lt 1 ]; then
	echo "FAIL: no warning that the API is on the example profile" >&2
	status=1
fi
echo "--- example profile warnings in the API's log since this container started: $n"
# R64: the seed's two templates whose names differ only in case, from the
# previous release, which allowed them: logged on start, by name.
if grep -q '"case_pair": true' "$OUT/a77-state.json"; then
	line=$(stack_compose logs --no-color api | grep 'differ only in letter case' | tail -n 1)
	case $line in
	*'\"A77 Water\" (id '*'\"a77 water\" (id '*) echo "--- logged: ${line#*\"templates\":}" ;;
	*)
		echo "FAIL: the API didn't log the templates whose names differ only in case: $line" >&2
		status=1
		;;
	esac
fi
# R27b: the seed's two aliases sharing a name, from the previous release,
# which allowed them: logged on start, by capcode (never by name).
if grep -q '"name_pair": true' "$OUT/a77-state.json"; then
	line=$(stack_compose logs --no-color api | grep 'aliases share names' | tail -n 1)
	case $line in
	*'0100078 (id '*' = 0100079 (id '*)
		case $line in
		*'A77 Pump'* | *'a77 pump'*)
			echo "FAIL: the API logged the aliases' names: $line" >&2
			status=1
			;;
		*) echo "--- logged: ${line#*\"aliases\":}" ;;
		esac
		;;
	*)
		echo "FAIL: the API didn't log the aliases sharing a name: $line" >&2
		status=1
		;;
	esac
fi
stack_check_logs || status=1

# R84i: the upgraded install still connects as the superuser, and works, with
# the warning on every start.
n=$(stack_compose logs --no-color api | grep -c 'connects to PostgreSQL as a superuser' || true)
if [ "$n" -lt 1 ]; then
	echo "FAIL: no warning that the API connects as the superuser" >&2
	status=1
fi
echo "--- superuser warnings in the API's log since the upgrade: $n"

echo "=== A77: switching the API to its own role (deploy/postgres/api-role.sh)"
# api_role: the script, from the install's folder, with only what a host has.
api_role() {
	(cd "$DIR" && env -i PATH="$PATH" HOME="$HOME" ${DOCKER_HOST:+DOCKER_HOST="$DOCKER_HOST"} \
		COMPOSE_PROJECT_NAME="$COMPOSE_PROJECT_NAME" sh deploy/postgres/api-role.sh "$@")
}
dbq() { stack_compose exec -T db psql -X -q -At -U postgres -d "${2:-postgres}" -c "$1" | tr -d '\r'; }
if ! api_role >"$OUT/a77-role.txt" 2>&1; then
	cat "$OUT/a77-role.txt" >&2
	echo "FAIL: api-role.sh failed" >&2
	exit 1
fi
cat "$OUT/a77-role.txt"
pw=$(sed -n 's/^PAGERARCHIVE_DB_PASSWORD=//p' "$DIR/.env")
case $pw in
*[!0-9a-f]* | "") echo "FAIL: .env has no new hex PAGERARCHIVE_DB_PASSWORD" >&2 && status=1 ;;
esac
[ ${#pw} -eq 64 ] || { echo "FAIL: the new password is ${#pw} characters, not 64" >&2; status=1; }
grep -q "$pw" "$OUT/a77-role.txt" && { echo "FAIL: api-role.sh printed the password" >&2; status=1; }
grep -qx "PAGERARCHIVE_DB_USER=pagerarchive" "$DIR/.env" || { echo "FAIL: .env doesn't name the role" >&2; status=1; }
# What the API connects as to PagerArchive's database, seen from there: its
# container's address. (The locked-accounts metric connects to Authentik's as
# its own role.)
api_ip=$(docker inspect -f '{{range .NetworkSettings.Networks}}{{.IPAddress}}{{end}}' "$(stack_compose ps -q api)")
users=$(dbq "SELECT string_agg(DISTINCT usename, ',') FROM pg_stat_activity WHERE client_addr = '$api_ip' AND datname = 'postgres'")
[ "$users" = pagerarchive ] || { echo "FAIL: the API connects as $users, not only as pagerarchive" >&2; status=1; }
[ "$(dbq "SELECT rolsuper OR rolcreaterole OR rolcreatedb FROM pg_roles WHERE rolname = 'pagerarchive'")" = f ] ||
	{ echo "FAIL: the role has rights it mustn't" >&2; status=1; }
others=$(dbq "SELECT count(*) FROM pg_class c WHERE c.relnamespace = 'public'::regnamespace AND c.relkind = 'r' AND c.relowner <> 'pagerarchive'::regrole AND c.relname <> 'spatial_ref_sys'")
[ "$others" = 0 ] || { echo "FAIL: $others of PagerArchive's tables aren't the role's" >&2; status=1; }
# As the role, as the API is: no Authentik database, no programs.
as_role() { PGPASSWORD=$pw stack_compose exec -T -e PGPASSWORD db psql -X -q -At -h 127.0.0.1 -U pagerarchive "$@"; }
as_role -d authentik -c 'SELECT 1' >/dev/null 2>&1 && { echo "FAIL: the role connects to Authentik's database" >&2; status=1; }
as_role -d postgres -c "COPY (SELECT 1) TO PROGRAM 'true'" >/dev/null 2>&1 && { echo "FAIL: the role runs programs" >&2; status=1; }
echo "--- the API connects as pagerarchive ($users), which owns every table, isn't a superuser, can't reach Authentik's database or run a program"
n=$(stack_compose logs --no-color api | grep -c 'connects to PostgreSQL as a superuser' || true)
[ "$n" -eq 0 ] || { echo "FAIL: the API still warns that it connects as the superuser" >&2; status=1; }
PA_STACK_PHASE=a77-role go test -count=1 -v -run 'TestA77Role' ./internal/stacktest/ || status=1

echo "=== A77: api-role.sh again: harmless"
env_sum=$(sha256sum "$DIR/.env")
api_id=$(stack_compose ps -q api)
api_role >"$OUT/a77-role-again.txt" 2>&1 || { cat "$OUT/a77-role-again.txt" >&2; echo "FAIL: api-role.sh failed the second time" >&2; status=1; }
[ "$(sha256sum "$DIR/.env")" = "$env_sum" ] || { echo "FAIL: the second run changed .env" >&2; status=1; }
[ "$(stack_compose ps -q api)" = "$api_id" ] || { echo "FAIL: the second run recreated the API" >&2; status=1; }
echo "--- the second run changed nothing: $(tail -n 1 "$OUT/a77-role-again.txt")"
stack_check_logs || status=1

echo "=== A77: a patch release under the same tag: pull and up, nothing else"
patch=$IMAGE_TAG.99
copy_checkout "$WORK/patch-src"
echo "$patch" >"$WORK/patch-src/VERSION"
registry_publish "$WORK/patch-src" "$IMAGE_TAG"
listing "$DIR" >"$WORK/before"
(in_folder "$DIR" pull && in_folder "$DIR" up -d) 2>&1 | tee "$OUT/a77-patch.txt" | grep -E 'Recreate|Running' || true
stack_wait_ready
got=$(stack_compose exec -T api /usr/local/bin/pagerarchive version)
echo "--- running: $got"
[ "$got" = "pagerarchive $patch" ] || { echo "FAIL: the API is $got after a patch release" >&2; status=1; }
listing "$DIR" | diff "$WORK/before" - >&2 || { echo "FAIL: a patch release changed the folder" >&2; status=1; }

if [ "$status" -eq 0 ]; then echo "=== A77 passed"; else echo "=== A77 FAILED"; fi
exit $status
