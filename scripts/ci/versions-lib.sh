# Helpers for check-versions.sh and release-plan.sh (ADR 0016). Sourced, not run.
#
# A release's version (VERSION) and the module's (the manifest's ModuleVersion)
# are both MAJOR.MINOR.PATCH, without a pre-release or build part: every merge
# to main is a release, so there's nothing to mark as a pre-release.

MANIFEST=powershell/PagerArchive/PagerArchive.psd1
MODULE_DIR=powershell/PagerArchive/

# is_version <text>: MAJOR.MINOR.PATCH, numbers without leading zeros.
is_version() {
	printf '%s\n' "$1" | grep -Eqx '(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)'
}

# higher <a> <b>: true if version a is higher than version b.
higher() {
	_a1=${1%%.*} _r=${1#*.}
	_a2=${_r%%.*} _a3=${_r#*.}
	_b1=${2%%.*} _r=${2#*.}
	_b2=${_r%%.*} _b3=${_r#*.}
	[ "$_a1" -ne "$_b1" ] && { [ "$_a1" -gt "$_b1" ]; return; }
	[ "$_a2" -ne "$_b2" ] && { [ "$_a2" -gt "$_b2" ]; return; }
	[ "$_a3" -gt "$_b3" ]
}

# version_at <rev>: VERSION at a commit. Before VERSION existed (main up to
# v0.12.0), the newest v* tag's version stands in. Empty if there's neither.
version_at() {
	if git cat-file -e "$1:VERSION" 2>/dev/null; then
		git show "$1:VERSION" | tr -d ' \r\n'
	else
		git tag -l 'v*' --sort=-v:refname --merged "$1" | head -n 1 | sed 's/^v//'
	fi
}

# module_version_at <rev>: the manifest's ModuleVersion at a commit.
module_version_at() {
	git show "$1:$MANIFEST" | tr -d '\r' |
		sed -n "s/^[[:space:]]*ModuleVersion[[:space:]]*=[[:space:]]*['\"]\([^'\"]*\)['\"].*/\1/p" | head -n 1
}

# module_changed <from> <to>: true if anything under powershell/PagerArchive/ differs.
module_changed() {
	! git diff --quiet "$1" "$2" -- "$MODULE_DIR"
}

# fail <file> <message>: a GitHub error annotation on the file, and the message.
fail() {
	echo "::error file=$1::$2"
	echo "FAIL: $2" >&2
	failed=1
}

# check_compose_image <version>: compose.yaml names our image by this version's
# MAJOR.MINOR, so `docker compose pull` gets its patch releases (R84). Needs $failed.
check_compose_image() {
	_named=$(tr -d '\r' <compose.yaml | sed -n 's/^[[:space:]]*image:[[:space:]]*ghcr\.io\/rikiku\/pagerarchive:\(.*\)$/\1/p')
	if [ "$_named" != "${1%.*}" ]; then
		fail compose.yaml "compose.yaml's API image is ghcr.io/rikiku/pagerarchive:${_named:-(none)}, which isn't VERSION $1's minor version: set it to ghcr.io/rikiku/pagerarchive:${1%.*}."
	fi
}

# CONFIG_PATHS: what a deployment only gets by downloading a release's files.
CONFIG_PATHS="compose.yaml deploy/monitoring/compose.yaml deploy/authentik deploy/backup deploy/postgres deploy/monitoring/alloy deploy/monitoring/grafana deploy/monitoring/loki deploy/monitoring/prometheus deploy/monitoring/tempo"

# previous_release <rev>: the newest v* tag in the commit's history, other
# than one on the commit itself (a re-run's own tag). Empty if there's none.
previous_release() {
	_commit=$(git rev-parse "$1^{commit}")
	git tag -l 'v*' --sort=-v:refname --merged "$_commit" | while read -r _t; do
		[ "$(git rev-parse "$_t^{commit}")" != "$_commit" ] && echo "$_t"
	done | head -n 1
}
