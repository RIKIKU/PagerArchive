#!/bin/sh
# A76's file-based checks (ADR 0018; internal/stacktest/import_test.go): the
# import command, through `docker compose exec`, on the compose file's API and
# database, with the API's telemetry going to a collector in the test.
#
# usage: scripts/a76-checks.sh [go test arguments]
# needs: Docker with compose, Go, curl. Uses 127.0.0.1:8080 and :55439, and ports
# 14320 and 14322 on every address, for the test collector and the test webhook.
set -eu
cd "$(dirname "$0")/.."

export COMPOSE_PROJECT_NAME="pa-a76-checks-$$"
export PAGERARCHIVE_VERSION=a76
export PA_TEST_COLLECTOR_PORT=14320
export STACK_OVERLAYS="-f deploy/test/import.yaml -f deploy/test/telemetry.yaml"
. ./scripts/stack-lib.sh

trap '[ -n "${STACK_KEEP:-}" ] || stack_down' EXIT
echo "--- building the API image and starting it with the database ($COMPOSE_PROJECT_NAME)"
stack_compose build -q api
stack_compose up -d db api
stack_wait_api
stack_compose exec -T db psql -q -v ON_ERROR_STOP=1 -U postgres >/dev/null <scripts/fixture.sql

export PA_A76_DB_URL="postgres://postgres:$POSTGRES_PASSWORD@127.0.0.1:${PA_A76_DB_PORT:-55439}/postgres?sslmode=disable"
export METRICS_TOKEN
go test -count=1 -v -timeout 20m -run 'TestA76' ./internal/stacktest/ "$@"
stack_check_logs
