#!/usr/bin/env bash
# End-to-end test of routing extras and network visibility: Traefik
# middleware presets (§5.7), the validated custom configuration, the
# redacted raw config, central per-task probes (§5.6), IPAM, IP history and
# internal DNS (§8.1–8.2), and node/mesh throughput (§8.4).
#
#   test/e2e/routing.sh          # run and clean up
#   KEEP=1 test/e2e/routing.sh   # leave the nodes running for inspection
WITH_TRAEFIK=1
WITH_METRICS=1
. "$(dirname "$0")/lib.sh"
trap cleanup EXIT
setup_cluster
start_traefik
start_vlogs
start_vmetrics
wait_mesh

P=localhost:7070/api/v1/projects
SVC=$P/shop/environments/production/services/web
H='Host: web-production-shop.localhost'
# req [curl args…]: HTTP status of a request to web through Traefik.
req() { x sc-e2e-ctl curl -s -o /dev/null -w '%{http_code}' -H "$H" "$@" http://127.0.0.1:8080/hostname; }
wait_status() { # want, curl args…
  local want=$1 got; shift
  for _ in $(seq 1 20); do got=$(req "$@"); [ "$got" = "$want" ] && return 0; sleep 1; done
  fail "wanted HTTP $want, got $got ($*)"
}
mw() { api "$P/shop/middlewares" "$@"; }

echo "== service"
api "$P" -d '{"name":"shop"}' >/dev/null
api -X PUT "$SVC" -d '{"image":"busybox:1.37","command":["httpd","-f","-p","8080","-h","/etc"],"ports":[{"container":8080}],"health":{"type":"http","path":"/hostname","interval":2},"resources":{"cpu":0.02,"memory":16},"desiredCount":2}' >/dev/null
for _ in $(seq 1 60); do [ "$(api "$SVC" | grep -o '"running":[0-9]*' | head -1 | cut -d: -f2)" = 2 ] && break; sleep 2; done
wait_status 200
echo "  ✓ web answers through Traefik"

echo "== middleware presets"
mw -d '{"name":"staff","type":"basic-auth","config":{"users":[{"username":"ann","password":"correct-horse"}]},"services":["production/web"]}' | grep -q '"username":"ann"' || fail "create basic-auth"
mw | grep -q 'hash' && fail "password hash returned by the API"
wait_status 401
wait_status 200 -u ann:correct-horse
req -u ann:wrong-password | grep -q 401 || fail "wrong password accepted"
echo "  ✓ basic-auth: 401 without credentials, 200 with them; hashes are never returned"
api -X PUT "$P/shop/middlewares/staff" -d '{"config":{"users":[{"username":"ann"}]},"services":["production/web"]}' >/dev/null || fail "re-save without password"
wait_status 200 -u ann:correct-horse
echo "  ✓ saving again without a password keeps it"
mw -d '{"name":"office","type":"ip-allowlist","config":{"sourceRange":["203.0.113.0/24"]},"services":["production/web"]}' >/dev/null
wait_status 403 -u ann:correct-horse
api -X PUT "$P/shop/middlewares/office" -d '{"config":{"sourceRange":["127.0.0.1"]},"services":["production/web"]}' >/dev/null
wait_status 200 -u ann:correct-horse
echo "  ✓ ip-allowlist blocks other addresses (403) and lets 127.0.0.1 in"
mw -d '{"name":"hdrs","type":"security-headers","config":{"frameDeny":true,"noSniff":true},"services":["production/web"]}' >/dev/null
for _ in $(seq 1 10); do x sc-e2e-ctl curl -s -D - -o /dev/null -u ann:correct-horse -H "$H" http://127.0.0.1:8080/hostname | grep -qi '^X-Frame-Options: DENY' && break; sleep 1; done
x sc-e2e-ctl curl -s -D - -o /dev/null -u ann:correct-horse -H "$H" http://127.0.0.1:8080/hostname | grep -qi '^X-Content-Type-Options: nosniff' || fail "security headers missing"
echo "  ✓ security headers are added"
mw -d '{"name":"limit","type":"rate-limit","config":{"average":1,"burst":1},"services":["production/web"]}' >/dev/null
sleep 3
codes=""
for _ in $(seq 1 8); do codes="$codes $(req -u ann:correct-horse)"; done
echo "$codes" | grep -q 429 || fail "rate limit never answered 429: $codes"
echo "  ✓ rate-limit answers 429 beyond the burst"
mw -d '{"name":"bad","type":"cors","config":{"origins":["javascript:x"]}}' >/dev/null 2>&1 && fail "bad CORS origin accepted"
mw -d '{"name":"bad","type":"circuit-breaker","config":{"expression":"rm -rf /"}}' >/dev/null 2>&1 && fail "bad breaker expression accepted"
for n in limit hdrs office staff; do api -X DELETE "$P/shop/middlewares/$n" >/dev/null; done
wait_status 200
echo "  ✓ invalid presets are refused; deleting them restores open access"

echo "== custom configuration"
SVCNAME=$(api localhost:7070/api/v1/traefik/config | grep -o '"svc-svc_[a-z0-9]*-http"' | head -1 | tr -d '"')
[ -n "$SVCNAME" ] || fail "no generated service in the raw config"
custom="http:
  routers:
    legacy:
      rule: Host(\`legacy.localhost\`)
      entryPoints: [web]
      middlewares: [old]
      service: $SVCNAME
  middlewares:
    old:
      replacePath:
        path: /hostname"
body=$(printf '%s' "$custom" | python3 -c 'import json,sys; print(json.dumps({"yaml": sys.stdin.read()}))')
api localhost:7070/api/v1/traefik/custom/validate -d "$body" | grep -q '"valid":true' || fail "valid custom config rejected"
api -X PUT localhost:7070/api/v1/traefik/custom -d "$body" >/dev/null
for _ in $(seq 1 10); do [ "$(x sc-e2e-ctl curl -s -o /dev/null -w '%{http_code}' -H 'Host: legacy.localhost' http://127.0.0.1:8080/anything)" = 200 ] && break; sleep 1; done
[ "$(x sc-e2e-ctl curl -s -o /dev/null -w '%{http_code}' -H 'Host: legacy.localhost' http://127.0.0.1:8080/anything)" = 200 ] || fail "custom router not served"
for bad in '{"yaml":"http:\n  routers:\n    x:\n      rule: Host(`x`)\n      service: api@internal"}' \
           '{"yaml":"http:\n  routers:\n    x:\n      rule: Host(`x`)\n      service: nope"}' \
           '{"yaml":"tls:\n  certificates: []"}' '{"yaml":"http: ["}'; do
  api -X PUT localhost:7070/api/v1/traefik/custom -d "$bad" >/dev/null 2>&1 && fail "bad custom config accepted: $bad"
done
[ "$(x sc-e2e-ctl curl -s -o /dev/null -w '%{http_code}' -H 'Host: legacy.localhost' http://127.0.0.1:8080/anything)" = 200 ] || fail "a rejected config replaced the good one"
api localhost:7070/api/v1/traefik/config | grep -q '"legacy"' || fail "raw config lacks the custom router"
echo "  ✓ custom YAML is validated, merged and served; bad YAML (api@internal, unknown service, tls, syntax) is refused"

echo "== IPAM and DNS"
ipam=$(api localhost:7070/api/v1/network/ipam)
[ "$(echo "$ipam" | grep -o '"subnet":"10\.91\.[0-9]*\.0/24"' | wc -l)" = 3 ] || fail "IPAM nodes: $ipam"
echo "$ipam" | grep -q '"owner":"shop/production/web","ownerId":"task_' || fail "IPAM addresses: $ipam"
VIP=$(echo "$ipam" | grep -o '"service":"shop/production/web","vip":"10\.92\.[0-9.]*"' | grep -o '10\.92\.[0-9.]*')
[ -n "$VIP" ] || fail "no VIP in IPAM"
TIP=$(echo "$ipam" | grep -o '"ip":"10\.91\.[0-9.]*","owner":"shop/production/web"' | head -1 | grep -o '10\.91\.[0-9.]*')
api "localhost:7070/api/v1/network/ipam/history?ip=$TIP" | grep -q '"owner":"shop/production/web"' || fail "no history for $TIP"
api localhost:7070/api/v1/network/dns | grep -q "\"name\":\"web.production.shop.syncloud.internal\",\"kind\":\"service\",\"ips\":\[\"$VIP\"\]" || fail "DNS records"
api "localhost:7070/api/v1/network/dns/lookup?name=web.production.shop" | grep -q "\"found\":true,\"source\":\"internal\",\"kind\":\"service\",\"ips\":\[\"$VIP\"\]" || fail "lookup web.production.shop"
[ "$(api "localhost:7070/api/v1/network/dns/lookup?name=tasks.web.production.shop" | grep -o '10\.91\.' | wc -l)" = 2 ] || fail "lookup tasks."
api "localhost:7070/api/v1/network/dns/lookup?name=nope.production.shop" | grep -q '"found":false' || fail "lookup of a missing name"
echo "  ✓ subnets, addresses with owners, VIPs, IP history, DNS records and lookups"

echo "== central probes"
# Block the controller from one web task (a broken path); routing must avoid it.
T=$(api "$SVC/tasks" | grep -o '"node":"w[12]","desired":"running","state":"running","ip":"10\.91\.[0-9.]*"' | head -1)
TN=$(echo "$T" | cut -d'"' -f4); BIP=$(echo "$T" | grep -o '10\.91\.[0-9.]*')
[ -n "$BIP" ] || fail "no web task on a worker"
x -i "sc-e2e-$TN" nft -f - <<EOF
table inet e2eblock {
  chain f {
    type filter hook forward priority -200; policy accept;
    ip saddr 10.90.0.1 ip daddr $BIP drop
  }
}
EOF
for _ in $(seq 1 40); do api "$SVC/tasks" | grep -q "\"ip\":\"$BIP\"[^}]*\"central\":\"unreachable\"" && break; sleep 2; done
api "$SVC/tasks" | grep -q "\"ip\":\"$BIP\"[^}]*\"central\":\"unreachable\"" || fail "task not marked unreachable: $(api "$SVC/tasks" | head -c 1500)"
api localhost:7070/api/v1/traefik/config | grep -q "$BIP" && fail "unreachable task still routed"
for _ in $(seq 1 10); do [ "$(req)" = 200 ] || fail "requests failed while a task was unreachable"; done
OTHER=$(x "sc-e2e-$TN" docker ps -q --filter label=syncloud.service=web | head -1)
x "sc-e2e-$TN" docker exec "$OTHER" wget -q -T 3 -O /dev/null "http://$VIP:8080/hostname" || fail "VIP lost the task"
echo "  ✓ a task the controller cannot reach leaves Traefik's routes (VIP and requests keep working)"
x "sc-e2e-$TN" nft delete table inet e2eblock
for _ in $(seq 1 20); do api localhost:7070/api/v1/traefik/config | grep -q "$BIP" && break; sleep 2; done
api localhost:7070/api/v1/traefik/config | grep -q "$BIP" || fail "task did not come back to routing"
echo "  ✓ it returns once reachable"

echo "== throughput"
for _ in $(seq 1 30); do
  t=$(api "localhost:7070/api/v1/network/throughput?range=15m")
  [ "$(echo "$t" | grep -o '"key":"\(ctl-0\|w1\|w2\)"' | sort -u | wc -l)" = 3 ] && echo "$t" | grep -q '"meshRx":\[{' && break
  sleep 3
done
[ "$(echo "$t" | grep -o '"key":"\(ctl-0\|w1\|w2\)"' | sort -u | wc -l)" = 3 ] || fail "node throughput: $(echo "$t" | head -c 600)"
echo "$t" | grep -q '"meshRx":\[{' || fail "mesh throughput missing"
echo "  ✓ node and mesh throughput per node"

echo "== synctl"
key=$(api localhost:7070/api/v1/iam/access-keys -d '{"description":"e2e"}')
KID=$(echo "$key" | grep -o '"id":"SYNAK[A-Z0-9]*"' | cut -d'"' -f4); KSEC=$(echo "$key" | grep -o '"secretAccessKey":"[^"]*"' | cut -d'"' -f4)
synctl() { x -i -e SYNCLOUD_ENDPOINT=http://127.0.0.1:7070 -e SYNCLOUD_ACCESS_KEY_ID="$KID" -e SYNCLOUD_SECRET_ACCESS_KEY="$KSEC" sc-e2e-ctl /opt/sc/synctl "$@"; }
synctl network ipam </dev/null | grep -q "shop/production/web" || fail "synctl network ipam"
synctl network lookup web.production.shop </dev/null | grep -q "$VIP" || fail "synctl network lookup"
synctl traefik custom get </dev/null | grep -q "legacy" || fail "synctl traefik custom get"
echo '{"name":"zip","type":"compress","services":["production/web"]}' | synctl mw apply -p shop -f - | grep -q "Created middleware shop/zip" || fail "synctl mw apply"
synctl mw list </dev/null | grep -q "zip *compress" || fail "synctl mw list"
printf 'http:\n  routers:\n    y:\n      rule: Host(`y`)\n      service: nope\n' | synctl traefik custom apply -f - --dry-run >/dev/null 2>&1 && fail "synctl traefik custom --dry-run accepted a bad config"
echo "  ✓ synctl network ipam/lookup, traefik custom, mw apply/list"

echo "PASS"
