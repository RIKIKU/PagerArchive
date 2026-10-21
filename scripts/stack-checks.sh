#!/bin/sh
# Runs the account checks A23-A28 and A33, the locked-accounts metric (R52)
# and the PowerShell module's checks (A20, A21, A51-A55) (internal/stacktest)
# against the full compose stack: API, PostgreSQL, Authentik and a Mailpit
# mail catcher, from a fresh install with A23's settings. Never uses a real
# mail server.
#
# usage: scripts/stack-checks.sh [go test arguments]
# needs: Docker with compose, Go. Uses ports 8080, 9000 and 8025 on 127.0.0.1.
# The PowerShell checks need pwsh (PA_STACK_PWSH, default: pwsh on PATH) and
# a module path (PA_STACK_PS_MODULES) holding Microsoft.PowerShell.SecretManagement
# and Microsoft.PowerShell.SecretStore; without pwsh they're skipped.
set -eu
cd "$(dirname "$0")/.."

export COMPOSE_PROJECT_NAME="pa-stack-$$"
export PAGERARCHIVE_VERSION=stacktest
# A23's Given: allowed domain example.com, first admin owner@example.org.
export ALLOWED_EMAIL_DOMAINS=example.com INITIAL_ADMIN_EMAIL=owner@example.org
. ./scripts/stack-lib.sh

trap '[ -n "${STACK_KEEP:-}" ] || stack_down' EXIT
stack_up

export PA_STACK_APP_URL=http://localhost:8080 PA_STACK_AUTHENTIK_URL=http://localhost:9000 \
	PA_STACK_MAILPIT_URL=http://localhost:8025 PA_STACK_AUTHENTIK_TOKEN="$AUTHENTIK_API_TOKEN" \
	PA_STACK_AKADMIN_EMAIL="$AUTHENTIK_ADMIN_EMAIL" PA_STACK_AKADMIN_PASSWORD="$AUTHENTIK_ADMIN_PASSWORD" \
	PA_STACK_METRICS_TOKEN="$METRICS_TOKEN" PA_STACK_METRICS_DB_PASSWORD="$AUTHENTIK_METRICS_DB_PASSWORD"
# A receiving client, to post pages for A55.
PA_STACK_CLIENT_KEY=$(stack_compose exec -T api /usr/local/bin/pagerarchive client add stack-ps "Stack receiver" | tail -n 1)
export PA_STACK_CLIENT_KEY
export PA_STACK_PWSH="${PA_STACK_PWSH:-$(command -v pwsh || true)}"
export PA_STACK_PS_MODULES="${PA_STACK_PS_MODULES:-${PSModulePath:-}}"
status=0
PA_STACK_PHASE=first go test -count=1 -v ./internal/stacktest/ "$@" || status=1

echo "--- restarting the API with INITIAL_ADMIN_EMAIL=second@example.org"
(export INITIAL_ADMIN_EMAIL=second@example.org; stack_compose up -d api >/dev/null 2>&1)
stack_wait_api
stack_compose exec -T api /usr/local/bin/pagerarchive version >/dev/null
PA_STACK_PHASE=restarted go test -count=1 -v ./internal/stacktest/ "$@" || status=1

# The PowerShell module, saving the sign-in in a SecretStore vault, then
# with no vault at all (R61, A51).
if [ -n "$PA_STACK_PWSH" ]; then
	for vault in secretstore none; do
		echo "--- PowerShell module checks, vault: $vault ($("$PA_STACK_PWSH" -NoProfile -Command '$PSVersionTable.PSVersion.ToString()'))"
		PA_STACK_PHASE=restarted PA_STACK_PS_VAULT=$vault go test -count=1 -v -run 'TestPowerShell' ./internal/stacktest/ "$@" || status=1
	done
else
	echo "--- PowerShell module checks skipped: no pwsh (set PA_STACK_PWSH)"
fi

stack_check_logs || status=1
exit $status
