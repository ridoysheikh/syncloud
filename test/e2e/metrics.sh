#!/usr/bin/env bash
# End-to-end test of task metrics (§9.1): agents on every node sample their
# task containers, the controller stores the samples in VictoriaMetrics, and
# the API charts a service per task and an environment per service.
. "$(dirname "$0")/lib.sh"
WITH_METRICS=1
trap cleanup EXIT
setup_cluster
start_vmetrics
wait_mesh

echo "== services"
api localhost:7070/api/v1/projects -d '{"name":"shop"}' >/dev/null
ENV=localhost:7070/api/v1/projects/shop/environments/production
# A busy service (burns CPU) and an idle one, spread over the nodes.
api -X PUT "$ENV/services/burner" -d '{"image":"busybox:1.37","command":["sh","-c","while :; do :; done"],"resources":{"cpu":0.1,"memory":16},"desiredCount":2}' >/dev/null
api -X PUT "$ENV/services/idle" -d '{"image":"busybox:1.37","command":["sleep","3600"],"resources":{"cpu":0.05,"memory":8}}' >/dev/null
for _ in $(seq 1 60); do
  [ "$(api "$ENV/services/burner/tasks" | grep -o '"state":"running"' | wc -l)" -ge 2 ] && break; sleep 1
done
echo "  ✓ 3 tasks running"

echo "== samples"
# Samples every 10s; the first needs a second sample for CPU.
m=""
for _ in $(seq 1 45); do
  m=$(api "$ENV/services/burner/metrics?range=15m" || true)
  [ "$(echo "$m" | grep -o '"key":"task_[a-z0-9]*"' | sort -u | wc -l)" -ge 2 ] && echo "$m" | grep -q '"cpu":\[{' && break
  sleep 2
done
echo "$m" | grep -q '"cpu":\[{' || { echo "$m"; x sc-e2e-ctl tail -20 /var/log/controller.log; fail "no CPU series for burner"; }
[ "$(echo "$m" | grep -o '"key":"task_[a-z0-9]*"' | sort -u | wc -l)" -ge 2 ] || fail "want a series per task: $m"
cpu=$(echo "$m" | python3 -c 'import json,sys; d=json.load(sys.stdin); print(max(p[1] for s in d["charts"]["cpu"] for p in s["points"]))')
python3 -c "import sys; sys.exit(0 if $cpu > 20 else 1)" || fail "burner CPU peak $cpu%, want > 20%"
mem=$(echo "$m" | python3 -c 'import json,sys; d=json.load(sys.stdin); print(max(p[1] for s in d["charts"]["memory"] for p in s["points"]))')
python3 -c "import sys; sys.exit(0 if 0 < $mem < 64*1024*1024 else 1)" || fail "burner memory $mem bytes"
echo "  ✓ per-task series for burner: CPU peak ${cpu%.*}%, memory $((${mem%.*} / 1024)) KiB"

e=$(api "$ENV/metrics?range=15m")
echo "$e" | grep -q '"key":"burner"' && echo "$e" | grep -q '"key":"idle"' || fail "environment metrics lack a service: $e"
echo "  ✓ environment metrics have a series per service"
# New series need a few samples before every range shows them.
for r in 1h 6h 24h 7d; do
  for _ in $(seq 1 20); do api "$ENV/metrics?range=$r" | grep -q '"key":"burner","node":"","points":\[\[' && break; sleep 3; done
  api "$ENV/metrics?range=$r" | grep -q '"key":"burner","node":"","points":\[\[' || fail "range $r hides the newest samples"
done
echo "  ✓ every range includes the newest samples"
api "$ENV/metrics?range=2y" >/dev/null 2>&1 && fail "a bad range was accepted"
PAT=$(api localhost:7070/api/v1/iam/tokens -d '{"name":"e2e"}' | grep -o '"token":"[^"]*"' | cut -d'"' -f4)
out=$(x -e SYNCLOUD_TOKEN="$PAT" sc-e2e-ctl /opt/sc/synctl --endpoint http://127.0.0.1:7070 metrics -p shop --range 15m)
echo "$out" | grep -q '^burner' || fail "synctl metrics: $out"
echo "  ✓ synctl metrics"
echo "PASS"
