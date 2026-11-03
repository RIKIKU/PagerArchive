#!/bin/sh
# A77, upgrade from the previous release (SPEC.md R84a, ADR 0019). A folder made
# from the previous release's attached files, started, and given made-up users,
# aliases, templates with icons, integrations and pages (internal/stacktest).
# Then this checkout's compose.yaml and archive are put in the folder, and
# `docker compose pull && docker compose up -d` is all that's run. The checks
# then find everything still there, the migrations run and a new page stored,
# found and delivered.
#
# The previous release is the newest v* tag in this commit's history. One from
# before M17 has nothing attached (A77 starts with the release after M17's),
# so the branch or merge that has only those makes a release of its own to
# start from, in the throwaway registry: this checkout's code with a lower
# VERSION, without its newest migration, and with a changed Authentik file.
#
# This checkout's image goes to a throwaway registry on this machine, never to
# GHCR. Before the upgrade the metrics role is dropped, so the API has to
# create it again: nothing may be left for a hand step.
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
IMAGE_TAG=${VERSION%.*}
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

# previous_from_release <tag>: its attached files, its images on GHCR.
previous_from_release() {
	echo "=== A77: starting from $1's attached files"
	mkdir -p "$WORK/previous"
	for asset in $(sh scripts/ci/release-assets.sh --list); do
		curl -fsSL -o "$WORK/previous/$asset" "https://github.com/RIKIKU/PagerArchive/releases/download/$1/$asset"
	done
	install_folder "$DIR" "$WORK/previous"
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

MADE_HERE=""
previous=$(previous_release HEAD 2>/dev/null || true)
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
