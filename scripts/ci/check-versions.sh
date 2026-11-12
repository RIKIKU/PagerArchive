#!/bin/sh
# The version checks on a pull request (SPEC.md 12.1 and R63; ADR 0016, ADR 0024):
#   - VERSION and compose.yaml are main's own, unstamped: VERSION says
#     0.0.0-dev and compose.yaml names our image :main. A release's version
#     is worked out when it's cut, and lives in its tag, so a pull request
#     never sets one;
#   - if the module (powershell/PagerArchive/) changed since the previous
#     release, the manifest's ModuleVersion is higher than that release's. Two
#     pull requests changing the module before the same release share one
#     raise: the second finds it raised already.
# Each failure says what to change. Run on the pull request's merge commit,
# with its history and the v* tags (actions/checkout with fetch-depth: 0).
#
# usage: scripts/ci/check-versions.sh [<commit>]   (default HEAD)
set -eu
cd "$(dirname "$0")/../.."
. scripts/ci/versions-lib.sh

COMMIT=${1:-HEAD}
failed=0

version=$(git show "$COMMIT:VERSION" | tr -d ' \r\n')
echo "VERSION: $version"
if [ "$version" != "$UNSTAMPED_VERSION" ]; then
	fail VERSION "VERSION is '$version': set it back to $UNSTAMPED_VERSION. A pull request doesn't set the version: the Release workflow works it out from the previous release when it cuts one (ADR 0024)."
fi
named=$(git show "$COMMIT:compose.yaml" | tr -d '\r' | sed -n 's/^[[:space:]]*image:[[:space:]]*ghcr\.io\/rikiku\/pagerarchive:\(.*\)$/\1/p')
if [ "$named" != "$UNSTAMPED_IMAGE_TAG" ]; then
	fail compose.yaml "compose.yaml's API image is ghcr.io/rikiku/pagerarchive:${named:-(none)}: set it back to ghcr.io/rikiku/pagerarchive:$UNSTAMPED_IMAGE_TAG. A release writes its own MAJOR.MINOR there when it's cut (ADR 0024)."
fi

previous=$(previous_release "$COMMIT")
if [ -z "$previous" ]; then
	fail VERSION "No v* tag in this commit's history: fetch the tags (actions/checkout with fetch-depth: 0)."
elif module_changed "$previous" "$COMMIT"; then
	module_version=$(module_version_at "$COMMIT")
	released=$(module_version_at "$previous")
	echo "The module changed since $previous (powershell/PagerArchive/). ModuleVersion: $module_version; $previous's: $released"
	if ! is_version "$module_version"; then
		fail "$MANIFEST" "ModuleVersion is '$module_version', which isn't MAJOR.MINOR.PATCH: set it in $MANIFEST, higher than $previous's $released."
	elif ! higher "$module_version" "$released"; then
		fail "$MANIFEST" "powershell/PagerArchive/ changed since $previous, but ModuleVersion is $module_version, which isn't higher than $previous's $released: raise ModuleVersion in $MANIFEST, or the next release won't publish the module."
	fi
else
	echo "The module didn't change since $previous (powershell/PagerArchive/): its version can stay $(module_version_at "$COMMIT")."
fi

[ "$failed" -eq 0 ] && echo "Versions: OK"
exit "$failed"
