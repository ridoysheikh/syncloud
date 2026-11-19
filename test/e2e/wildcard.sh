#!/usr/bin/env bash
# End-to-end test of wildcard hosts on services (Phase 28): *.example.com and
# *.sub.example.com route through real Traefik on real nodes, one label each;
# an exact host beats a wildcard; the apex and deeper names are not matched;
# a path on a wildcard works; platform names and grants are enforced; and
# services the wildcard does not cover keep working as before.
#
#   test/e2e/wildcard.sh          # run and clean up
#   KEEP=1 test/e2e/wildcard.sh   # leave the nodes running for inspection
export E2E_PREFIX=${E2E_PREFIX:-sc-wc}
WITH_TRAEFIK=1
WORKERS=${WORKERS-w1}
. "$(dirname "$0")/lib.sh"
trap cleanup EXIT
setup_cluster
start_traefik
wait_mesh

P=localhost:7070/api/v1
json() { python3 -c "import json,sys; d=json.load(sys.stdin); print($1)"; }
# get HOST [PATH]: what Traefik answers for a Host header. A host no route matches
# lands on the dashboard (this cluster has no base domain), which is "dashboard".
get() {
  local out
  out=$(x "$E2E-ctl" curl -s -m 3 -w '|%{http_code}' -H "Host: $1" "http://127.0.0.1:8080${2:-/}")
  case "$out" in
    *'<title>SynCloud</title>'*) echo dashboard ;;
    *'|200') printf '%s' "${out%|200}" | tr -d '\n'; echo ;;
    *) echo "HTTP ${out##*|}" ;;
  esac
}
expect() { # host path want
  local got want=$3
  for _ in $(seq 1 30); do got=$(get "$1" "$2"); [ "$got" = "$want" ] && return 0; sleep 1; done
  fail "$1$2 answered '$got', wanted '$want'"
}
mkweb() { # name content
  api -X PUT "$P/projects/shop/environments/production/services/$1" -d "{\"image\":\"busybox:1.37\",\"command\":[\"sh\",\"-c\",\"mkdir -p /www/app && echo $2 > /www/index.html && echo $2-app > /www/app/index.html && exec httpd -f -p 8080 -h /www\"],\"ports\":[{\"name\":\"http\",\"container\":8080}],\"resources\":{\"cpu\":0.05,\"memory\":16},\"health\":{\"type\":\"http\",\"path\":\"/\",\"interval\":2,\"startPeriod\":1},\"desiredCount\":1}" >/dev/null
}
dom() { api "$P/projects/shop/environments/production/services/$1/domains" -d "$2"; }

api $P/projects -d '{"name":"shop"}' >/dev/null
api $P/projects -d '{"name":"blog"}' >/dev/null
for s in wild nested exact; do mkweb $s "$s"; done
for _ in $(seq 1 60); do
  n=0; for s in wild nested exact; do [ "$(api "$P/projects/shop/environments/production/services/$s" | json "d['running']")" = 1 ] && n=$((n+1)); done
  [ $n = 3 ] && break; sleep 2
done
[ $n = 3 ] || fail "the services did not start"

echo "== wildcard hosts are accepted, validated and refused where they must be"
dom wild '{"host":"*.example.test"}' >/dev/null || fail "a wildcard host was not accepted"
dom nested '{"host":"*.sub.example.test"}' >/dev/null || fail "a nested wildcard was not accepted"
dom exact '{"host":"api.example.test"}' >/dev/null || fail "an exact host was not accepted"
for bad in '*' '*.' '*.com' 'a.*.example.test' '*.*.example.test' '*.localhost' '**.example.test'; do
  dom wild "{\"host\":\"$bad\"}" >/dev/null 2>&1 && fail "the host '$bad' was accepted"
done
dom nested '{"host":"*.example.test"}' >/dev/null 2>&1 && fail "the same wildcard went to a second service"
api "$P/projects/shop/environments/production/services/wild/domains" | json "[d['wildcard'] for d in d['items']]" | grep -q True || fail "the list does not say it is a wildcard"
echo "  ✓ *.example.test, *.sub.example.test and api.example.test are accepted; '*', '*.com', a middle or double wildcard and *.localhost are refused; a second service cannot take the same wildcard"

echo "== Traefik routes them: one label, the exact host first"
expect shop.example.test / wild
expect anything.example.test / wild
expect api.example.test / exact              # an exact host beats the wildcard that also matches it
expect x.sub.example.test / nested           # the longer wildcard for its own names
expect sub.example.test / wild               # "sub" is one label of *.example.test
expect example.test / dashboard             # the apex is not a label
expect a.b.example.test / dashboard         # two labels: neither wildcard matches
expect other.test / dashboard
echo "  ✓ shop.example.test → wild, api.example.test → exact, x.sub.example.test → nested; example.test and a.b.example.test are not matched (they fall to the dashboard)"

echo "== a path on a wildcard, a redirect from a wildcard"
dom exact '{"host":"*.example.test","path":"/app"}' >/dev/null || fail "a wildcard with a path"
expect shop.example.test /app/ exact-app
expect shop.example.test / wild
dom wild '{"host":"*.old.example.test","redirectTo":"new.example.test"}' >/dev/null || fail "a wildcard redirect"
loc=""
for _ in $(seq 1 20); do loc=$(x "$E2E-ctl" curl -s -m 3 -D - -o /dev/null -H "Host: a.old.example.test" "http://127.0.0.1:8080/some/path" | tr -d '\r' | sed -n 's/^[Ll]ocation: //p'); [ -n "$loc" ] && break; sleep 1; done
case "$loc" in *new.example.test/some/path) ;; *) fail "the wildcard redirect answered '$loc'" ;; esac
echo "  ✓ /app of a wildcard goes to its own service, the rest to the other; a wildcard redirect keeps the path"

echo "== registered domains decide who may use their names"
api $P/domains -d '{"name":"granted.test","grants":[{"project":"shop","wildcards":false}]}' >/dev/null || fail "register a domain for shop only"
dom wild '{"host":"ok.granted.test"}' >/dev/null || fail "the granted project was refused"
dom wild '{"host":"*.granted.test"}' >/dev/null 2>&1 && fail "wildcards were not granted"
api -X PUT "$P/projects/blog/environments/production/services/web" -d '{"image":"busybox:1.37","ports":[{"name":"http","container":8080}],"resources":{"cpu":0.02,"memory":16},"desiredCount":0}' >/dev/null
out=$(x "$E2E-ctl" curl -s -b /tmp/jar -H 'content-type: application/json' -H 'Origin: http://localhost:7070' "$P/projects/blog/environments/production/services/web/domains" -d '{"host":"blog.granted.test"}')
echo "$out" | grep -q 'not granted' || fail "blog was not refused with the reason: $out"
dom wild '{"host":"free.elsewhere.test"}' >/dev/null || fail "a name outside every registered domain must stay free"
echo "  ✓ a project with a grant uses its names, wildcards only when granted; another project is refused with the reason; unregistered names stay free"

echo "== what existed keeps working"
dom exact '{"host":"legacy.example.org"}' >/dev/null
expect legacy.example.org / exact
echo "  ✓ a plain custom domain still routes"
echo "PASS"
