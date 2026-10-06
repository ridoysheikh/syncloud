#!/usr/bin/env bash
# End-to-end test of registry lifecycle policies and garbage collection
# (§5.10) with the controller's real system tasks: preview, in-use
# protection, a cleanup run that expires tags and reclaims space with the
# registry briefly read-only, and pushes working again afterwards.
. "$(dirname "$0")/lib.sh"
WITH_REGISTRY=1
WITH_TRAEFIK=1
WITH_METRICS=1
CTL_FLAGS="--system-tasks=true --registry-pull-host 127.0.0.1:5000"
trap cleanup EXIT
setup_cluster
for t in registry traefik vlogs vmetrics; do x sc-e2e-ctl docker load -q -i /opt/sc/$t.tar >/dev/null; done
wait_mesh
for _ in $(seq 1 60); do
  [ "$(api localhost:7070/api/v1/system/tasks | grep -o '"state":"running"' | wc -l)" -ge 4 ] && break; sleep 2
done
api localhost:7070/api/v1/system/tasks | grep -q '"taskId":"sys-registry"[^}]*"state":"running"' || { api localhost:7070/api/v1/system/tasks; fail "registry system task not running"; }
echo "  ✓ system tasks running (registry, Traefik, VictoriaMetrics, VictoriaLogs)"

echo "== push"
key=$(api localhost:7070/api/v1/iam/access-keys -d '{"description":"e2e"}')
KID=$(echo "$key" | grep -o '"id":"SYNAK[A-Z0-9]*"' | cut -d'"' -f4); KSEC=$(echo "$key" | grep -o '"secretAccessKey":"[^"]*"' | cut -d'"' -f4)
x sc-e2e-ctl sh -c "echo '$KSEC' | docker login 127.0.0.1:5000 -u '$KID' --password-stdin" >/dev/null 2>&1 || fail "docker login failed"
push() { # push TAG CONTENT: a distinct image per content
  x sc-e2e-ctl sh -c "printf 'FROM busybox:1.37\nRUN echo $2 > /v\n' | docker build -q -t 127.0.0.1:5000/shop/web:$1 - >/dev/null && docker push -q 127.0.0.1:5000/shop/web:$1 >/dev/null" || fail "push $1"
}
for i in 1 2 3 4; do push v$i v$i; sleep 1; done
push dev dev-old
push dev dev-new # the old dev manifest is now untagged garbage
echo "  ✓ pushed v1..v4 and dev (twice)"

echo "== in use"
for w in w1 w2; do
  id=$(api localhost:7070/api/v1/nodes | grep -o "\"id\":\"node_[a-z0-9]*\",\"name\":\"$w\"" | cut -d'"' -f4)
  api -X PUT "localhost:7070/api/v1/nodes/$id/schedulable" -d '{"schedulable":false}' >/dev/null
done
api localhost:7070/api/v1/projects -d '{"name":"shop"}' >/dev/null
SVC=localhost:7070/api/v1/projects/shop/environments/production/services/web
api -X PUT "$SVC" -d '{"image":"@registry/shop/web:v1","command":["sleep","3600"],"resources":{"cpu":0.05,"memory":16}}' >/dev/null
for _ in $(seq 1 60); do api "$SVC/tasks" | grep -q '"state":"running"' && break; sleep 2; done
api "$SVC/tasks" | grep -q '"state":"running"' || fail "service on v1 did not start"
echo "  ✓ a service runs v1 (the oldest image)"

echo "== preview"
RULES='{"rules":[{"priority":1,"tagPrefix":"v","keepLast":2}]}'
pv=$(api localhost:7070/api/v1/registry/lifecycle/preview -d "{\"repository\":\"shop/web\",${RULES:1}")
decision() { echo "$pv" | grep -o "\"tag\":\"$1\"[^}]*" | grep -o '"expire":[a-z]*' | cut -d: -f2; }
[ "$(decision v4)" = false ] && [ "$(decision v3)" = false ] || fail "v3/v4 must be kept: $pv"
[ "$(decision v2)" = true ] || fail "v2 must expire: $pv"
[ "$(decision v1)" = false ] && echo "$pv" | grep -o '"tag":"v1"[^}]*' | grep -q '"inUse":true' || fail "v1 is in use and must be kept: $pv"
[ "$(decision dev)" = false ] || fail "dev matches no rule: $pv"
echo "$pv" | grep -q '"expire":1' || fail "preview count: $pv"
echo "  ✓ preview: v2 expires; v3, v4 kept; v1 kept because a service uses it"
api -X PUT "localhost:7070/api/v1/registry/lifecycle?repository=shop/web" -d "$RULES" >/dev/null
api localhost:7070/api/v1/registry/repositories | grep -q '"name":"shop/web","tags":5,"lifecycle":true' || fail "repository list lacks the policy"
api -X PUT "localhost:7070/api/v1/registry/lifecycle?repository=shop/web" -d '{"rules":[{"priority":1,"keepLast":1,"olderThanDays":3}]}' >/dev/null 2>&1 && fail "an invalid rule was accepted"
echo "  ✓ policy saved; invalid rules refused"

echo "== cleanup"
PAT=$(api localhost:7070/api/v1/iam/tokens -d '{"name":"e2e"}' | grep -o '"token":"[^"]*"' | cut -d'"' -f4)
out=$(x -e SYNCLOUD_TOKEN="$PAT" sc-e2e-ctl /opt/sc/synctl --endpoint http://127.0.0.1:7070 registry gc run --wait -o json 2>&1) || { echo "$out"; x sc-e2e-ctl tail -20 /var/log/controller.log; fail "cleanup failed"; }
echo "$out" | grep -q '"expired": 1' || fail "want 1 expired image: $out"
reclaimed=$(echo "$out" | grep -o '"reclaimedBytes": [0-9]*' | grep -o '[0-9]*$')
[ "${reclaimed:-0}" -gt 0 ] || fail "nothing reclaimed: $out"
tags=$(api "localhost:7070/api/v1/registry/images?repository=shop/web" | grep -o '"tag":"[^"]*"' | cut -d'"' -f4 | sort | tr '\n' ' ')
[ "$tags" = "dev v1 v3 v4 " ] || fail "tags after cleanup: $tags"
for _ in $(seq 1 10); do api "localhost:7070/api/v1/registry/events?repository=shop/web" | grep -q '"action":"delete"' && break; sleep 1; done
ev=$(api "localhost:7070/api/v1/registry/events?repository=shop/web&limit=1000")
[ "$(echo "$ev" | grep -o '"action":"push"' | wc -l)" -ge 6 ] || fail "pushes missing from registry events: $ev"
echo "$ev" | grep -q '"action":"delete"' || fail "the cleanup's delete is missing from registry events"
echo "  ✓ cleanup expired v2 and reclaimed $((reclaimed / 1024)) KiB (untagged dev included); v1, v3, v4, dev remain; pushes and the delete arrived as registry notifications"

echo "== after"
for _ in $(seq 1 30); do api localhost:7070/api/v1/system/tasks | grep -q '"taskId":"sys-registry"[^}]*"state":"running"' && break; sleep 1; done
push v5 v5
api "localhost:7070/api/v1/registry/images?repository=shop/web" | grep -q '"tag":"v5"' || fail "push after cleanup"
api "$SVC/tasks" | grep -q '"state":"running"' || fail "the service stopped"
api localhost:7070/api/v1/registry/gc | grep -q '"status":"succeeded"' || fail "run not logged"
echo "  ✓ the registry accepts pushes again; the service kept running; the run is logged"
echo "PASS"
