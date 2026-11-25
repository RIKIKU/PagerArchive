#!/bin/sh
# A companion app's files with this stack's own (SPEC.md R76a,
# docs/monitoring.md, "A companion app"): run as the entrypoint of Alloy and
# Grafana, it puts the stack's files and those in the include folders (each
# mounted read-only, as a folder) together where the service reads them, then
# runs the service. The stack's own files stay one by one from the release.
#
#   include.sh alloy <alloy command...>     /etc/alloy/config.alloy and every
#       *.alloy in /etc/alloy/include, in /tmp/alloy-config: run Alloy on that
#       folder, which it reads as one configuration
#   include.sh grafana <grafana command...> /etc/grafana/provisioning and the
#       rule files in /etc/grafana/include/alerts, in /tmp/grafana-provisioning,
#       which GF_PATHS_PROVISIONING then names
#
# The include folders' files are copied as they are at start: restart the
# service after changing one. A file here is never the stack's: each is
# copied under a name starting "include-", so none can replace one of ours.
# Dashboards need none of this: Grafana reads their folder itself
# (grafana/provisioning/dashboards/dashboards.yaml).
set -eu

# extras <folder> <to> <extension>...: each file in the folder with one of
# the extensions, copied to <to>/include-<name>.
extras() {
	_from=$1 _to=$2
	shift 2
	for _f in "$_from"/*; do
		[ -f "$_f" ] || continue
		for _ext; do
			case $_f in *."$_ext")
				cp "$_f" "$_to/include-${_f##*/}"
				echo "include: $_to/include-${_f##*/}"
				;;
			esac
		done
	done
}

mode=${1:-}
[ $# -eq 0 ] || shift
case $mode in
alloy)
	out=/tmp/alloy-config
	rm -rf "$out"
	mkdir -p "$out"
	cp /etc/alloy/config.alloy "$out/config.alloy"
	extras /etc/alloy/include "$out" alloy
	;;
grafana)
	out=/tmp/grafana-provisioning
	rm -rf "$out"
	mkdir -p "$out"
	cp -RL /etc/grafana/provisioning/. "$out/"
	mkdir -p "$out/alerting"
	extras /etc/grafana/include/alerts "$out/alerting" yaml yml json
	export GF_PATHS_PROVISIONING="$out"
	;;
*)
	echo "usage: include.sh alloy|grafana <command...>" >&2
	exit 2
	;;
esac
exec "$@"
