#!/bin/sh
# Reports A75's and A76's timings (ADR 0046): a table in the job's summary,
# and a warning annotation, "slow: …", for each step over its limit. In a gate
# (a pull request, main, a release: PAGERARCHIVE_SPEED unset or "gate") a slow
# step never fails: only their correctness checks do, in the tests themselves.
# In the weekly benchmark (PAGERARCHIVE_SPEED=benchmark) it fails when a step
# took more than twice its limit, a big slowdown, not a slow runner. Off
# GitHub Actions the table and warnings are just printed.
#
# usage: scripts/speed-report.sh <title> <timings> [<notes>]
#   timings: one step a line, tab-separated: step, seconds, limit in seconds,
#            detail (which search, the rate…)
#   notes:   a text file shown under the table as it is (A76's memory peaks)
set -eu
title=$1 timings=$2 notes=${3:-}
mode=${PAGERARCHIVE_SPEED:-gate}
# A big slowdown: the shared runners' times for one commit vary up to about
# 1.9 times (ADR 0046's history), so twice the limit is past the noise.
bound=2
case $mode in
gate | benchmark) ;;
*)
	echo "speed-report: PAGERARCHIVE_SPEED is gate or benchmark, not $mode" >&2
	exit 2
	;;
esac

# Annotation text: %, CR and LF are escaped (GitHub's workflow commands).
esc() { printf '%s' "$1" | sed -e 's/%/%25/g' -e 's/\r/%0D/g' | awk 'NR > 1 {printf "%%0A"} {printf "%s", $0}'; }
summary() { [ -z "${GITHUB_STEP_SUMMARY:-}" ] || printf "$@" >>"$GITHUB_STEP_SUMMARY"; }

if [ ! -s "$timings" ]; then
	if [ "$mode" = benchmark ]; then
		echo "::error title=No timings::$(esc "$title"): no timings in $timings: the run ended before its steps were timed"
		exit 1
	fi
	echo "--- $title: no timings in $timings (the run ended before its steps were timed)"
	summary '\n### %s\n\nNo timings: the run ended before its steps were timed.\n' "$title"
	exit 0
fi

if [ "$mode" = benchmark ]; then
	how="the weekly benchmark: a step over $bound times its limit fails"
else
	how="a gate: a step over its limit is a warning, never a failure"
fi
echo "--- $title ($how)"
summary '\n### %s\n\nTimings, %s (ADR 0046).\n\n| Step | Seconds | Limit (s) | Notes |\n|---|---:|---:|---|\n' "$title" "$how"
failed=0
while IFS="$(printf '\t')" read -r step took limit detail; do
	[ -n "$step" ] || continue
	verdict=$(awk -v t="$took" -v l="$limit" -v b="$bound" 'BEGIN { print (t > b * l ? "far" : (t > l ? "slow" : "ok")) }')
	shown=$(awk -v t="$took" 'BEGIN { printf "%.3f", t }')
	case $verdict in
	ok) mark= ;;
	slow) mark="slow" ;;
	far) mark="slow, over $bound times the limit" ;;
	esac
	printf '  %-60s %10s s  limit %6s s  %s%s\n' "$step" "$shown" "$limit" "${mark:+$mark: }" "$detail"
	summary '| %s | %s | %s | %s%s |\n' "$step" "$shown" "$limit" "${mark:+**$mark**: }" "$detail"
	[ "$verdict" = ok ] && continue
	what="slow: $step took $shown s, over its limit of $limit s${detail:+ ($detail)}"
	if [ "$verdict" = far ] && [ "$mode" = benchmark ]; then
		echo "::error title=Slow::$(esc "$what"), over $bound times the limit"
		failed=1
	else
		echo "::warning title=Slow::$(esc "$what")"
	fi
done <"$timings"
if [ -n "$notes" ] && [ -s "$notes" ]; then
	cat "$notes"
	summary '\n```\n%s\n```\n' "$(cat "$notes")"
fi
if [ "$failed" -ne 0 ]; then
	echo "--- $title: a step took more than $bound times its limit"
	exit 1
fi
