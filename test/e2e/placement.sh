#!/usr/bin/env bash
# End-to-end test of node limits and node pages (§6.3, §6.4): a project's
# allowed nodes, a service narrowing them, tasks moving when the list
# changes, the controller taking a service that names it, a node's history,
# and a shell on a node itself.
#
#   test/e2e/placement.sh          # run and clean up
#   KEEP=1 test/e2e/placement.sh   # leave the nodes running for inspection
. "$(dirname "$0")/lib.sh"
WITH_METRICS=1
trap cleanup EXIT
setup_cluster
start_vmetrics
wait_mesh

P=localhost:7070/api/v1/projects/shop
SVC=$P/environments/production/services/web
spec() { # desired, placement JSON
  echo "{\"image\":\"busybox:1.37\",\"command\":[\"sleep\",\"3600\"],\"resources\":{\"cpu\":0.05,\"memory\":8},\"placement\":$2,\"desiredCount\":$1}"
}
# Node names of the service's running tasks, sorted and joined.
nodes_of() { api "$SVC/tasks" | python3 -c 'import json,sys; print(",".join(sorted(t["node"] for t in json.load(sys.stdin)["items"] if t["desired"]=="running" and t["state"]=="running")))'; }
wait_nodes() { # want, description
  local got=""
  for _ in $(seq 1 60); do got=$(nodes_of); [ "$got" = "$1" ] && return 0; sleep 2; done
  api "$SVC" | head -c 600; echo; fail "$2: tasks on '$got', want '$1'"
}

echo "== project limited to w1"
api localhost:7070/api/v1/projects -d '{"name":"shop"}' >/dev/null
api -X PUT $P/nodes -d '{"nodes":["w1"]}' | grep -q '"nodes":\["w1"\]' || fail "set project nodes"
api -X PUT "$SVC" -d "$(spec 3 '{}')" >/dev/null
wait_nodes "w1,w1,w1" "every task on the project's only node"
echo "  ✓ 3 tasks, all on w1"

echo "== moving the project to w2"
api -X PUT $P/nodes -d '{"nodes":["w2"]}' >/dev/null
wait_nodes "w2,w2,w2" "tasks move off a node the project no longer allows"
echo "  ✓ tasks moved to w2 (replacements first)"

echo "== a service cannot widen its project's nodes"
code=$(x sc-e2e-ctl curl -s -o /dev/null -w '%{http_code}' -b /tmp/jar -H 'content-type: application/json' -H 'Origin: http://localhost:7070' -X PUT "$SVC" -d "$(spec 3 '{"nodes":["w1"]}')")
[ "$code" = 400 ] || fail "service placement outside the project: HTTP $code"
echo "  ✓ rejected (HTTP 400)"

echo "== the controller takes a service that names it"
api -X PUT $P/nodes -d '{"nodes":[]}' >/dev/null
api -X PUT "$SVC" -d "$(spec 2 '{"nodes":["ctl-0"]}')" >/dev/null
wait_nodes "ctl-0,ctl-0" "service limited to the controller node"
echo "  ✓ 2 tasks on ctl-0"
code=$(x sc-e2e-ctl curl -s -o /dev/null -w '%{http_code}' -b /tmp/jar -H 'content-type: application/json' -H 'Origin: http://localhost:7070' -X PUT $P/nodes -d '{"nodes":["w1"]}')
[ "$code" = 409 ] || fail "project list leaving out a node a service needs: HTTP $code"
echo "  ✓ the project cannot drop a node a service is limited to (HTTP 409)"

echo "== node history"
W1=$(api localhost:7070/api/v1/nodes | python3 -c 'import json,sys; print([n["id"] for n in json.load(sys.stdin)["items"] if n["name"]=="w1"][0])')
CTL=$(api localhost:7070/api/v1/nodes | python3 -c 'import json,sys; print([n["id"] for n in json.load(sys.stdin)["items"] if n["name"]=="ctl-0"][0])')
m=""
for _ in $(seq 1 30); do
  m=$(api "localhost:7070/api/v1/nodes/$CTL/metrics?range=15m")
  echo "$m" | python3 -c 'import json,sys; c=json.load(sys.stdin)["charts"]; sys.exit(0 if c["cpu"] and c["memory"] and c["tasks"] and c["serviceCpu"] else 1)' && break
  sleep 2
done
echo "$m" | python3 -c '
import json,sys
c=json.load(sys.stdin)["charts"]
keys={s["key"] for s in c["memory"]}
assert keys=={"used","total"}, keys
assert any(s["key"]=="shop/production/web" for s in c["serviceCpu"]), [s["key"] for s in c["serviceCpu"]]
tasks=max(p[1] for s in c["tasks"] for p in s["points"])
assert tasks>=2, tasks
print(f"  ✓ ctl-0 history: memory used and total, {tasks:.0f} tasks, CPU by service")' || fail "node metrics: $m"

echo "== node shell"
PAT=$(api localhost:7070/api/v1/iam/tokens -d '{"name":"e2e"}' | grep -o '"token":"[^"]*"' | cut -d'"' -f4)
out=$(x -e SYNCLOUD_TOKEN="$PAT" sc-e2e-ctl /opt/sc/synctl --endpoint http://127.0.0.1:7070 nodes shell w1 -- sh -c 'hostname; id -u')
echo "$out" | grep -qx "w1" || fail "node shell on w1: $out"
echo "$out" | grep -qx "0" || fail "node shell user: $out"
out=$(echo 'echo piped-$((6*7))' | x -i -e SYNCLOUD_TOKEN="$PAT" sc-e2e-ctl /opt/sc/synctl --endpoint http://127.0.0.1:7070 nodes shell ctl-0 -- sh)
echo "$out" | grep -q "piped-42" || fail "node shell stdin: $out"
api "localhost:7070/api/v1/audit?action=node:Shell" | grep -q '"node:Shell"' || fail "node shells are not audited"
echo "  ✓ a command and a piped shell on w1 and ctl-0, as root, audited"
x -e SYNCLOUD_TOKEN="$PAT" sc-e2e-ctl /opt/sc/synctl --endpoint http://127.0.0.1:7070 nodes metrics w1 --range 15m | grep -q '^cpu' || fail "synctl nodes metrics"
x -e SYNCLOUD_TOKEN="$PAT" sc-e2e-ctl /opt/sc/synctl --endpoint http://127.0.0.1:7070 projects nodes shop | grep -q 'any schedulable node' || fail "synctl projects nodes"
echo "  ✓ synctl nodes metrics, projects nodes"
echo "PASS"
