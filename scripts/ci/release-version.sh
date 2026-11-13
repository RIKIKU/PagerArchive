#!/bin/sh
# Works out whether a commit is to be released, and as which version, from the
# previous release (SPEC.md 12.1, ADR 0024). It changes nothing. The previous
# release is the newest v* tag in the commit's history, other than one on the
# commit itself.
#
#   - Nothing but documentation changed since the previous release (what CI
#     checks nothing for: SPEC.md, docs/**, other Markdown), or nothing
#     at all: release=false, "nothing to release".
#   - The next minor version (owner, 2026-11-14) if, since the previous
#     release, a file a deployment downloads changed (CONFIG_PATHS, other
#     than our own image's tag in compose.yaml: a patch release needs only
#     pull and up, R84b), or a migration was added (internal/store/migrations),
#     or a pull request labelled "feature" merged (asked of GitHub with gh,
#     so GH_TOKEN is needed). "why" names each rule that fired, and what it
#     found.
#   - Anything else: the next patch version.
#   - Never a major version: that's the owner's, by hand.
#   - A release on a later commit already has this one (a run that waited
#     behind a newer release): nothing to release.
#   - A v* tag on the commit itself: if it's the release GitHub marks Latest
#     ($PA_LATEST_RELEASE, or one higher), it was released and there's nothing
#     to release. Otherwise the release failed partway, and this run finishes
#     it, as the same version.
#
# With --dev, the version of a build that isn't a release (a merge to main, a
# pull request, a manual run): the version a release of this commit would get,
# then -dev+<short commit>, such as 1.7.6-dev+3f2c1ab. It's always a version,
# and counts from the newest v* tag in the commit's history, its own included.
#
# Needs the commit's history and the v* tags (fetch-depth: 0). Prints why, then
# key=value lines, which it also appends to $GITHUB_OUTPUT when that's set:
#   release=true|false  version=  major_minor=  tag=  previous=  why=
#
# usage: PA_LATEST_RELEASE=<tag> scripts/ci/release-version.sh <commit>
#        scripts/ci/release-version.sh --dev <commit>
set -eu
cd "$(dirname "$0")/../.."
. scripts/ci/versions-lib.sh

DEV=""
if [ "${1:-}" = --dev ]; then
	DEV=1
	shift
fi
commit=$(git rev-parse "${1:?usage: release-version.sh [--dev] <commit>}^{commit}")
latest=${PA_LATEST_RELEASE:-}
failed=0

if [ -n "$DEV" ]; then
	previous=$(git tag -l 'v*' --sort=-v:refname --merged "$commit" | head -n 1)
else
	previous=$(previous_release "$commit")
fi
if [ -z "$previous" ]; then
	fail VERSION "No v* tag in $commit's history: there's no previous release to count from. Fetch the tags (git fetch --tags, or actions/checkout with fetch-depth: 0)."
	exit 1
fi

# next: the version a release of the commit would get, counting from $previous.
prev=${previous#v}
major=${prev%%.*} rest=${prev#*.}
minor=${rest%%.*} patch=${rest#*.}
# joined: stdin's lines as "a, b, c", or "a, b, c and 4 more".
joined() {
	awk 'NF { n++; if (n <= 3) s = s (n > 1 ? ", " : "") $0 } END { if (n > 3) s = s " and " (n - 3) " more"; print s }'
}
# Each rule that makes a minor version, and what it found (owner, 2026-11-14).
reasons=""
because() { reasons=${reasons:+$reasons; }$1; }
files=$(changed_config "$previous" "$commit" | joined)
[ -z "$files" ] || because "a file a deployment downloads changed ($files)"
migrations=$(added_migrations "$previous" "$commit" | joined)
[ -z "$migrations" ] || because "a migration was added ($migrations)"
unchecked=""
if prs=$(feature_prs "$previous" "$commit"); then
	prs=$(printf '%s\n' "$prs" | joined)
	[ -z "$prs" ] || because "a pull request labelled $FEATURE_LABEL merged ($prs)"
elif [ -n "$DEV" ]; then
	# A build that isn't a release still gets a version, without the labels.
	echo "warning: couldn't ask GitHub for the pull requests labelled $FEATURE_LABEL, so this build's version leaves them out" >&2
	unchecked=" (the pull requests' labels not checked: GitHub didn't answer)"
else
	fail VERSION "Couldn't ask GitHub for the pull requests labelled $FEATURE_LABEL since $previous, so the version can't be worked out. Run it again."
	exit 1
fi
if [ -n "$reasons" ]; then
	version=$major.$((minor + 1)).0
	why="a minor version, since $previous: $reasons"
else
	version=$major.$minor.$((patch + 1))
	why="a patch: since $previous no file a deployment downloads changed, no migration was added and no pull request labelled $FEATURE_LABEL merged$unchecked"
fi
release=true

if [ -n "$DEV" ]; then
	version=$version-dev+$(git rev-parse --short=7 "$commit")
	why="not a release: ${why#a }, then -dev+<commit>"
else
	later=$(later_release "$commit")
	own=$(own_release "$commit")
	if [ -n "$later" ]; then
		release=false
		why="nothing to release: $later, on a later commit, has everything this one has"
	elif [ -n "$own" ]; then
		version=${own#v}
		if [ -n "$latest" ] && { [ "$latest" = "$own" ] || higher "${latest#v}" "$version"; }; then
			release=false
			why="nothing to release: $own is on this commit, and released"
		elif [ -z "$latest" ]; then
			release=false
			why="nothing to release: $own is on this commit (no Latest release given to tell whether it finished)"
		else
			why="$own is on this commit, but the Latest release is $latest: finishing $own"
		fi
	elif [ -z "$(code_changes "$previous" "$commit")" ]; then
		release=false
		if git diff --quiet "$previous" "$commit"; then
			why="nothing to release: nothing changed since $previous"
		else
			why="nothing to release: only documentation changed since $previous"
		fi
	fi
fi

echo "$why"
out="release=$release
version=$version
major_minor=$(major_minor "$version")
tag=v$version
previous=$previous
why=$why"
echo "$out"
if [ -n "${GITHUB_OUTPUT:-}" ]; then echo "$out" >>"$GITHUB_OUTPUT"; fi
exit 0
