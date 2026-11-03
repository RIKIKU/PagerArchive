#!/bin/sh
# A78, install with no checkout (SPEC.md R84, ADR 0019): an empty folder with
# only what a release attaches, filled in with test values, plus the example
# profile and map. `docker compose up -d`, then section 11's steps in a browser.
# The folder must end up with nothing new in it, and no image other than ours
# and the pinned upstream ones may run. Then the same again with the
# compose.override.yaml from docs/deploy.md, behind a reverse proxy container on
# an external network named `proxy`.
#
# Images built here go to a throwaway registry on this machine, never to GHCR.
# Mail goes to a Mailpit started beside the stack, outside the folder.
#
# usage: scripts/a78.sh [playwright arguments]
# needs: Docker with compose, Node with web/ dependencies, Playwright browsers.
# Uses ports 8080, 9000, 8025 and 15000 on 127.0.0.1, and no network named proxy.
set -eu
cd "$(dirname "$0")/.."

export COMPOSE_PROJECT_NAME="pa-a78-$$"
. ./scripts/stack-lib.sh
. ./scripts/install-lib.sh
VERSION=$(tr -d ' \r\n' <VERSION)
WORK=$(mktemp -d)
DIR=$WORK/deployment
MAILPIT=pa-a78-mailpit-$$
NGINX=pa-a78-nginx-$$
stack_compose() { in_folder "$DIR" "$@"; }

proxy_made=""
stop_stack() {
	docker rm -fv "$MAILPIT" "$NGINX" >/dev/null 2>&1 || true
	stack_compose down -v --remove-orphans >/dev/null 2>&1 || true
}
cleanup() {
	stop_stack
	[ -z "$proxy_made" ] || docker network rm proxy >/dev/null 2>&1 || true
	registry_stop
	rm -rf "$WORK"
}
trap cleanup EXIT

if docker network inspect proxy >/dev/null 2>&1; then
	echo "a network named proxy already exists here: A78 needs that name" >&2
	exit 1
fi

echo "--- the release's files, and this checkout's image in the throwaway registry"
sh scripts/ci/release-assets.sh "$WORK/assets"
ls -l "$WORK/assets"
registry_start
registry_publish . "$VERSION"
docker build -q -f Dockerfile.client -t pagerarchive-client:stacktest . >/dev/null

install_folder "$DIR" "$WORK/assets"
use_registry "$DIR/compose.yaml"
cp -r profiles/example "$DIR/profile"
cp deploy/example-map/example-map.pmtiles "$DIR/map.pmtiles"
chmod -R a+rX "$WORK"
cat >"$WORK/vars" <<VARS
PROFILES_DIR=./profile
MAP_TILES_FILE=./map.pmtiles
VARS
fill_env "$DIR/.env" deploy/test/stack.vars "$WORK/vars"

# After a failed browser run, what the stack's teardown would otherwise lose.
# Made-up test data only. Which mail arrived, and what Authentik's worker did.
show_why() {
	echo "--- mail that reached the catcher:"
	curl -s 'http://127.0.0.1:8025/api/v1/messages?limit=10' | grep -o '"Subject":"[^"]*"' || echo "(none)"
	echo "--- the catcher's log:"
	docker logs --tail 20 "$MAILPIT" 2>&1 || true
	echo "--- Authentik's worker, its last lines:"
	stack_compose logs --no-color --tail 60 authentik-worker 2>&1 | cut -c1-300 || true
}

# run_install <what> [a step right after up -d]: from `up -d` on an empty stack
# to section 11's steps.
run_install() {
	echo "=== A78: $1"
	listing "$DIR" >"$WORK/before"
	stack_compose up -d
	docker run -d --name "$MAILPIT" --network "${COMPOSE_PROJECT_NAME}_default" --network-alias mailpit \
		-e MP_SMTP_AUTH_ACCEPT_ANY=true -e MP_SMTP_AUTH_ALLOW_INSECURE=true \
		-p 127.0.0.1:8025:8025 axllent/mailpit:v1.31.5 >/dev/null
	[ -z "${2:-}" ] || "$2"
	stack_wait_ready
	stack_compose exec -T db psql -q -v ON_ERROR_STOP=1 -U postgres >/dev/null <scripts/fixture.sql
	key=$(stack_compose exec -T api /usr/local/bin/pagerarchive client add section11 "Section 11 client" | tail -n 1)
	(cd web && PA_URL=http://localhost:8080 PA_MAILPIT_URL=http://localhost:8025 PA_OWNER_EMAIL="$INITIAL_ADMIN_EMAIL" \
		PA_CLIENT_KEY="$key" PA_CLIENT_IMAGE=pagerarchive-client:stacktest PA_NETWORK="${COMPOSE_PROJECT_NAME}_default" \
		npx playwright test tests/stack.spec.ts $PLAYWRIGHT_ARGS) || { status=1; show_why; }

	if listing "$DIR" | diff "$WORK/before" - >&2; then
		echo "--- the folder holds what it did before up -d: $(wc -l <"$WORK/before") entries"
	else
		echo "FAIL: the folder changed" >&2
		status=1
	fi
	if check_images "$COMPOSE_PROJECT_NAME" postgis/postgis ghcr.io/goauthentik/server; then
		echo "--- images: ours from the registry, and pinned upstream: $(stack_compose config --images | sort -u | tr '\n' ' ')"
	else
		status=1
	fi
	if stack_compose logs --no-color api | grep -q 'made-up example profile'; then
		echo "FAIL: the API says it's on the example profile, with PROFILES_DIR set" >&2
		status=1
	fi
	stack_check_logs || status=1
	stop_stack
}

# Behind the proxy: nothing published, and the database out of its reach.
start_proxy() {
	docker run -d --name "$NGINX" --network proxy -p 127.0.0.1:8080:8080 -p 127.0.0.1:9000:9000 \
		-v "$PWD/deploy/test/a78-proxy.conf:/etc/nginx/conf.d/default.conf:ro" nginx:1.29-alpine >/dev/null
	published=$(docker ps --filter "label=com.docker.compose.project=$COMPOSE_PROJECT_NAME" --format '{{.Names}} {{.Ports}}' | grep -- '->' || true)
	if [ -n "$published" ]; then
		echo "FAIL: published with the override: $published" >&2
		status=1
	fi
	if docker exec "$NGINX" getent hosts db >/dev/null; then
		echo "FAIL: the database can be reached from the proxy's network" >&2
		status=1
	fi
	echo "--- behind the proxy: no port published, the database out of its reach"
}

status=0
PLAYWRIGHT_ARGS="$*"
run_install "the folder as a release makes it"

# The example from the docs, as it's written there.
awk '/<!-- a78:override -->/ { found = 1; next } found && /^```yaml/ { on = 1; next } on && /^```/ { exit } on' \
	docs/deploy.md >"$DIR/compose.override.yaml"
grep -q 'external: true' "$DIR/compose.override.yaml" || { echo "no override example found in docs/deploy.md" >&2; exit 1; }
cat "$DIR/compose.override.yaml"
docker network create proxy >/dev/null
proxy_made=1
run_install "with compose.override.yaml, behind a proxy on the external network" start_proxy

if [ "$status" -eq 0 ]; then echo "=== A78 passed"; else echo "=== A78 FAILED"; fi
exit $status
