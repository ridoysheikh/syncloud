#!/usr/bin/env bash
# End-to-end test of traffic insights (§5.7) with the controller's real
# system tasks: requests through Traefik become per-service request rates,
# error rates and latency (Traefik metrics scraped into VictoriaMetrics) and
# request lines (its access log, in VictoriaLogs); the traffic map shows the
# tasks that answered.
. "$(dirname "$0")/lib.sh"
WITH_REGISTRY=1
WITH_TRAEFIK=1
WITH_METRICS=1
CTL_FLAGS="--system-tasks=true"
trap cleanup EXIT
setup_cluster
for t in registry traefik vlogs vmetrics; do x sc-e2e-ctl docker load -q -i /opt/sc/$t.tar >/dev/null; done
wait_mesh
for _ in $(seq 1 60); do
  [ "$(api localhost:7070/api/v1/system/tasks | grep -o '"state":"running"' | wc -l)" -ge 4 ] && break; sleep 2
done
echo "  ✓ system tasks running"

echo "== services"
api localhost:7070/api/v1/projects -d '{"name":"shop"}' >/dev/null
SVC=localhost:7070/api/v1/projects/shop/environments/production/services
api -X PUT "$SVC/web" -d '{"image":"busybox:1.37","command":["httpd","-f","-p","8080","-h","/etc"],"ports":[{"container":8080}],"resources":{"cpu":0.05,"memory":16},"desiredCount":2}' >/dev/null
api -X PUT "$SVC/flaky" -d '{"image":"busybox:1.37","command":["sh","-c","while true; do printf \"HTTP/1.1 503 Service Unavailable\\r\\nContent-Length: 0\\r\\nConnection: close\\r\\n\\r\\n\" | nc -l -p 8080; done"],"ports":[{"container":8080}],"resources":{"cpu":0.05,"memory":16}}' >/dev/null
get() { x sc-e2e-ctl curl -s -o /dev/null -w '%{http_code}' -H "Host: $1" "http://127.0.0.1:8080$2" || true; }
for _ in $(seq 1 60); do [ "$(get web-production-shop.localhost /hostname)" = 200 ] && break; sleep 2; done
[ "$(get web-production-shop.localhost /hostname)" = 200 ] || fail "web is not routed"
for _ in $(seq 1 60); do [ "$(get flaky-production-shop.localhost /)" = 503 ] && break; sleep 2; done
echo "  ✓ web (2 tasks) and flaky (answers 503) routed by Traefik"

echo "== load"
x sc-e2e-ctl sh -c '
  for i in $(seq 1 120); do curl -s -o /dev/null -H "Host: web-production-shop.localhost" http://127.0.0.1:8080/hostname; done
  for i in $(seq 1 20); do curl -s -o /dev/null -H "Host: web-production-shop.localhost" http://127.0.0.1:8080/nope; done
  for i in $(seq 1 15); do curl -s -o /dev/null -H "Host: flaky-production-shop.localhost" -A e2e-agent http://127.0.0.1:8080/boom; done'
echo "  ✓ 140 requests to web (20 of them 404), 15 to flaky"

echo "== metrics"
route() { api localhost:7070/api/v1/traffic | grep -o "{\"serviceId\":\"svc_[a-z0-9]*\",\"project\":\"shop\",\"environment\":\"production\",\"service\":\"$1\"[^}]*}" || true; }
for _ in $(seq 1 30); do route web | grep -q '"rps":[0-9.]*[1-9]' && route flaky | grep -q '"errors5xx":[0-9.]*[1-9]' && break; sleep 2; done
r=$(route web); echo "$r" | grep -q '"rps":[0-9.]*[1-9]' || { api localhost:7070/api/v1/traffic; fail "no request rate for web"; }
echo "$r" | grep -q '"errors4xx":[0-9.]*[1-9]' || fail "web 404s missing: $r"
echo "$r" | grep -q '"errors5xx":0,' || fail "web has 5xx: $r"
echo "$r" | grep -q '"p95Ms":[0-9.]*[1-9]' || fail "web latency missing: $r"
route flaky | grep -q '"errors5xx":[0-9.]*[1-9]' || fail "flaky 5xx missing: $(route flaky)"
p50=$(echo "$r" | grep -o '"p50Ms":[0-9.]*' | cut -d: -f2)
awk -v v="$p50" 'BEGIN { exit !(v > 0 && v < 25) }' || fail "web p50 is ${p50}ms: Traefik's latency buckets are too coarse"
for _ in $(seq 1 30); do api "$SVC/web/traffic?range=15m" | grep -q '"key":"4xx"' && break; sleep 2; done
t=$(api "$SVC/web/traffic?range=15m")
echo "$t" | grep -q '"key":"2xx"' || fail "no 2xx series: $t"
echo "$t" | grep -q '"key":"4xx"' || fail "no 4xx series"
echo "$t" | grep -q '"key":"p95"' || fail "no latency series"
echo "$t" | grep -q '"key":"out"' || fail "no bandwidth series"
api "localhost:7070/api/v1/projects/shop/environments/production/traffic?range=15m" | grep -q '"key":"flaky"' || fail "no per-service series in the environment"
echo "  ✓ request rate, 4xx/5xx rates, p95 latency and charts per service, environment and in total"

echo "== request log"
q() { api "localhost:7070/api/v1/logs?project=shop&environment=production&since=15m&limit=1000&$1"; }
for _ in $(seq 1 15); do q "service=web&stream=access" | grep -q '"path":"/nope"' && break; sleep 1; done
l=$(q "service=web&stream=access")
[ "$(echo "$l" | grep -o '"status":"200"' | wc -l)" -ge 100 ] || fail "request lines missing: $(echo "$l" | head -c 600)"
echo "$l" | grep -q '"fields":{[^}]*"method":"GET"' || fail "no method field"
echo "$l" | grep -q '"upstream":"10\.' || fail "no upstream task address"
q "service=web&stream=access&status=4xx" | grep -q '"status":"200"' && fail "status filter"
q "service=flaky&stream=access&status=5xx" | grep -q '"path":"/boom"' || fail "flaky 503s missing"
q "service=web" | grep -q '"stream":"access"' && fail "request lines mixed into application logs"
echo "  ✓ Traefik's access log stored per service with method, path, status, latency and the answering task; status filter"

echo "== map"
m=$(api localhost:7070/api/v1/traffic/map)
echo "$m" | grep -q '"hosts":\["web-production-shop.localhost"\]' || fail "map hosts: $m"
web=$(echo "$m" | grep -o '"service":"web".*' | head -1)
[ "$(echo "$web" | grep -o '"rps":[0-9.]*[1-9]' | wc -l)" -ge 2 ] || fail "map task rates: $m"
echo "  ✓ traffic map: hostname → service → tasks, each task with its request rate"

echo "== synctl"
key=$(api localhost:7070/api/v1/iam/access-keys -d '{"description":"e2e"}')
KID=$(echo "$key" | grep -o '"id":"SYNAK[A-Z0-9]*"' | cut -d'"' -f4); KSEC=$(echo "$key" | grep -o '"secretAccessKey":"[^"]*"' | cut -d'"' -f4)
synctl() { x -i -e SYNCLOUD_ENDPOINT=http://127.0.0.1:7070 -e SYNCLOUD_ACCESS_KEY_ID="$KID" -e SYNCLOUD_SECRET_ACCESS_KEY="$KSEC" sc-e2e-ctl /opt/sc/synctl "$@" </dev/null; }
synctl traffic -p shop | grep -q 'shop/production/web' || fail "synctl traffic"
synctl requests flaky -p shop --status 5xx | grep -q '503 GET.*/boom' || fail "synctl requests: $(synctl requests flaky -p shop --status 5xx | head -3)"
synctl traffic map | grep -q 'web-production-shop.localhost' || fail "synctl traffic map"
echo "  ✓ synctl traffic, traffic map and requests"
echo "PASS"
