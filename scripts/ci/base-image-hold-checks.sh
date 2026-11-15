#!/bin/sh
# Checks base-image-hold.sh (R85, ADR 0032) with a stand-in for curl that
# answers as Docker Hub and gcr.io would, and asks no registry:
#   - a digest 4 days old is let through, one 2 days old isn't;
#   - so for gcr.io, by the upload time of the manifest the tag names;
#   - a newer patch is let through when the newest patch tag is 4 days old,
#     not when it's 1 day old, and an older or other variant's tag isn't it;
#   - a registry that doesn't answer, or one it can't date, holds the image.
#
# usage: scripts/ci/base-image-hold-checks.sh
set -eu
repo=$(cd "$(dirname "$0")/../.." && pwd)
work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT
failed=0
fail() {
	echo "FAIL: $*" >&2
	failed=1
}

now=$(date -u -d 2026-11-15T12:00:00Z +%s)
day=86400
iso() { date -u -d "@$1" +%Y-%m-%dT%H:%M:%SZ; }
old=$(iso $((now - 4 * day))) young=$(iso $((now - 2 * day))) newer=$(iso $((now - 1 * day)))

mkdir "$work/bin"
cat >"$work/bin/curl" <<CURL
#!/bin/sh
for url; do :; done
case \$url in
*/library/node/tags/24-slim) echo '{"tag_last_pushed": "$old"}' ;;
*/library/debian/tags/trixie-slim) echo '{"tag_last_pushed": "$young"}' ;;
*/library/golang/tags/1.27.4) echo '{"tag_last_pushed": "$old"}' ;;
*/library/golang/tags/1.26.1) echo '{"tag_last_pushed": "$old"}' ;;
*/library/golang/tags?name=1.27.*) echo '{"results": [
	{"name": "1.27.6-alpine", "tag_last_pushed": "$old"},
	{"name": "1.27.5", "tag_last_pushed": "$old"},
	{"name": "1.27.3", "tag_last_pushed": "$old"}]}' ;;
*/library/golang/tags?name=1.26.*) echo '{"results": [
	{"name": "1.26.3", "tag_last_pushed": "$newer"},
	{"name": "1.26.2", "tag_last_pushed": "$old"}]}' ;;
*/gcr.io/v2/distroless/static-debian13/tags/list) echo '{"manifest": {
	"sha256:a": {"tag": ["nonroot", "nonroot-1"], "timeUploadedMs": "$(((now - 4 * day) * 1000))"},
	"sha256:b": {"tag": ["latest"], "timeUploadedMs": "$(((now - 2 * day) * 1000))"}}}' ;;
*/library/busybox/*) exit 22 ;;
*) echo "curl stand-in: unexpected \$url" >&2; exit 22 ;;
esac
CURL
chmod +x "$work/bin/curl"
export PATH="$work/bin:$PATH" HOLD_NOW=$now

cat >"$work/Dockerfile" <<'EOF'
FROM node:24-slim@sha256:1 AS web
FROM --platform=$BUILDPLATFORM golang:1.27.4@sha256:2 AS build
FROM golang:1.26.1@sha256:3
FROM debian:trixie-slim@sha256:4
FROM gcr.io/distroless/static-debian13:nonroot@sha256:5
FROM gcr.io/distroless/static-debian13:latest@sha256:6
FROM busybox:1.37@sha256:7
FROM ghcr.io/owner/image:1.0.0@sha256:8
EOF
out=$(sh "$repo/scripts/ci/base-image-hold.sh" "$work/Dockerfile" 2>"$work/err") || fail "it failed: $(cat "$work/err")"
sed 's/^/    /' "$work/err"
through() { # package value update types
	echo "$out" | jq -e --arg p "$1" --arg v "$2" --argjson u "$3" 'any(.[];
		.matchPackageNames == [$p] and .matchCurrentValue == $v and .matchUpdateTypes == $u
		and .matchManagers == ["dockerfile"] and .minimumReleaseAgeBehaviour == "timestamp-optional")' >/dev/null
}
through node 24-slim '["digest","pinDigest"]' || fail "node's 4-day-old digest is held: $out"
through golang 1.27.4 '["digest","pinDigest"]' || fail "golang 1.27.4's digest is held: $out"
through golang 1.27.4 '["patch"]' || fail "golang 1.27.5, 4 days old, is held: $out"
through golang 1.26.1 '["patch"]' && fail "golang 1.26.3, a day old, is let through: $out"
through debian trixie-slim '["digest","pinDigest"]' && fail "debian's 2-day-old digest is let through: $out"
through gcr.io/distroless/static-debian13 nonroot '["digest","pinDigest"]' || fail "distroless nonroot, 4 days old, is held: $out"
through gcr.io/distroless/static-debian13 latest '["digest","pinDigest"]' && fail "distroless latest, 2 days old, is let through: $out"
echo "$out" | jq -e 'any(.[]; .matchPackageNames[0] | test("busybox|ghcr"))' >/dev/null && fail "an image with no date is let through: $out"
grep -q "busybox:1.37 (its digest): no date, so held" "$work/err" || fail "a registry that doesn't answer isn't named"
grep -q "ghcr.io/owner/image:1.0.0: can't be dated" "$work/err" || fail "an image on another registry isn't named"
[ "$(echo "$out" | jq length)" -eq 5 ] || fail "not 5 rules: $out"

[ "$failed" -eq 0 ] && echo "base-image-hold.sh: OK"
exit "$failed"
