#!/bin/sh
# The version checks on a branch or pull request (SPEC.md 12.1 and R63, ADR 0016):
#   - VERSION is MAJOR.MINOR.PATCH and higher than main's;
#   - compose.yaml names that version's image (R84a);
#   - if the branch changes powershell/PagerArchive/, the manifest's
#     ModuleVersion is higher than main's.
# Each failure says what to change. Needs main's history and the v* tags
# (actions/checkout with fetch-depth: 0).
#
# usage: scripts/ci/check-versions.sh [<main>]   (default origin/main)
set -eu
cd "$(dirname "$0")/../.."
. scripts/ci/versions-lib.sh

MAIN=${1:-origin/main}
failed=0

version=$(tr -d ' \r\n' <VERSION)
main_version=$(version_at "$MAIN")
echo "VERSION: $version; main's: ${main_version:-none}"
if ! is_version "$version"; then
	fail VERSION "VERSION is '$version', which isn't a semantic version: set it to MAJOR.MINOR.PATCH, higher than main's $main_version (a milestone raises the minor number, a fix the patch number)."
elif [ -n "$main_version" ] && ! higher "$version" "$main_version"; then
	fail VERSION "VERSION is $version, which isn't higher than main's $main_version: raise it (a milestone raises the minor number, a fix the patch number), so this merge is a new release."
fi

check_compose_image "$version"

base=$(git merge-base "$MAIN" HEAD)
if module_changed "$base" HEAD; then
	module_version=$(module_version_at HEAD)
	main_module_version=$(module_version_at "$MAIN")
	echo "The module changed (powershell/PagerArchive/). ModuleVersion: $module_version; main's: $main_module_version"
	if ! is_version "$module_version"; then
		fail "$MANIFEST" "ModuleVersion is '$module_version', which isn't MAJOR.MINOR.PATCH: set it in $MANIFEST, higher than main's $main_module_version."
	elif ! higher "$module_version" "$main_module_version"; then
		fail "$MANIFEST" "This branch changes powershell/PagerArchive/ but ModuleVersion is $module_version, which isn't higher than main's $main_module_version: raise ModuleVersion in $MANIFEST."
	fi
else
	echo "The module didn't change (powershell/PagerArchive/): its version can stay $(module_version_at HEAD)."
fi

[ "$failed" -eq 0 ] && echo "Versions: OK"
exit "$failed"
