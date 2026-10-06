#!/usr/bin/env bash
# Load test (Phase 9 target: 50 nodes and 2,000 tasks on a 4 vCPU / 8 GB
# controller). The controller runs in a container limited to 4 CPUs and 8 GB;
# test/load/fakeagent simulates the nodes (real join, mTLS streams, heartbeats
# and task reports, no Docker). Prints scheduling time, controller CPU and
# memory, and API latency.
#
#   test/load/run.sh                      # 50 nodes, 40 services × 50 tasks
#   NODES=100 SERVICES=80 test/load/run.sh
#   KEEP=1 test/load/run.sh               # leave the controller running
set -euo pipefail
cd "$(dirname "$0")/../.."
NODES=${NODES:-50} SERVICES=${SERVICES:-40} PER=${PER:-50}
NET=sc-load CTL=sc-load-ctl
BIN=$(mktemp -d)
FAKE_PID=""
cleanup() {
  [ -n "$FAKE_PID" ] && kill "$FAKE_PID" 2>/dev/null || true
  [ "${KEEP:-0}" = 1 ] && { echo "kept $CTL"; return; }
  docker rm -f $CTL >/dev/null 2>&1 || true
  docker network rm $NET >/dev/null 2>&1 || true
}
trap cleanup EXIT
fail() { echo "FAIL: $*" >&2; exit 1; }

echo "== build"
CGO_ENABLED=0 GOOS=linux go build -o "$BIN/syncloud-controller" ./cmd/controller
go build -o "$BIN/fakeagent" ./test/load/fakeagent
docker rm -f $CTL >/dev/null 2>&1 || true
docker network rm $NET >/dev/null 2>&1 || true
docker network create $NET >/dev/null
docker run -d --name $CTL --network $NET --cpus 4 --memory 8g -v "$BIN:/opt/sc:ro" alpine:3.22 sleep infinity >/dev/null
docker exec $CTL apk add -q --no-cache curl >/dev/null
IP=$(docker inspect -f "{{(index .NetworkSettings.Networks \"$NET\").IPAddress}}" $CTL)
docker exec -d $CTL sh -c "/opt/sc/syncloud-controller --data-dir /data --listen 0.0.0.0:7070 --agent-listen 0.0.0.0:7443 --agent-advertise $IP:7443 \
  --system-tasks=false --acme=false --firewall=false --central-probes=false --base-domain load.test > /var/log/controller.log 2>&1"
for _ in $(seq 1 30); do curl -fs "http://$IP:7070/api/v1/system/status" >/dev/null 2>&1 && break; sleep 1; done
JAR=$BIN/jar
TOK=$(docker exec $CTL cat /data/setup-token)
SUF=$(docker exec $CTL cat /data/recovery-key | tr -d '-' | tail -c 7 | tr -d '\n')
curl -fs -c "$JAR" -H 'content-type: application/json' -H "Origin: http://$IP:7070" "http://$IP:7070/api/v1/setup" \
  -d "{\"setupToken\":\"$TOK\",\"email\":\"load@example.com\",\"name\":\"Load\",\"password\":\"load-password-123\",\"recoveryKeySuffix\":\"$SUF\"}" >/dev/null
api() { curl -fsS -b "$JAR" -H 'content-type: application/json' -H "Origin: http://$IP:7070" "$@"; }
P=http://$IP:7070/api/v1
JOIN=$(api $P/nodes/join-tokens -d '{"singleUse":false,"ttlMinutes":60}' | sed -n 's/.*"token":"\([^"]*\)".*/\1/p')

echo "== $NODES fake nodes"
t0=$(date +%s)
"$BIN/fakeagent" -controller "http://$IP:7070" -token "$JOIN" -nodes "$NODES" -dir "$BIN/fake" > "$BIN/fake.log" 2>&1 &
FAKE_PID=$!
ready() { api $P/nodes | grep -o '"status":"ready"' | wc -l; }
for _ in $(seq 1 120); do [ "$(ready)" -ge "$NODES" ] && break; sleep 1; done
[ "$(ready)" -ge "$NODES" ] || fail "only $(ready) nodes ready: $(tail -5 "$BIN/fake.log")"
echo "  $NODES nodes joined and ready in $(( $(date +%s) - t0 ))s"

echo "== $SERVICES services × $PER tasks"
projects=$(( (SERVICES + 9) / 10 ))
for p in $(seq 1 $projects); do api $P/projects -d "{\"name\":\"p$p\"}" >/dev/null; done
t0=$(date +%s)
for s in $(seq 1 "$SERVICES"); do
  p=$(( (s - 1) / 10 + 1 ))
  api -X PUT "$P/projects/p$p/environments/production/services/s$s" \
    -d "{\"image\":\"busybox:1.37\",\"command\":[\"sleep\",\"1d\"],\"resources\":{\"cpu\":0.05,\"memory\":16},\"desiredCount\":$PER}" >/dev/null
done
want=$(( SERVICES * PER ))
running() { api "$P/tasks" | grep -o '"desired":"running","state":"running"' | wc -l; }
for _ in $(seq 1 300); do [ "$(running)" -ge $want ] && break; sleep 2; done
got=$(running)
[ "$got" -ge $want ] || fail "only $got of $want tasks running"
echo "  $want tasks running $(( $(date +%s) - t0 ))s after the first apply"

echo "== steady state (60 s)"
lat() { # median and p95 of 20 requests, ms
  for _ in $(seq 1 20); do curl -fs -o /dev/null -w '%{time_total}\n' -b "$JAR" "$1"; done | sort -n | awk '{a[NR]=$1} END {printf "p50 %.0f ms, p95 %.0f ms", a[int(NR*0.5)]*1000, a[int(NR*0.95)]*1000}'
}
cpu=() mem=()
for _ in $(seq 1 6); do
  read -r c m < <(docker stats --no-stream --format '{{.CPUPerc}} {{.MemUsage}}' $CTL | awk '{gsub("%","",$1); print $1, $2}')
  cpu+=("$c") mem+=("$m"); sleep 8
done
echo "  controller CPU (of 400%): ${cpu[*]}"
echo "  controller memory: ${mem[*]}"
echo "  GET /nodes:    $(lat $P/nodes)"
echo "  GET /services: $(lat $P/services)"
echo "  GET /tasks:    $(lat $P/tasks)"
echo "  GET /projects/p1/environments/production/services/s1: $(lat $P/projects/p1/environments/production/services/s1)"

echo "== rolling update of one service ($PER tasks)"
t0=$(date +%s)
api -X PUT "$P/projects/p1/environments/production/services/s1" \
  -d "{\"image\":\"busybox:1.37\",\"command\":[\"sleep\",\"2d\"],\"resources\":{\"cpu\":0.05,\"memory\":16},\"desiredCount\":$PER}" >/dev/null
for _ in $(seq 1 300); do
  api "$P/projects/p1/environments/production/services/s1/deployments" 2>/dev/null | grep -q '"toRevision":2,"status":"succeeded"' && break; sleep 1
done
echo "  rolled out in $(( $(date +%s) - t0 ))s"
grep -c 'level=ERROR' <(docker exec $CTL cat /var/log/controller.log) | sed 's/^/  controller errors logged: /' || true
echo "DONE"
