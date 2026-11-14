#!/bin/sh
# Which check jobs a pull request runs (ADR 0025): only those its changed
# files can affect. Each changed file belongs to one area (area_of), and each
# area runs the jobs whose inputs it is (area_jobs): what the job builds,
# mounts and runs, worked out from the jobs and their scripts.
#
#   - A file in no area runs everything, as does anything in
#     .github/workflows/, .github/actions/ or scripts/ci/, and the shared test
#     fixtures (profiles/, testdata/, deploy/test/, deploy/example-map/,
#     scripts/fixture.sql, scripts/stack-lib.sh, scripts/install-lib.sh and the
#     stand-ins in scripts/test-*/).
#   - Documentation (SPEC.md, docs/**, other Markdown, apart from
#     web/static/**, which is built into the server) runs nothing (ADR 0014).
#     A change of documentation alone runs no check at all, not even versions:
#     "All checks passed" reports at once (versions=false).
#   - Files no check reads (the scripts run only by hand, GitHub's release
#     notes categories and Renovate's settings) run nothing either.
#   - A push to main runs everything, unless it changed only documentation
#     (--all-unless-docs). A manual run and the Release workflow's run
#     everything: --all.
#
# A new top-level file or folder runs everything until it's given an area
# here; scripts/ci/changes-checks.sh checks the table.
#
# usage: scripts/ci/changes.sh --all <why>        every job
#        scripts/ci/changes.sh --all-unless-docs <base> <head>
#                                                  every job, or none if only documentation changed
#        scripts/ci/changes.sh <base> <head>       the files changed between two commits
#        scripts/ci/changes.sh --files             file names on standard input
#        scripts/ci/changes.sh --areas             each file name on standard input with its area
# Writes <job>=true|false for every job in CHECK_JOBS, versions=true|false
# (false only when nothing but documentation changed) and all=true|false, to
# standard output and $GITHUB_OUTPUT, and a table of what runs and why to
# $GITHUB_STEP_SUMMARY.
set -eu

# Every check job that can be left out by area. versions runs unless only
# documentation changed; stamp and changes always run.
CHECK_JOBS="test search import web powershell stack stack-e2e upgrade install backups images budget monitoring govulncheck"

area_of() {
	case $1 in
	web/static/*) echo web ;;
	SPEC.md | docs/* | *.md) echo docs ;;
	.github/workflows/* | .github/actions/* | scripts/ci/*) echo all ;;
	profiles/* | testdata/* | deploy/test/* | deploy/example-map/* | scripts/fixture.sql | scripts/stack-lib.sh | scripts/install-lib.sh | scripts/test-*/*) echo all ;;
	*.go | go.mod | go.sum | sqlc.yaml | VERSION | cmd/* | internal/* | schemas/*) echo go ;;
	web/*) echo web ;;
	powershell/*) echo powershell ;;
	compose.yaml | .env.example | deploy/authentik/* | deploy/postgres/*) echo deployment ;;
	deploy/backup/*) echo backup ;;
	deploy/monitoring/*) echo monitoring ;;
	Dockerfile) echo server-image ;;
	Dockerfile.client) echo client-image ;;
	.dockerignore) echo images ;;
	# A job's own script.
	scripts/e2e.sh) echo job:web ;;
	scripts/stack-checks.sh) echo job:stack ;;
	scripts/stack-e2e.sh) echo job:stack-e2e ;;
	scripts/a76.sh | scripts/a76-checks.sh) echo job:import ;;
	scripts/a77.sh) echo job:upgrade ;;
	scripts/a78.sh) echo job:install ;;
	scripts/a80.sh | scripts/a80-size.sh | scripts/backup-checks.sh) echo job:backups ;;
	scripts/a15.sh) echo job:budget ;;
	scripts/monitoring-checks.sh | scripts/monitoring-screenshots.*) echo job:monitoring ;;
	# Read by no check: run by hand, or by GitHub and Renovate themselves.
	scripts/mutation-checks.sh | scripts/oauth-probe.py | scripts/example-map/*.sh | .github/release.yml | .github/renovate.json5) echo none ;;
	*) echo all ;;
	esac
}

# The jobs an area's files are inputs of.
area_jobs() {
	case $1 in
	all) echo "$CHECK_JOBS" ;;
	docs | none) ;;
	# Go: the API and client are in every image, stack and browser test; the
	# root package's tests and go vet cover scripts/**/*.go too.
	go) echo "test search import web stack stack-e2e upgrade install backups images budget monitoring govulncheck" ;;
	# The browser app: its own tests, the stack's browser steps (stack-e2e, A78,
	# A80), and the server image it's built into.
	web) echo "web stack-e2e install backups images" ;;
	# The module: its own tests, and A20, A21 and A51-A59 against the stack.
	powershell) echo "powershell stack" ;;
	# What a deployment runs: every compose stack, and the root package's
	# checks of the files (R84, R84a).
	deployment) echo "test stack stack-e2e import upgrade install backups budget monitoring" ;;
	# The backup service: A80, and the installs and upgrade that run it.
	backup) echo "test upgrade install backups monitoring" ;;
	# The example monitoring stack, and the root package's and telemetry's checks of it.
	monitoring) echo "test monitoring" ;;
	# The server image, and every job that builds it.
	server-image) echo "images stack stack-e2e import upgrade install backups budget monitoring" ;;
	# The client image, and every job that builds it.
	client-image) echo "images stack-e2e install monitoring" ;;
	images) echo "images stack stack-e2e import upgrade install backups budget monitoring" ;;
	job:*) echo "${1#job:}" ;;
	*)
		echo "changes: no jobs for area $1" >&2
		exit 1
		;;
	esac
}

var_of() { printf '%s' "$1" | tr -c 'a-z0-9\n' '_'; }

for job in $CHECK_JOBS; do
	eval "run_$(var_of "$job")=false why_$(var_of "$job")="
done
all=false
code=false
reason=

mark() { # mark <job> <file>
	v=$(var_of "$1")
	eval "run_$v=true"
	eval "n=\${count_$v:-0}"
	n=$((n + 1))
	eval "count_$v=$n"
	if [ "$n" -le 3 ]; then
		eval "why_$v=\"\${why_$v:+\$why_$v, }\$2\""
	elif [ "$n" -eq 4 ]; then
		eval "why_$v=\"\$why_$v, …\""
	fi
}

classify() {
	files=0
	while IFS= read -r file; do
		[ -n "$file" ] || continue
		files=$((files + 1))
		area=$(area_of "$file")
		case $area in docs | none) ;; *) code=true ;; esac
		if [ "$area" = all ] && [ "$all" = false ]; then
			all=true
			reason="\`$file\` changed, which runs everything"
		fi
		for job in $(area_jobs "$area"); do
			mark "$job" "\`$file\`"
		done
	done
	echo "changes: $files files changed${reason:+; $reason}" | tr -d '`'
}

case ${1:-} in
--all)
	all=true code=true
	reason=${2:-every job}
	for job in $CHECK_JOBS; do
		eval "run_$(var_of "$job")=true"
	done
	;;
--all-unless-docs)
	list=$(git diff --name-only --no-renames "$2" "$3")
	classify <<EOF
$list
EOF
	if [ "$code" = true ]; then
		all=true
		reason="a push to main checks everything"
		for job in $CHECK_JOBS; do
			eval "run_$(var_of "$job")=true"
		done
	fi
	;;
--files)
	classify
	;;
--areas) # for changes-checks.sh: each file's area
	while IFS= read -r file; do echo "$(area_of "$file") $file"; done
	exit 0
	;;
"")
	echo "usage: changes.sh --all <why> | --all-unless-docs <base> <head> | <base> <head> | --files | --areas" >&2
	exit 2
	;;
*)
	list=$(git diff --name-only --no-renames "$1" "$2")
	classify <<EOF
$list
EOF
	;;
esac

out() {
	echo "$1"
	[ -z "${GITHUB_OUTPUT:-}" ] || echo "$1" >>"$GITHUB_OUTPUT"
}
out "all=$all"
out "versions=$code"
for job in $CHECK_JOBS; do
	eval "out \"$job=\$run_$(var_of "$job")\""
done

if [ -n "${GITHUB_STEP_SUMMARY:-}" ]; then
	{
		echo "### What runs"
		echo
		if [ "$code" = false ]; then
			echo "No check: only documentation, or files no check reads, changed (ADR 0014, ADR 0025)."
		elif [ "$all" = true ]; then
			echo "Every check: $reason."
		else
			echo "Only the checks the changed files affect (\`scripts/ci/changes.sh\`, ADR 0025)."
		fi
		echo
		echo "| Job | Runs | Because of |"
		echo "|---|---|---|"
		for job in $CHECK_JOBS; do
			v=$(var_of "$job")
			r=false w=
			eval "r=\$run_$v w=\$why_$v"
			if [ "$r" = true ]; then
				[ "$all" = true ] && w=${reason}
				echo "| $job | yes | $w |"
			else
				echo "| $job | skipped | nothing it uses changed |"
			fi
		done
	} >>"$GITHUB_STEP_SUMMARY"
fi
