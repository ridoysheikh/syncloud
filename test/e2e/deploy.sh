#!/usr/bin/env bash
# End-to-end test of deployments (§5.4, §5.6): health-gated rollouts,
# replacement of unhealthy tasks, and the circuit breaker's automatic rollback.
#
#   test/e2e/deploy.sh          # run and clean up
#   KEEP=1 test/e2e/deploy.sh   # leave the nodes running for inspection
. "$(dirname "$0")/lib.sh"
trap cleanup EXIT
setup_cluster
wait_mesh

SVC=localhost:7070/api/v1/projects/shop/environments/production/services/api
field() { grep -o "\"$1\":[^,}]*" | head -1 | cut -d: -f2- | tr -d '"'; }
wait_for() { # description, condition
  local d=$1; shift
  for _ in $(seq 1 90); do "$@" && return 0; sleep 2; done
  echo "--- service:"; api "$SVC" | head -c 2000; echo; echo "--- deployments:"; api "$SVC/deployments" | head -c 2000; echo
  fail "timed out: $d"
}
spec() { # health path, desired
  echo "{\"image\":\"busybox:1.37\",\"command\":[\"sh\",\"-c\",\"mkdir -p /www && echo ok > /www/ok && exec httpd -f -p 8080 -h /www\"],\"ports\":[{\"container\":8080}],\"resources\":{\"cpu\":0.05,\"memory\":16},\"health\":{\"type\":\"http\",\"path\":\"$1\",\"interval\":2,\"retries\":2,\"startPeriod\":1},\"desiredCount\":$2}"
}
running() { api "$SVC" | field running; }
dep_status() { api "$SVC/deployments" | grep -o "\"toRevision\":$1,\"status\":\"[a-z_]*\"" | head -1 | cut -d'"' -f6; }

echo "== health-gated first rollout"
api localhost:7070/api/v1/projects -d '{"name":"shop"}' >/dev/null
api -X PUT "$SVC" -d "$(spec /ok 2)" >/dev/null
wait_for "2 healthy tasks" sh -c "[ \"\$(docker exec sc-e2e-ctl curl -fs -b /tmp/jar $SVC | grep -o '\"running\":[0-9]*' | cut -d: -f2)\" = 2 ]"
api "$SVC/tasks" | grep -q '"health":"healthy"' || fail "tasks not reported healthy"
wait_for "deployment 1 succeeded" sh -c "docker exec sc-e2e-ctl curl -fs -b /tmp/jar $SVC/deployments | grep -q '\"toRevision\":1,\"status\":\"succeeded\"'"
echo "  ✓ 2 tasks healthy; deployment → 1 succeeded"

echo "== unhealthy task is replaced"
victim=$(api "$SVC/tasks" | grep -o '"id":"task_[a-z0-9]*"[^}]*"desired":"running","state":"running"[^}]*"containerId":"[a-f0-9]*"' | head -1)
vid=$(echo "$victim" | field id); vnode=$(echo "$victim" | grep -o '"node":"[a-z0-9-]*"' | cut -d'"' -f4); vcid=$(echo "$victim" | grep -o '"containerId":"[a-f0-9]*"' | cut -d'"' -f4)
x "sc-e2e-$([ "$vnode" = ctl-0 ] && echo ctl || echo "$vnode")" docker exec "$vcid" rm /www/ok
wait_for "unhealthy task $vid replaced" sh -c "docker exec sc-e2e-ctl curl -fs -b /tmp/jar $SVC/tasks | grep -q '\"id\":\"$vid\",[^}]*\"desired\":\"stopped\"'"
wait_for "back to 2 healthy" sh -c "[ \"\$(docker exec sc-e2e-ctl curl -fs -b /tmp/jar $SVC | grep -o '\"running\":[0-9]*' | cut -d: -f2)\" = 2 ]"
echo "  ✓ task $vid turned unhealthy and was replaced"

echo "== circuit breaker"
old=$(api "$SVC/tasks" | grep -o '"id":"task_[a-z0-9]*","serviceId":"[^"]*","project":"shop","environment":"production","service":"api","revision":1,[^}]*"desired":"running"' | grep -o '"id":"task_[a-z0-9]*"' | cut -d'"' -f4 | sort)
v=$(api -X PUT "$SVC" -d "$(spec /missing 2)")
[ "$(echo "$v" | field revision)" = 2 ] || fail "bad spec did not create revision 2"
wait_for "deployment 2 rolled back" sh -c "docker exec sc-e2e-ctl curl -fs -b /tmp/jar $SVC/deployments | grep -q '\"toRevision\":2,\"status\":\"rolled_back\"'"
now_old=$(api "$SVC/tasks" | grep -o '"id":"task_[a-z0-9]*","serviceId":"[^"]*","project":"shop","environment":"production","service":"api","revision":1,[^}]*"desired":"running"' | grep -o '"id":"task_[a-z0-9]*"' | cut -d'"' -f4 | sort)
[ "$old" = "$now_old" ] || fail "revision 1 tasks were stopped during the failed rollout (before: $old, after: $now_old)"
echo "  ✓ revision 2 never became healthy; revision 1 tasks kept serving throughout"
wait_for "rollback deployment → 3 succeeded" sh -c "docker exec sc-e2e-ctl curl -fs -b /tmp/jar $SVC/deployments | grep -q '\"toRevision\":3,\"status\":\"succeeded\"'"
api "$SVC/deployments" | grep -q 'automatic rollback' || fail "rollback deployment lacks its reason"
api "$SVC" | grep -q '"path":"/ok"' || fail "revision 3 is not revision 1's spec"
echo "  ✓ circuit breaker tripped and rolled back to revision 1's spec (revision 3)"
echo "== jobs"
key=$(api localhost:7070/api/v1/iam/access-keys -d '{"description":"e2e"}')
KID=$(echo "$key" | grep -o '"id":"SYNAK[A-Z0-9]*"' | cut -d'"' -f4); KSEC=$(echo "$key" | grep -o '"secretAccessKey":"[^"]*"' | cut -d'"' -f4)
synctl() { x -i -e SYNCLOUD_ENDPOINT=http://127.0.0.1:7070 -e SYNCLOUD_ACCESS_KEY_ID="$KID" -e SYNCLOUD_SECRET_ACCESS_KEY="$KSEC" sc-e2e-ctl /opt/sc/synctl "$@"; }
out=$(synctl run service/api -p shop -- sh -c 'echo migrating; echo done' </dev/null 2>&1)
echo "$out" | grep -q migrating || fail "synctl run did not stream the output: $out"
set +e; synctl run service/api -p shop -- sh -c 'exit 4' </dev/null >/dev/null 2>&1; code=$?; set -e
[ "$code" = 4 ] || fail "synctl run exit code $code, want 4"
echo "  ✓ synctl run service/api: output streamed, exit code (4) propagated"

JOBS=localhost:7070/api/v1/projects/shop/environments/production/jobs
api -X PUT "$JOBS/flaky" -d '{"service":"api","command":["sh","-c","exit 2"],"retries":1}' >/dev/null
api -X POST "$JOBS/flaky/runs" >/dev/null
wait_for "flaky retried" sh -c "[ \$(docker exec sc-e2e-ctl curl -fs -b /tmp/jar $JOBS/flaky/runs | grep -o '\"status\":\"failed\"' | wc -l) -ge 2 ]"
api "$JOBS/flaky/runs" | grep -q '"trigger":"retry","attempt":2' || fail "no retry attempt recorded"
echo "  ✓ failed run retried once (attempt 2), then given up"

api -X PUT "$JOBS/tick" -d '{"service":"api","command":["sh","-c","echo tick"],"schedule":"* * * * *","timezone":"Asia/Dhaka"}' | grep -q '"nextRunAt":"' || fail "scheduled job has no next run"
wait_for "scheduled run succeeded" sh -c "docker exec sc-e2e-ctl curl -fs -b /tmp/jar $JOBS/tick/runs | grep -q '\"trigger\":\"schedule\",[^}]*\"status\":\"succeeded\"'"
api -X DELETE "$JOBS/tick" >/dev/null
echo "  ✓ cron job ran on schedule"

rev=$(api "$SVC" | field revision)
api -X PUT "$JOBS/migrate" -d '{"kind":"pre-deploy","service":"api","command":["sh","-c","echo checking; exit 1"]}' >/dev/null
api -X PUT "$SVC" -d "$(spec /ok 2 | sed 's/"desiredCount"/"env":{"V":"2"},"desiredCount"/')" >/dev/null
wait_for "deployment aborted by hook" sh -c "docker exec sc-e2e-ctl curl -fs -b /tmp/jar $SVC/deployments | grep -q 'pre-deploy job failed'"
[ "$(api "$SVC" | field revision)" = "$rev" ] || fail "revision switched although the pre-deploy hook failed"
echo "  ✓ failing pre-deploy hook aborted the deployment; revision $rev kept running"
api -X PUT "$JOBS/migrate" -d '{"kind":"pre-deploy","service":"api","command":["sh","-c","echo migrated"]}' >/dev/null
api -X PUT "$SVC" -d "$(spec /ok 2 | sed 's/"desiredCount"/"env":{"V":"3"},"desiredCount"/')" >/dev/null
wait_for "hook passed, deployment succeeded" sh -c "docker exec sc-e2e-ctl curl -fs -b /tmp/jar $SVC/deployments | head -c 400 | grep -q '\"status\":\"succeeded\"'"
[ "$(api "$SVC" | field revision)" != "$rev" ] || fail "revision did not switch after the hook passed"
api -X DELETE "$JOBS/migrate" >/dev/null
echo "  ✓ passing pre-deploy hook let the new revision roll out"

echo "== drain"
dnode=$(api "$SVC/tasks" | grep -o '"nodeId":"node_[a-z0-9]*","node":"[a-z0-9-]*","desired":"running","state":"running"' | head -1)
did=$(echo "$dnode" | cut -d'"' -f4); dname=$(echo "$dnode" | cut -d'"' -f8)
api -X POST "localhost:7070/api/v1/nodes/$did/drain" >/dev/null
wait_for "no tasks left on $dname" sh -c "! docker exec sc-e2e-ctl curl -fs -b /tmp/jar $SVC/tasks | grep -q '\"node\":\"$dname\",\"desired\":\"running\"'"
wait_for "2 healthy after drain" sh -c "[ \"\$(docker exec sc-e2e-ctl curl -fs -b /tmp/jar $SVC | grep -o '\"running\":[0-9]*' | cut -d: -f2)\" = 2 ]"
api -X PUT "localhost:7070/api/v1/nodes/$did/schedulable" -d '{"schedulable":true}' | grep -q '"draining":false' || fail "uncordon did not end the drain"
echo "  ✓ drained $dname: tasks moved to other nodes, 2 healthy throughout the end state"
echo "PASS"
