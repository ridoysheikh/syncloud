#!/usr/bin/env bash
# End-to-end test of ports and domains (Phase 15c): labels and the
# generated address, custom domains shared by path with prefix stripping,
# redirects, public TCP and UDP ports through Traefik with firewall rules,
# and the synctl commands.
#
#   test/e2e/ports.sh                         # run and clean up
#   E2E_PREFIX=sc-prt KEEP=1 test/e2e/ports.sh # beside a kept cluster
WITH_TRAEFIK=1
WORKERS=${WORKERS-w1}
CTL_FLAGS="--public-ports 20000-20001"
. "$(dirname "$0")/lib.sh"
trap cleanup EXIT
setup_cluster
# Traefik as the system task runs it, with the entrypoints of the two
# public ports the range can hand out.
x "$E2E-ctl" docker load -q -i /opt/sc/traefik.tar >/dev/null
TOK=$(x "$E2E-ctl" cat /data/traefik.token)
x "$E2E-ctl" docker run -d --name traefik --network host traefik:v3.7.13 \
  --entrypoints.web.address=:8080 --entrypoints.websecure.address=:8443 \
  --entrypoints.tcp-20000.address=:20000 --entrypoints.udp-20001.address=:20001/udp \
  --providers.http.endpoint=http://127.0.0.1:7070/internal/traefik/config --providers.http.pollInterval=1s \
  "--providers.http.headers.X-Syncloud-Token=$TOK" >/dev/null
wait_mesh

P=localhost:7070/api/v1/projects/shop
ENV=$P/environments/production
json() { python3 -c "import json,sys; d=json.load(sys.stdin); print($1)"; }
# get HOST PATH: status and body of an HTTP request through Traefik.
get() { x "$E2E-ctl" curl -s -m 5 -w ' %{http_code}' -H "Host: $1" "http://127.0.0.1:8080$2" | tr -d '\n' || true; }
wait_get() { # host path want-substring
  local out
  for _ in $(seq 1 40); do out=$(get "$1" "$2"); case "$out" in *"$3"*) return 0 ;; esac; sleep 1; done
  fail "GET $1$2: wanted '$3', got '$out'"
}
# wait_gone HOST PATH: until the service no longer answers there (unknown
# hosts reach the dashboard without a base domain).
wait_gone() {
  local out
  for _ in $(seq 1 40); do out=$(get "$1" "$2"); case "$out" in "web 200"|"docs 200") sleep 1 ;; *) return 0 ;; esac; done
  fail "GET $1$2 still answers: '$out'"
}
traefik_config() { x "$E2E-ctl" curl -fs -H "X-Syncloud-Token: $TOK" localhost:7070/internal/traefik/config; }
ctl_id=$(api localhost:7070/api/v1/nodes | json "next(n['id'] for n in d['items'] if n['name'] == 'ctl-0')")
fw_rules() { api "localhost:7070/api/v1/firewall/nodes/$ctl_id/effective" | json "' '.join(r['id'] for r in d['rules'])"; }

# web answers /hostname with its own name, on 8080 (http) and 8081 (tcp).
site() { # name
  echo "{\"image\":\"busybox:1.37\",\"command\":[\"sh\",\"-c\",\"mkdir -p /www && echo $1 > /www/hostname && httpd -p 8081 -h /www && exec httpd -f -p 8080 -h /www\"],\"ports\":[{\"name\":\"http\",\"container\":8080},{\"name\":\"raw\",\"container\":8081,\"protocol\":\"tcp\"},{\"name\":\"game\",\"container\":7777,\"protocol\":\"udp\"}],\"resources\":{\"cpu\":0.05,\"memory\":16},\"health\":{\"type\":\"http\",\"path\":\"/hostname\",\"interval\":2,\"startPeriod\":1},\"desiredCount\":1}"
}
api localhost:7070/api/v1/projects -d '{"name":"shop"}' >/dev/null
api -X PUT "$ENV/services/web" -d "$(site web)" >/dev/null
api -X PUT "$ENV/services/docs" -d "$(site docs)" >/dev/null

echo "== generated address and label"
wait_get web-production-shop.localhost /hostname "web 200"
api -X PUT "$ENV/services/web/routing" -d '{"ports":{"http":{"label":"shop"}}}' | json "d['items'][0]['host']" | grep -qx shop.localhost || fail "label"
wait_get shop.localhost /hostname "web 200"
wait_gone web-production-shop.localhost /hostname
api -X PUT "$ENV/services/docs/routing" -d '{"ports":{"http":{"label":"shop"}}}' >/dev/null 2>&1 && fail "a label of another service was accepted"
api -X PUT "$ENV/services/web/routing" -d '{"ports":{"http":{"label":"registry"}}}' >/dev/null 2>&1 && fail "a reserved label was accepted"
api -X PUT "$ENV/services/web/routing" -d '{"ports":{"http":{"generated":false}}}' >/dev/null
wait_gone shop.localhost /hostname
api "$ENV/services/web" | json "d['endpoints']" | grep -q "shop.localhost" && fail "an endpoint for a switched-off address"
api -X PUT "$ENV/services/web/routing" -d '{"ports":{"http":{"generated":true,"label":""}}}' >/dev/null
wait_get web-production-shop.localhost /hostname "web 200"
echo "  ✓ a label renames the address (unique, not reserved); the generated address can be switched off and on"

echo "== custom domains: paths, prefix stripping, redirects"
api "$ENV/services/docs/domains" -d '{"host":"shop.example.com"}' >/dev/null
api "$ENV/services/web/domains" -d '{"host":"shop.example.com","path":"/api","stripPrefix":true}' >/dev/null
api "$ENV/services/web/domains" -d '{"host":"shop.example.com","path":"/api"}' >/dev/null 2>&1 && fail "the same host and path twice"
api "$ENV/services/docs/domains" -d '{"host":"www.shop.example.com","redirectTo":"shop.example.com"}' >/dev/null
wait_get shop.example.com /hostname "docs 200"
wait_get shop.example.com /api/hostname "web 200"
loc=$(x "$E2E-ctl" curl -s -o /dev/null -w '%{http_code} %{redirect_url}' -H "Host: www.shop.example.com" http://127.0.0.1:8080/a/b?c=1)
[ "$loc" = "301 https://shop.example.com/a/b?c=1" ] || fail "redirect: $loc"
api "$ENV/services/web" | json "d['endpoints']" | grep -Eq "shop.example.com(:[0-9]+)?/api" || fail "the path domain is not an endpoint"
id=$(api "$ENV/services/web/domains" | json "d['items'][0]['id']")
api -X DELETE "$ENV/services/web/domains/$id" >/dev/null
for _ in $(seq 1 40); do case "$(get shop.example.com /api/hostname)" in "web 200") sleep 1 ;; *) break ;; esac; done
case "$(get shop.example.com /api/hostname)" in "web 200") fail "the removed path still reaches web" ;; esac
echo "  ✓ two services share a host by path (prefix stripped), a host redirects with its path, a domain is removed by ID"

echo "== public TCP and UDP ports"
rs=$(api -X PUT "$ENV/services/web/routing" -d '{"ports":{"raw":{"public":true,"allow":["127.0.0.1","10.0.0.0/8"]},"game":{"public":true}}}')
[ "$(echo "$rs" | json "d['items'][1]['address'] + ' ' + d['items'][2]['address']")" = "localhost:20000 localhost:20001" ] || fail "public addresses: $rs"
api -X PUT "$ENV/services/docs/routing" -d '{"ports":{"raw":{"public":true}}}' >/dev/null 2>&1 && fail "a port beyond the range was assigned"
for _ in $(seq 1 30); do out=$(x "$E2E-ctl" curl -s -m 3 http://127.0.0.1:20000/hostname | tr -d '\n' || true); [ "$out" = web ] && break; sleep 1; done
[ "$out" = web ] || fail "public TCP port: '$out'"
traefik_config | json "d['udp']['routers']['pub-udp-20001']['entryPoints'][0] + ' ' + d['udp']['services']['pub-udp-20001']['loadBalancer']['servers'][0]['address']" | grep -q '^udp-20001 10\.' || fail "no UDP route"
traefik_config | json "d['tcp']['middlewares']['pub-tcp-20000-allow']['ipAllowList']['sourceRange']" | grep -q "127.0.0.1/32" || fail "no allow-list"
rules=$(fw_rules)
case "$rules" in *builtin:service-tcp-20000*builtin:service-udp-20001*) ;; *) fail "firewall rules: $rules" ;; esac
api "$ENV/services/web" | json "d['endpoints']" | grep -q "udp://localhost:20001" || fail "public ports are not endpoints"
echo "  ✓ a TCP port is served on public port 20000 (allow-list on Traefik and the firewall), a UDP port on 20001"
api -X PUT "$ENV/services/web/routing" -d '{"ports":{"raw":{"public":false}}}' >/dev/null
for _ in $(seq 1 10); do traefik_config | grep -q pub-tcp-20000 || break; sleep 1; done
traefik_config | grep -q pub-tcp-20000 && fail "the closed port is still routed"
case "$(fw_rules)" in *service-tcp-20000*) fail "the closed port is still open in the firewall" ;; esac
api -X PUT "$ENV/services/docs/routing" -d '{"ports":{"raw":{"public":true}}}' | json "d['items'][1]['publicPort']" | grep -qx 20000 || fail "the released port was not reused"
api -X DELETE "$ENV/services/docs" >/dev/null
for _ in $(seq 1 30); do case "$(fw_rules)" in *service-tcp-20000*) sleep 1 ;; *) break ;; esac; done
case "$(fw_rules)" in *service-tcp-20000*) fail "a deleted service's port is still open" ;; esac
echo "  ✓ closing a port and deleting a service release it (route and firewall rule gone); a released port is reused"

echo "== project addresses and synctl"
key=$(api localhost:7070/api/v1/iam/access-keys -d '{"description":"e2e"}')
KID=$(echo "$key" | json "d['id']"); KSEC=$(echo "$key" | json "d['secretAccessKey']")
synctl() { x -i -e SYNCLOUD_ENDPOINT=http://127.0.0.1:7070 -e SYNCLOUD_ACCESS_KEY_ID="$KID" -e SYNCLOUD_SECRET_ACCESS_KEY="$KSEC" "$E2E-ctl" /opt/sc/synctl "$@" </dev/null; }
synctl services routing web -p shop | grep -q "localhost:20001" || fail "synctl services routing"
synctl services expose web http -p shop --label front | grep -q "front.localhost" || fail "synctl services expose --label"
synctl projects addresses shop | grep -q "front.localhost" || fail "synctl projects addresses"
synctl services domains add web api.example.com -p shop --path /v1 --strip-prefix | grep -q "Routing api.example.com/v1" || fail "synctl domains add --path"
kinds=$(api "$P/addresses?environment=production" | json "' '.join(sorted(set(a['kind'] for a in d['items'])))")
[ "$kinds" = "domain generated public" ] || fail "address kinds: $kinds"
echo "  ✓ project addresses list every kind; synctl routing, expose, projects addresses and domains add --path"
echo "PASS"
