#!/bin/sh
# Pulls the third-party images a job's builds and stacks use, each through
# retry.sh (ADR 0025), so a registry that fails to answer once doesn't fail
# the check that needed it. The builds and `docker compose up` then find them
# here. Our own images (built by the job) and ones named by a variable are
# left out.
#
# usage: scripts/ci/pull-images.sh <Dockerfile | compose file | script | image>...
#   (PULL_DRY_RUN=1: only list what it would pull)
#   e.g. scripts/ci/pull-images.sh Dockerfile compose.yaml deploy/test/compose.yaml
set -eu
here=$(cd "$(dirname "$0")" && pwd)

images=$(for arg in "$@"; do
	case $arg in
	*Dockerfile*)
		# FROM [--platform=…] <image> [AS <stage>]; a stage's own name isn't an image.
		stages=$(sed -n 's/^FROM .* AS \([A-Za-z0-9_-]*\).*/\1/p' "$arg")
		sed -n 's/^FROM \(--[^ ]* \)*\([^ ]*\).*/\2/p' "$arg" | while IFS= read -r image; do
			printf '%s\n' "$stages" | grep -qx "$image" || echo "$image"
		done
		;;
	*.yaml | *.yml)
		tr -d '\r' <"$arg" | sed -n 's/^[[:space:]]*image:[[:space:]]*"\{0,1\}\([^"[:space:]]*\)"\{0,1\}[[:space:]]*$/\1/p'
		;;
	*.sh)
		# A script's own images, pinned by digest (install-lib.sh's registry).
		grep -oE '[a-z0-9][a-z0-9./_-]*:[A-Za-z0-9._-]+@sha256:[0-9a-f]{64}' "$arg" || true
		;;
	*) echo "$arg" ;;
	esac
done | grep -v -e '\$' -e '^ghcr\.io/rikiku/' | sort -u)

for image in $images; do
	${PULL_DRY_RUN:+echo} sh "$here/retry.sh" -l "docker pull $image" docker pull -q "$image"
done
