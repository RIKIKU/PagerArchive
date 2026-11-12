#!/bin/sh
# Checks changes.sh (ADR 0025): for made-up lists of changed files, exactly
# the jobs each should run. Also that every file in the repository has an
# area that isn't "everything" unless it's meant to (a new top-level file or
# folder runs everything, which is safe but slow: a warning says to give it
# an area), and that
# ci.yml's jobs and changes.sh's CHECK_JOBS agree.
#
# usage: scripts/ci/changes-checks.sh
set -eu
cd "$(dirname "$0")/../.."
failed=0
fail() {
	echo "FAIL: $*" >&2
	failed=1
}

# expect <jobs that run, or "none" or "all"> <file>...
expect() {
	want=$1
	shift
	got=$(printf '%s\n' "$@" | sh scripts/ci/changes.sh --files | sed -n 's/^\([a-z0-9-]*\)=true$/\1/p' | grep -vx all | tr '\n' ' ' | sed 's/ $//')
	every=$(sed -n 's/^CHECK_JOBS="\(.*\)"$/\1/p' scripts/ci/changes.sh)
	case $want in
	none) want= ;;
	all) want=$every ;;
	esac
	# shellcheck disable=SC2086 # a list of words
	sorted() { printf '%s\n' $1 | sort | tr '\n' ' ' | sed 's/ $//'; }
	if [ "$(sorted "$got")" = "$(sorted "$want")" ]; then
		echo "--- $*: ${got:-nothing}"
	else
		fail "$*: runs '${got:-nothing}', want '${want:-nothing}'"
	fi
}

GO="test search import web stack stack-e2e upgrade install backups images budget monitoring govulncheck"
STACKS="stack stack-e2e import upgrade install backups budget monitoring"

expect "$GO" internal/server/server.go
expect "$GO" go.mod go.sum
expect "$GO" internal/store/migrations/00099_made_up.sql
expect "$GO" scripts/a15/load.go
expect "web stack-e2e install backups images" web/src/lib/SearchBox.svelte
expect "web stack-e2e install backups images" web/package-lock.json
expect "web stack-e2e install backups images" web/static/map/sprites/LICENSE.md
expect "powershell stack" powershell/PagerArchive/PagerArchive.psd1
expect "test $STACKS" compose.yaml
expect "test $STACKS" deploy/authentik/blueprints/pagerarchive.yaml
expect "test upgrade install backups monitoring" deploy/backup/backup.sh
expect "test monitoring" deploy/monitoring/grafana/dashboards/requests.json
expect "images $STACKS" Dockerfile
expect "images stack-e2e install monitoring" Dockerfile.client
expect "web" scripts/e2e.sh
expect "upgrade" scripts/a77.sh
expect "none" SPEC.md docs/adr/0025-ci-speed.md README.md deploy/made-up.md
expect "none" scripts/mutation-checks.sh .github/renovate.json5 .github/release.yml
expect "powershell stack" docs/powershell-guidelines.md powershell/tests/Meta.Tests.ps1
expect "web stack-e2e install backups images test monitoring" web/src/app.css deploy/monitoring/alloy/config.alloy
expect all .github/workflows/ci.yml
expect all scripts/ci/retry.sh
expect all .github/actions/upload/action.yml
expect all profiles/example/golden-fire.json
expect all testdata/icons/example-icon.png
expect all deploy/test/compose.yaml
expect all scripts/fixture.sql
expect all scripts/stack-lib.sh
expect all a-new-top-level-file
expect all web/src/lib/SearchBox.svelte made-up/new-folder/file.txt

# Each file in the repository: those that run everything are meant to.
meant='^(\.github/(workflows|actions)/|scripts/ci/|profiles/|testdata/|deploy/test/|deploy/example-map/|scripts/fixture\.sql$|scripts/stack-lib\.sh$|scripts/install-lib\.sh$|scripts/test-[a-z-]*/|\.gitattributes$|\.gitignore$|LICENSE$)'
unmeant=$(git ls-files | sh scripts/ci/changes.sh --areas | sed -n 's/^all //p' | grep -Ev "$meant" || true)
if [ -n "$unmeant" ]; then
	# Safe (they run every check) but slow, so a warning, not a failure.
	echo "::warning title=Files with no area::These run every check, as changes.sh has no area for them: give them one. $(printf '%s\n' "$unmeant" | tr '\n' ' ')"
else
	echo "--- every file in the repository has an area, or is meant to run everything"
fi

# ci.yml's check jobs are CHECK_JOBS: each has an if on its own output.
# shellcheck disable=SC2013 # CHECK_JOBS is a list of words
for job in $(sed -n 's/^CHECK_JOBS="\(.*\)"$/\1/p' scripts/ci/changes.sh); do
	grep -q "^  $job:\$" .github/workflows/ci.yml || fail "CHECK_JOBS names $job, which ci.yml hasn't"
	awk -v job="  $job:" '$0 == job {inside = 1; next} inside && /^  [a-z]/ {exit} inside' .github/workflows/ci.yml |
		grep -qF "needs.changes.outputs.$job == 'true'" || fail "ci.yml's $job has no if on needs.changes.outputs.$job"
done
echo "--- ci.yml's jobs follow changes.sh"

[ "$failed" -eq 0 ] || exit 1
echo "Changes checks: OK"
