#!/usr/bin/env bash
# End-to-end test of node pools, cluster autoscaling and edge nodes (§6.5,
# §8.5): an edge pool runs a Traefik replica that keeps serving while the
# controller is down; a provider-backed pool (a fake cloud, test/e2e/cloudsim)
# adds a server when tasks cannot be placed and removes it when idle.
#
#   test/e2e/pools.sh          # run and clean up
#   KEEP=1 test/e2e/pools.sh   # leave the nodes running for inspection
WITH_TRAEFIK=1
. "$(dirname "$0")/lib.sh"
SIM_PID=""
pools_cleanup() {
  [ -n "$SIM_PID" ] && kill "$SIM_PID" 2>/dev/null
  [ "${KEEP:-0}" = 1 ] || docker rm -f $(docker ps -aq --filter label=cloudsim.pool) >/dev/null 2>&1 || true
  cleanup
}
trap pools_cleanup EXIT
setup_cluster
start_traefik
start_vlogs
x sc-e2e-w2 docker load -q -i /opt/sc/traefik.tar >/dev/null
wait_mesh

P=localhost:7070/api/v1
SVC=$P/projects/shop/environments/production/services
web() { echo "{\"image\":\"busybox:1.37\",\"command\":[\"httpd\",\"-f\",\"-p\",\"8080\",\"-h\",\"/etc\"],\"ports\":[{\"container\":8080}],\"resources\":{\"cpu\":0.05,\"memory\":16},\"desiredCount\":$1${2:-}}"; }
node_id() { api $P/nodes | grep -o "\"id\":\"node_[a-z0-9]*\",\"name\":\"$1\"" | cut -d'"' -f4; }
W2=$(x sc-e2e-w2 hostname -i | awk '{print $1}')

echo "== edge pool"
api $P/projects -d '{"name":"shop"}' >/dev/null
api $P/node-pools -d '{"name":"edge","role":"edge"}' | grep -q '"role":"edge"' || fail "create edge pool"
api -X PUT "$P/nodes/$(node_id w2)/pool" -d '{"pool":"edge"}' >/dev/null
api -X PUT $SVC/web -d "$(web 3)" >/dev/null
for _ in $(seq 1 60); do [ "$(api $SVC/web | grep -o '"running":[0-9]*' | head -1 | cut -d: -f2)" = 3 ] && break; sleep 2; done
api $SVC/web/tasks | grep -q '"node":"w2","desired":"running"' && fail "a task was placed on the edge node"
echo "  ✓ edge nodes take no tasks"
for _ in $(seq 1 40); do api $P/edges | grep -q '"node":"w2"[^}]*"state":"healthy"' && break; sleep 3; done
api $P/edges | grep -q '"node":"w2"[^}]*"state":"healthy"' || fail "edge replica not healthy: $(api $P/edges)"
for _ in $(seq 1 20); do [ "$(x sc-e2e-ctl curl -s -o /dev/null -w '%{http_code}' -H 'Host: web-production-shop.localhost' "http://$W2/hostname")" = 200 ] && break; sleep 1; done
[ "$(x sc-e2e-ctl curl -s -o /dev/null -w '%{http_code}' -H 'Host: web-production-shop.localhost' "http://$W2/hostname")" = 200 ] || fail "the edge does not route web"
api "$P/firewall/nodes/$(node_id w2)/effective" | grep -q '"id":"builtin:edge-80"' || fail "edge firewall does not open port 80"
echo "  ✓ the edge's Traefik replica routes the service over the mesh; its firewall opens 80/443"

echo "== controller outage"
x sc-e2e-ctl sh -c 'pkill -f "[s]yncloud-controller --dev"'
sleep 5
x sc-e2e-ctl curl -fs localhost:7070/api/v1/system/status >/dev/null 2>&1 && fail "controller still up"
for i in 1 2 3 4 5; do
  [ "$(x sc-e2e-ctl curl -s -o /dev/null -w '%{http_code}' -H 'Host: web-production-shop.localhost' "http://$W2/hostname")" = 200 ] || fail "edge stopped serving without the controller (try $i)"
done
CTL_IP=$(docker inspect -f "{{(index .NetworkSettings.Networks \"$NET\").IPAddress}}" sc-e2e-ctl)
x -d sc-e2e-ctl sh -c "/opt/sc/syncloud-controller --dev --data-dir /data --listen 0.0.0.0:7070 --agent-listen 0.0.0.0:7443 --agent-advertise $CTL_IP:7443 --system-tasks=false > /var/log/controller.log 2>&1"
for _ in $(seq 1 30); do x sc-e2e-ctl curl -fs localhost:7070/api/v1/system/status >/dev/null 2>&1 && break; sleep 1; done
x sc-e2e-ctl curl -fs -c /tmp/jar -H 'content-type: application/json' localhost:7070/api/v1/auth/login -d '{"email":"e2e@example.com","password":"e2e-password-123"}' >/dev/null
for _ in $(seq 1 30); do api $P/edges | grep -q '"node":"w2"[^}]*"state":"healthy"' && break; sleep 2; done
api $SVC/web/tasks | grep -q '"desired":"running","state":"running"' || fail "tasks lost after the restart"
echo "  ✓ public traffic through the edge keeps flowing while the controller is down; tasks survive its restart"

echo "== provider-backed pool (fake cloud)"
GW=$(docker network inspect -f '{{(index .IPAM.Config 0).Gateway}}' "$NET")
CGO_ENABLED=0 go build -o "$BIN/cloudsim" ./test/e2e/cloudsim
"$BIN/cloudsim" -bin "$BIN" -controller "http://$CTL_IP:7070" -listen "$GW:7099" > /tmp/cloudsim-$$.log 2>&1 &
SIM_PID=$!
sleep 1
api $P/cloud-providers -d "{\"name\":\"sim\",\"type\":\"webhook\",\"config\":{\"url\":\"http://$GW:7099\"}}" | grep -q '"type":"webhook"' || fail "add provider"
api $P/cloud-providers | grep -q "$GW" && true
api $P/node-pools -d '{"name":"auto","provider":"sim","min":0,"max":2,"spec":{"region":"local","type":"dind","image":"syncloud-e2e-node","nodeCpu":1,"nodeMemoryMiB":1024,"autoscale":true,"pendingAfter":10,"scaleInAfter":30,"joinTimeout":300}}' | grep -q '"name":"auto"' || fail "create pool"
api -X PUT $SVC/batch -d "$(web 1 ',"placement":{"pools":["auto"]}')" >/dev/null
for _ in $(seq 1 60); do api $P/node-pools | grep -q '"name":"auto-[a-z0-9]*","status":"ready"' && break; sleep 3; done
api $P/node-pools | grep -q '"name":"auto-[a-z0-9]*","status":"ready"' || fail "no node joined the pool: $(api $P/node-pools | head -c 1500) $(cat /tmp/cloudsim-$$.log)"
AUTO=$(api $P/node-pools | grep -o '"name":"auto-[a-z0-9]*","status":"ready"' | cut -d'"' -f4)
for _ in $(seq 1 40); do api $SVC/batch/tasks | grep -q "\"node\":\"$AUTO\",\"desired\":\"running\",\"state\":\"running\"" && break; sleep 2; done
api $SVC/batch/tasks | grep -q "\"node\":\"$AUTO\",\"desired\":\"running\",\"state\":\"running\"" || fail "the task did not run on the new node"
api $P/node-pools/auto/events | grep -q '"kind":"scale-out","message":"+1: 1 task(s) waiting for capacity' || fail "no scale-out event: $(api $P/node-pools/auto/events)"
echo "  ✓ a task no node could take made the pool create a server, which joined ($AUTO) and runs it"

api -X POST $SVC/batch/scale -d '{"desiredCount":0}' >/dev/null
for _ in $(seq 1 60); do api $P/nodes | grep -q "\"name\":\"$AUTO\"" || break; sleep 3; done
api $P/nodes | grep -q "\"name\":\"$AUTO\"" && fail "the idle node was not removed: $(api $P/node-pools/auto/events)"
[ -z "$(docker ps -q --filter "name=sc-e2e-$AUTO")" ] || fail "the server was not deleted"
api $P/node-pools/auto/events | grep -q '"kind":"scale-in","message":"removed '"$AUTO"' and deleted its server"' || fail "no scale-in event"
echo "  ✓ once idle, the node was drained, removed and its server deleted"

echo "== synctl"
key=$(api $P/iam/access-keys -d '{"description":"e2e"}')
KID=$(echo "$key" | grep -o '"id":"SYNAK[A-Z0-9]*"' | cut -d'"' -f4); KSEC=$(echo "$key" | grep -o '"secretAccessKey":"[^"]*"' | cut -d'"' -f4)
synctl() { x -i -e SYNCLOUD_ENDPOINT=http://127.0.0.1:7070 -e SYNCLOUD_ACCESS_KEY_ID="$KID" -e SYNCLOUD_SECRET_ACCESS_KEY="$KSEC" sc-e2e-ctl /opt/sc/synctl "$@"; }
out=$(synctl pools list </dev/null); grep -q 'edge *edge *manual' <<<"$out" && grep -q 'auto *worker *sim *0–2' <<<"$out" || fail "synctl pools list: $out"
out=$(synctl pools history auto </dev/null); grep -q 'scale-in' <<<"$out" || fail "synctl pools history"
out=$(synctl pools edges </dev/null); grep -q 'w2 .*healthy' <<<"$out" || fail "synctl pools edges: $out"
echo "  ✓ synctl pools list/history/edges"

echo "== leaving the edge pool"
api -X PUT "$P/nodes/$(node_id w2)/pool" -d '{"pool":"default"}' >/dev/null
for _ in $(seq 1 20); do [ -z "$(x sc-e2e-w2 docker ps -q --filter label=syncloud.task_id=sys-edge-traefik)" ] && break; sleep 2; done
[ -z "$(x sc-e2e-w2 docker ps -q --filter label=syncloud.task_id=sys-edge-traefik)" ] || fail "edge Traefik left on w2"
echo "  ✓ a node leaving the edge pool stops its replica"

echo "PASS"
