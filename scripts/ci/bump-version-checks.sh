#!/bin/sh
# Checks bump-version.sh (R85c, ADR 0022) in a throwaway clone, as Renovate
# runs it: on an update's files before they're committed, on top of main.
#   - an update to a Dockerfile is a patch, and check-versions.sh passes it;
#   - an update to the monitoring stack's compose.yaml is a minor version with
#     the API's image renamed, and check-versions.sh passes it;
#   - two patch updates open at once both want the same version; once one
#     merges, the other, rebased and bumped again, takes the next one and
#     passes, where without the rebase it would fail.
#
# usage: scripts/ci/bump-version-checks.sh
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
git config user.name test
git config user.email test@example.com
git checkout -q -B main "$(git -C "$repo" rev-parse HEAD)"
# The worktree's own changes, so this checks what's being committed.
(cd "$repo" && git diff HEAD) | git apply --allow-empty
for f in $(cd "$repo" && git ls-files --others --exclude-standard); do
	mkdir -p "$(dirname "$f")" && cp "$repo/$f" "$f"
done
git add -A && git commit -q --allow-empty -m "main"
base=$(tr -d ' \r\n' <VERSION)
major=${base%%.*} rest=${base#*.}
minor=${rest%%.*} patch=${rest#*.}
echo "main's VERSION: $base"

# update <branch> <file> <sed>: an update as Renovate makes it, from main.
update() {
	git checkout -q -B "$1" main
	sed -i "$3" "$2"
	sh scripts/ci/bump-version.sh
	git commit -qam "update $2"
}
versions_ok() {
	sh scripts/ci/check-versions.sh main >"$work/out" 2>&1
}

update renovate/base-images Dockerfile 's/^FROM golang:1\.27\.2@sha256:[0-9a-f]*/FROM golang:1.27.3@sha256:e519d3b0a5b285e5aa53ab9eef02bc6bbb9cb04608389b743a136a9ce75f0d1e/'
[ "$(cat VERSION)" = "$major.$minor.$((patch + 1))" ] || fail "a base image's update is $(cat VERSION), not a patch of $base"
versions_ok || { cat "$work/out"; fail "check-versions.sh fails a base image's update"; }
git diff --quiet main -- compose.yaml || fail "a patch changed compose.yaml"
echo "--- a base image: $base to $(cat VERSION), check-versions.sh: $(tail -n 1 "$work/out")"

update renovate/prom-prometheus-3.x deploy/monitoring/compose.yaml 's|prom/prometheus:v[0-9.]*|prom/prometheus:v3.15.2|'
[ "$(cat VERSION)" = "$major.$((minor + 1)).0" ] || fail "an update to the monitoring stack is $(cat VERSION), not $major.$((minor + 1)).0"
grep -q "ghcr.io/rikiku/pagerarchive:$major.$((minor + 1))\$" compose.yaml || fail "compose.yaml doesn't name $major.$((minor + 1))"
versions_ok || { cat "$work/out"; fail "check-versions.sh fails an update to the monitoring stack"; }
echo "--- the monitoring stack's compose.yaml: $base to $(cat VERSION), check-versions.sh: $(tail -n 1 "$work/out")"

# Two at once: both from the same main, both a patch.
update renovate/libraries-patch web/package.json 's/"svelte": "\^\([0-9.]*\)"/"svelte": "^\1-test"/'
[ "$(cat VERSION)" = "$(git show renovate/base-images:VERSION)" ] || fail "two updates from one main want different versions"
git checkout -q main
git merge -q --no-ff -m "merge base images" renovate/base-images
echo "--- renovate/base-images merged: main is $(cat VERSION)"
git checkout -q renovate/libraries-patch
if versions_ok; then
	fail "check-versions.sh passes the second update with main's own version"
else
	echo "--- renovate/libraries-patch, not rebased: check-versions.sh fails it: $(grep FAIL "$work/out" | head -n 1)"
fi
# Renovate's rebase: the update again, on the new main.
git checkout -q -B renovate/libraries-patch main
sed -i 's/"svelte": "\^\([0-9.]*\)"/"svelte": "^\1-test"/' web/package.json
sh scripts/ci/bump-version.sh
git commit -qam "update web/package.json"
[ "$(cat VERSION)" = "$major.$minor.$((patch + 2))" ] || fail "the rebased update is $(cat VERSION), not $major.$minor.$((patch + 2))"
versions_ok || { cat "$work/out"; fail "check-versions.sh fails the rebased update"; }
echo "--- renovate/libraries-patch, rebased: $(cat VERSION), check-versions.sh: $(tail -n 1 "$work/out")"

# Run twice on the same files (Renovate may), it gives the same answer.
git checkout -q -B renovate/twice main
sed -i 's|prom/prometheus:v[0-9.]*|prom/prometheus:v3.15.2|' deploy/monitoring/compose.yaml
sh scripts/ci/bump-version.sh >/dev/null
first=$(cat VERSION)
sh scripts/ci/bump-version.sh >/dev/null
[ "$(cat VERSION)" = "$first" ] || fail "run twice, bump-version.sh gives $first, then $(cat VERSION)"

[ "$failed" -eq 0 ] && echo "bump-version.sh: OK"
exit "$failed"
