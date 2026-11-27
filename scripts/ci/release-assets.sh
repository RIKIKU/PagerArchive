#!/bin/sh
# Writes the files every release attaches (SPEC.md R84, ADR 0019) to a folder.
# The files are the repository's own, unchanged, so what's attached is what
# the checks ran. A77, A78 and the monitoring stack's check install from this
# folder too. It fails if compose.yaml doesn't name this minor version's image.
#
#   compose.yaml                 PagerArchive's stack
#   env.example                  its settings (GitHub renames a file starting with a dot)
#   pagerarchive-config.tar.gz   third-party configuration, unpacked next to compose.yaml
#   monitoring-compose.yaml      the example monitoring stack
#   monitoring-env.example       its settings
#   client-compose.yaml          a receiving client on its own host (docs/client.md)
#   client-env.example           its settings
#   client-auto-upgrade.sh       its automatic upgrades (R84n, ADR 0053)
#
# usage: scripts/ci/release-assets.sh <folder>
#        scripts/ci/release-assets.sh --list
#        scripts/ci/release-assets.sh --list-server   only the server's, which
#                                  every release since v0.15 attaches
set -eu
SERVER_ASSETS="compose.yaml env.example pagerarchive-config.tar.gz monitoring-compose.yaml monitoring-env.example"
CLIENT_ASSETS="client-compose.yaml client-env.example client-auto-upgrade.sh"
case $1 in
--list)
	echo "$SERVER_ASSETS $CLIENT_ASSETS" | tr ' ' '\n'
	exit 0
	;;
--list-server)
	echo "$SERVER_ASSETS" | tr ' ' '\n'
	exit 0
	;;
esac
mkdir -p "$1"
out=$(cd "$1" && pwd)
cd "$(dirname "$0")/../.."
. scripts/ci/versions-lib.sh

failed=0
check_compose_image "$(tr -d ' \r\n' <VERSION)"
check_client_compose_image "$(tr -d ' \r\n' <VERSION)"
[ "$failed" -eq 0 ] || exit 1

cp deploy/client/compose.yaml "$out/client-compose.yaml"
cp deploy/client/.env.example "$out/client-env.example"
cp deploy/client/auto-upgrade.sh "$out/client-auto-upgrade.sh"
cp compose.yaml "$out/compose.yaml"
cp .env.example "$out/env.example"
cp deploy/monitoring/compose.yaml "$out/monitoring-compose.yaml"
cp deploy/monitoring/.env.example "$out/monitoring-env.example"
# The same bytes from the same files, whoever builds it. The folders a
# companion app drops its own files into (R76a, R84l) are in it empty, so
# unpacking a release makes them but never writes in them: their READMEs are
# the repository's only.
tar --sort=name --owner=0 --group=0 --numeric-owner --mtime=@0 --mode=u+rwX,go+rX \
	--exclude='deploy/authentik/blueprints.d/*' --exclude='deploy/monitoring/include/*/*' -cf - \
	deploy/authentik deploy/auto-upgrade deploy/backup deploy/postgres deploy/monitoring/alloy deploy/monitoring/grafana \
	deploy/monitoring/include deploy/monitoring/include.sh deploy/monitoring/loki deploy/monitoring/prometheus deploy/monitoring/tempo |
	gzip -n >"$out/pagerarchive-config.tar.gz"
