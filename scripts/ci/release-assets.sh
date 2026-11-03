#!/bin/sh
# Writes the files every release attaches (SPEC.md R84, ADR 0019) to a folder.
# The files are the repository's own, unchanged, so what's attached is what
# the checks ran. A77, A78 and the monitoring stack's check install from this
# folder too. It fails if compose.yaml doesn't name this release's image.
#
#   compose.yaml                 PagerArchive's stack
#   env.example                  its settings (GitHub renames a file starting with a dot)
#   pagerarchive-config.tar.gz   third-party configuration, unpacked next to compose.yaml
#   monitoring-compose.yaml      the example monitoring stack
#   monitoring-env.example       its settings
#
# usage: scripts/ci/release-assets.sh <folder>
#        scripts/ci/release-assets.sh --list
set -eu
ASSETS="compose.yaml env.example pagerarchive-config.tar.gz monitoring-compose.yaml monitoring-env.example"
if [ "$1" = --list ]; then
	echo "$ASSETS" | tr ' ' '\n'
	exit 0
fi
mkdir -p "$1"
out=$(cd "$1" && pwd)
cd "$(dirname "$0")/../.."
. scripts/ci/versions-lib.sh

failed=0
check_compose_image "$(tr -d ' \r\n' <VERSION)"
[ "$failed" -eq 0 ] || exit 1

cp compose.yaml "$out/compose.yaml"
cp .env.example "$out/env.example"
cp deploy/monitoring/compose.yaml "$out/monitoring-compose.yaml"
cp deploy/monitoring/.env.example "$out/monitoring-env.example"
# The same bytes from the same files, whoever builds it.
tar --sort=name --owner=0 --group=0 --numeric-owner --mtime=@0 --mode=u+rwX,go+rX -cf - \
	deploy/authentik deploy/postgres deploy/monitoring/alloy deploy/monitoring/grafana \
	deploy/monitoring/loki deploy/monitoring/prometheus deploy/monitoring/tempo |
	gzip -n >"$out/pagerarchive-config.tar.gz"
