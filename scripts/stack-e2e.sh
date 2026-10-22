#!/bin/sh
# SPEC.md section 11, steps 2 and 3, in a real browser: the full compose stack
# (API, PostgreSQL, Authentik, Mailpit) from a fresh install with the
# documented test settings (deploy/test/stack.vars: allowed domain
# example.test), and web/tests/stack.spec.ts driving it.
#
# usage: scripts/stack-e2e.sh [playwright arguments]
# needs: Docker with compose, Node with web/ dependencies, Playwright browsers.
# Uses ports 8080, 9000 and 8025 on 127.0.0.1.
set -eu
cd "$(dirname "$0")/.."

export COMPOSE_PROJECT_NAME="pa-e2e-$$"
export PAGERARCHIVE_VERSION=stacktest
. ./scripts/stack-lib.sh

trap '[ -n "${STACK_KEEP:-}" ] || stack_down' EXIT
stack_up

# Section 11 step 3: the standard test fixture, a client key, and the client
# image the test feeds fixture lines through (stdin, no radio).
echo "--- building the client image and adding the test fixture"
docker build -q -f Dockerfile.client -t pagerarchive-client:stacktest . >/dev/null
cli() { stack_compose exec -T api /usr/local/bin/pagerarchive "$@"; }
stack_compose exec -T db psql -q -v ON_ERROR_STOP=1 -U postgres >/dev/null <scripts/fixture.sql
PA_CLIENT_KEY=$(cli client add section11 "Section 11 client" | tail -n 1)
export PA_CLIENT_KEY PA_CLIENT_IMAGE=pagerarchive-client:stacktest PA_NETWORK="${COMPOSE_PROJECT_NAME}_default"

status=0
(cd web && PA_URL=http://localhost:8080 PA_MAILPIT_URL=http://localhost:8025 PA_OWNER_EMAIL="$INITIAL_ADMIN_EMAIL" \
	npx playwright test tests/stack.spec.ts "$@") || status=$?

stack_check_logs || status=1
exit $status
