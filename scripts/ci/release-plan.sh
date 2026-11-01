#!/bin/sh
# Decides what a push to main releases (SPEC.md 12.1 and R63, ADR 0016),
# publishing nothing itself. Earlier releases are the v* tags, other than
# one on this commit (a re-run's own tag):
#   - version and tag: VERSION at the commit, tag v<VERSION>. It fails if
#     VERSION isn't higher than the newest earlier release, or if the tag
#     already exists on another commit: that version was released. A tag on
#     this same commit is a re-run finishing the release, and it goes on.
#   - module: "publish" only if powershell/PagerArchive/ changed since the
#     previous release (the newest earlier v* tag merged into the commit)
#     and the manifest's version isn't on the PowerShell Gallery yet;
#     otherwise "unchanged". With no previous release, the module counts as
#     changed and the Gallery decides.
# Needs the tags (git fetch --tags). Prints key=value lines, and appends
# them to $GITHUB_OUTPUT when it's set.
#
# usage: scripts/ci/release-plan.sh <commit>
set -eu
cd "$(dirname "$0")/../.."
. scripts/ci/versions-lib.sh

COMMIT=$1
GALLERY=${PA_GALLERY_URL:-https://www.powershellgallery.com/api/v2}
failed=0

version=$(git show "$COMMIT:VERSION" | tr -d ' \r\n')
is_version "$version" || { fail VERSION "VERSION is '$version', not MAJOR.MINOR.PATCH: nothing is released."; exit 1; }
tag=v$version
commit=$(git rev-parse "$COMMIT^{commit}")

# earlier_releases [--merged]: v* tags, newest version first, other than any
# on this commit; with --merged, only those in the commit's history.
earlier_releases() {
	git tag -l 'v*' --sort=-v:refname ${1:+"$1"} ${1:+"$commit"} | while read -r t; do
		[ "$(git rev-parse "$t^{commit}")" != "$commit" ] && echo "$t"
	done
	return 0
}

if tagged=$(git rev-parse -q --verify "refs/tags/$tag^{commit}"); then
	if [ "$tagged" != "$commit" ]; then
		fail VERSION "Tag $tag already exists, on $tagged: $version was released already, and a version is never reused. Nothing is published; raise VERSION in a new branch."
		exit 1
	fi
	echo "Tag $tag is already on $commit: a re-run, finishing the release."
fi

newest=$(earlier_releases | head -n 1)
if [ -n "$newest" ]; then
	if ! higher "$version" "${newest#v}"; then
		fail VERSION "VERSION is $version, which isn't higher than the newest release, $newest: every release is a new, higher version. Nothing is published; raise VERSION above ${newest#v} in a new branch."
		exit 1
	fi
	echo "VERSION $version is higher than the newest release, $newest."
fi

module_version=$(module_version_at "$commit")
previous=$(earlier_releases --merged | head -n 1)
if [ -z "$previous" ]; then
	echo "No earlier release in this commit's history: counting the module as changed."
	changed=true since="with no earlier release"
elif module_changed "$previous" "$commit"; then
	changed=true since="since $previous"
else
	changed=false since="since $previous"
fi

if [ "$changed" = true ]; then
	status=$(curl -sS -o /dev/null -w '%{http_code}' --retry 3 "$GALLERY/Packages(Id='PagerArchive',Version='$module_version')")
	case $status in
	200) module=unchanged why="the module changed $since, but $module_version is on the Gallery already" ;;
	404) module=publish why="the module changed $since and $module_version isn't on the Gallery yet" ;;
	*) fail "$MANIFEST" "The PowerShell Gallery answered HTTP $status for PagerArchive $module_version: can't tell whether it's published. Re-run this job."; exit 1 ;;
	esac
else
	module=unchanged why="powershell/PagerArchive/ didn't change $since"
fi

if [ "$module" = publish ]; then
	echo "module: publishing PagerArchive $module_version ($why)"
	notes="PowerShell module: PagerArchive $module_version, published to the PowerShell Gallery by this release."
else
	echo "module unchanged: the Gallery keeps $module_version ($why)"
	notes="PowerShell module: unchanged; the Gallery keeps $module_version."
fi

out="version=$version
major_minor=${version%.*}
tag=$tag
module=$module
module_version=$module_version
notes=$notes"
echo "$out"
[ -n "${GITHUB_OUTPUT:-}" ] && echo "$out" >>"$GITHUB_OUTPUT"
exit 0
