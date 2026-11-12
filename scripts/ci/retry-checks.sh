#!/bin/sh
# Checks CI's retries (ADR 0025) without a network or GitHub:
#   - retry.sh: a command that passes at once isn't retried or reported; one
#     that passes on its second try is reported as flaky, in an annotation and
#     the job's summary; one that fails every try fails with its last status;
#   - rerun-decision.sh, with a stand-in for gh: a failed first attempt of a
#     pull request's or main's run is re-run, and not a second attempt, a
#     manual run, a run whose branch has moved on, one where a job or step
#     that only the change can fix failed, one where only the gate failed, or
#     one whose failed job logged a security or privacy finding;
#   - govulncheck.sh, on made-up findings: it fails only for a vulnerability
#     the code calls that has a fix.
#
# usage: scripts/ci/retry-checks.sh
set -eu
repo=$(cd "$(dirname "$0")/../.." && pwd)
work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT
failed=0
fail() {
	echo "FAIL: $*" >&2
	failed=1
}
export PA_RETRY_WAIT=0 GITHUB_OUTPUT=
cd "$work"

# --- retry.sh
export GITHUB_STEP_SUMMARY="$work/summary"
: >"$GITHUB_STEP_SUMMARY"
out=$(sh "$repo/scripts/ci/retry.sh" -l once true 2>&1)
[ -z "$out" ] && [ ! -s "$GITHUB_STEP_SUMMARY" ] || fail "a command that passed at once was reported: $out"
echo "--- passes at once: nothing reported"

rm -f "$work/tried"
out=$(sh "$repo/scripts/ci/retry.sh" -l "second time lucky" sh -c '[ -e tried ] || { touch tried; exit 7; }' 2>&1) || fail "a command that passed on its second try failed: $out"
printf '%s\n' "$out" | grep -qx '::warning title=Flaky::flaky: second time lucky (passed on try 2 of 3)' || fail "no flaky annotation: $out"
grep -qx -- '- flaky: second time lucky (passed on try 2 of 3)' "$GITHUB_STEP_SUMMARY" || fail "not in the job's summary: $(cat "$GITHUB_STEP_SUMMARY")"
echo "--- passes on try 2: $(printf '%s\n' "$out" | grep '^::warning')"

status=0
out=$(sh "$repo/scripts/ci/retry.sh" -l "always fails" sh -c 'echo try; exit 9' 2>&1) || status=$?
[ "$status" -eq 9 ] || fail "a command that failed every try exited $status, not 9"
[ "$(printf '%s\n' "$out" | grep -cx try)" -eq 3 ] || fail "not tried 3 times: $out"
printf '%s\n' "$out" | grep -q '^::warning' && fail "a failure was reported as flaky: $out"
echo "--- fails every try: exit $status after 3 tries, not reported as flaky"
unset GITHUB_STEP_SUMMARY

# --- rerun-decision.sh
mkdir "$work/bin"
cat >"$work/bin/gh" <<'GH'
#!/bin/sh
# gh api repos/<repo>/branches/<branch> --jq .commit.sha
# gh api --paginate repos/<repo>/actions/runs/<id>/attempts/<n>/jobs --jq ...
# gh run view --job <id> --log --repo <repo>
case "$*" in
*/branches/*) echo "$GH_BRANCH_SHA" ;;
*/jobs*) printf '%b' "$GH_JOBS" ;;
"run view --job 7 "*) printf '%b' "$GH_LOG" ;;
"run view --job "*) echo "an ordinary log" ;;
esac
GH
chmod +x "$work/bin/gh"
export PATH="$work/bin:$PATH" GITHUB_REPOSITORY=owner/repo GH_TOKEN=x
tab=$(printf '\t')

# decision <want rerun> <attempt> <event> <branch> <jobs (name<TAB>failed steps<TAB>id per line)> [<branch now at>] [<job 7's log>]
decision() {
	GH_JOBS=$5 GH_BRANCH_SHA=${6:-abc123} GH_LOG=${7:-a log}
	export GH_JOBS GH_BRANCH_SHA GH_LOG
	got=$(sh "$repo/scripts/ci/rerun-decision.sh" 41 "$2" "$3" "$4" abc123 owner/repo)
	if [ "$(printf '%s\n' "$got" | sed -n 's/^rerun=//p')" = "$1" ]; then
		echo "--- $3 on $4, attempt $2: rerun=$1 ($(printf '%s\n' "$got" | sed -n 's/^why=//p'))"
	else
		fail "$3 on $4, attempt $2, jobs '$5': want rerun=$1, got: $got"
	fi
}
web="Browser app and browser tests (A1, …)${tab}Browser tests against a real API and PostGIS${tab}5\n"
gate="All checks passed${tab}Every check passed${tab}6\n"
accounts="Against Authentik (A23-A28, A33, …)${tab}The checks against the compose stack${tab}7\n"
decision true 1 pull_request ci-speed "$web$gate"
decision true 1 push main "Backups (A80)${tab}Upload the logs${tab}8\nResource budget, short run (A15), telemetry off${tab}A15 short run${tab}9\n$gate"
decision false 2 pull_request ci-speed "$web$gate"
decision false 1 workflow_dispatch ci-speed "$web$gate"
decision false 1 push some-branch "$web$gate"
decision false 1 pull_request ci-speed "$web$gate" def456
decision false 1 pull_request ci-speed "Versions are left to the release, and the module's is raised${tab}Unstamped, and the module's version against the previous release\n$gate"
decision false 1 pull_request ci-speed "Known vulnerabilities the code calls (govulncheck)${tab}govulncheck\n$gate"
decision false 1 pull_request ci-speed "Tests, golden tables and acceptance checks${tab}gofmt\n$web$gate"
decision false 1 pull_request ci-speed "Browser app and browser tests (A1, …)${tab}svelte-check|Browser tests against a real API and PostGIS\n$gate"
decision false 1 pull_request ci-speed "$gate"
decision true 1 pull_request ci-speed "$accounts$gate" abc123 'accepted: accepted (ak-stage-prompt): passed false, failures ["SECURITY FAILURE: the right code: accepted"]
--- FAIL: TestA33WithinOneFlow'
decision false 1 pull_request ci-speed "$accounts$gate" abc123 '    a33_test.go:255: SECURITY FAILURE: the right password in a new flow after 5 wrong ones: accepted'
decision false 1 pull_request ci-speed "$accounts$gate" abc123 'page text or an email address found in the API log'

# --- govulncheck.sh
finding() { # finding <id> <module> <fixed or ""> <function or ""> [<package>]
	printf '{"finding":{"osv":"%s",%s"trace":[{"module":"%s","version":"v1.0.0"%s%s}]}}\n' "$1" \
		"$([ -n "$3" ] && printf '"fixed_version":"%s",' "$3")" "$2" \
		"$([ -n "${5:-}" ] && printf ',"package":"%s"' "$5")" "$([ -n "$4" ] && printf ',"function":"%s"' "$4")"
}
{
	echo '{"config":{"scanner_version":"v1.9.0","db_last_modified":"2026-11-11T00:00:00Z","go_version":"go1.27.4"}}'
	echo '{"osv":{"id":"GO-1","summary":"called, no fix"}}'
	finding GO-1 example.test/a "" F example.test/a
	echo '{"osv":{"id":"GO-2","summary":"imported only"}}'
	finding GO-2 example.test/b v1.0.1 "" example.test/b
	finding GO-3 example.test/c v1.0.1 ""
} >"$work/vuln.json"
out=$(sh "$repo/scripts/ci/govulncheck.sh" "$work/vuln.json") || fail "govulncheck.sh failed with nothing called that has a fix: $out"
printf '%s\n' "$out" | grep -q '^::warning title=Vulnerability the code calls, no fix yet::GO-1' || fail "no warning for GO-1: $out"
echo "--- called with no fix, imported, required: passes, $(printf '%s\n' "$out" | tail -1)"
finding GO-4 example.test/d v1.0.1 F example.test/d >>"$work/vuln.json"
status=0
out=$(sh "$repo/scripts/ci/govulncheck.sh" "$work/vuln.json") || status=$?
[ "$status" -eq 1 ] || fail "govulncheck.sh passed with GO-4 called and fixed: $out"
printf '%s\n' "$out" | grep -q '^::error title=Vulnerability the code calls::GO-4 in example.test/d v1.0.0, fixed in v1.0.1' || fail "no error for GO-4: $out"
echo "--- called with a fix: fails, $(printf '%s\n' "$out" | tail -1)"

[ "$failed" -eq 0 ] || exit 1
echo "Retry checks: OK"
