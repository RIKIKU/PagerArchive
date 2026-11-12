#!/bin/sh
# Layer 1 of CI's retries (ADR 0025): a network step tries again by itself.
# Runs a command up to 3 times, waiting 15 s and then 45 s between tries, for
# what fails because of the network and never because of the change: apt,
# image pulls, pushes to GHCR, downloads, GitHub's API. A command that passes
# only on a retry is reported as flaky (flaky.sh); one that fails every try
# fails the step with its last exit status. Never put a check behind it.
#
# usage: scripts/ci/retry.sh [-n <tries>] [-w <first wait, s>] [-l <label>] [--] <command> [<argument>...]
#   PA_RETRY_WAIT sets the first wait (retry-checks.sh: 0).
#   e.g. scripts/ci/retry.sh -l "apt-get install" sh -c 'apt-get update -qq && apt-get install -y -qq jq'
set -eu
tries=3 wait=${PA_RETRY_WAIT:-15} label=
while [ $# -gt 0 ]; do
	case $1 in
	-n) tries=$2 && shift 2 ;;
	-w) wait=$2 && shift 2 ;;
	-l) label=$2 && shift 2 ;;
	--) shift && break ;;
	*) break ;;
	esac
done
[ $# -gt 0 ] || { echo "usage: retry.sh [-n tries] [-w wait] [-l label] [--] command..." >&2; exit 2; }
[ -n "$label" ] || label=$*
here=$(cd "$(dirname "$0")" && pwd)

try=1
while :; do
	status=0
	"$@" || status=$?
	if [ "$status" -eq 0 ]; then
		# To standard error, so a command whose output is captured keeps only its own.
		[ "$try" -eq 1 ] || sh "$here/flaky.sh" "$label" "passed on try $try of $tries" >&2
		exit 0
	fi
	if [ "$try" -ge "$tries" ]; then
		echo "retry: $label failed on every try ($tries), the last with exit status $status" >&2
		exit "$status"
	fi
	echo "retry: $label failed with exit status $status on try $try of $tries; trying again in $wait s" >&2
	sleep "$wait"
	wait=$((wait * 3))
	try=$((try + 1))
done
