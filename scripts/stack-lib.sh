# Shared by scripts/stack-checks.sh and scripts/stack-e2e.sh: start and stop
# the compose stack with the test overlay (deploy/test), and check its logs.
# Set COMPOSE_PROJECT_NAME before sourcing. Settings come from
# deploy/test/stack.vars; variables already in the environment win.

while IFS= read -r line; do
	case $line in "" | "#"*) continue ;; esac
	eval "set_already=\${${line%%=*}+x}"
	[ -n "$set_already" ] || export "$line"
done <deploy/test/stack.vars

# STACK_OVERLAYS adds compose files: "-f deploy/test/telemetry.yaml".
stack_compose() {
	docker compose -f compose.yaml -f deploy/test/compose.yaml ${STACK_OVERLAYS:-} "$@"
}

stack_wait_api() {
	i=0
	until curl -sf http://127.0.0.1:8080/healthz >/dev/null; do
		i=$((i + 1))
		[ $i -gt "${STACK_API_WAIT:-120}" ] && { echo "API didn't start" >&2; stack_compose logs api | tail -20 >&2; return 1; }
		sleep 1
	done
}

stack_up() {
	echo "--- building the API image and starting the stack ($COMPOSE_PROJECT_NAME)"
	stack_compose build -q api
	# Quiet unless it fails; then its output says why (a pull, a port).
	if ! out=$(stack_compose up -d 2>&1); then
		printf '%s\n' "$out" | tail -n 30 >&2
		stack_compose ps
		return 1
	fi
	stack_wait_ready
}

# Waits for Authentik, its blueprint and the API, however the stack was started.
stack_wait_ready() {
	start=$(date +%s)
	# Authentik is ready, and has applied the PagerArchive blueprint, when the
	# app's API token can see the enrolment flow.
	until curl -sf -H "Authorization: Bearer $AUTHENTIK_API_TOKEN" \
		"http://127.0.0.1:9000/api/v3/flows/instances/?slug=pagerarchive-enrolment" 2>/dev/null | grep -q '"count":1'; do
		[ $(($(date +%s) - start)) -gt 300 ] && { echo "Authentik wasn't ready after 300 s" >&2; stack_compose logs --tail 30 authentik-server authentik-worker >&2; return 1; }
		sleep 3
	done
	# On a cold start the flow executor can still take seconds to answer its
	# first visitors after that (stack-e2e.sh once timed out on the
	# enrolment page's spinner). Ready means it answers an anonymous visitor
	# for the sign-in and the enrolment flow, three times in a row, each
	# within 2 s.
	for flow in pagerarchive-authentication pagerarchive-enrolment; do
		ok=0
		until [ $ok -ge 3 ]; do
			if curl -sf -m 2 -H 'Accept: application/json' \
				"http://127.0.0.1:9000/api/v3/flows/executor/$flow/?query=" 2>/dev/null | grep -q '"component"'; then
				ok=$((ok + 1))
			else
				ok=0
				[ $(($(date +%s) - start)) -gt 300 ] && { echo "Authentik's $flow flow didn't answer within 300 s" >&2; return 1; }
				sleep 1
			fi
		done
	done
	stack_wait_api
	echo "--- stack ready after $(($(date +%s) - start)) s"
}

# R44c. stack_api_says <text> <seconds>: the API's current container logs it.
stack_api_says() {
	_end=$(($(date +%s) + $2))
	until stack_compose logs --no-color api 2>/dev/null | grep -qF "$1"; do
		[ "$(date +%s)" -lt "$_end" ] || return 1
		sleep 2
	done
}

ADDRESS_SHARED="address is also held by another account in Authentik"
ADDRESS_OK="address isn't held by another account in Authentik"

# stack_check_address_warning warn|none <what>: the API asked Authentik about
# the first admin's address, and warned or didn't, without saying the address.
stack_check_address_warning() {
	if [ "$1" = warn ]; then _want=$ADDRESS_SHARED _other=$ADDRESS_OK; else _want=$ADDRESS_OK _other=$ADDRESS_SHARED; fi
	if ! stack_api_says "$_want" 60; then
		echo "FAIL: $2: the API didn't log \"$_want\"" >&2
		return 1
	fi
	_logs=$(stack_compose logs --no-color api)
	if printf '%s' "$_logs" | grep -qF "$_other"; then
		echo "FAIL: $2: the API logged \"$_other\"" >&2
		return 1
	fi
	if printf '%s' "$_logs" | grep -qiE '@example\.(com|org|test)'; then
		echo "FAIL: $2: an email address in the API log" >&2
		return 1
	fi
	echo "--- R44c: $2: the API logged \"$_want\", without the address"
}

# stack_set_akadmin_address <address>: the fix in docs/deploy.md, as written there.
stack_set_akadmin_address() {
	_fix=$(awk '/<!-- r44c:fix -->/ { found = 1; next } found && /^```sh/ { on = 1; next } on && /^```/ { exit } on' docs/deploy.md)
	[ -n "$_fix" ] || { echo "FAIL: no fix for two accounts with one address in docs/deploy.md" >&2; return 1; }
	_fix=$(printf '%s\n' "$_fix" | sed -e 's/^docker compose exec /stack_compose exec -T /' -e "s/authentik-admin@example\.org/$1/")
	eval "$_fix" >/dev/null 2>&1
}

# P1 and privacy: no page text and no email address in the API's log.
stack_check_logs() {
	logs=$(stack_compose logs --no-color api)
	if printf '%s' "$logs" | grep -qiE 'SMOKE|GARBLED|@example\.(com|org|test)'; then
		echo "page text or an email address found in the API log" >&2
		return 1
	fi
	echo "API log lines checked: $(printf '%s\n' "$logs" | wc -l), page texts or email addresses found: 0"
}

stack_down() {
	stack_compose down -v --remove-orphans >/dev/null 2>&1 || true
}
