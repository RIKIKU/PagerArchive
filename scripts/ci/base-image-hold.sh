#!/bin/sh
# R85 (ADR 0032): a base image's update waits 3 days after its release, as the
# libraries' do. Renovate can't tell how old a base image's update is: Docker
# Hub's tag list stops at 1,000 tags for a client with no account, so node,
# golang and debian come back with no dates, and gcr.io has none at all. So
# .github/renovate.json5 holds every base image update (minimumReleaseAge with
# no date to age against), and this works out which are old enough, from the
# registries' own record of the one tag: Docker Hub's tag_last_pushed, gcr.io's
# timeUploadedMs. Each one is let through by a package rule that lets Renovate
# go ahead without a date (minimumReleaseAgeBehaviour "timestamp-optional").
# The maintenance workflow passes them to Renovate (RENOVATE_PACKAGE_RULES).
#
# A tag's date is its last push, so a digest is never older than it says. A
# new patch (golang 1.27.4 to 1.27.5) is let through when the newest patch tag
# is 3 days old. Anything this can't date (another registry, a registry that
# doesn't answer) isn't let through, and is named on standard error.
#
# usage: scripts/ci/base-image-hold.sh [Dockerfile...]
#   writes a JSON array of package rules on standard output; with no
#   arguments, reads the Dockerfiles R85 names. HOLD_NOW (seconds since 1970)
#   stands in for the time, for the checks (base-image-hold-checks.sh).
set -eu

hold=$((3 * 24 * 60 * 60))
now=${HOLD_NOW:-$(date -u +%s)}
[ "$#" -gt 0 ] || set -- Dockerfile Dockerfile.client scripts/test-receiver/Dockerfile

# image tag, one per line, from FROM lines like
# "FROM [--platform=...] image:tag@sha256:... [AS name]"
refs=$(cat "$@" | awk 'toupper($1) == "FROM" {
	for (i = 2; i <= NF; i++) if ($i !~ /^--/) { print $i; break }
}' | sed -n 's/^\([^:@]*\):\([^@]*\)\(@.*\)\{0,1\}$/\1 \2/p' | sort -u)

# seconds since 1970 from an ISO 8601 time, or nothing
seconds() {
	[ -n "$1" ] && date -u -d "$1" +%s 2>/dev/null || true
}

# The time a Docker Hub tag was last pushed, in seconds since 1970.
hub_pushed() { # repository tag
	curl -fsS --max-time 30 "https://hub.docker.com/v2/repositories/$1/tags/$2" |
		jq -r '.tag_last_pushed // empty' | { read -r t && seconds "$t"; } || true
}

# The newest tag like "<prefix><n><suffix>" with n above <current>, and when
# it was last pushed: "tag seconds", or nothing.
hub_newest_patch() { # repository prefix current suffix
	curl -fsS --max-time 30 "https://hub.docker.com/v2/repositories/$1/tags?name=$2&page_size=100&ordering=last_updated" |
		jq -r --arg p "$2" --arg s "$4" --argjson c "$3" '
			[.results[] | .name as $n
				| select(($n | startswith($p)) and ($n | endswith($s)))
				| ($n[($p | length):($n | length) - ($s | length)]) as $v
				| select($v | test("^[0-9]+$")) | select(($v | tonumber) > $c)
				| {name: $n, v: ($v | tonumber), t: .tag_last_pushed}]
			| max_by(.v) // empty | "\(.name) \(.t)"' | {
		read -r name t && s=$(seconds "$t") && [ -n "$s" ] && echo "$name $s"
	} || true
}

# When gcr.io last uploaded the manifest a tag names, in seconds since 1970.
gcr_uploaded() { # path tag
	curl -fsS --max-time 30 "https://gcr.io/v2/$1/tags/list" |
		jq -r --arg tag "$2" '[.manifest // {} | to_entries[] | .value
			| select((.tag // []) | index($tag)) | .timeUploadedMs | tonumber]
			| max // empty | . / 1000 | floor' || true
}

rule() { # package value update types (a JSON array)
	jq -nc --arg p "$1" --arg v "$2" --argjson u "$3" '{matchManagers: ["dockerfile"],
		matchPackageNames: [$p], matchCurrentValue: $v, matchUpdateTypes: $u,
		minimumReleaseAgeBehaviour: "timestamp-optional"}'
}

old_enough() { # what seconds
	if [ -z "$2" ]; then
		echo "base-image-hold: $1: no date, so held" >&2
		return 1
	fi
	age=$((now - $2))
	if [ "$age" -lt "$hold" ]; then
		echo "base-image-hold: $1: $((age / 3600)) hours old, held until 3 days" >&2
		return 1
	fi
	echo "base-image-hold: $1: $((age / 86400)) days old, let through" >&2
}

rules=$(echo "$refs" | while read -r image tag; do
	[ -n "$image" ] || continue
	case $image in
	gcr.io/*)
		pushed=$(gcr_uploaded "${image#gcr.io/}" "$tag")
		patch=
		;;
	*.*/* | *:*/* | localhost/*)
		echo "base-image-hold: $image:$tag: can't be dated on this registry, so held" >&2
		continue
		;;
	*)
		case $image in */*) repo=$image ;; *) repo=library/$image ;; esac
		pushed=$(hub_pushed "$repo" "$tag")
		# a version's last number, and what's either side of it
		patch=$(echo "$tag" | sed -n 's/^\([0-9][0-9]*\.[0-9][0-9]*\.\)\([0-9][0-9]*\)\(.*\)$/\1 \2 \3/p')
		;;
	esac
	if old_enough "$image:$tag (its digest)" "$pushed"; then
		rule "$image" "$tag" '["digest", "pinDigest"]'
	fi
	if [ -n "$patch" ]; then
		set -- $patch
		newest=$(hub_newest_patch "$repo" "$1" "$2" "${3:-}")
		if [ -n "$newest" ] && old_enough "$image:${newest% *} (a patch of $tag)" "${newest#* }"; then
			rule "$image" "$tag" '["patch"]'
		fi
	fi
done)

echo "$rules" | jq -sc '.'
