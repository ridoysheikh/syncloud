#!/usr/bin/env bash
# End-to-end test of security groups (§8.3): default project/environment
# isolation on the same node and across nodes, a custom group attached to a
# service, egress rules, job runs as members, the drop log, rule hit
# counters, the preview and the reachability check.
#
#   test/e2e/secgroups.sh          # run and clean up
#   KEEP=1 test/e2e/secgroups.sh   # leave the nodes running for inspection
WITH_TRAEFIK=1
. "$(dirname "$0")/lib.sh"
trap cleanup EXIT
setup_cluster
start_vlogs
wait_mesh

P=localhost:7070/api/v1/projects
svc() { api "$P/$1/environments/$2/services/$3" "${@:4}"; }
web() { # desired count
  echo "{\"image\":\"busybox:1.37\",\"command\":[\"httpd\",\"-f\",\"-p\",\"${2:-8080}\",\"-h\",\"/etc\"],\"ports\":[{\"container\":${2:-8080}}],\"resources\":{\"cpu\":0.02,\"memory\":16},\"desiredCount\":$1}"
}
sleeper='{"image":"busybox:1.37","command":["sleep","1d"],"resources":{"cpu":0.02,"memory":16},"desiredCount":3}'
running() { svc "$@" | grep -o '"running":[0-9]*' | head -1 | cut -d: -f2; }
wait_running() { # project env service count
  for _ in $(seq 1 60); do [ "$(running "$1" "$2" "$3")" = "$4" ] && return 0; sleep 2; done
  fail "$1/$2/$3 did not reach $4 running tasks"
}
# cid NODE PROJECT ENV SERVICE: a running container of the service on NODE.
cid() { x "$1" docker ps -q --filter "label=syncloud.project=$2" --filter "label=syncloud.environment=$3" --filter "label=syncloud.service=$4" | head -1; }
# get NODE PROJECT ENV SERVICE URL: fetch URL from inside that container.
get() {
  local c; c=$(cid "$1" "$2" "$3" "$4"); [ -n "$c" ] || fail "no $2/$3/$4 task on $1"
  x "$1" docker exec "$c" wget -q -T 2 -O /dev/null "$5" 2>/dev/null
}
must() { # description, then a get command; retried while rules propagate
  local d=$1; shift
  for _ in $(seq 1 10); do get "$@" && return 0; sleep 1; done
  fail "$d: $* was blocked"
}
never() { local d=$1; shift; get "$@" && fail "$d: $* was allowed"; return 0; }
tasks() { api "$P/$1/environments/$2/services/$3/tasks"; }
ips() { tasks "$1" "$2" "$3" | grep -o '"desired":"running","state":"running"[^}]*"ip":"10\.91\.[0-9.]*"' | grep -o '10\.91\.[0-9.]*'; }
node_of() { tasks "$1" "$2" "$3" | grep -o "\"node\":\"[a-z0-9-]*\"[^}]*\"ip\":\"$4\"" | head -1 | cut -d'"' -f4; }

echo "== projects and services"
api "$P" -d '{"name":"shop"}' >/dev/null
api "$P/shop/environments" -d '{"name":"staging"}' >/dev/null
api "$P" -d '{"name":"billing"}' >/dev/null
svc shop production web -X PUT -d "$(web 3)" >/dev/null
svc shop production db -X PUT -d "$(web 1 5432)" >/dev/null
svc shop staging web -X PUT -d "$(web 3)" >/dev/null
svc billing production worker -X PUT -d "$sleeper" >/dev/null
wait_running shop production web 3
wait_running shop production db 1
wait_running shop staging web 3
wait_running billing production worker 3
api localhost:7070/api/v1/security-groups | grep -q '"project":"shop","name":"default"' || fail "shop has no default group"
echo "  ✓ every project has a default group"

[ "$(ips shop production web | wc -l)" = 3 ] || fail "task IPs: $(tasks shop production web)"
echo "== default isolation"
for n in sc-e2e-ctl sc-e2e-w1 sc-e2e-w2; do
  must "same environment ($n)" "$n" shop production web http://db:5432/hostname
done
echo "  ✓ services in the same environment reach each other (through the VIP, from every node)"
for n in sc-e2e-ctl sc-e2e-w1 sc-e2e-w2; do
  for ip in $(ips shop production web); do
    never "staging → production" "$n" shop staging web "http://$ip:8080/hostname"
    never "billing → shop" "$n" billing production worker "http://$ip:8080/hostname"
  done
done
echo "  ✓ another environment and another project are blocked, on the same node and across nodes"
for ip in $(ips shop production web); do
  x sc-e2e-w1 wget -q -T 3 -O /dev/null "http://$ip:8080/hostname" || fail "node w1 cannot reach task $ip"
done
echo "  ✓ node hosts (Traefik, health checks) reach every task"

# Same-node traffic is routed through the forward chain (isolated bridge ports).
for n in sc-e2e-w1 sc-e2e-w2; do
  x "$n" sh -c 'for p in /sys/class/net/syncloud0/brif/*; do ip -d link show $(basename $p) | grep -q "isolated on" || exit 1; done' || fail "bridge ports on $n are not isolated"
done
echo "  ✓ bridge ports are isolated"

echo "== drop log"
for _ in $(seq 1 15); do
  d=$(api "localhost:7070/api/v1/firewall/drops?since=10m")
  echo "$d" | grep -q '"direction":"in","src":"10\.91\.[0-9.]*","srcName":"shop/staging/web","dst":"10\.91\.[0-9.]*","dstName":"shop/production/web","protocol":"tcp","port":8080' && break
  sleep 2
done
echo "$d" | grep -q '"srcName":"shop/staging/web"' || fail "drop log has no staging → production drop: $(echo "$d" | head -c 600)"
echo "$d" | grep -q '"source":"logs"' || fail "drop log was not read from VictoriaLogs"
echo "  ✓ dropped attempts are in the drop log with names, read back from VictoriaLogs"

echo "== a custom group"
G='{"name":"db","description":"Postgres","inbound":[{"protocol":"tcp","ports":"5432","peers":["environment:self"]},{"protocol":"tcp","ports":"5432","peers":["service:billing/production/worker"]}],"outbound":[{"protocol":"any","peers":["any"]}],"services":["production/db"]}'
pv=$(api "$P/shop/security-groups/preview" -d "$G")
echo "$pv" | grep -q '"service":"production/db"' || fail "preview: $pv"
echo "$pv" | grep -q '"+ in  tcp 5432 from service:billing/production/worker  (db)"' || fail "preview has no added rule: $pv"
echo "$pv" | grep -q '"- groups: shop/default"' || fail "preview has no membership change: $pv"
echo "  ✓ preview shows production/db's effective rules as a diff, with its tasks and nodes"
api "$P/shop/security-groups" -d "$G" | grep -q '"members":\["production/db"\]' || fail "create group"
api "$P/shop/security-groups" -d "$G" >/dev/null 2>&1 && fail "duplicate group name accepted"
api "$P/shop/security-groups" -d '{"name":"x","inbound":[{"protocol":"tcp","ports":"80","peers":["service:production/ghost"]}]}' >/dev/null 2>&1 && fail "rule for a missing service accepted"
DB=$(ips shop production db)
for n in sc-e2e-ctl sc-e2e-w1 sc-e2e-w2; do
  must "billing → db ($n)" "$n" billing production worker "http://$DB:5432/hostname"
  never "billing → web still" "$n" billing production worker "http://$(ips shop production web | head -1):8080/hostname"
  must "web → db ($n)" "$n" shop production web http://db:5432/hostname
done
echo "  ✓ the group lets billing's worker reach db on 5432, web still reaches db, billing still cannot reach web"

r=$(api localhost:7070/api/v1/network/reachability -d '{"from":"billing/production/worker","to":"shop/production/db","protocol":"tcp","port":5432}')
echo "$r" | grep -q '"allowed":true' && echo "$r" | grep -q '"ingress":{[^}]*"group":"shop/db","direction":"in","index":1' || fail "reachability allowed: $r"
r=$(api localhost:7070/api/v1/network/reachability -d '{"from":"shop/staging/web","to":"shop/production/db","protocol":"tcp","port":5432}')
echo "$r" | grep -q '"allowed":false' && echo "$r" | grep -q 'no inbound rule of shop/db' || fail "reachability denied: $r"
echo "  ✓ the reachability check names the deciding rule, or why nothing allows it"

gid=$(api "$P/shop/security-groups/db" | grep -o '"id":"sg_[a-z0-9]*"' | head -1 | cut -d'"' -f4)
for _ in $(seq 1 10); do
  c=$(api localhost:7070/api/v1/firewall/counters)
  echo "$c" | grep -q "\"id\":\"$gid:in:1\",\"packets\":[1-9]" && break
  sleep 2
done
echo "$c" | grep -q "\"id\":\"$gid:in:1\",\"packets\":[1-9]" || fail "no hits on $gid:in:1: $c"
api "$P/shop/security-groups/db" | grep -q '"peers":\["service:billing/production/worker"\],"description":"","packets":[1-9]' || fail "group view has no hit counter"
echo "  ✓ rule hit counters are reported per rule"

echo "== egress rules"
G2=${G/'"outbound":[{"protocol":"any","peers":["any"]}]'/'"outbound":[{"protocol":"udp","ports":"53","peers":["any"]}]'}
echo "$G2" | grep -q '"ports":"53"' || fail "G2"
api -X PUT "$P/shop/security-groups/db" -d "$G2" >/dev/null
DBNODE=$(node_of shop production db "$DB"); DBNODE=sc-e2e-${DBNODE%-0}
sleep 2
for ip in $(ips shop production web); do
  never "db egress to web" "$DBNODE" shop production db "http://$ip:8080/hostname"
done
must "web → db after egress change" sc-e2e-w1 shop production web http://db:5432/hostname
for _ in $(seq 1 15); do
  api "localhost:7070/api/v1/firewall/drops?direction=out&since=10m" | grep -q '"srcName":"shop/production/db"' && break
  sleep 2
done
api "localhost:7070/api/v1/firewall/drops?direction=out&since=10m" | grep -q '"srcName":"shop/production/db"' || fail "no outbound drop recorded"
echo "  ✓ an outbound rule limits db to DNS; replies to inbound connections still flow"

echo "== job runs are members"
key=$(api localhost:7070/api/v1/iam/access-keys -d '{"description":"e2e"}')
KID=$(echo "$key" | grep -o '"id":"SYNAK[A-Z0-9]*"' | cut -d'"' -f4); KSEC=$(echo "$key" | grep -o '"secretAccessKey":"[^"]*"' | cut -d'"' -f4)
synctl() { x -i -e SYNCLOUD_ENDPOINT=http://127.0.0.1:7070 -e SYNCLOUD_ACCESS_KEY_ID="$KID" -e SYNCLOUD_SECRET_ACCESS_KEY="$KSEC" sc-e2e-ctl /opt/sc/synctl "$@"; }
synctl run service/web -p shop -- wget -q -T 3 -O - http://db:5432/hostname </dev/null >/dev/null 2>&1 || fail "a job run of web cannot reach db"
synctl run service/worker -p billing -- wget -q -T 3 -O - "http://$(ips shop production web | head -1):8080/hostname" </dev/null >/dev/null 2>&1 && fail "a billing job run reached shop/web"
echo "  ✓ job runs get their service's groups (allowed to db, blocked from another project)"

echo "== synctl"
out=$(synctl sg list </dev/null); grep -q 'shop *db *2 *1 *production/db' <<<"$out" || fail "synctl sg list: $(synctl sg list </dev/null)"
out=$(synctl sg check billing/production/worker shop/production/db --port 5432 </dev/null); grep -q '^ALLOWED' <<<"$out" || fail "synctl sg check"
out=$(synctl sg service db -p shop </dev/null); grep -q 'in  tcp 5432 from service:billing/production/worker  (db)' <<<"$out" || fail "synctl sg service"
out=$(echo "$G" | synctl sg apply -p shop -f - --dry-run); grep -q '+ out all traffic to any' <<<"$out" || fail "synctl sg apply --dry-run"
out=$(synctl fw drops --since 1h </dev/null); grep -q 'shop/staging/web' <<<"$out" || fail "synctl fw drops"
out=$(synctl fw counters </dev/null); grep -q "$gid:in:1" <<<"$out" || fail "synctl fw counters"
echo "  ✓ synctl sg list/check/service/apply --dry-run, fw drops/counters"

echo "== deleting the group"
api -X DELETE "$P/shop/security-groups/default" >/dev/null 2>&1 && fail "deleted the default group"
api -X DELETE "$P/shop/security-groups/db" >/dev/null
sleep 2
never "billing → db after delete" sc-e2e-w1 billing production worker "http://$DB:5432/hostname"
must "web → db after delete (default group)" sc-e2e-w2 shop production web http://db:5432/hostname
echo "  ✓ after deletion db falls back to the default group"

echo "PASS"
