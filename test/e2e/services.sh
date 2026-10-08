#!/usr/bin/env bash
# End-to-end test of services (§4, §5.2–5.3): placement across nodes, scaling,
# rolling updates, replacement of crashed tasks, rollback and deletion.
#
#   test/e2e/services.sh          # run and clean up
#   KEEP=1 test/e2e/services.sh   # leave the nodes running for inspection
WITH_TRAEFIK=1
. "$(dirname "$0")/lib.sh"
trap cleanup EXIT
setup_cluster
start_traefik
start_vlogs
wait_mesh

SVC=localhost:7070/api/v1/projects/shop/environments/production/services/web
svc() { api "$SVC" "$@"; }
field() { grep -o "\"$1\":[^,}]*" | head -1 | cut -d: -f2- | tr -d '"'; }
tasks() { api "$SVC/tasks"; }
active_running() { tasks | grep -o '"desired":"running","state":"running"' | wc -l || true; }
wait_for() { # description, condition
  local d=$1; shift
  for _ in $(seq 1 60); do "$@" && return 0; sleep 2; done
  echo "--- tasks:"; tasks | head -c 3000; echo
  fail "timed out: $d"
}

echo "== project and service"
api localhost:7070/api/v1/projects -d '{"name":"shop"}' >/dev/null
spec() { # desired, env value
  echo "{\"image\":\"busybox:1.37\",\"command\":[\"httpd\",\"-f\",\"-p\",\"8080\",\"-h\",\"/etc\"],\"env\":{\"VERSION\":\"$2\"},\"ports\":[{\"container\":8080}],\"resources\":{\"cpu\":0.05,\"memory\":16},\"desiredCount\":$1}"
}
svc -X PUT -d "$(spec 3 one)" >/dev/null
wait_for "3 tasks running" sh -c "[ \$(docker exec $E2E-ctl curl -fs -b /tmp/jar localhost:7070/api/v1/projects/shop/environments/production/services/web/tasks | grep -o '\"state\":\"running\"' | wc -l) -ge 3 ]"
nodes=$(tasks | grep -o '"node":"[a-z0-9-]*"' | sort -u | wc -l)
[ "$nodes" -eq 3 ] || fail "spread placement used $nodes nodes, want 3"
echo "  ✓ 3 tasks running, spread over 3 nodes"

for ip in $(tasks | grep -o '"ip":"10\.91\.[0-9.]*"' | cut -d'"' -f4); do
  x $E2E-w1 wget -q -T 5 -O /dev/null "http://$ip:8080/hostname" || fail "task $ip not reachable from w1"
done
echo "  ✓ every task answers on its mesh IP"

# Traefik on the controller balances the default hostname over all tasks.
seen=""
for _ in $(seq 1 30); do
  h=$(x $E2E-ctl curl -fs -H 'Host: web-production-shop.localhost' http://127.0.0.1:8080/hostname || true)
  [ -n "$h" ] && seen="$seen $h"
done
distinct=$(echo $seen | tr ' ' '\n' | sort -u | grep -c . || true)
[ "$distinct" -eq 3 ] || fail "Traefik reached $distinct distinct tasks, want 3 (responses: $seen)"
api "$SVC" | grep -q '"endpoints":\["http://web-production-shop.localhost:8080"\]' || fail "service endpoint missing"
echo "  ✓ Traefik routes web-production-shop.localhost to all 3 tasks across nodes"

# A custom domain routes to the same tasks; its DNS check reports the record to create.
api "$SVC/domains" -d '{"host":"Shop.Example.TEST"}' | grep -q '"record":"shop.example.test. A ' || fail "domain add did not return the DNS record"
for _ in $(seq 1 10); do x $E2E-ctl curl -fs -H 'Host: shop.example.test' http://127.0.0.1:8080/hostname >/dev/null 2>&1 && break; sleep 1; done
x $E2E-ctl curl -fs -H 'Host: shop.example.test' http://127.0.0.1:8080/hostname >/dev/null || fail "custom domain not routed"
api "$SVC" | grep -q 'http://shop.example.test:8080' || fail "custom domain missing from endpoints"
api "$SVC/domains" -d '{"host":"shop.example.test"}' >/dev/null 2>&1 && fail "duplicate domain accepted"
api -X DELETE "$SVC/domains/shop.example.test" >/dev/null
echo "  ✓ custom domain routed through Traefik, listed in endpoints, removable"

# Central health (§5.6): end-to-end checks through Traefik, uptime, incidents.
HEALTH=localhost:7070/api/v1/health/services
wait_for "web healthy with uptime" sh -c "docker exec $E2E-ctl curl -fs -b /tmp/jar $HEALTH | grep -o '\"service\":\"web\",\"state\":\"healthy\"[^}]*\"uptime24h\":100' >/dev/null"
echo "  ✓ web is healthy with 100% uptime from end-to-end checks"
BROKEN=localhost:7070/api/v1/projects/shop/environments/production/services/broken
api -X PUT "$BROKEN" -d '{"image":"busybox:1.37","command":["sh","-c","while true; do printf \"HTTP/1.1 503 Service Unavailable\\r\\nContent-Length: 0\\r\\n\\r\\n\" | nc -l -p 8080; done"],"ports":[{"container":8080}],"resources":{"cpu":0.05,"memory":16},"desiredCount":1}' >/dev/null
wait_for "incident for broken" sh -c "docker exec $E2E-ctl curl -fs -b /tmp/jar 'localhost:7070/api/v1/health/incidents?open=1' | grep -q '\"state\":\"down\",\"cause\":\"public route check failed: HTTP 503[^}]*\"service\":\"broken\"'"
echo "  ✓ a service answering 503 opened a 'down' incident with its cause"
api -X PUT "$BROKEN" -d '{"image":"busybox:1.37","command":["httpd","-f","-p","8080","-h","/etc"],"ports":[{"container":8080}],"resources":{"cpu":0.05,"memory":16}}' >/dev/null
wait_for "incident closed" sh -c "! docker exec $E2E-ctl curl -fs -b /tmp/jar 'localhost:7070/api/v1/health/incidents?open=1' | grep -q '\"service\":\"broken\"'"
api -X DELETE "$BROKEN" >/dev/null
echo "  ✓ the incident closed after the fix"

# Service discovery from inside a task: DNS names, search domains and the VIP.
VIP=$(api "$SVC" | field vip)
[ -n "$VIP" ] || fail "service has no VIP"
one=$(tasks | grep -o '"node":"w2","desired":"running","state":"running","ip":"[^"]*","containerId":"[a-f0-9]*"' | head -1 | grep -o '"containerId":"[a-f0-9]*"' | cut -d'"' -f4)
[ -n "$one" ] || fail "no running task on w2"
inx() { x $E2E-w2 docker exec "$one" "$@"; }
for _ in $(seq 1 15); do inx nslookup web.production.shop.syncloud.internal 2>/dev/null | grep -q "$VIP" && break; sleep 1; done
inx nslookup web.production.shop.syncloud.internal 2>&1 | grep -q "$VIP" || fail "DNS name does not resolve to the VIP $VIP"
n=$(inx nslookup tasks.web.production.shop.syncloud.internal 2>/dev/null | grep -c 'Address.*10\.91\.' || true)
[ "$n" -eq 3 ] || fail "tasks record has $n addresses, want 3"
inx nslookup example.com >/dev/null 2>&1 || fail "external names do not resolve inside tasks"
echo "  ✓ DNS: web.production.shop.syncloud.internal → $VIP, tasks.… → 3 IPs, external names forwarded"
seen=""
for _ in $(seq 1 30); do
  h=$(inx wget -q -T 3 -O - "http://web:8080/hostname" 2>/dev/null || true)
  [ -n "$h" ] && seen="$seen $h"
done
distinct=$(echo $seen | tr ' ' '\n' | sort -u | grep -c . || true)
[ "$distinct" -ge 2 ] || fail "VIP reached $distinct distinct tasks via the short name, want at least 2 (responses: $seen)"
echo "$seen" | tr ' ' '\n' | grep -qx "$(inx hostname)" || echo "  (the calling task did not pick itself in 30 tries; hairpin not exercised)"
echo "  ✓ http://web:8080 (search domain + VIP) load-balanced over $distinct tasks on several nodes"

echo "== exec"
key=$(api localhost:7070/api/v1/iam/access-keys -d '{"description":"e2e"}')
KID=$(echo "$key" | grep -o '"id":"SYNAK[A-Z0-9]*"' | cut -d'"' -f4); KSEC=$(echo "$key" | grep -o '"secretAccessKey":"[^"]*"' | cut -d'"' -f4)
synctl() { x -i -e SYNCLOUD_ENDPOINT=http://127.0.0.1:7070 -e SYNCLOUD_ACCESS_KEY_ID="$KID" -e SYNCLOUD_SECRET_ACCESS_KEY="$KSEC" $E2E-ctl /opt/sc/synctl "$@"; }
out=$(synctl exec service/web -p shop -- cat /etc/hostname </dev/null)
[ -n "$out" ] || fail "exec printed nothing"
out=$(echo 'echo hello-from-stdin' | synctl exec service/web -p shop -- sh)
echo "$out" | grep -q hello-from-stdin || fail "exec stdin not delivered: $out"
set +e; synctl exec service/web -p shop -- sh -c 'exit 3' </dev/null; code=$?; set -e
[ "$code" = 3 ] || fail "exit code $code, want 3"
echo "  ✓ synctl exec: output, stdin and exit code (3) through controller and agent"

echo "== scale"
api "$SVC/scale" -d '{"desiredCount":1}' >/dev/null
wait_for "scaled to 1" sh -c "[ \$(docker exec $E2E-ctl curl -fs -b /tmp/jar localhost:7070/api/v1/projects/shop/environments/production/services/web/tasks | grep -o '\"desired\":\"running\"' | wc -l) -eq 1 ]"
echo "  ✓ scaled down to 1"

echo "== rolling update"
v=$(svc -X PUT -d "$(spec 2 two)")
[ "$(echo "$v" | field revision)" = 2 ] || fail "spec change did not create revision 2: $v"
wait_for "revision 2 running" sh -c "docker exec $E2E-ctl curl -fs -b /tmp/jar localhost:7070/api/v1/projects/shop/environments/production/services/web/tasks | grep -o '\"revision\":[0-9],\"nodeId\":\"[^\"]*\",\"node\":\"[^\"]*\",\"desired\":\"running\",\"state\":\"running\"' | grep -c '\"revision\":2' | grep -q 2"
old=$(tasks | grep -o '"revision":1,"nodeId":"[^"]*","node":"[^"]*","desired":"running"' | wc -l || true)
[ "$old" -eq 0 ] || fail "revision 1 tasks still active after the rollout"
echo "  ✓ revision 2 rolled out (2 tasks), revision 1 retired"

echo "== crash replacement"
first=$(tasks | grep -o '"id":"task_[a-z0-9]*","serviceId":"[^"]*","project":"shop","environment":"production","service":"web","revision":2,"nodeId":"[^"]*","node":"[a-z0-9-]*","desired":"running","state":"running","ip":"[^"]*","containerId":"[a-f0-9]*"' | head -1)
tid=$(echo "$first" | field id); node=$(echo "$first" | grep -o '"node":"[a-z0-9-]*"' | cut -d'"' -f4); cid=$(echo "$first" | grep -o '"containerId":"[a-f0-9]*"' | cut -d'"' -f4)
container=$E2E-$([ "$node" = ctl-0 ] && echo ctl || echo "$node")
x "$container" docker kill "$cid" >/dev/null
wait_for "replacement for $tid" sh -c "docker exec $E2E-ctl curl -fs -b /tmp/jar localhost:7070/api/v1/projects/shop/environments/production/services/web/tasks | grep -o '\"desired\":\"running\",\"state\":\"running\"' | wc -l | grep -q 2"
tasks | grep -q "\"id\":\"$tid\",[^}]*\"desired\":\"stopped\"" || fail "crashed task $tid not marked stopped"
echo "  ✓ killed task $tid on $node was replaced"

echo "== rollback"
v=$(api "$SVC/rollback" -d '{"revision":1}')
[ "$(echo "$v" | field revision)" = 3 ] || fail "rollback should create revision 3: $v"
echo "$v" | grep -q '"VERSION":"one"' || fail "revision 3 does not carry revision 1's spec"
wait_for "revision 3 running" sh -c "docker exec $E2E-ctl curl -fs -b /tmp/jar localhost:7070/api/v1/projects/shop/environments/production/services/web/tasks | grep -o '\"revision\":3,[^}]*\"desired\":\"running\",\"state\":\"running\"' | wc -l | grep -q 2"
echo "  ✓ rolled back to revision 1's spec as revision 3"

echo "== logs"
LOG=localhost:7070/api/v1/projects/shop/environments/production/services/logger
api -X PUT "$LOG" -d '{"image":"busybox:1.37","command":["sh","-c","i=0; while true; do echo \"tick $i from $HOSTNAME\"; i=$((i+1)); sleep 1; done"],"resources":{"cpu":0.05,"memory":16},"desiredCount":2}' >/dev/null
tailout=$(x $E2E-ctl sh -c "timeout 6 curl -sN -b /tmp/jar 'localhost:7070/api/v1/logs/tail?project=shop&service=logger' || true")
echo "$tailout" | grep -q '^data: {.*"message":"tick ' || fail "live tail delivered no lines"
echo "  ✓ live tail streams lines"
for _ in $(seq 1 20); do
  hist=$(api "localhost:7070/api/v1/logs?project=shop&service=logger&since=10m&limit=1000" || true)
  tasks_seen=$(echo "$hist" | grep -o '"taskId":"task_[a-z0-9]*"' | sort -u | wc -l || true)
  nodes_seen=$(echo "$hist" | grep -o '"node":"[a-z0-9-]*"' | sort -u | wc -l || true)
  [ "$tasks_seen" -ge 2 ] && [ "$nodes_seen" -ge 2 ] && break
  sleep 2
done
[ "$tasks_seen" -ge 2 ] && [ "$nodes_seen" -ge 2 ] || fail "history has lines from $tasks_seen tasks on $nodes_seen nodes, want 2 and 2"
echo "$hist" | grep -q '"message":"tick 0 from ' || fail "first lines missing from history"
echo "  ✓ history merges lines from 2 tasks on 2 nodes (VictoriaLogs)"
api -X DELETE "$LOG" >/dev/null

echo "== delete"
svc -X DELETE >/dev/null
wait_for "service deleted" sh -c "! docker exec $E2E-ctl curl -fs -b /tmp/jar localhost:7070/api/v1/projects/shop/environments/production/services/web >/dev/null 2>&1"
for n in "${NODES[@]}"; do
  left=$(x "$n" docker ps -aq --filter label=syncloud.service=web | wc -l)
  [ "$left" -eq 0 ] || fail "$left containers left on $n"
done
echo "  ✓ service deleted, no containers left"
echo "PASS"
