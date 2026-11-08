#!/bin/sh
# Checks tell-owner.sh (R85c, ADR 0022) with a stand-in for gh that records
# what it's asked and changes nothing on GitHub:
#   - CI failed on an automatic update's pull request: it's assigned to the
#     owner, labelled "ci failed" and commented on with the run's link;
#   - with --dry-run, nothing is changed;
#   - a branch that isn't an automatic update's, or one with no open pull
#     request, is left alone.
#
# usage: scripts/ci/tell-owner-checks.sh
set -eu
repo=$(cd "$(dirname "$0")/../.." && pwd)
work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT
failed=0
fail() {
	echo "FAIL: $*" >&2
	failed=1
}

mkdir "$work/bin"
cat >"$work/bin/gh" <<'GH'
#!/bin/sh
echo "gh $*" >>"$GH_LOG"
case "$1 $2" in
"pr list") [ "$6" = renovate/libraries-patch ] && echo 42 ;;
esac
exit 0
GH
chmod +x "$work/bin/gh"
export PATH="$work/bin:$PATH" GH_LOG="$work/log" GITHUB_REPOSITORY=owner/repo GITHUB_REPOSITORY_OWNER=owner
run=https://github.com/owner/repo/actions/runs/1

: >"$work/log"
sh "$repo/scripts/ci/tell-owner.sh" renovate/libraries-patch "$run" >/dev/null
grep -q '^gh pr edit 42 --repo owner/repo --add-assignee owner --add-label ci failed$' "$work/log" || fail "the pull request isn't assigned to the owner: $(cat "$work/log")"
grep -q "^gh pr comment 42 --repo owner/repo --body CI failed on this automatic update: $run" "$work/log" || fail "no comment with the run's link: $(cat "$work/log")"
echo "--- CI failed on renovate/libraries-patch (#42):"
sed 's/^/    /' "$work/log"

: >"$work/log"
sh "$repo/scripts/ci/tell-owner.sh" renovate/libraries-patch "$run" --dry-run | grep -q '^DRY-RUN: gh pr edit 42' || fail "--dry-run doesn't say what it would do"
grep -v '^gh pr list' "$work/log" | grep -q . && fail "--dry-run changed something: $(cat "$work/log")"

: >"$work/log"
sh "$repo/scripts/ci/tell-owner.sh" m18 "$run" >/dev/null
[ -s "$work/log" ] && fail "a branch that isn't an update's was looked at: $(cat "$work/log")"

: >"$work/log"
sh "$repo/scripts/ci/tell-owner.sh" renovate/base-images "$run" >/dev/null
grep -v '^gh pr list' "$work/log" | grep -q . && fail "an update with no open pull request was changed: $(cat "$work/log")"

[ "$failed" -eq 0 ] && echo "tell-owner.sh: OK"
exit "$failed"
