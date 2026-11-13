# Helpers for the version scripts (ADR 0016, ADR 0024). Sourced, not run.
#
# A release's version and the module's (the manifest's ModuleVersion) are
# MAJOR.MINOR.PATCH. A release's version lives in its tag, v<version>, and
# nowhere in the repository: main's VERSION says 0.0.0-dev and compose.yaml
# names our image :main. Every build in CI writes its version into both first
# (stamp-version.sh): a release's own, or for any other build
# <next>-dev+<commit>, such as 1.7.6-dev+3f2c1ab (release-version.sh --dev).

MANIFEST=powershell/PagerArchive/PagerArchive.psd1
MODULE_DIR=powershell/PagerArchive/

# What main's own files say, before a build stamps them.
UNSTAMPED_VERSION=0.0.0-dev
UNSTAMPED_IMAGE_TAG=main

# is_version <text>: MAJOR.MINOR.PATCH, numbers without leading zeros.
is_version() {
	printf '%s\n' "$1" | grep -Eqx '(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)'
}

# is_dev_version <text>: <MAJOR.MINOR.PATCH>-dev+<short commit>.
is_dev_version() {
	printf '%s\n' "$1" | grep -Eqx '(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)-dev\+[0-9a-f]{7,40}'
}

# major_minor <version>: 1.8 for 1.8.0 and for 1.8.0-dev+3f2c1ab.
major_minor() {
	_v=${1%%-*}
	echo "${_v%.*}"
}

# higher <a> <b>: true if version a is higher than version b (MAJOR.MINOR.PATCH).
higher() {
	_a1=${1%%.*} _r=${1#*.}
	_a2=${_r%%.*} _a3=${_r#*.}
	_b1=${2%%.*} _r=${2#*.}
	_b2=${_r%%.*} _b3=${_r#*.}
	[ "$_a1" -ne "$_b1" ] && { [ "$_a1" -gt "$_b1" ]; return; }
	[ "$_a2" -ne "$_b2" ] && { [ "$_a2" -gt "$_b2" ]; return; }
	[ "$_a3" -gt "$_b3" ]
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

# compose_image_tag [<file>]: the tag compose.yaml (or the file) names our image by.
compose_image_tag() {
	tr -d '\r' <"${1:-compose.yaml}" | sed -n 's/^[[:space:]]*image:[[:space:]]*ghcr\.io\/rikiku\/pagerarchive:\(.*\)$/\1/p'
}

# image_tag_for <version>: the tag compose.yaml names our image by for that
# VERSION: its MAJOR.MINOR (R84), or main in main's own unstamped files.
image_tag_for() {
	if [ "$1" = "$UNSTAMPED_VERSION" ]; then
		echo "$UNSTAMPED_IMAGE_TAG"
	else
		major_minor "$1"
	fi
}

# check_compose_image <version>: compose.yaml names our image as image_tag_for
# says, so `docker compose pull` gets the release's patches. Needs $failed.
check_compose_image() {
	_named=$(compose_image_tag)
	_want=$(image_tag_for "$1")
	if [ "$_named" != "$_want" ]; then
		fail compose.yaml "compose.yaml's API image is ghcr.io/rikiku/pagerarchive:${_named:-(none)}, but VERSION is $1, so it should be ghcr.io/rikiku/pagerarchive:$_want. In the repository VERSION says $UNSTAMPED_VERSION and compose.yaml :$UNSTAMPED_IMAGE_TAG; a build stamps both (scripts/ci/stamp-version.sh)."
	fi
}

# CONFIG_PATHS: what a deployment only gets by downloading a release's files.
CONFIG_PATHS="compose.yaml deploy/monitoring/compose.yaml deploy/authentik deploy/backup deploy/postgres deploy/monitoring/alloy deploy/monitoring/grafana deploy/monitoring/loki deploy/monitoring/prometheus deploy/monitoring/tempo"
# Our own image's line in compose.yaml. Releases before ADR 0024 committed
# their MAJOR.MINOR there and main now says :main, but a release stamps its
# own, so a change to that line alone changes nothing a deployment downloads.
OUR_IMAGE_LINE='^[[:space:]]*image:[[:space:]]*ghcr\.io/rikiku/pagerarchive:'

# config_changed <from> <to>: true if a file a deployment downloads differs,
# other than our image's tag.
config_changed() {
	# shellcheck disable=SC2086
	! git diff --quiet -I"$OUR_IMAGE_LINE" "$1" "$2" -- $CONFIG_PATHS
}

# changed_config <from> <to>: those files, one per line.
changed_config() {
	# shellcheck disable=SC2086
	git diff --name-only -I"$OUR_IMAGE_LINE" "$1" "$2" -- $CONFIG_PATHS
}

# MIGRATIONS: the database's migrations, which the API runs when it starts.
MIGRATIONS=internal/store/migrations

# added_migrations <from> <to>: the migrations added since, by file name.
added_migrations() {
	git diff --name-only --diff-filter=A "$1" "$2" -- "$MIGRATIONS" | sed 's#.*/##'
}

# FEATURE_LABEL: the label on a pull request that adds a feature (owner,
# 2026-11-14; .github/release.yml).
FEATURE_LABEL=feature

# feature_prs <from> <to>: the pull requests labelled $FEATURE_LABEL whose
# merge (or squash) commit is in <to>'s history and not in <from>'s, as #<n>
# per line. Asks GitHub (gh, with GH_TOKEN, retried); returns non-zero if it
# couldn't. A pull request whose commit isn't in this clone isn't counted.
feature_prs() {
	# shellcheck disable=SC2016 # jq's own syntax
	_prs=$(sh scripts/ci/retry.sh -l "gh pr list --label $FEATURE_LABEL" \
		gh pr list ${GITHUB_REPOSITORY:+--repo "$GITHUB_REPOSITORY"} --state merged --label "$FEATURE_LABEL" \
		--limit 1000 --json number,mergeCommit --jq '.[] | "\(.number) \(.mergeCommit.oid // "")"') || return 1
	printf '%s\n' "$_prs" | while read -r _n _sha; do
		[ -n "$_sha" ] && git cat-file -e "$_sha^{commit}" 2>/dev/null || continue
		if git merge-base --is-ancestor "$_sha" "$2" && ! git merge-base --is-ancestor "$_sha" "$1"; then
			echo "#$_n"
		fi
	done | sort -t'#' -k2 -n
}

# code_changes <from> <to>: the files that differ, other than documentation:
# what CI checks nothing for (SPEC.md, docs/**, other Markdown; changes.sh), except
# web/static/**, which is built into the server. A change to our
# image's line in compose.yaml alone doesn't count, as for config_changed.
code_changes() {
	git diff --name-only "$1" "$2" | while IFS= read -r _f; do
		case $_f in
		web/static/*) echo "$_f" ;;
		SPEC.md | docs/* | *.md) ;;
		compose.yaml) git diff --quiet -I"$OUR_IMAGE_LINE" "$1" "$2" -- compose.yaml || echo "$_f" ;;
		*) echo "$_f" ;;
		esac
	done
}

# own_release <rev>: the highest v* tag on the commit itself, if any.
own_release() {
	git tag -l 'v*' --sort=-v:refname --points-at "$1" | head -n 1
}

# later_release <rev>: the highest v* tag on a later commit whose history holds
# this one, if any: a release that already has everything this commit has.
later_release() {
	_commit=$(git rev-parse "$1^{commit}")
	git tag -l 'v*' --sort=-v:refname --contains "$_commit" | while read -r _t; do
		[ "$(git rev-parse "$_t^{commit}")" != "$_commit" ] && echo "$_t"
	done | head -n 1
}

# previous_release <rev>: the newest v* tag in the commit's history, other
# than one on the commit itself (a re-run's own tag). Empty if there's none.
previous_release() {
	_commit=$(git rev-parse "$1^{commit}")
	git tag -l 'v*' --sort=-v:refname --merged "$_commit" | while read -r _t; do
		[ "$(git rev-parse "$_t^{commit}")" != "$_commit" ] && echo "$_t"
	done | head -n 1
}
