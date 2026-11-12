#!/bin/sh
# Writes a build's version into the checkout before anything is built or
# tested (ADR 0024): VERSION, which the server and the client embed, and our
# image's tag in compose.yaml, the version's MAJOR.MINOR (R84). The
# repository's own files say 0.0.0-dev and :main; nothing stamped is committed.
#
# A release's version is MAJOR.MINOR.PATCH; any other build's is
# <next>-dev+<commit> (release-version.sh --dev), so it's never taken for a
# release. 0.0.0-dev puts the repository's own files back.
#
# usage: scripts/ci/stamp-version.sh <version>
set -eu
cd "$(dirname "$0")/../.."
. scripts/ci/versions-lib.sh

version=${1:?usage: stamp-version.sh <version>}
if ! is_version "$version" && ! is_dev_version "$version" && [ "$version" != "$UNSTAMPED_VERSION" ]; then
	echo "stamp-version: '$version' is neither MAJOR.MINOR.PATCH nor <MAJOR.MINOR.PATCH>-dev+<commit>" >&2
	exit 1
fi
printf '%s\n' "$version" >VERSION
tag=$(image_tag_for "$version")
sed -i "s|^\([[:space:]]*image:[[:space:]]*ghcr\.io/rikiku/pagerarchive:\).*\$|\1$tag|" compose.yaml
failed=0
check_compose_image "$version"
[ "$failed" -eq 0 ] || exit 1
echo "stamp-version: VERSION $version, compose.yaml names ghcr.io/rikiku/pagerarchive:$tag"
