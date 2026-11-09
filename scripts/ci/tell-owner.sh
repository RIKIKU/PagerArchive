#!/bin/sh
# R85c: an automatic update whose CI failed stays open, not merged, and the
# owner is told (ADR 0022): its pull request is assigned to them, labelled
# "ci failed", and gets a comment linking the run. GitHub notifies the
# assignee. Auto-merge stays on: a re-run that passes every check may merge it.
#
# usage: scripts/ci/tell-owner.sh <branch> <run URL> [--dry-run]
#   needs GH_TOKEN, GITHUB_REPOSITORY and GITHUB_REPOSITORY_OWNER (the owner
#   told); --dry-run prints what it would do and changes nothing.
set -eu

branch=$1 run=$2 dry=${3:-}
owner=${GITHUB_REPOSITORY_OWNER:?}
case $branch in
renovate/*) ;;
*)
	echo "tell-owner: $branch isn't an automatic update's branch: nothing to do"
	exit 0
	;;
esac

pr=$(gh pr list --repo "$GITHUB_REPOSITORY" --head "$branch" --state open --json number --jq '.[0].number // empty')
if [ -z "$pr" ]; then
	echo "tell-owner: no open pull request from $branch (merged or closed since): nothing to do"
	exit 0
fi

body="CI failed on this automatic update: $run

It stays open and won't merge until every check passes (SPEC.md R85c). @$owner, it's yours."

run_gh() {
	if [ "$dry" = --dry-run ]; then
		echo "DRY-RUN: gh $*"
	else
		gh "$@"
	fi
}

echo "tell-owner: pull request #$pr ($branch): assigning $owner"
run_gh label create "ci failed" --repo "$GITHUB_REPOSITORY" --color B60205 --description "An automatic update failed CI (R85c)" --force
run_gh pr edit "$pr" --repo "$GITHUB_REPOSITORY" --add-assignee "$owner" --add-label "ci failed"
run_gh pr comment "$pr" --repo "$GITHUB_REPOSITORY" --body "$body"
