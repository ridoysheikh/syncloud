#!/usr/bin/env bash
# Chaos test (Phase 9): a worker freezes (host crash or network loss) and
# comes back; the controller is killed; the private network between two
# workers is partitioned and healed. The service keeps its task count,
# nothing is rescheduled without cause, and stale tasks are cleaned up.
#
#   test/e2e/chaos.sh          # run and clean up
#   KEEP=1 test/e2e/chaos.sh   # leave the nodes running for inspection
. "$(dirname "$0")/lib.sh"
setup_cluster
wait_mesh

P=localhost:7070/api/v1
SVC=$P/projects/shop/environments/production/services/web
CTL_IP=$(docker inspect -f "{{(index .NetworkSettings.Networks \"$NET\").IPAddress}}" sc-e2e-ctl)
running() { api $SVC/tasks | grep -o '"desired":"running","state":"running"' | wc -l; }
on() { api $SVC/tasks | grep -o "\"node\":\"$1\",\"desired\":\"running\",\"state\":\"running\"" | wc -l; }
containers() { x sc-e2e-$1 docker ps -q --filter name=shop-production-web- | wc -l; }
status() { api $P/nodes | grep -o "\"name\":\"$1\",\"status\":\"[a-z_]*\"" | cut -d'"' -f8; }
reach() { # reach FROM-NODE TASK-NODE: every running task on TASK-NODE answers from FROM-NODE's host
  local ips; ips=$(api $SVC/tasks | grep -o "\"node\":\"$2\",\"desired\":\"running\",\"state\":\"running\",\"ip\":\"[0-9.]*\"" | grep -o '10\.91\.[0-9.]*')
  [ -n "$ips" ] || return 1
  for ip in $ips; do x sc-e2e-$1 wget -q -T 3 -O /dev/null "http://$ip:8080/hostname" || return 1; done
}

api $P/projects -d '{"name":"shop"}' >/dev/null
api -X PUT $SVC -d '{"image":"busybox:1.37","command":["httpd","-f","-p","8080","-h","/etc"],"ports":[{"container":8080}],"resources":{"cpu":0.05,"memory":16},"desiredCount":4}' >/dev/null
for _ in $(seq 1 60); do [ "$(running)" = 4 ] && break; sleep 2; done
[ "$(running)" = 4 ] || fail "web not running"
[ "$(on w2)" -ge 1 ] || fail "no task on w2: $(api $SVC/tasks)"
echo "  ✓ 4 tasks spread over ctl-0, w1 and w2"

echo "== a worker freezes and comes back"
LOST=$(on w2)
docker pause sc-e2e-w2 >/dev/null
for _ in $(seq 1 60); do [ "$(status w2)" = not_ready ] && break; sleep 2; done
[ "$(status w2)" = not_ready ] || fail "w2 is $(status w2) after freezing"
for _ in $(seq 1 60); do [ "$(running)" = 4 ] && [ "$(on w2)" = 0 ] && break; sleep 2; done
[ "$(running)" = 4 ] && [ "$(on w2)" = 0 ] || fail "tasks not replaced: $(api $SVC/tasks)"
reach w1 ctl-0 || fail "replacement tasks not reachable"
echo "  ✓ w2 went NotReady; its $LOST task(s) were replaced on the other nodes"
docker unpause sc-e2e-w2 >/dev/null
for _ in $(seq 1 60); do [ "$(status w2)" = ready ] && [ "$(containers w2)" = 0 ] && break; sleep 2; done
[ "$(status w2)" = ready ] || fail "w2 did not come back"
[ "$(containers w2)" = 0 ] || fail "stale containers left on w2: $(x sc-e2e-w2 docker ps --filter name=shop-production-web-)"
[ "$(running)" = 4 ] || fail "task count changed after w2 came back: $(running)"
echo "  ✓ w2 reconnected; its stale containers were removed; still 4 tasks"

echo "== the controller is killed"
BEFORE=$(for n in ctl w1 w2; do x sc-e2e-$n docker ps -q --filter name=shop-production-web-; done | sort | tr '\n' ' ')
IPS=$(api $SVC/tasks | grep -o '"desired":"running","state":"running","ip":"[0-9.]*"' | grep -o '10\.91\.[0-9.]*')
x sc-e2e-ctl sh -c 'pkill -9 -f "[s]yncloud-controller --dev"'
sleep 15
for ip in $IPS; do x sc-e2e-w2 wget -q -T 3 -O /dev/null "http://$ip:8080/hostname" || fail "task $ip not reachable while the controller is down"; done
x -d sc-e2e-ctl sh -c "/opt/sc/syncloud-controller --dev --data-dir /data --listen 0.0.0.0:7070 --agent-listen 0.0.0.0:7443 --agent-advertise $CTL_IP:7443 --system-tasks=false > /var/log/controller.log 2>&1"
for _ in $(seq 1 30); do x sc-e2e-ctl curl -fs localhost:7070/api/v1/system/status >/dev/null 2>&1 && break; sleep 1; done
x sc-e2e-ctl curl -fs -c /tmp/jar -H 'content-type: application/json' localhost:7070/api/v1/auth/login -d '{"email":"e2e@example.com","password":"e2e-password-123"}' >/dev/null
sleep 20
[ "$(running)" = 4 ] || fail "task count after the restart: $(running)"
AFTER=$(for n in ctl w1 w2; do x sc-e2e-$n docker ps -q --filter name=shop-production-web-; done | sort | tr '\n' ' ')
[ "$AFTER" = "$BEFORE" ] || fail "containers changed across the controller restart: $BEFORE -> $AFTER"
echo "  ✓ tasks kept serving without the controller; nothing was rescheduled after it restarted"

echo "== the mesh is partitioned between w1 and w2"
# Scale up once every node is back (agents reconnect with backoff after the
# controller restart), so both sides of the partition run tasks.
for _ in $(seq 1 30); do [ "$(status w1)" = ready ] && [ "$(status w2)" = ready ] && [ "$(status ctl-0)" = ready ] && break; sleep 2; done
api -X POST $SVC/scale -d '{"desiredCount":6}' >/dev/null
for _ in $(seq 1 60); do [ "$(running)" = 6 ] && [ "$(on w2)" -ge 1 ] && [ "$(on w1)" -ge 1 ] && break; sleep 2; done
reach w1 w2 || fail "w1 cannot reach w2's tasks before the partition"
W1=$(docker inspect -f "{{(index .NetworkSettings.Networks \"$NET\").IPAddress}}" sc-e2e-w1)
W2=$(docker inspect -f "{{(index .NetworkSettings.Networks \"$NET\").IPAddress}}" sc-e2e-w2)
x -i sc-e2e-w1 nft -f - <<<"table inet chaos { chain out { type filter hook output priority -10; ip daddr $W2 meta l4proto udp drop; }; chain in { type filter hook input priority -10; ip saddr $W2 meta l4proto udp drop; }; }"
sleep 5
reach w1 w2 && fail "the partition did not cut w1 from w2"
reach w1 ctl-0 || fail "w1 lost the controller node too"
sleep 30
[ "$(status w1)" = ready ] && [ "$(status w2)" = ready ] || fail "a partition between workers changed node status: w1=$(status w1) w2=$(status w2)"
[ "$(running)" = 6 ] || fail "tasks rescheduled during the partition"
echo "  ✓ w1 cannot reach w2's tasks; both stay Ready (the controller still reaches them); no task moved"
x sc-e2e-w1 nft delete table inet chaos
for _ in $(seq 1 30); do reach w1 w2 && break; sleep 2; done
reach w1 w2 || fail "w1 did not reach w2 again after healing"
echo "  ✓ healed: w1 reaches w2's tasks again"

echo "PASS"
