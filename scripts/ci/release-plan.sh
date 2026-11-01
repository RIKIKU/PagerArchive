#!/bin/sh
# Decides what a push to main releases (SPEC.md 12.1 and R63, ADR 0016),
# publishing nothing itself:
#   - version and tag: VERSION at the commit, tag v<VERSION>. If the tag
#     already exists on another commit, it fails: that version was released.
#     On this same commit it's a re-run finishing the release, and goes on.
#   - module: "publish" only if the push changed powershell/PagerArchive/
#     (compared with the push's previous main commit) and the manifest's
#     version isn't on the PowerShell Gallery yet; otherwise "unchanged".
# Prints key=value lines, and appends them to $GITHUB_OUTPUT when it's set.
#
# usage: scripts/ci/release-plan.sh <previous main commit> <commit>
set -eu
cd "$(dirname "$0")/../.."
. scripts/ci/versions-lib.sh

BEFORE=$1
AFTER=$2
GALLERY=${PA_GALLERY_URL:-https://www.powershellgallery.com/api/v2}
failed=0

version=$(git show "$AFTER:VERSION" | tr -d ' \r\n')
is_version "$version" || { fail VERSION "VERSION is '$version', not MAJOR.MINOR.PATCH: nothing is released."; exit 1; }
tag=v$version
commit=$(git rev-parse "$AFTER^{commit}")

if tagged=$(git rev-parse -q --verify "refs/tags/$tag^{commit}"); then
	if [ "$tagged" != "$commit" ]; then
		fail VERSION "Tag $tag already exists, on $tagged: $version was released already, and a version is never reused. Nothing is published; raise VERSION in a new branch."
		exit 1
	fi
	echo "Tag $tag is already on $commit: a re-run, finishing the release."
fi

module_version=$(module_version_at "$AFTER")
if [ -z "$BEFORE" ] || ! git cat-file -e "$BEFORE^{commit}" 2>/dev/null; then
	# No previous main commit to compare with (a new branch, or history that
	# was rewritten): count the module as changed; the Gallery check below
	# still keeps a version from being published twice.
	echo "No previous main commit to compare with ('$BEFORE'): counting the module as changed."
	changed=true
elif module_changed "$BEFORE" "$AFTER"; then
	changed=true
else
	changed=false
fi

if [ "$changed" = true ]; then
	status=$(curl -sS -o /dev/null -w '%{http_code}' --retry 3 "$GALLERY/Packages(Id='PagerArchive',Version='$module_version')")
	case $status in
	200) module=unchanged why="the module changed, but $module_version is on the Gallery already" ;;
	404) module=publish why="the module changed and $module_version isn't on the Gallery yet" ;;
	*) fail "$MANIFEST" "The PowerShell Gallery answered HTTP $status for PagerArchive $module_version: can't tell whether it's published. Re-run this job."; exit 1 ;;
	esac
else
	module=unchanged why="this push didn't change powershell/PagerArchive/"
fi

if [ "$module" = publish ]; then
	echo "module: publishing PagerArchive $module_version ($why)"
	notes="PowerShell module: PagerArchive $module_version, published to the PowerShell Gallery by this release."
else
	echo "module unchanged: the Gallery keeps $module_version ($why)"
	notes="PowerShell module: unchanged; the Gallery keeps $module_version."
fi

out="version=$version
tag=$tag
module=$module
module_version=$module_version
notes=$notes"
echo "$out"
[ -n "${GITHUB_OUTPUT:-}" ] && echo "$out" >>"$GITHUB_OUTPUT"
exit 0
