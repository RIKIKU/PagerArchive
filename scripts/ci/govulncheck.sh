#!/bin/sh
# Known vulnerabilities in the Go code and its modules, as govulncheck finds
# them (ADR 0025): it reports only what the code can reach, unlike OSV's
# alerts (ADR 0024), which name a module whether or not the vulnerable code
# is called. Information first: every finding is listed in the job's summary,
# and the job fails only for a vulnerability the code calls that has a fixed
# version, which is the one case with something to do (update the module, or
# Go for the standard library).
#
#   called, fixed      an error annotation each; the job fails
#   called, no fix     a warning each
#   imported/required  counted in the summary: the code doesn't call it
#
# usage: scripts/ci/govulncheck.sh [<findings JSON>]
#   With a file (govulncheck -format json's output) it reads that instead of
#   running govulncheck, for checking this script.
set -eu
cd "$(dirname "$0")/../.."
VERSION=v1.9.0

if [ $# -gt 0 ]; then
	json=$1
else
	json=$(mktemp)
	# -format json exits 0 whatever it finds, and non-zero only when it
	# couldn't check (the vulnerability database or a module didn't download):
	# that's tried again (retry.sh).
	sh scripts/ci/retry.sh -l "govulncheck $VERSION" sh -c "go run golang.org/x/vuln/cmd/govulncheck@$VERSION -format json ./... >'$json'"
fi

jq -r 'select(.config) | .config | "govulncheck \(.scanner_version), database \(.db_last_modified), \(.go_version)"' "$json"

# One line per vulnerability, at its most precise level: called (a trace to a
# function), imported (a package), or required (only the module). Tab-separated:
# level, id, module, version found, fixed version (or "none"), summary.
findings=$(jq -rs '
	(map(select(.osv) | .osv | {key: .id, value: (.summary // "")}) | from_entries) as $summary
	| map(select(.finding) | .finding)
	| group_by(.osv)
	| map(
		(map(if .trace[0].function then 3 elif .trace[0].package then 2 else 1 end) | max) as $level
		| [ (["", "required", "imported", "called"][$level]), .[0].osv, .[0].trace[0].module, (.[0].trace[0].version // ""),
		    ((map(.fixed_version // empty) | first) // "none"), ($summary[.[0].osv] // "") ]
		| @tsv)
	| .[]' "$json")

count() { printf '%s\n' "$findings" | awk -F '\t' -v l="$1" -v f="$2" '$1 == l && (f == "" || (f == "fixed" && $5 != "none") || (f == "none" && $5 == "none"))' | grep -c . || true; }
fail_n=$(count called fixed)
warn_n=$(count called none)
imported_n=$(count imported "")
required_n=$(count required "")

printf '%s\n' "$findings" | while IFS="$(printf '\t')" read -r level id module version fixed summary; do
	[ -n "$level" ] || continue
	echo "$level: $id in $module $version (fixed in $fixed): $summary"
	if [ "$level" = called ] && [ "$fixed" != none ]; then
		echo "::error title=Vulnerability the code calls::$id in $module $version, fixed in $fixed: $summary. Update $module (for stdlib, Go)."
	elif [ "$level" = called ]; then
		echo "::warning title=Vulnerability the code calls, no fix yet::$id in $module $version: $summary"
	fi
done

if [ -n "${GITHUB_STEP_SUMMARY:-}" ]; then
	{
		echo "### govulncheck $VERSION"
		echo
		echo "Called with a fix: $fail_n (fails the job). Called, no fix yet: $warn_n. Imported but not called: $imported_n. Only required: $required_n."
		if [ -n "$findings" ]; then
			echo
			echo "| Level | Vulnerability | Module | Found | Fixed in | Summary |"
			echo "|---|---|---|---|---|---|"
			printf '%s\n' "$findings" | awk -F '\t' '{printf "| %s | %s | %s | %s | %s | %s |\n", $1, $2, $3, $4, $5, $6}'
		fi
	} >>"$GITHUB_STEP_SUMMARY"
fi

echo "called with a fix: $fail_n; called, no fix: $warn_n; imported only: $imported_n; required only: $required_n"
[ "$fail_n" -eq 0 ] || exit 1
