#!/bin/sh
# Raises VERSION for an automatic update (SPEC.md R85c, ADR 0022), as
# check-versions.sh wants it: one patch above the base's, or, if the update
# changes compose.yaml or the configuration archive's files, the next minor
# version, with the API's image in compose.yaml named by it (R84b).
#
# Renovate runs it after each update, on its branch's files before they're
# committed (postUpgradeTasks), and again whenever it rebases the branch on a
# newer main. So the version always follows the base the branch is on, and of
# two automatic pull requests open at once, the one rebased after the other
# merged takes the next number.
#
# usage: scripts/ci/bump-version.sh [<base>]   (default HEAD, the branch's base
#        while Renovate works on it; the working tree holds the update)
set -eu
cd "$(dirname "$0")/../.."
. scripts/ci/versions-lib.sh

BASE=${1:-HEAD}
base_version=$(version_at "$BASE")
if ! is_version "$base_version"; then
	echo "bump-version: the base's VERSION is '$base_version', which isn't MAJOR.MINOR.PATCH" >&2
	exit 1
fi
major=${base_version%%.*} rest=${base_version#*.}
minor=${rest%%.*} patch=${rest#*.}

# shellcheck disable=SC2086
if git diff --quiet "$BASE" -- $CONFIG_PATHS; then
	version=$major.$minor.$((patch + 1))
	why="a patch: nothing a deployment downloads changed"
else
	version=$major.$((minor + 1)).0
	why="a minor version: compose.yaml or the configuration archive changed"
	sed -i "s|^\([[:space:]]*image:[[:space:]]*ghcr\.io/rikiku/pagerarchive:\).*\$|\1$major.$((minor + 1))|" compose.yaml
fi
printf '%s\n' "$version" >VERSION
echo "bump-version: $base_version to $version, $why"
