#!/usr/bin/env bash
# End-to-end test of target tracking autoscaling (§5.5) on the real system
# tasks: requests per task through Traefik scale a service out and, once the
# load stops, back in after its checks and cooldown; CPU scales a busy
# service out to its maximum; the minimum is enforced right away. Every
# change is recorded with its reason.
. "$(dirname "$0")/lib.sh"
WITH_REGISTRY=1
WITH_TRAEFIK=1
WITH_METRICS=1
CTL_FLAGS="--system-tasks=true"
trap cleanup EXIT
setup_cluster
for t in registry traefik vlogs vmetrics; do x sc-e2e-ctl docker load -q -i /opt/sc/$t.tar >/dev/null; done
wait_mesh
for _ in $(seq 1 60); do
  [ "$(api localhost:7070/api/v1/system/tasks | grep -o '"state":"running"' | wc -l)" -ge 4 ] && break; sleep 2
done
echo "  ✓ system tasks running"

api localhost:7070/api/v1/projects -d '{"name":"shop"}' >/dev/null
SVC=localhost:7070/api/v1/projects/shop/environments/production/services
desired() { api "$SVC/$1" | grep -o '"desiredCount":[0-9]*' | cut -d: -f2; }
# wait_desired SERVICE OP N SECONDS: until desiredCount OP N (-ge, -le, -eq).
wait_desired() {
  local d
  for _ in $(seq 1 $(($4 / 3))); do d=$(desired "$1"); [ "$d" "$2" "$3" ] && return; sleep 3; done
  api "$SVC/$1/scaling-events"; api "$SVC/$1/autoscaling"
  fail "$1: desiredCount $d, wanted $2 $3"
}

echo "== validation"
api -X PUT "$SVC/web" -d '{"image":"busybox:1.37","command":["httpd","-f","-p","8080","-h","/etc"],"ports":[{"container":8080}],"resources":{"cpu":0.05,"memory":16},"desiredCount":1}' >/dev/null
api -X PUT "$SVC/web/autoscaling" -d '{"min":1,"max":4,"metric":"qps","target":2}' >/dev/null 2>&1 && fail "an unknown metric was accepted"
api -X PUT "$SVC/web/autoscaling" -d '{"min":5,"max":4,"metric":"rps","target":2}' >/dev/null 2>&1 && fail "min > max was accepted"
api -X PUT "$SVC/web/autoscaling" -d '{"min":1,"max":4,"metric":"rps","target":2,"scaleOutCooldown":15,"scaleInCooldown":15,"scaleInChecks":2}' | grep -q '"metric":"rps"' || fail "policy not saved"
echo "  ✓ policies are validated"

echo "== scale out on requests per task"
for _ in $(seq 1 60); do [ "$(x sc-e2e-ctl curl -s -o /dev/null -w '%{http_code}' -H 'Host: web-production-shop.localhost' http://127.0.0.1:8080/hostname)" = 200 ] && break; sleep 2; done
x -d sc-e2e-ctl sh -c 'touch /tmp/load; while [ -f /tmp/load ]; do curl -s -o /dev/null -H "Host: web-production-shop.localhost" http://127.0.0.1:8080/hostname; sleep 0.08; done'
wait_desired web -ge 3 150
api "$SVC/web/scaling-events" | grep -q '"reason":"rps [0-9.]* > target 2 requests/s per task → 1→[34] tasks"' || fail "scale-out reason: $(api "$SVC/web/scaling-events")"
echo "  ✓ ~10 req/s against a target of 2 per task: 1 → $(desired web) tasks ($(api "$SVC/web/scaling-events" | grep -o '"reason":"[^"]*"' | head -1))"

echo "== scale in when the load stops"
x sc-e2e-ctl rm -f /tmp/load
wait_desired web -eq 1 240
api "$SVC/web/scaling-events" | grep -q '"reason":"rps [0-9.]* < target 2 requests/s per task for 2 checks → [2-4]→1 tasks"' || fail "scale-in reason: $(api "$SVC/web/scaling-events")"
echo "  ✓ idle: back to 1 task after 2 checks and the cooldown"

echo "== CPU and the minimum"
api -X PUT "$SVC/burner" -d '{"image":"busybox:1.37","command":["sh","-c","while :; do :; done"],"resources":{"cpu":0.25,"memory":16},"desiredCount":1}' >/dev/null
api -X PUT "$SVC/burner/autoscaling" -d '{"min":1,"max":3,"metric":"cpu","target":50,"scaleOutCooldown":15}' >/dev/null
api -X PUT "$SVC/web/autoscaling" -d '{"min":2,"max":4,"metric":"rps","target":2}' >/dev/null
wait_desired web -eq 2 60
api "$SVC/web/scaling-events" | grep -q 'below the minimum of 2 tasks → 1→2 tasks' || fail "min clamp reason"
echo "  ✓ raising the minimum to 2 scales web out at the next check"
wait_desired burner -eq 3 180
api "$SVC/burner/scaling-events" | grep -q '"reason":"cpu [0-9]* > target 50 % of reserved CPU' || fail "cpu reason: $(api "$SVC/burner/scaling-events")"
api "$SVC/burner/autoscaling" | grep -q '"value":[0-9]' || fail "no current value"
echo "  ✓ a busy loop (≈400% of a 0.25 CPU reservation) scales burner to its maximum of 3"

echo "== charts, synctl, off"
charts_ok() { local c; c=$(api "$SVC/burner/autoscaling/charts?range=15m"); echo "$c" | grep -q '"key":"value"' && echo "$c" | grep -q '"key":"target"' && echo "$c" | grep -q '"key":"desired","node":"","points":\[[^}]*,3\]'; }
for _ in $(seq 1 30); do charts_ok && break; sleep 3; done
c=$(api "$SVC/burner/autoscaling/charts?range=15m")
charts_ok || fail "charts: $c"
key=$(api localhost:7070/api/v1/iam/access-keys -d '{"description":"e2e"}')
KID=$(echo "$key" | grep -o '"id":"SYNAK[A-Z0-9]*"' | cut -d'"' -f4); KSEC=$(echo "$key" | grep -o '"secretAccessKey":"[^"]*"' | cut -d'"' -f4)
synctl() { x -i -e SYNCLOUD_ENDPOINT=http://127.0.0.1:7070 -e SYNCLOUD_ACCESS_KEY_ID="$KID" -e SYNCLOUD_SECRET_ACCESS_KEY="$KSEC" sc-e2e-ctl /opt/sc/synctl "$@" </dev/null; }
synctl autoscale history burner -p shop | grep -q '1 → 3' || fail "synctl autoscale history"
synctl autoscale set burner -p shop --min 1 --max 2 --cpu 70 | grep -q 'Autoscaling on: 1–2 tasks, cpu target 70' || fail "synctl autoscale set"
synctl autoscale off burner -p shop >/dev/null
api "$SVC/burner/autoscaling" | grep -q '"policy":null' || fail "policy not deleted"
echo "  ✓ desired/running and value/target charts; synctl autoscale set, history, off"
echo "PASS"
