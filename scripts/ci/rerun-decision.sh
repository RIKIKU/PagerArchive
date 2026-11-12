#!/bin/sh
# Layer 3 of CI's retries (ADR 0025): whether a failed run of CI has its
# failed jobs re-run, once, by the CI retry workflow (ci-retry.yml). The
# maintenance-failed workflow asks the same, and tells the owner only when
# the answer is no: after the retry, or when there's none.
#
# Re-run when all of these hold:
#   - it's the run's first attempt (a retry is never retried);
#   - it's a pull request's run, or a push to main's (never a manual run,
#     and the Release workflow's runs aren't CI's: its owner re-runs them);
#   - the commit is still the branch's newest: a re-run of an older one
#     would cancel the pull request's newer run (CI's concurrency), or on
#     main publish an older commit as main;
#   - no failure is one that's always the change's: the jobs versions, stamp,
#     changes and govulncheck, or the steps gofmt, go vet, sqlc's, the example
#     profile's validation, svelte-check, the style tokens, the icons and the
#     Playwright version check;
#   - something other than "All checks passed" failed (it fails only because
#     another did);
#   - no failed job's log has a security or privacy finding: A33's "SECURITY
#     FAILURE" (a lock that let a guess through), or page text or an email
#     address in the API's log (P1). Those are never retried away, even once.
#
# usage: scripts/ci/rerun-decision.sh <run id> <attempt> <event> <head branch> <head commit> <head repository>
#   needs GH_TOKEN and GITHUB_REPOSITORY. Writes rerun=true|false, why=… and
#   jobs=… (the failed jobs' names) to standard output and $GITHUB_OUTPUT.
set -eu
run=$1 attempt=$2 event=$3 branch=$4 sha=$5 head_repo=$6
here=$(cd "$(dirname "$0")" && pwd)

ALWAYS_THE_CHANGE_JOBS='^(Versions are left to the release, and the module.s is raised|The build.s version|What the change affects|Known vulnerabilities the code calls \(govulncheck\))$'
ALWAYS_THE_CHANGE_STEPS='^(gofmt|go vet|sqlc generated code is up to date|validate-profile \(example golden tables\)|svelte-check|Style tokens only \(A73\)|Icons are as generated from @tabler/icons|Playwright is the image.s version)$'
GATE='All checks passed'
# A failed job whose log says one of these is never re-run.
# (A passing run logs "SECURITY FAILURE" too, as a unit test's expected text,
# but never straight after a test's file and line, as a failed check does.)
NEVER_RETRIED='_test\.go:[0-9]+: SECURITY FAILURE|page text[^.]* found in the API log'

decide() {
	echo "rerun=$1"
	echo "why=$2"
	echo "jobs=${failed_jobs:-}"
	if [ -n "${GITHUB_OUTPUT:-}" ]; then
		{
			echo "rerun=$1"
			echo "why=$2"
			echo "jobs=${failed_jobs:-}"
		} >>"$GITHUB_OUTPUT"
	fi
	exit 0
}

failed_jobs=
[ "$attempt" = 1 ] || decide false "attempt $attempt: a run is re-run once at most"
case $event/$branch in
pull_request/* | push/main) ;;
*) decide false "a $event run on $branch: only pull requests' runs and main's are re-run" ;;
esac

now=$(sh "$here/retry.sh" -l "the branch's newest commit" gh api "repos/$head_repo/branches/$branch" --jq .commit.sha 2>/dev/null || true)
[ "$now" = "$sha" ] || decide false "$branch is now at ${now:-(gone)}, not $sha: a newer run has it"

jobs=$(sh "$here/retry.sh" -l "the run's jobs" gh api --paginate "repos/$GITHUB_REPOSITORY/actions/runs/$run/attempts/$attempt/jobs" \
	--jq '.jobs[] | select(.conclusion == "failure" or .conclusion == "timed_out") | [.name, ([.steps[]? | select(.conclusion == "failure") | .name] | join("|")), .id] | @tsv')
failed_jobs=$(printf '%s\n' "$jobs" | cut -f1 | grep -v -x "$GATE" | grep . | paste -sd ',' - | sed 's/,/, /g' || true)

[ -n "$failed_jobs" ] || decide false "no job failed but \"$GATE\""
change=$(printf '%s\n' "$jobs" | cut -f1 | grep -E "$ALWAYS_THE_CHANGE_JOBS" | head -1 || true)
[ -z "$change" ] || decide false "\"$change\" failed, which only the change can fix"
step=$(printf '%s\n' "$jobs" | cut -f2 | tr '|' '\n' | grep -E "$ALWAYS_THE_CHANGE_STEPS" | head -1 || true)
[ -z "$step" ] || decide false "the step \"$step\" failed, which only the change can fix"
for id in $(printf '%s\n' "$jobs" | awk -F '\t' -v gate="$GATE" '$1 != gate && $3 != "" {print $3}'); do
	log=$(sh "$here/retry.sh" -l "job $id's log" gh run view --job "$id" --log --repo "$GITHUB_REPOSITORY") ||
		decide false "job $id's log couldn't be read, so it isn't known to be safe to re-run"
	finding=$(printf '%s\n' "$log" | grep -oE "$NEVER_RETRIED" | head -1 || true)
	[ -z "$finding" ] || decide false "a failed job's log says \"$finding\": a security or privacy finding is never re-run"
done
decide true "first attempt, and no failure is one only the change can fix"
