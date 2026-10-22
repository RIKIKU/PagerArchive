# Shared by scripts/stack-checks.sh and scripts/stack-e2e.sh: start and stop
# the compose stack with the test overlay (deploy/test), and check its logs.
# Set COMPOSE_PROJECT_NAME before sourcing. Settings come from
# deploy/test/stack.vars; variables already in the environment win.

while IFS= read -r line; do
	case $line in "" | "#"*) continue ;; esac
	eval "set_already=\${${line%%=*}+x}"
	[ -n "$set_already" ] || export "$line"
done <deploy/test/stack.vars

stack_compose() {
	docker compose -f compose.yaml -f deploy/test/compose.yaml "$@"
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
	stack_compose up -d >/dev/null 2>&1 || { stack_compose ps; return 1; }
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
