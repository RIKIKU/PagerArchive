#!/bin/sh
# Reports something that passed only on a retry (ADR 0025), so a retry never
# hides it: a warning annotation, "flaky: <what>", and a line in the job's
# summary. Off GitHub Actions it's just printed.
#
# usage: scripts/ci/flaky.sh <what> [<how>]
#   e.g. scripts/ci/flaky.sh "apt-get install postgresql-client" "passed on try 2 of 3"
set -eu
what=$1 how=${2:-passed on a retry}
# Annotation text: %, CR and LF are escaped (GitHub's workflow commands).
esc() { printf '%s' "$1" | sed -e 's/%/%25/g' -e 's/\r/%0D/g' | awk 'NR > 1 {printf "%%0A"} {printf "%s", $0}'; }
echo "::warning title=Flaky::flaky: $(esc "$what") ($(esc "$how"))"
if [ -n "${GITHUB_STEP_SUMMARY:-}" ]; then
	if ! grep -q '^### Passed only on a retry$' "$GITHUB_STEP_SUMMARY" 2>/dev/null; then
		printf '\n### Passed only on a retry\n\nEach is a flake to look into: it failed, then passed when tried again (ADR 0025).\n\n' >>"$GITHUB_STEP_SUMMARY"
	fi
	printf -- '- flaky: %s (%s)\n' "$what" "$how" >>"$GITHUB_STEP_SUMMARY"
fi
