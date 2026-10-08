#!/usr/bin/env bash
# End-to-end test of upgrades and uninstall (§5.0.1): a controller release
# that does not start is rolled back (binary and database); a good one is
# installed and serves; an agent binary that does not start is rolled back by
# the agent's guard; agents are upgraded node by node with their containers
# left running; uninstall removes containers, WireGuard and nftables.
#
#   test/e2e/upgrade.sh          # run and clean up
#   KEEP=1 test/e2e/upgrade.sh   # leave the nodes running for inspection
CTL_FLAGS="--release-url file:///opt/sc/releases --downloads-dir /data/downloads --upgrade-settle 5s"
. "$(dirname "$0")/lib.sh"
setup_cluster
wait_mesh

P=localhost:7070/api/v1
SVC=$P/projects/shop/environments/production/services/web
ARCH=$(go env GOARCH)
NEW=0.0.2-e2e
CTL_IP=$(docker inspect -f "{{(index .NetworkSettings.Networks \"$NET\").IPAddress}}" sc-e2e-ctl)

echo "== releases"
R=$BIN/releases
mkdir -p "$R/$NEW" "$R/0.0.3-broken" "$R/channels"
for c in controller agent synctl; do
  name=syncloud-$c; [ $c = synctl ] && name=synctl
  CGO_ENABLED=0 GOOS=linux go build -ldflags "-X github.com/ridoysheikh/syncloud/internal/version.Version=$NEW" -o "$R/$NEW/$name-linux-$ARCH" ./cmd/$c
done
(cd "$R/$NEW" && sha256sum ./* | sed 's| \./| |' > SHA256SUMS)
printf '#!/bin/sh\n[ "$1" = version ] && { echo 0.0.3-broken; exit 0; }\necho "broken build" >&2\nexit 1\n' > "$R/0.0.3-broken/syncloud-controller-linux-$ARCH"
chmod +x "$R/0.0.3-broken/syncloud-controller-linux-$ARCH"
cp "$R/$NEW/syncloud-agent-linux-$ARCH" "$R/0.0.3-broken/"
(cd "$R/0.0.3-broken" && sha256sum ./* | sed 's| \./| |' > SHA256SUMS)
echo $NEW > "$R/channels/stable"

# Binaries replace themselves, so run them from a writable copy.
x sc-e2e-ctl sh -c 'pkill -f "[s]yncloud-controller --dev"; sleep 1; cp /opt/sc/syncloud-controller /usr/local/bin/'
x -d sc-e2e-ctl sh -c "/usr/local/bin/syncloud-controller --dev --data-dir /data --listen 0.0.0.0:7070 --agent-listen 0.0.0.0:7443 --agent-advertise $CTL_IP:7443 --system-tasks=false --base-domain off $CTL_FLAGS > /var/log/controller.log 2>&1"
for _ in $(seq 1 30); do x sc-e2e-ctl curl -fs localhost:7070/api/v1/system/status >/dev/null 2>&1 && break; sleep 1; done
for n in ctl w1 w2; do
  mode=kernel; [ $n = w2 ] && mode=userspace
  x sc-e2e-$n sh -c 'pkill -f "[s]yncloud-agent run"; sleep 1; cp /opt/sc/syncloud-agent /usr/local/bin/'
  x -d -e SYNCLOUD_WIREGUARD_MODE=$mode sc-e2e-$n sh -c "/usr/local/bin/syncloud-agent run --data-dir /agent --network on > /var/log/agent.log 2>&1"
done
wait_mesh
api $P/projects -d '{"name":"shop"}' >/dev/null
api -X PUT $SVC -d '{"image":"busybox:1.37","command":["httpd","-f","-p","8080","-h","/etc"],"ports":[{"container":8080}],"resources":{"cpu":0.05,"memory":16},"desiredCount":3}' >/dev/null
for _ in $(seq 1 60); do [ "$(api $SVC/tasks | grep -o '"desired":"running","state":"running"' | wc -l)" = 3 ] && break; sleep 2; done
containers() { for n in ctl w1 w2; do x sc-e2e-$n docker ps -q --filter name=shop-production-web-; done | sort | tr '\n' ' '; }
BEFORE=$(containers)
[ "$(wc -w <<<"$BEFORE")" = 3 ] || fail "expected 3 web containers: $BEFORE"
version() { x sc-e2e-ctl curl -fs localhost:7070/api/v1/system/health | grep -o '"version":"[^"]*"' | cut -d'"' -f4; }
[ "$(version)" = 0.0.0-dev ] || fail "health: $(x sc-e2e-ctl curl -s localhost:7070/api/v1/system/health)"
echo "  ✓ 3 tasks running; /system/health ready"

echo "== a broken controller release rolls back (host CLI)"
out=$(x sc-e2e-ctl /usr/local/bin/syncloud-controller upgrade --data-dir /data --version 0.0.3-broken --release-url file:///opt/sc/releases \
  --downloads-dir /data/downloads --settle 5s --timeout 60s 2>&1) && fail "the broken upgrade succeeded: $out"
grep -q 'rolled back to 0.0.0-dev' <<<"$out" || fail "no rollback: $out"
for _ in $(seq 1 30); do [ "$(version)" = 0.0.0-dev ] && break; sleep 1; done
[ "$(version)" = 0.0.0-dev ] || fail "controller not back after the rollback"
x sc-e2e-ctl /usr/local/bin/syncloud-controller version | grep -q 0.0.0-dev || fail "binary not restored"
api $P/system/upgrade | grep -q '"phase":"rolled-back"' || fail "state: $(api $P/system/upgrade)"
for _ in $(seq 1 30); do [ "$(api $SVC/tasks | grep -o '"desired":"running","state":"running"' | wc -l)" = 3 ] && break; sleep 1; done
[ "$(containers)" = "$BEFORE" ] || fail "containers changed during the rollback"
echo "  ✓ the new controller exited, the old binary and database came back, tasks untouched"

echo "== a broken agent binary rolls back"
x sc-e2e-ctl sh -c 'mkdir -p /data/downloads && printf "#!/bin/sh\nexit 1\n" > /data/downloads/syncloud-agent-linux-'"$ARCH"' && chmod +x /data/downloads/syncloud-agent-linux-'"$ARCH"
api $P/nodes/agent-upgrade -d '{"nodes":["w1"],"force":true}' | grep -q '"state":"running"' || fail "rollout did not start"
for _ in $(seq 1 60); do api $P/nodes/agent-upgrade | grep -q '"rollout":{"target":"[^"]*","state":"failed"' && break; sleep 2; done
out=$(api $P/nodes/agent-upgrade)
grep -q '"rollout":{"target":"[^"]*","state":"failed"' <<<"$out" || fail "rollout did not fail: $out"
grep -q 'the new agent exited; rolled back to 0.0.0-dev' <<<"$out" || fail "no rollback reported: $out"
grep -q '"name":"w1","version":"0.0.0-dev","arch":"[a-z0-9]*","connected":true' <<<"$out" || fail "w1 not back: $out"
[ "$(containers)" = "$BEFORE" ] || fail "containers changed during the agent rollback"
echo "  ✓ the guard restored the old agent, which reconnected and reported why"

echo "== controller upgrade (API)"
api $P/system/upgrade?refresh=1 | grep -q "\"latest\":\"$NEW\",\"available\":true" || fail "channel: $(api $P/system/upgrade?refresh=1)"
api -X POST $P/system/upgrade -d '{}' | grep -q "\"to\":\"$NEW\"" || fail "upgrade did not start"
for _ in $(seq 1 60); do api $P/system/upgrade 2>/dev/null | grep -q '"phase":"\(done\|rolled-back\|failed\)"' && break; sleep 2; done
out=$(api $P/system/upgrade)
grep -q '"phase":"done"' <<<"$out" || fail "upgrade: $out $(x sc-e2e-ctl cat /var/log/controller.log | tail -20)"
[ "$(version)" = $NEW ] || fail "running $(version)"
x sc-e2e-ctl /data/downloads/syncloud-agent-linux-$ARCH version | grep -q $NEW || fail "worker downloads not updated"
[ "$(containers)" = "$BEFORE" ] || fail "containers changed during the upgrade"
echo "  ✓ upgraded to $NEW; worker downloads updated; tasks untouched"

echo "== rolling agent upgrade"
out=$(api $P/nodes/agent-upgrade)
[ "$(grep -o '"outdated":true' <<<"$out" | wc -l)" = 3 ] || fail "expected 3 outdated agents: $out"
api $P/nodes/agent-upgrade -d '{}' >/dev/null
for _ in $(seq 1 90); do api $P/nodes/agent-upgrade | grep -q '"rollout":{"target":"[^"]*","state":"\(done\|failed\)"' && break; sleep 2; done
out=$(api $P/nodes/agent-upgrade)
grep -q '"rollout":{"target":"[^"]*","state":"done"' <<<"$out" || fail "rollout: $out"
[ "$(grep -o "\"version\":\"$NEW\"" <<<"$out" | wc -l)" = 3 ] || fail "not every agent runs $NEW: $out"
[ "$(containers)" = "$BEFORE" ] || fail "containers changed during the agent upgrade"
wait_mesh
echo "  ✓ every agent upgraded one node at a time; containers kept running; the mesh converged"

echo "== synctl"
key=$(api $P/iam/access-keys -d '{"description":"e2e"}')
KID=$(echo "$key" | grep -o '"id":"SYNAK[A-Z0-9]*"' | cut -d'"' -f4); KSEC=$(echo "$key" | grep -o '"secretAccessKey":"[^"]*"' | cut -d'"' -f4)
synctl() { x -i -e SYNCLOUD_ENDPOINT=http://127.0.0.1:7070 -e SYNCLOUD_ACCESS_KEY_ID="$KID" -e SYNCLOUD_SECRET_ACCESS_KEY="$KSEC" sc-e2e-ctl /opt/sc/synctl "$@"; }
out=$(synctl system health </dev/null); grep -q "^ready ($NEW)" <<<"$out" || fail "synctl system health: $out"
out=$(synctl system upgrade --check </dev/null); grep -q "Last upgrade 0.0.0-dev → $NEW: done" <<<"$out" || fail "synctl system upgrade --check: $out"
out=$(synctl nodes agents </dev/null); grep -q "w2 *$NEW .*current" <<<"$out" || fail "synctl nodes agents: $out"
echo "  ✓ synctl system health/upgrade --check, nodes agents"

echo "== uninstall"
x sc-e2e-w1 sh -c 'pkill -f "[s]yncloud-agent run"; sleep 1'
x sc-e2e-w1 /usr/local/bin/syncloud-agent uninstall --data-dir /agent | grep -q 'kept /agent' || fail "uninstall"
[ -z "$(x sc-e2e-w1 docker ps -aq --filter label=syncloud.managed=true)" ] || fail "containers left on w1"
x sc-e2e-w1 ip link show wg-syncloud >/dev/null 2>&1 && fail "WireGuard interface left"
x sc-e2e-w1 nft list table inet syncloud >/dev/null 2>&1 && fail "nftables table left"
x sc-e2e-w1 test -f /agent/agent.json || fail "data removed without --purge"
x sc-e2e-w1 /usr/local/bin/syncloud-agent uninstall --purge --data-dir /agent >/dev/null
x sc-e2e-w1 test -e /agent && fail "--purge kept the data"
echo "  ✓ uninstall removed containers, WireGuard and nftables, kept data until --purge"

echo "PASS"
