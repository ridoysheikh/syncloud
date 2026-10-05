#!/usr/bin/env bash
# End-to-end test of the private network (§8): a controller node and two
# workers in Docker-in-Docker containers form a WireGuard mesh; containers on
# different nodes reach each other by IP without NAT, and reach the internet.
#
#   test/e2e/mesh.sh          # run and clean up
#   KEEP=1 test/e2e/mesh.sh   # leave the nodes running for inspection
set -euo pipefail
cd "$(dirname "$0")/../.."
NET=sc-e2e
NODES=(sc-e2e-ctl sc-e2e-w1 sc-e2e-w2)
BIN=$(mktemp -d)

cleanup() {
  [ "${KEEP:-0}" = 1 ] && { echo "nodes kept: ${NODES[*]}"; return; }
  docker rm -f "${NODES[@]}" >/dev/null 2>&1 || true
  docker network rm "$NET" >/dev/null 2>&1 || true
}
trap cleanup EXIT
fail() { echo "FAIL: $*" >&2; exit 1; }
x() { docker exec "$@"; }

echo "== build"
for c in controller agent synctl; do
  name=syncloud-$c; [ $c = synctl ] && name=synctl
  CGO_ENABLED=0 GOOS=linux go build -o "$BIN/$name" ./cmd/$c
done
docker build -q -t syncloud-e2e-node -f test/e2e/node.Dockerfile test/e2e >/dev/null
docker image inspect busybox:1.37 >/dev/null 2>&1 || docker pull -q busybox:1.37 >/dev/null
docker save busybox:1.37 -o "$BIN/busybox.tar"

echo "== nodes"
cleanup; trap cleanup EXIT
docker network create "$NET" >/dev/null
for n in "${NODES[@]}"; do
  docker run -d --privileged --name "$n" --hostname "${n#sc-e2e-}" --network "$NET" -v "$BIN:/opt/sc:ro" \
    syncloud-e2e-node dockerd -H unix:///var/run/docker.sock >/dev/null
done
for n in "${NODES[@]}"; do
  for _ in $(seq 1 60); do x "$n" docker info >/dev/null 2>&1 && break; sleep 1; done
  x "$n" docker load -q -i /opt/sc/busybox.tar >/dev/null
done
CTL_IP=$(docker inspect -f "{{(index .NetworkSettings.Networks \"$NET\").IPAddress}}" sc-e2e-ctl)

echo "== controller on $CTL_IP"
x -d sc-e2e-ctl sh -c "/opt/sc/syncloud-controller --dev --data-dir /data --listen 0.0.0.0:7070 --agent-listen 0.0.0.0:7443 --agent-advertise $CTL_IP:7443 --system-tasks=false > /var/log/controller.log 2>&1"
for _ in $(seq 1 30); do x sc-e2e-ctl curl -fs localhost:7070/api/v1/system/status >/dev/null 2>&1 && break; sleep 1; done
x sc-e2e-ctl /opt/sc/syncloud-agent join --controller http://127.0.0.1:7070 --token-file /data/local-join.token --name ctl-0 --data-dir /agent >/dev/null
x -d sc-e2e-ctl sh -c "/opt/sc/syncloud-agent run --data-dir /agent --network on > /var/log/agent.log 2>&1"

# Root account and a join token for the workers.
TOK=$(x sc-e2e-ctl cat /data/setup-token)
SUF=$(x sc-e2e-ctl cat /data/recovery-key | tr -d '-' | tail -c 7 | tr -d '\n')
x sc-e2e-ctl curl -fs -c /tmp/jar -H 'content-type: application/json' localhost:7070/api/v1/setup \
  -d "{\"setupToken\":\"$TOK\",\"email\":\"e2e@example.com\",\"name\":\"E2E\",\"password\":\"e2e-password-123\",\"recoveryKeySuffix\":\"$SUF\"}" >/dev/null
JOIN=$(x sc-e2e-ctl curl -fs -b /tmp/jar -H 'content-type: application/json' localhost:7070/api/v1/nodes/join-tokens -d '{"singleUse":false,"ttlMinutes":30}' | sed -n 's/.*"token":"\([^"]*\)".*/\1/p')
[ -n "$JOIN" ] || fail "no join token"
for w in w1 w2; do
  x sc-e2e-$w /opt/sc/syncloud-agent join --controller "http://$CTL_IP:7070" --token "$JOIN" --name "$w" --data-dir /agent >/dev/null
  # w2 uses userspace WireGuard, so both implementations are tested together.
  mode=kernel; [ $w = w2 ] && mode=userspace
  x -d -e SYNCLOUD_WIREGUARD_MODE=$mode sc-e2e-$w sh -c "/opt/sc/syncloud-agent run --data-dir /agent --network on > /var/log/agent.log 2>&1"
done

echo "== waiting for the mesh"
mesh() { x sc-e2e-ctl curl -fs -b /tmp/jar localhost:7070/api/v1/network/mesh; }
ok=0
for _ in $(seq 1 60); do
  m=$(mesh || true)
  applied=$(echo "$m" | grep -o '"appliedGeneration":[1-9]' | wc -l || true)
  handshakes=$(echo "$m" | grep -o '"lastHandshake":"' | wc -l || true)
  if [ "$applied" -eq 3 ] && [ "$handshakes" -ge 6 ]; then ok=1; break; fi
  sleep 2
done
[ $ok = 1 ] || { echo "$m"; x sc-e2e-w1 tail -20 /var/log/agent.log; fail "mesh did not converge (applied=$applied handshakes=$handshakes)"; }
echo "$m" | grep -o '"name":"[a-z0-9-]*","address":"[0-9.]*","subnet":"[0-9./]*"' | sed 's/"//g'
echo "$m" | grep -o '"mode":"[a-z]*"' | sort | uniq -c

echo "== cross-node container traffic"
# A web container on w1 whose CGI echoes the caller's address.
CGI='mkdir -p /www/cgi-bin && printf "#!/bin/sh\necho Content-Type: text/plain\necho\necho \$REMOTE_ADDR\n" > /www/cgi-bin/ip && chmod +x /www/cgi-bin/ip && exec httpd -f -p 8080 -h /www'
x sc-e2e-w1 docker run -d --name web --network syncloud busybox:1.37 sh -c "$CGI" >/dev/null
W1_IP=$(x sc-e2e-w1 docker inspect -f '{{.NetworkSettings.Networks.syncloud.IPAddress}}' web)
echo "web on w1: $W1_IP"
x sc-e2e-w2 docker run -d --name client --network syncloud busybox:1.37 sleep 300 >/dev/null
W2C=$(x sc-e2e-w2 docker inspect -f '{{.NetworkSettings.Networks.syncloud.IPAddress}}' client)
sleep 1
seen=$(x sc-e2e-w2 docker exec client wget -q -T 5 -O - "http://$W1_IP:8080/cgi-bin/ip") || fail "w2 container -> w1 container"
seen=$(echo "$seen" | tr -d '[]' | sed 's/^::ffff://')
[ "$seen" = "$W2C" ] || fail "source address rewritten: w1 saw $seen, client is $W2C"
echo "  ✓ container on w2 ($W2C) -> container on w1, source address preserved"
x sc-e2e-ctl wget -q -T 5 -O /dev/null "http://$W1_IP:8080/cgi-bin/ip" || fail "controller host -> w1 container"
echo "  ✓ controller host (Traefik's view) -> container on w1"
x sc-e2e-w2 docker exec client wget -q -T 8 -O /dev/null http://example.com || fail "egress"
echo "  ✓ container egress to the internet (masqueraded)"
if x sc-e2e-w2 wget -q -T 3 -O /dev/null "http://$W1_IP:8080/cgi-bin/ip" 2>/dev/null; then
  echo "  ✓ worker host -> remote container (mesh)"
fi

echo "== node removal"
W2_ID=$(x sc-e2e-ctl curl -fs -b /tmp/jar localhost:7070/api/v1/nodes | grep -o '"id":"node_[a-z0-9]*","name":"w2"' | cut -d'"' -f4)
x sc-e2e-ctl curl -fs -b /tmp/jar -X DELETE "localhost:7070/api/v1/nodes/$W2_ID" -H 'Origin: http://localhost:7070' >/dev/null
sleep 3
m=$(mesh); echo "$m" | grep -q '"name":"w2"' && fail "w2 still in mesh"
echo "  ✓ w2 removed from the mesh"
echo "PASS"
