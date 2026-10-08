#!/usr/bin/env bash
# End-to-end test of deployment history and actions (Phase 15a): triggers,
# change summaries and timelines, pre-deploy hooks on the timeline,
# redeploy, rollback with and without hooks, cancelling a deployment that
# waits on its hooks and one that is rolling out, the project-wide listing,
# and the synctl commands.
#
#   test/e2e/deployments.sh                         # run and clean up
#   E2E_PREFIX=sc-dep KEEP=1 test/e2e/deployments.sh # beside a kept cluster
WORKERS=${WORKERS-w1}
. "$(dirname "$0")/lib.sh"
trap cleanup EXIT
setup_cluster
wait_mesh

P=localhost:7070/api/v1/projects/shop
SVC=$P/environments/production/services/api
JOBS=$P/environments/production/jobs
# json PATH: prints a value of stdin's JSON (python expression on d).
json() { python3 -c "import json,sys; d=json.load(sys.stdin); print($1)"; }
wait_for() { # description, condition
  local d=$1; shift
  for _ in $(seq 1 90); do "$@" && return 0; sleep 2; done
  echo "--- deployments:"; api "$SVC/deployments" | head -c 3000; echo
  fail "timed out: $d"
}
spec() { # command prefix, env V
  echo "{\"image\":\"busybox:1.37\",\"command\":[\"sh\",\"-c\",\"$1mkdir -p /www && echo ok > /www/ok && exec httpd -f -p 8080 -h /www\"],\"ports\":[{\"container\":8080}],\"resources\":{\"cpu\":0.05,\"memory\":16},\"env\":{\"V\":\"$2\"},\"health\":{\"type\":\"http\",\"path\":\"/ok\",\"interval\":2,\"retries\":2,\"startPeriod\":1},\"desiredCount\":1}"
}
latest() { api "$SVC/deployments?limit=1" | json "d['items'][0]$1"; }
latest_is() { [ "$(latest "['status']")" = "$1" ]; }

echo "== history: trigger, who, changes, timeline"
api localhost:7070/api/v1/projects -d '{"name":"shop"}' >/dev/null
api -X PUT "$SVC" -d "$(spec '' 1)" >/dev/null
wait_for "first deployment succeeded" latest_is succeeded
[ "$(latest "['trigger']")" = manual ] || fail "first deployment trigger $(latest "['trigger']")"
[ "$(latest "['actorName']")" = e2e@example.com ] || fail "actor $(latest "['actorName']")"
api -X PUT "$SVC" -d "$(spec '' 2)" >/dev/null
wait_for "second deployment succeeded" latest_is succeeded
[ "$(latest "['changes']")" = "[{'field': 'variable V', 'from': 'changed', 'to': 'changed'}]" ] || fail "changes $(latest "['changes']")"
DEP=$(latest "['id']")
kinds=$(api "$SVC/deployments/$DEP" | json "' '.join(e['kind'] for e in d['events'])")
for k in started tasks-started serving drained succeeded; do
  case " $kinds " in *" $k "*) ;; *) fail "timeline lacks $k: $kinds" ;; esac
done
echo "  ✓ manual deployments by e2e@example.com; variable change named, value hidden; timeline: $kinds"

echo "== redeploy with a pre-deploy hook"
api -X PUT "$JOBS/migrate" -d '{"kind":"pre-deploy","service":"api","command":["sh","-c","echo migrated"]}' >/dev/null
rev=$(api "$SVC" | json "d['revision']")
api -X POST "$SVC/redeploy" -d '{}' | json "d['revision']" >/dev/null
wait_for "redeploy succeeded" latest_is succeeded
[ "$(latest "['trigger']")" = redeploy ] || fail "redeploy trigger $(latest "['trigger']")"
[ "$(latest "['hooks'][0]['trigger']")/$(latest "['hooks'][0]['status']")" = pre-deploy/succeeded ] || fail "redeploy hooks $(latest "['hooks']")"
DEP=$(latest "['id']")
kinds=$(api "$SVC/deployments/$DEP" | json "' '.join(e['kind'] for e in d['events'])")
case "$kinds" in *"hook-started hook-succeeded hooks-done"*) ;; *) fail "hook steps missing: $kinds" ;; esac
[ "$(api "$SVC/deployments/$DEP" | json "d['runs'][0]['trigger']")" = pre-deploy ] || fail "detail lacks the hook run"
[ "$(api "$SVC" | json "d['revision']")" = $((rev + 1)) ] || fail "redeploy did not make a new revision"
echo "  ✓ redeploy rolled out revision $((rev + 1)) after its pre-deploy job; the timeline shows the job"

echo "== rollback: hooks only when asked"
api -X POST "$SVC/rollback" -d '{"revision":1}' >/dev/null
wait_for "rollback succeeded" latest_is succeeded
[ "$(latest "['trigger']")" = rollback ] && [ "$(latest "['hooks']")" = "[]" ] || fail "rollback without hooks: $(latest "")"
api -X POST "$SVC/rollback" -d '{"revision":2,"runHooks":true}' >/dev/null
wait_for "rollback with hooks succeeded" latest_is succeeded
[ "$(latest "['hooks'][0]['status']")" = succeeded ] || fail "rollback with runHooks ran no hook"
echo "  ✓ rollback skips pre-deploy jobs by default and runs them with runHooks"

echo "== cancel while the pre-deploy job runs"
api -X PUT "$JOBS/migrate" -d '{"kind":"pre-deploy","service":"api","command":["sh","-c","sleep 60"]}' >/dev/null
rev=$(api "$SVC" | json "d['revision']")
api -X PUT "$SVC" -d "$(spec '' 3)" >/dev/null
wait_for "deployment waits on its hook" latest_is waiting_hook
DEP=$(latest "['id']")
wait_for "hook run started" sh -c "docker exec $E2E-ctl curl -fs -b /tmp/jar $SVC/deployments/$DEP | grep -q '\"runs\":\[{[^]]*\"status\":\"running\"'"
api -X POST "$SVC/deployments/$DEP/cancel" >/dev/null
[ "$(api "$SVC/deployments/$DEP" | json "d['status']")" = cancelled ] || fail "deployment not cancelled"
run_status() { [ "$(api "$SVC/deployments/$DEP" | json "d['runs'][0]['status']")" = "$1" ]; }
wait_for "hook run cancelled" run_status cancelled
[ "$(api "$SVC" | json "d['revision']")" = "$rev" ] || fail "revision changed although the deployment was cancelled"
api -X DELETE "$JOBS/migrate" >/dev/null
echo "  ✓ cancelled before the hook finished: its run stopped and revision $rev kept running"

echo "== cancel a rollout"
before=$(api "$SVC" | json "d['spec']['env']['V']")
api -X PUT "$SVC" -d "$(spec 'sleep 40; ' 4)" >/dev/null
wait_for "slow rollout in progress" latest_is in_progress
DEP=$(latest "['id']")
code=$(x "$E2E-ctl" curl -s -o /dev/null -w '%{http_code}' -b /tmp/jar -H 'Origin: http://localhost:7070' -X POST "$SVC/deployments/$DEP/cancel")
[ "$code" = 200 ] || fail "cancel answered $code"
[ "$(api "$SVC/deployments/$DEP" | json "d['status']")" = cancelled ] || fail "rollout not cancelled"
wait_for "rollback after cancel succeeded" latest_is succeeded
[ "$(latest "['trigger']")" = rollback ] || fail "cancel did not roll back"
[ "$(api "$SVC" | json "d['spec']['env']['V']")" = "$before" ] || fail "not back on the spec before the cancelled one"
code=$(x "$E2E-ctl" curl -s -o /dev/null -w '%{http_code}' -b /tmp/jar -H 'Origin: http://localhost:7070' -X POST "$SVC/deployments/$DEP/cancel")
[ "$code" = 400 ] || fail "cancelling a finished deployment answered $code"
echo "  ✓ cancelled a rolling deployment; the previous spec rolled out again"

echo "== project listing"
n=$(api "$P/deployments?environment=production&status=cancelled" | json "len(d['items'])")
[ "$n" = 2 ] || fail "project listing found $n cancelled deployments"
[ "$(api "$P/deployments?service=api&limit=2" | json "d['items'][0]['service'] + ' ' + str(bool(d['next']))")" = "api True" ] || fail "project listing page"
echo "  ✓ project deployments filter by status and page"

echo "== synctl"
key=$(api localhost:7070/api/v1/iam/access-keys -d '{"description":"e2e"}')
KID=$(echo "$key" | json "d['id']"); KSEC=$(echo "$key" | json "d['secretAccessKey']")
synctl() { x -i -e SYNCLOUD_ENDPOINT=http://127.0.0.1:7070 -e SYNCLOUD_ACCESS_KEY_ID="$KID" -e SYNCLOUD_SECRET_ACCESS_KEY="$KSEC" "$E2E-ctl" /opt/sc/synctl "$@" </dev/null; }
synctl services deployments api -p shop | grep -q "rollback" || fail "synctl services deployments"
synctl services deployment api "$DEP" -p shop | grep -q "Timeline:" || fail "synctl services deployment"
synctl projects deployments shop --status cancelled | grep -c cancelled | grep -qx 2 || fail "synctl projects deployments"
synctl services redeploy api -p shop --skip-hooks | grep -q "revision" || fail "synctl services redeploy"
synctl projects update shop --rollback-window 5 | grep -q "rollback window: 5" || fail "synctl projects update"
[ "$(api localhost:7070/api/v1/projects | json "d['items'][0]['rollbackWindow']")" = 5 ] || fail "rollback window not saved"
echo "  ✓ synctl deployments, deployment, projects deployments, redeploy, projects update"
echo "== hooks run one at a time, in order"
api -X PUT "$JOBS/second" -d '{"kind":"pre-deploy","service":"api","entrypoint":["sh","-c"],"command":["sleep 2; echo second"],"order":2}' >/dev/null
api -X PUT "$JOBS/first" -d '{"kind":"pre-deploy","service":"api","entrypoint":["sh","-c"],"command":["sleep 2; echo first"],"order":1}' >/dev/null
api -X PUT "$SVC" -d "$(spec '' 5)" >/dev/null
wait_for "ordered hooks passed" latest_is succeeded
DEP=$(latest "['id']")
order=$(api "$SVC/deployments/$DEP" | json "' '.join(r['job'] + ':' + r['status'] for r in d['runs'])")
[ "$order" = "first:succeeded second:succeeded" ] || fail "hook order: $order"
api "$SVC/deployments/$DEP" | json "[(r['startedAt'], r['finishedAt']) for r in d['runs']]" | python3 -c "
import sys, ast
(s1, f1), (s2, f2) = ast.literal_eval(sys.stdin.read())
sys.exit(0 if s2 >= f1 else 1)" || fail "the second hook started before the first finished"
echo "  ✓ pre-deploy jobs ran in their order, the second after the first finished"
api -X PUT "$JOBS/first" -d '{"kind":"pre-deploy","service":"api","entrypoint":["sh","-c"],"command":["echo broken; exit 5"],"order":1}' >/dev/null
rev=$(api "$SVC" | json "d['revision']")
api -X PUT "$SVC" -d "$(spec '' 6)" >/dev/null
wait_for "failing hook stopped the deployment" latest_is failed
DEP=$(latest "['id']")
[ "$(api "$SVC/deployments/$DEP" | json "' '.join(r['job'] for r in d['runs'])")" = first ] || fail "a job after the failing one ran"
[ "$(api "$SVC" | json "d['revision']")" = "$rev" ] || fail "revision switched after a failing hook"
api -X DELETE "$JOBS/first" >/dev/null; api -X DELETE "$JOBS/second" >/dev/null
echo "  ✓ the first job failing stopped the deployment before the second ran; revision $rev kept running"

echo "== release commands on a new service"
WEB=$P/environments/production/services/web
web_spec() { # release command
  echo "{\"image\":\"busybox:1.37\",\"command\":[\"sh\",\"-c\",\"exec httpd -f -p 8080 -h /tmp\"],\"ports\":[{\"container\":8080}],\"resources\":{\"cpu\":0.05,\"memory\":16},\"desiredCount\":1,\"releaseCommands\":{\"preDeploy\":[{\"command\":\"$1\"}],\"postDeploy\":[{\"command\":\"echo deployed\"}]}}"
}
api -X PUT "$WEB" -d "$(web_spec 'sleep 8; echo migrated')" >/dev/null
sleep 3
[ "$(api "$WEB" | json "d['running'] + d['pending']")" = 0 ] || fail "tasks started before the release command passed"
api "$JOBS" | json "sorted(j['name'] for j in d['items'] if j['spec'].get('service') == 'web')" | grep -q "'web-post-1', 'web-pre-1'" || fail "release command jobs: $(api "$JOBS")"
wait_for "web runs after its release command" sh -c "[ \"\$(docker exec $E2E-ctl curl -fs -b /tmp/jar $WEB | python3 -c 'import json,sys; print(json.load(sys.stdin)[\"running\"])')\" = 1 ]"
[ "$(api "$WEB/deployments?limit=1" | json "d['items'][0]['status']")" = succeeded ] || fail "first web deployment"
api -X PUT "$P/environments/production/services/bad" -d "$(web_spec 'exit 3')" >/dev/null
BAD=$P/environments/production/services/bad
wait_for "bad's release command failed" sh -c "docker exec $E2E-ctl curl -fs -b /tmp/jar '$BAD/deployments?limit=1' | grep -q '\"status\":\"failed\"'"
sleep 3
[ "$(api "$BAD" | json "d['running'] + d['pending']")" = 0 ] || fail "a service whose first release command failed started tasks"
api "$BAD" | json "d['status']" | grep -q "did not pass" || fail "status of a held service: $(api "$BAD" | json "d['status']")"
api -X PUT "$JOBS/bad-pre-1" -d '{"kind":"pre-deploy","service":"bad","entrypoint":["sh","-c"],"command":["echo fixed"]}' >/dev/null
api -X POST "$BAD/redeploy" -d '{}' >/dev/null
wait_for "bad runs after the fix" sh -c "[ \"\$(docker exec $E2E-ctl curl -fs -b /tmp/jar $BAD | python3 -c 'import json,sys; print(json.load(sys.stdin)[\"running\"])')\" = 1 ]"
code=$(x "$E2E-ctl" curl -s -o /dev/null -w '%{http_code}' -b /tmp/jar -H 'content-type: application/json' -H 'Origin: http://localhost:7070' -X PUT "$WEB" -d "$(web_spec 'echo again')")
[ "$code" = 400 ] || fail "release commands on an existing service answered $code"
echo "  ✓ a new service ran no task until its release command passed; a failing one stayed stopped until fixed and redeployed"
echo "PASS"
