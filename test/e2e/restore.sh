#!/usr/bin/env bash
# Restore drill (§13, §5.0.1): the controller host is lost; its backup is
# restored on a new host with a different IP address. The sslip.io base
# domain follows the new address, the controller's own agent re-joins as
# ctl-0 (same node), workers are pointed at the new address and keep their
# containers, and sign-in, services and secrets come back.
#
#   test/e2e/restore.sh          # run and clean up
#   KEEP=1 test/e2e/restore.sh   # leave the nodes running for inspection
CTL_FLAGS="--public-ip 172.31.255.1 --base-domain 172-31-255-1.sslip.io"
. "$(dirname "$0")/lib.sh"
docker rm -f sc-e2e-ctl2 >/dev/null 2>&1 || true
setup_cluster
NODES+=(sc-e2e-ctl2) # created below; removed with the others
wait_mesh

P=localhost:7070/api/v1
SVC=$P/projects/shop/environments/production/services/web
CTL_IP=$(docker inspect -f "{{(index .NetworkSettings.Networks \"$NET\").IPAddress}}" sc-e2e-ctl)
# Place the old controller's public IP where sslip.io says (only the stored domain matters).
x sc-e2e-ctl curl -fs localhost:7070/api/v1/system/status | grep -q '"baseDomain":"172-31-255-1.sslip.io"' || fail "base domain not set"

echo "== state to recover"
api $P/projects -d '{"name":"shop"}' >/dev/null
CTL0=$(api $P/nodes | grep -o '"id":"node_[a-z0-9]*","name":"ctl-0"' | cut -d'"' -f4)
api -X PUT "$P/nodes/$CTL0/schedulable" -d '{"schedulable":false}' >/dev/null # the host to be lost runs no tasks
api -X PUT $SVC -d '{"image":"busybox:1.37","command":["httpd","-f","-p","8080","-h","/etc"],"env":{"GREETING":"hello"},"ports":[{"container":8080}],"resources":{"cpu":0.05,"memory":16},"desiredCount":3}' >/dev/null
for _ in $(seq 1 60); do [ "$(api $SVC/tasks | grep -o '"desired":"running","state":"running"' | wc -l)" = 3 ] && break; sleep 2; done
[ "$(api $SVC/tasks | grep -o '"desired":"running","state":"running"' | wc -l)" = 3 ] || fail "web not running"
workers() { for n in w1 w2; do x sc-e2e-$n docker ps -q --filter name=shop-production-web-; done | sort | tr '\n' ' '; }
BEFORE=$(workers)
x sc-e2e-ctl curl -fsS -b /tmp/jar -o /root/backup.synbak localhost:7070/api/v1/backups/download
x sc-e2e-ctl sh -c 'test -s /root/backup.synbak' || fail "empty backup"
docker cp sc-e2e-ctl:/root/backup.synbak "$BIN/backup.synbak"
docker cp sc-e2e-ctl:/root/recovery-key "$BIN/recovery-key"
echo "  ✓ backup downloaded ($(stat -c %s "$BIN/backup.synbak") bytes); web runs on the workers"

echo "== the controller host is lost"
docker run -d --privileged --name sc-e2e-ctl2 --hostname ctl2 --network "$NET" -v "$BIN:/opt/sc:ro" syncloud-e2e-node dockerd -H unix:///var/run/docker.sock >/dev/null
for _ in $(seq 1 60); do x sc-e2e-ctl2 docker info >/dev/null 2>&1 && break; sleep 1; done
x sc-e2e-ctl2 docker load -q -i /opt/sc/busybox.tar >/dev/null
docker rm -f sc-e2e-ctl >/dev/null
NEW_IP=$(docker inspect -f "{{(index .NetworkSettings.Networks \"$NET\").IPAddress}}" sc-e2e-ctl2)
[ "$NEW_IP" != "$CTL_IP" ] || fail "the new host got the same IP"

echo "== restore on $NEW_IP"
out=$(x sc-e2e-ctl2 sh -c '/opt/sc/syncloud-controller restore --data-dir /data --file /opt/sc/backup.synbak --recovery-key "$(cat /opt/sc/recovery-key)" 2>&1') || fail "restore: $out"
grep -q "Re-join this host's agent as ctl-0" <<<"$out" || fail "no re-join token: $out"
x -d sc-e2e-ctl2 sh -c "/opt/sc/syncloud-controller --dev --data-dir /data --listen 0.0.0.0:7070 --agent-listen 0.0.0.0:7443 --agent-advertise $NEW_IP:7443 --system-tasks=false --public-ip $NEW_IP > /var/log/controller.log 2>&1"
for _ in $(seq 1 30); do x sc-e2e-ctl2 curl -fs localhost:7070/api/v1/system/status >/dev/null 2>&1 && break; sleep 1; done
want=$(echo "$NEW_IP" | tr . -).sslip.io
x sc-e2e-ctl2 curl -fs localhost:7070/api/v1/system/status | grep -q "\"baseDomain\":\"$want\"" || fail "base domain did not follow the new IP: $(x sc-e2e-ctl2 curl -fs localhost:7070/api/v1/system/status)"
echo "  ✓ restored; the sslip.io base domain moved to $want"

x sc-e2e-ctl2 /opt/sc/syncloud-agent join --controller http://127.0.0.1:7070 --token-file /data/local-rejoin.token --name ctl-0 --data-dir /agent >/dev/null
x -d sc-e2e-ctl2 sh -c "/opt/sc/syncloud-agent run --data-dir /agent --network on > /var/log/agent.log 2>&1"
for w in w1 w2; do
  mode=kernel; [ $w = w2 ] && mode=userspace
  x sc-e2e-$w /opt/sc/syncloud-agent set-controller --data-dir /agent --gateway "$NEW_IP:7443" --controller "http://$NEW_IP:7070" >/dev/null
  x sc-e2e-$w sh -c 'pkill -f "[s]yncloud-agent run"; sleep 1'
  x -d -e SYNCLOUD_WIREGUARD_MODE=$mode sc-e2e-$w sh -c "/opt/sc/syncloud-agent run --data-dir /agent --network on >> /var/log/agent.log 2>&1"
done

api2() { x sc-e2e-ctl2 curl -fsS -b /tmp/jar -H 'content-type: application/json' -H 'Origin: http://localhost:7070' "$@"; }
x sc-e2e-ctl2 curl -fs -c /tmp/jar -H 'content-type: application/json' localhost:7070/api/v1/auth/login -d '{"email":"e2e@example.com","password":"e2e-password-123"}' >/dev/null || fail "sign-in after restore"
for _ in $(seq 1 60); do [ "$(api2 $P/nodes | grep -o '"status":"ready","statusAt":"[^"]*","connected":true' | wc -l)" = 3 ] && break; sleep 2; done
out=$(api2 $P/nodes)
[ "$(grep -o '"status":"ready","statusAt":"[^"]*","connected":true' <<<"$out" | wc -l)" = 3 ] || fail "nodes not back: $out"
grep -q "\"id\":\"$CTL0\",\"name\":\"ctl-0\"" <<<"$out" || fail "ctl-0 is a new node: $out"
echo "  ✓ signed in with the old password; ctl-0 re-joined as the same node; workers reconnected"

for _ in $(seq 1 60); do [ "$(api2 $SVC/tasks | grep -o '"desired":"running","state":"running"' | wc -l)" = 3 ] && break; sleep 2; done
[ "$(api2 $SVC/tasks | grep -o '"desired":"running","state":"running"' | wc -l)" = 3 ] || fail "web tasks: $(api2 $SVC/tasks)"
[ "$(workers)" = "$BEFORE" ] || fail "worker containers were replaced: before $BEFORE after $(workers)"
C=$(x sc-e2e-w1 docker ps -q --filter name=shop-production-web- | head -1)
[ -z "$C" ] || x sc-e2e-w1 docker exec "$C" sh -c 'test "$GREETING" = hello' || fail "env lost"
api2 -X POST $SVC/scale -d '{"desiredCount":4}' >/dev/null
for _ in $(seq 1 60); do [ "$(api2 $SVC/tasks | grep -o '"desired":"running","state":"running"' | wc -l)" = 4 ] && break; sleep 2; done
[ "$(api2 $SVC/tasks | grep -o '"desired":"running","state":"running"' | wc -l)" = 4 ] || fail "cannot scale after the restore"
echo "  ✓ the workers' containers kept running; the restored controller schedules new tasks"

echo "PASS"
