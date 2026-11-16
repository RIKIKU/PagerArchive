#!/bin/sh
# A105, the reverse proxy example's LAN-only lines hold (SPEC.md R84j):
# docs/deploy.md's nginx blocks (<!-- proxy:... --> markers), changed only as
# the docs say, in throwaway nginx containers on two made-up networks: a LAN
# (192.168.77.0/24) and the internet (198.51.100.0/24). Nothing is published
# on the host. Run by hand after changing those blocks, on a machine with
# Docker (docs-only changes run no checks, ADR 0014):
#
#   sh scripts/a105.sh [docs/deploy.md]
#
# Uses the container names a105-* and the network names a105-lan and a105-wan.
set -eu
DOC=${1:-docs/deploy.md}
# In the current folder, not /tmp: a snap-installed Docker can't see /tmp.
W=$(mktemp -d "$PWD/.a105.XXXXXX"); chmod 755 "$W"
NG=nginx:1.29-alpine
SWAG=lscr.io/linuxserver/swag:5.10.0-ls500
CURL=curlimages/curl:8.17.0
P=a105
block() { awk -v m="<!-- proxy:$1 -->" '$0 == m { f = 1; next } f && /^```nginx/ { on = 1; next } on && /^```/ { exit } on' "$DOC"; }
for b in app auth front real-ip; do block $b >"$W/$b.conf"; [ -s "$W/$b.conf" ] || { echo "no block $b"; exit 1; }; done
# Made-up test addresses: LAN 192.168.77.0/24, internet 198.51.100.0/24.
# The docs' backends (127.0.0.1) become the echo container; the docs'
# forwarder (192.168.1.10) becomes the test's front proxy, 192.168.77.10.
sed 's#http://127.0.0.1:8080#http://192.168.77.30:8080#; s#http://127.0.0.1:9000#http://192.168.77.30:9000#' "$W/app.conf" >"$W/app-t.conf"
sed 's#http://127.0.0.1:8080#http://192.168.77.30:8080#; s#http://127.0.0.1:9000#http://192.168.77.30:9000#' "$W/auth.conf" >"$W/auth-t.conf"
# The documented edit for the real-ip layout: deny the forwarder above each allow.
sed 's/^\( *\)allow 192.168.0.0\/16;/\1deny 192.168.1.10;\n&/' "$W/app-t.conf" | sed 's/192\.168\.1\.10/192.168.77.10/' >"$W/app-r.conf"
sed 's/^\( *\)allow 192.168.0.0\/16;/\1deny 192.168.1.10;\n&/' "$W/auth-t.conf" | sed 's/192\.168\.1\.10/192.168.77.10/' >"$W/auth-r.conf"
sed 's/192\.168\.1\.10/192.168.77.10/' "$W/real-ip.conf" >"$W/real-ip-t.conf"
cat >"$W/inner-direct.conf" <<'C'
server { listen 80; server_name pager.example.org; include /s/app-t.conf; }
server { listen 80; server_name auth.example.org; include /s/auth-t.conf; }
C
cat >"$W/inner-realip.conf" <<'C'
server { listen 80; server_name pager.example.org; include /s/real-ip-t.conf; include /s/app-r.conf; }
server { listen 80; server_name auth.example.org; include /s/real-ip-t.conf; include /s/auth-r.conf; }
C
# front: :80 forwards to the real-ip inner, :81 to the direct inner (the
# failure the docs warn about), :82 blocks the paths, then forwards to the direct inner.
fwd() { echo "location / { proxy_pass http://$1; proxy_set_header Host \$host; proxy_set_header X-Forwarded-For \$proxy_add_x_forwarded_for; }"; }
cat >"$W/front-server.conf" <<C
server { listen 80; $(fwd 192.168.77.3) }
server { listen 81; $(fwd 192.168.77.2) }
server { listen 82; server_name auth.example.org pager.example.org; include /s/front.conf; $(fwd 192.168.77.2) }
C
cat >"$W/echo.conf" <<'C'
server { listen 8080; listen 9000; location / { default_type text/plain; return 200 "ok xff=$http_x_forwarded_for\n"; } }
C
down() { docker rm -f $P-inner $P-inner2 $P-inner2b $P-front $P-echo >/dev/null 2>&1 || true; docker network rm $P-lan $P-wan >/dev/null 2>&1 || true; }
trap 'down; rm -rf "$W"' EXIT; down
docker network create --subnet 192.168.77.0/24 $P-lan >/dev/null
docker network create --subnet 198.51.100.0/24 $P-wan >/dev/null
run() { name=$1 ip=$2 conf=$3; shift 3
  docker run -d --name $P-$name --network $P-lan --ip $ip -v "$W:/s:ro" -v "$W/$conf:/etc/nginx/conf.d/default.conf:ro" $NG >/dev/null
  for x in "$@"; do docker network connect --ip $x $P-wan $P-$name; done; }
run echo 192.168.77.30 echo.conf
run inner 192.168.77.2 inner-direct.conf 198.51.100.2
run inner2 192.168.77.3 inner-realip.conf
run front 192.168.77.10 front-server.conf 198.51.100.10
for c in inner inner2 front; do echo "== nginx -t ($c, $NG)"; docker exec $P-$c nginx -t 2>&1; done
# The same blocks through SWAG's own nginx build.
cat >"$W/swag-test.conf" <<'C'
events {}
pid /tmp/nginx.pid;
error_log /dev/stderr;
http {
	access_log off;
	include /s/inner-direct.conf;
	server { listen 8081; server_name pager.example.org; include /s/real-ip-t.conf; include /s/app-r.conf; }
	server { listen 8081; server_name auth.example.org; include /s/real-ip-t.conf; include /s/auth-r.conf; }
	server { listen 8082; server_name auth.example.org; include /s/front.conf; location / { return 200; } }
}
C
echo "== nginx -t ($SWAG)"
docker run --rm --entrypoint nginx -v "$W:/s:ro" $SWAG -t -c /s/swag-test.conf 2>&1
sleep 1
fails=0
# check <name> <net> <ip> <expect> <host> <url> [curl args]
check() { name=$1 net=$2 ip=$3 want=$4 host=$5 url=$6; shift 6
  out=$(docker run --rm --network $P-$net --ip $ip $CURL -s -w ' %{http_code}' -H "Host: $host" "$@" "$url")
  code=${out##* }; ok=PASS; case "$want" in *=*) case "$out" in *"$want"*) ;; *) ok=FAIL;; esac;; *) [ "$code" = "$want" ] || ok=FAIL;; esac
  [ $ok = PASS ] || fails=$((fails+1)); echo "$ok  $name: want $want, got: $(echo $out | tr '\n' ' ')"; }
S='-H X-Forwarded-For:192.168.77.20'
echo "== direct layout (internet reaches this proxy)"
check "internet /if/admin/" wan 198.51.100.20 403 auth.example.org http://198.51.100.2/if/admin/
check "internet /if/admin/, spoofed XFF" wan 198.51.100.20 403 auth.example.org http://198.51.100.2/if/admin/ $S
check "internet /metrics" wan 198.51.100.20 403 pager.example.org http://198.51.100.2/metrics
check "internet /metrics/x" wan 198.51.100.20 403 pager.example.org http://198.51.100.2/metrics/x
check "internet /%6Detrics" wan 198.51.100.20 403 pager.example.org 'http://198.51.100.2/%6Detrics' --path-as-is
check "internet app / before first admin" wan 198.51.100.20 403 pager.example.org http://198.51.100.2/
check "LAN /if/admin/" lan 192.168.77.20 200 auth.example.org http://192.168.77.2/if/admin/
check "LAN auth /: Authentik gets the real address, not the client's XFF" lan 192.168.77.20 xff=192.168.77.20 auth.example.org http://192.168.77.2/ -H X-Forwarded-For:203.0.113.9
echo "== chained, nothing done (the docs' warning)"
check "internet via front /if/admin/ (fails open)" wan 198.51.100.20 200 auth.example.org http://198.51.100.10:81/if/admin/
echo "== chained, blocked at the front proxy"
check "internet via front /if/admin/" wan 198.51.100.20 403 auth.example.org http://198.51.100.10:82/if/admin/
check "internet via front /metrics" wan 198.51.100.20 403 pager.example.org http://198.51.100.10:82/metrics
echo "== chained, real address at this proxy"
check "internet via front /if/admin/" wan 198.51.100.20 403 auth.example.org http://198.51.100.10/if/admin/
check "internet via front /if/admin/, spoofed XFF" wan 198.51.100.20 403 auth.example.org http://198.51.100.10/if/admin/ $S
check "internet via front app / before first admin" wan 198.51.100.20 403 pager.example.org http://198.51.100.10/
check "internet via front /metrics" wan 198.51.100.20 403 pager.example.org http://198.51.100.10/metrics
check "LAN direct to this proxy /if/admin/" lan 192.168.77.20 200 auth.example.org http://192.168.77.3/if/admin/
check "LAN direct, spoofing an internet XFF from LAN isn't trusted" lan 192.168.77.20 xff=192.168.77.20 auth.example.org http://192.168.77.3/ -H X-Forwarded-For:203.0.113.9
out=$(docker exec $P-front wget -q -S -O /dev/null --header 'Host: auth.example.org' http://192.168.77.3/if/admin/ 2>&1 || true); case "$out" in *"403 Forbidden"*) echo "PASS  forwarder with no header is refused: $(echo "$out" | head -1)";; *) fails=$((fails+1)); echo "FAIL  forwarder with no header: $out";; esac
echo "== Authentik's X-Forwarded-For once open (LAN-only lines removed from auth /)"
sed '/allow 192.168.0.0\/16;.*LAN only until/d; /deny all;.*then remove/d; /deny 192.168.77.10;/{N; /LAN only until/d}' "$W/auth-r.conf" >"$W/auth-open.conf"
sed 's#/s/auth-r.conf#/s/auth-open.conf#' "$W/inner-realip.conf" >"$W/inner-open.conf"
docker run -d --name $P-inner2b --network $P-lan --ip 192.168.77.4 -v "$W:/s:ro" -v "$W/inner-open.conf:/etc/nginx/conf.d/default.conf:ro" $NG >/dev/null
sed 's/192.168.77.3/192.168.77.4/' "$W/front-server.conf" >"$W/front2.conf"; docker rm -f $P-front >/dev/null
run front 192.168.77.10 front2.conf 198.51.100.10; sleep 1
check "internet via front auth /, spoofed XFF: Authentik sees the client" wan 198.51.100.20 xff=198.51.100.20 auth.example.org http://198.51.100.10/ -H X-Forwarded-For:192.168.77.99
check "internet via front /if/admin/ still 403" wan 198.51.100.20 403 auth.example.org http://198.51.100.10/if/admin/
docker rm -f $P-inner2b >/dev/null
echo "== failures: $fails"; [ $fails = 0 ]
