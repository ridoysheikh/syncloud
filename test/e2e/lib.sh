#!/usr/bin/env bash
# Shared setup for end-to-end tests: a controller node and two workers in
# Docker-in-Docker containers on an isolated network. Source it, then call
# setup_cluster. Helpers: x (docker exec), api (controller API as root),
# mesh, fail.
set -euo pipefail
cd "$(dirname "${BASH_SOURCE[0]}")/../.."
NET=sc-e2e
# WORKERS: the worker nodes (WORKERS= for a single node); CTL_NETWORK: the
# controller node's private network (off = a node outside the mesh, like
# single-node development).
WORKERS=${WORKERS-w1 w2}
CTL_NETWORK=${CTL_NETWORK:-on}
NODES=(sc-e2e-ctl)
for w in $WORKERS; do NODES+=("sc-e2e-$w"); done
BIN=$(mktemp -d)

cleanup() {
  [ "${KEEP:-0}" = 1 ] && { echo "nodes kept: ${NODES[*]}"; return; }
  # -v: each node's Docker data root is an anonymous volume (GBs of images).
  docker rm -fv "${NODES[@]}" >/dev/null 2>&1 || true
  docker network rm "$NET" >/dev/null 2>&1 || true
  rm -rf "$BIN"
}
trap cleanup EXIT
fail() { echo "FAIL: $*" >&2; exit 1; }
x() { docker exec "$@"; }

setup_cluster() {
docker rm -fv "${NODES[@]}" >/dev/null 2>&1 || true
docker network rm "$NET" >/dev/null 2>&1 || true
echo "== build"
for c in controller agent synctl; do
  name=syncloud-$c; [ $c = synctl ] && name=synctl
  CGO_ENABLED=0 GOOS=linux go build -o "$BIN/$name" ./cmd/$c
done
docker build -q -t syncloud-e2e-node -f test/e2e/node.Dockerfile test/e2e >/dev/null
docker image inspect busybox:1.37 >/dev/null 2>&1 || docker pull -q busybox:1.37 >/dev/null
docker save busybox:1.37 -o "$BIN/busybox.tar"
if [ "${WITH_REGISTRY:-0}" = 1 ]; then
  docker image inspect registry:3.1.2 >/dev/null 2>&1 || docker pull -q registry:3.1.2 >/dev/null
  docker save registry:3.1.2 -o "$BIN/registry.tar"
fi
if [ "${WITH_BUILDS:-0}" = 1 ]; then
  CGO_ENABLED=0 GOOS=linux go build -o "$BIN/gitserver" ./test/e2e/gitserver
  docker image inspect moby/buildkit:v0.25.1 >/dev/null 2>&1 || docker pull -q moby/buildkit:v0.25.1 >/dev/null
  docker save moby/buildkit:v0.25.1 -o "$BIN/buildkit.tar"
fi
if [ "${WITH_ALERTS:-0}" = 1 ]; then
  CGO_ENABLED=0 GOOS=linux go build -o "$BIN/hooksink" ./test/e2e/hooksink
fi
if [ "${WITH_METRICS:-0}" = 1 ]; then
  docker image inspect victoriametrics/victoria-metrics:v1.153.0 >/dev/null 2>&1 || docker pull -q victoriametrics/victoria-metrics:v1.153.0 >/dev/null
  docker save victoriametrics/victoria-metrics:v1.153.0 -o "$BIN/vmetrics.tar"
fi
if [ "${WITH_TRAEFIK:-0}" = 1 ]; then
  docker image inspect traefik:v3.7.13 >/dev/null 2>&1 || docker pull -q traefik:v3.7.13 >/dev/null
  docker save traefik:v3.7.13 -o "$BIN/traefik.tar"
  docker image inspect victoriametrics/victoria-logs:v1.53.0 >/dev/null 2>&1 || docker pull -q victoriametrics/victoria-logs:v1.53.0 >/dev/null
  docker save victoriametrics/victoria-logs:v1.53.0 -o "$BIN/vlogs.tar"
fi

if [ "${WITH_POSTGRES:-0}" = 1 ]; then
  # PG_VERSIONS: the major versions whose images the nodes get (the first
  # one is $PG_IMAGE); the platform etcd runs from the default version's.
  PG_IMAGES=()
  for v in ${PG_VERSIONS:-18}; do
    img=$(sed -n "s/.*ImagePostgres$v *= *\"\(.*\)\"/\1/p" internal/system/manifest.go)
    docker image inspect "$img" >/dev/null 2>&1 || make -s "postgres-image-$v" >/dev/null
    PG_IMAGES+=("$img")
  done
  PG_IMAGE=${PG_IMAGES[0]}
  docker save "${PG_IMAGES[@]}" -o "$BIN/postgres.tar"
fi

echo "== nodes"
docker network create "$NET" >/dev/null
for n in "${NODES[@]}"; do
  docker run -d --privileged --name "$n" --hostname "${n#sc-e2e-}" --network "$NET" -v "$BIN:/opt/sc:ro" \
    syncloud-e2e-node dockerd -H unix:///var/run/docker.sock >/dev/null
done
for n in "${NODES[@]}"; do
  for _ in $(seq 1 60); do x "$n" docker info >/dev/null 2>&1 && break; sleep 1; done
  x "$n" docker load -q -i /opt/sc/busybox.tar >/dev/null
  if [ "${WITH_POSTGRES:-0}" = 1 ]; then x "$n" docker load -q -i /opt/sc/postgres.tar >/dev/null; fi
done
CTL_IP=$(docker inspect -f "{{(index .NetworkSettings.Networks \"$NET\").IPAddress}}" sc-e2e-ctl)

echo "== controller on $CTL_IP"
x -d sc-e2e-ctl sh -c "/opt/sc/syncloud-controller --dev --data-dir /data --listen 0.0.0.0:7070 --agent-listen 0.0.0.0:7443 --agent-advertise $CTL_IP:7443 --system-tasks=false ${CTL_FLAGS:-} > /var/log/controller.log 2>&1"
for _ in $(seq 1 30); do x sc-e2e-ctl curl -fs localhost:7070/api/v1/system/status >/dev/null 2>&1 && break; sleep 1; done
x sc-e2e-ctl /opt/sc/syncloud-agent join --controller http://127.0.0.1:7070 --token-file /data/local-join.token --name ctl-0 --data-dir /agent >/dev/null
x -d sc-e2e-ctl sh -c "/opt/sc/syncloud-agent run --data-dir /agent --network $CTL_NETWORK > /var/log/agent.log 2>&1"

# Root account and a join token for the workers.
TOK=$(x sc-e2e-ctl cat /data/setup-token)
SUF=$(x sc-e2e-ctl cat /data/recovery-key | tr -d '-' | tail -c 7 | tr -d '\n')
x sc-e2e-ctl cp /data/recovery-key /root/recovery-key # setup deletes it; restore drills need it
x sc-e2e-ctl curl -fs -c /tmp/jar -H 'content-type: application/json' localhost:7070/api/v1/setup \
  -d "{\"setupToken\":\"$TOK\",\"email\":\"e2e@example.com\",\"name\":\"E2E\",\"password\":\"e2e-password-123\",\"recoveryKeySuffix\":\"$SUF\"}" >/dev/null
JOIN=$(x sc-e2e-ctl curl -fs -b /tmp/jar -H 'content-type: application/json' localhost:7070/api/v1/nodes/join-tokens -d '{"singleUse":false,"ttlMinutes":30}' | sed -n 's/.*"token":"\([^"]*\)".*/\1/p')
[ -n "$JOIN" ] || fail "no join token"
for w in $WORKERS; do
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

# start_vlogs runs VictoriaLogs on the controller node like the system task.
start_vlogs() {
  x sc-e2e-ctl docker load -q -i /opt/sc/vlogs.tar >/dev/null
  x sc-e2e-ctl docker run -d --name vlogs -p 127.0.0.1:9428:9428 victoriametrics/victoria-logs:v1.53.0 -storageDataPath=/vlogs >/dev/null
}

# start_vmetrics runs VictoriaMetrics on the controller node like the system task.
start_vmetrics() {
  x sc-e2e-ctl docker load -q -i /opt/sc/vmetrics.tar >/dev/null
  x sc-e2e-ctl docker run -d --name vmetrics -p 127.0.0.1:8428:8428 victoriametrics/victoria-metrics:v1.153.0 -storageDataPath=/vm -search.latencyOffset=0s >/dev/null
}

# start_traefik runs Traefik on the controller node like the system task does,
# polling the controller's dynamic config (§5.7).
start_traefik() {
  x sc-e2e-ctl docker load -q -i /opt/sc/traefik.tar >/dev/null
  local tok; tok=$(x sc-e2e-ctl cat /data/traefik.token)
  x sc-e2e-ctl docker run -d --name traefik --network host traefik:v3.7.13 \
    --entrypoints.web.address=:8080 --entrypoints.websecure.address=:8443 --entrypoints.valkey.address=:6379 \
    --providers.http.endpoint=http://127.0.0.1:7070/internal/traefik/config --providers.http.pollInterval=2s \
    "--providers.http.headers.X-Syncloud-Token=$tok" >/dev/null
}

# start_registry runs the private registry on the controller node like the
# system task: token auth against the controller (§5.9).
start_registry() {
  x sc-e2e-ctl docker load -q -i /opt/sc/registry.tar >/dev/null
  # Host networking on loopback, like the system task, so notifications
  # reach the controller's loopback API.
  local tok notify
  tok=$(x sc-e2e-ctl cat /data/traefik.token)
  notify="[{\"name\":\"syncloud\",\"url\":\"http://127.0.0.1:7070/internal/registry/events\",\"headers\":{\"X-Syncloud-Token\":[\"$tok\"]},\"timeout\":\"3s\",\"threshold\":5,\"backoff\":\"10s\"}]"
  x sc-e2e-ctl docker run -d --name registry --network host -v /data/registry/registry-token.crt:/etc/syncloud/registry-token.crt:ro \
    -e REGISTRY_HTTP_ADDR=127.0.0.1:5000 -e REGISTRY_HTTP_DEBUG_ADDR=127.0.0.1:5001 -e "REGISTRY_NOTIFICATIONS_ENDPOINTS=$notify" \
    -e REGISTRY_AUTH_TOKEN_REALM=http://127.0.0.1:7070/api/v1/registry/token -e REGISTRY_AUTH_TOKEN_SERVICE=syncloud-registry \
    -e REGISTRY_AUTH_TOKEN_ISSUER=syncloud -e REGISTRY_AUTH_TOKEN_ROOTCERTBUNDLE=/etc/syncloud/registry-token.crt \
    -e REGISTRY_STORAGE_DELETE_ENABLED=true -e OTEL_TRACES_EXPORTER=none registry:3.1.2 >/dev/null
}
