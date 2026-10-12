#!/bin/sh
# SPEC.md section 11, step 2, in a real browser: the full compose stack
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

status=0
(cd web && PA_URL=http://localhost:8080 PA_MAILPIT_URL=http://localhost:8025 PA_OWNER_EMAIL="$INITIAL_ADMIN_EMAIL" \
	npx playwright test tests/stack.spec.ts "$@") || status=$?

stack_check_logs || status=1
exit $status
