#!/bin/sh
# Checks the version scripts (ADR 0024) in a throwaway clone, with made-up
# commits and made-up release tags on top of this checkout. Nothing is pushed:
# the clone has no remote.
#   - release-version.sh: nothing changed, or only documentation, is nothing
#     to release; code is a patch, and so is the first release after the
#     version left the repository (our image's tag in compose.yaml going from
#     MAJOR.MINOR to main); a file a deployment downloads is a minor version;
#     web/static/ counts as code; a release on a later commit, or this
#     commit's own finished release, is nothing to release, and its own
#     unfinished one is finished; --dev is <next>-dev+<commit>;
#   - stamp-version.sh writes VERSION and compose.yaml's tag, and puts the
#     repository's own back;
#   - check-versions.sh wants the repository's own unstamped files, and
#     ModuleVersion raised once per release when the module changes;
#   - release-plan.sh refuses a version whose tag is on another commit.
#
# usage: scripts/ci/release-version-checks.sh
set -eu
repo=$(cd "$(dirname "$0")/../.." && pwd)
work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT
failed=0
fail() {
	echo "FAIL: $*" >&2
	failed=1
}

git clone -q --no-hardlinks "$repo" "$work/r"
cd "$work/r"
git remote remove origin
git tag -l | xargs -r git tag -d >/dev/null
git config user.name test
git config user.email test@example.com
git checkout -q -B main "$(git -C "$repo" rev-parse HEAD)"
# The checkout's own uncommitted changes, so a local run checks what's about to
# be committed (in CI there are none).
(cd "$repo" && git diff HEAD) | git apply --allow-empty
for f in $(cd "$repo" && git ls-files --others --exclude-standard); do
	mkdir -p "$(dirname "$f")" && cp "$repo/$f" "$f"
done
git add -A && git commit -q --allow-empty -m "this checkout"
here=$(git rev-parse HEAD)

# A made-up release from before ADR 0024: VERSION and compose.yaml stamped and
# committed. Then this checkout's own files on top of it.
git checkout -q -B old "$here"
sh scripts/ci/stamp-version.sh 9.4.1 >/dev/null
git commit -qam "old-style release 9.4.1" && git tag -a v9.4.1 -m "made-up release" HEAD
git checkout -q -B main old
git checkout -q "$here" -- VERSION compose.yaml
git commit -qam "the version leaves the repository: VERSION 0.0.0-dev, compose.yaml :main"

# expect <what> <args> <release> <version>: release-version.sh's answer.
expect() {
	_what=$1 _args=$2 _release=$3 _version=$4
	# shellcheck disable=SC2086
	_out=$(sh scripts/ci/release-version.sh $_args)
	_got_release=$(printf '%s\n' "$_out" | sed -n 's/^release=//p')
	_got_version=$(printf '%s\n' "$_out" | sed -n 's/^version=//p')
	_why=$(printf '%s\n' "$_out" | sed -n 's/^why=//p')
	if [ "$_got_release" != "$_release" ] || [ "$_got_version" != "$_version" ]; then
		fail "$_what: release=$_got_release version=$_got_version ($_why), want release=$_release version=$_version"
	else
		echo "--- $_what: release=$_got_release version=$_got_version ($_why)"
	fi
}

expect "the version leaves the repository (VERSION, our image :9.4 to :main) since v9.4.1" HEAD true 9.4.2
git tag -a v9.4.2 -m "made-up release" HEAD
release=$(git rev-parse HEAD)

# commit <name> <file> <sed or text>: a change on a branch from the release.
commit() {
	git checkout -q -B "$1" "$release"
	case $3 in
	s[\|/#]*) sed -i "$3" "$2" ;;
	*) mkdir -p "$(dirname "$2")" && printf '%s\n' "$3" >>"$2" ;;
	esac
	git add -A && git commit -qm "$1"
}

git checkout -q -B empty "$release" && git commit -q --allow-empty -m "an empty commit"
expect "nothing changed since v9.4.2" HEAD false 9.4.3
commit docs-only docs/made-up-notes.md "made-up notes"
printf 'made-up line\n' >>SPEC.md && printf 'made-up line\n' >>README.md && printf 'made-up\n' >deploy/made-up.md
git add -A && git commit -qm "more documentation"
expect "documentation only (docs/, SPEC.md, README.md, deploy/made-up.md)" HEAD false 9.4.3
commit static web/static/LICENSE.md "made-up licence line"
expect "web/static/LICENSE.md (built into the server)" HEAD true 9.4.3
commit code internal/made_up.go "package internal"
expect "code" HEAD true 9.4.3
code=$(git rev-parse HEAD)
commit our-image compose.yaml 's|\(image: ghcr\.io/rikiku/pagerarchive:\).*|\19.9|'
expect "only our image's tag in compose.yaml" HEAD false 9.4.3
printf 'package x\n' >made_up.go && git add -A && git commit -qm "and code"
expect "our image's tag and code" HEAD true 9.4.3
commit monitoring deploy/monitoring/compose.yaml 's|prom/prometheus:v[0-9.]*|prom/prometheus:v9.99.9|'
expect "the monitoring stack's compose.yaml" HEAD true 9.5.0
commit authentik deploy/authentik/blueprints/pagerarchive.yaml "# made-up change"
expect "the configuration archive (deploy/authentik)" HEAD true 9.5.0
config=$(git rev-parse HEAD)

# Releases on the commit, or after it.
git checkout -q -B later "$code"
printf 'package internal\n' >internal/made_up_later.go && git add -A && git commit -qm "later"
git tag -a v9.4.3 -m "made-up release" HEAD
later=$(git rev-parse HEAD)
git checkout -q "$code"
PA_LATEST_RELEASE=v9.4.3 expect "a release on a later commit (v9.4.3) has it" HEAD false 9.4.3
git checkout -q "$later"
PA_LATEST_RELEASE=v9.4.3 expect "its own release, v9.4.3, is the Latest" HEAD false 9.4.3
PA_LATEST_RELEASE=v9.4.2 expect "its own release, v9.4.3, isn't the Latest yet (v9.4.2): finished" HEAD true 9.4.3
expect "its own release, v9.4.3, and no Latest given" HEAD false 9.4.3

# --dev: the next release's version, then -dev+<commit>.
git checkout -q "$code"
expect "--dev, code since v9.4.2" "--dev HEAD" true "9.4.3-dev+$(git rev-parse --short=7 HEAD)"
git checkout -q "$config"
expect "--dev, a minor change since v9.4.2" "--dev HEAD" true "9.5.0-dev+$(git rev-parse --short=7 HEAD)"
git checkout -q "$later"
expect "--dev on a released commit (v9.4.3)" "--dev HEAD" true "9.4.4-dev+$(git rev-parse --short=7 HEAD)"

# stamp-version.sh, and release-assets.sh after it.
git checkout -q -B stamp "$release"
sh scripts/ci/stamp-version.sh 9.5.0 >/dev/null
[ "$(cat VERSION)" = 9.5.0 ] || fail "stamped VERSION is $(cat VERSION)"
grep -q '^ *image: ghcr.io/rikiku/pagerarchive:9.5$' compose.yaml || fail "stamped 9.5.0, compose.yaml doesn't name :9.5"
sh scripts/ci/release-assets.sh "$work/assets" >/dev/null || fail "release-assets.sh refuses a stamped checkout"
grep -q '^ *image: ghcr.io/rikiku/pagerarchive:9.5$' "$work/assets/compose.yaml" || fail "the attached compose.yaml doesn't name :9.5"
dev=9.4.3-dev+$(git rev-parse --short=7 HEAD)
sh scripts/ci/stamp-version.sh "$dev" >/dev/null
grep -q '^ *image: ghcr.io/rikiku/pagerarchive:9.4$' compose.yaml || fail "stamped $dev, compose.yaml doesn't name :9.4"
echo "--- stamp-version.sh: VERSION $(cat VERSION), compose.yaml :$(sed -n 's/^ *image: ghcr.io\/rikiku\/pagerarchive://p' compose.yaml)"
for bad in 9.5 v9.5.0 9.5.0-rc1 "9.5.0-dev+XYZ1234"; do
	if sh scripts/ci/stamp-version.sh "$bad" >/dev/null 2>&1; then fail "stamp-version.sh takes $bad"; fi
done
sh scripts/ci/stamp-version.sh 0.0.0-dev >/dev/null
git diff --quiet || fail "stamp-version.sh 0.0.0-dev doesn't put the repository's own files back"
echo "--- stamp-version.sh 0.0.0-dev: the repository's own files again"

# check-versions.sh, as on a pull request.
versions() { sh scripts/ci/check-versions.sh HEAD >"$work/out" 2>&1; }
git checkout -q "$release"
versions || { cat "$work/out"; fail "check-versions.sh fails the repository's own files"; }
commit set-version VERSION 's/.*/9.4.3/'
if versions; then fail "check-versions.sh passes a pull request that sets VERSION"; else echo "--- VERSION set by hand: $(grep FAIL "$work/out" | head -n 1)"; fi
commit set-image compose.yaml 's|\(image: ghcr\.io/rikiku/pagerarchive:\).*|\19.4|'
if versions; then fail "check-versions.sh passes a pull request that names a version in compose.yaml"; else echo "--- compose.yaml set by hand: $(grep FAIL "$work/out" | head -n 1)"; fi
module=$(sed -n "s/^ *ModuleVersion *= *'\([^']*\)'.*/\1/p" powershell/PagerArchive/PagerArchive.psd1)
mmajor=${module%%.*} mrest=${module#*.}
raised=$mmajor.$((${mrest%%.*} + 1)).0
commit module-unraised powershell/PagerArchive/PagerArchive.psm1 "# made-up change"
if versions; then fail "check-versions.sh passes a module change without a raise"; else echo "--- the module changed, not raised: $(grep FAIL "$work/out" | head -n 1)"; fi
sed -i "s/^\( *ModuleVersion *= *'\)[^']*'/\1$raised'/" powershell/PagerArchive/PagerArchive.psd1
git commit -qam "raise ModuleVersion"
versions || { cat "$work/out"; fail "check-versions.sh fails a module change with ModuleVersion raised"; }
echo "--- the module changed, ModuleVersion $module to $raised: $(tail -n 1 "$work/out")"
# A second pull request before the same release, on top of the first: the raise is shared.
printf '# another made-up change\n' >>powershell/PagerArchive/PagerArchive.psm1
git commit -qam "another module change"
versions || { cat "$work/out"; fail "check-versions.sh fails a second module change before the same release"; }
echo "--- a second module change before the release: $(tail -n 1 "$work/out")"

# release-plan.sh: a version whose tag is on another commit.
git checkout -q "$config"
if PA_GALLERY_URL=http://127.0.0.1:9 sh scripts/ci/release-plan.sh HEAD 9.4.3 >"$work/out" 2>&1; then
	fail "release-plan.sh plans 9.4.3, whose tag is on another commit"
else
	echo "--- release-plan.sh, v9.4.3 on another commit: $(grep FAIL "$work/out" | head -n 1)"
fi

[ "$failed" -eq 0 ] && echo "Version scripts: OK"
exit "$failed"
