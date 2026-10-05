#!/usr/bin/env bash
# Shared setup for end-to-end tests: a controller node and two workers in
# Docker-in-Docker containers on an isolated network. Source it, then call
# setup_cluster. Helpers: x (docker exec), api (controller API as root),
# mesh, fail.
set -euo pipefail
cd "$(dirname "${BASH_SOURCE[0]}")/../.."
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

setup_cluster() {
docker rm -f "${NODES[@]}" >/dev/null 2>&1 || true
docker network rm "$NET" >/dev/null 2>&1 || true
echo "== build"
for c in controller agent synctl; do
  name=syncloud-$c; [ $c = synctl ] && name=synctl
  CGO_ENABLED=0 GOOS=linux go build -o "$BIN/$name" ./cmd/$c
done
docker build -q -t syncloud-e2e-node -f test/e2e/node.Dockerfile test/e2e >/dev/null
docker image inspect busybox:1.37 >/dev/null 2>&1 || docker pull -q busybox:1.37 >/dev/null
docker save busybox:1.37 -o "$BIN/busybox.tar"

echo "== nodes"
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

}
api() { x sc-e2e-ctl curl -fsS -b /tmp/jar -H 'content-type: application/json' -H 'Origin: http://localhost:7070' "$@"; }
mesh() { x sc-e2e-ctl curl -fs -b /tmp/jar localhost:7070/api/v1/network/mesh; }
wait_mesh() {
  echo "== waiting for the mesh"
  local ok=0 m applied handshakes
  for _ in $(seq 1 60); do
    m=$(mesh || true)
    applied=$(echo "$m" | grep -o '"appliedGeneration":[1-9]' | wc -l || true)
    handshakes=$(echo "$m" | grep -o '"lastHandshake":"' | wc -l || true)
    if [ "$applied" -eq 3 ] && [ "$handshakes" -ge 6 ]; then ok=1; break; fi
    sleep 2
  done
  [ $ok = 1 ] || { echo "$m"; fail "mesh did not converge (applied=$applied handshakes=$handshakes)"; }
}
