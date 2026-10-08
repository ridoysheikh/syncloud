#!/usr/bin/env bash
# End-to-end test of project settings (Phase 15d): the deploy lock and the
# auto-deploy switch per environment, cloning an environment (services,
# shared variables, jobs, security groups), and deleting an environment and
# a project with everything in them; and the synctl commands.
#
#   test/e2e/settings.sh                          # run and clean up
#   E2E_PREFIX=sc-set KEEP=1 test/e2e/settings.sh  # beside a kept cluster
WORKERS=${WORKERS-w1}
. "$(dirname "$0")/lib.sh"
trap cleanup EXIT
setup_cluster
wait_mesh

P=localhost:7070/api/v1/projects/shop
PROD=$P/environments/production
json() { python3 -c "import json,sys; d=json.load(sys.stdin); print($1)"; }
# code METHOD URL [BODY]: the HTTP status of a request.
code() { x "$E2E-ctl" curl -s -o /tmp/body -w '%{http_code}' -b /tmp/jar -H 'content-type: application/json' -H 'Origin: http://localhost:7070' -X "$1" "$2" ${3:+-d "$3"}; }
body() { x "$E2E-ctl" cat /tmp/body; }
wait_for() { # description, condition
  local d=$1; shift
  for _ in $(seq 1 60); do "$@" && return 0; sleep 2; done
  fail "timed out: $d"
}
spec() { echo "{\"image\":\"busybox:1.37\",\"command\":[\"sh\",\"-c\",\"exec httpd -f -p 8080 -h /tmp\"],\"ports\":[{\"container\":8080}],\"env\":{\"V\":\"$1\"},\"resources\":{\"cpu\":0.05,\"memory\":16},\"desiredCount\":1}"; }
running() { [ "$(api "$1" | json "d['running']")" = "$2" ]; }

api localhost:7070/api/v1/projects -d '{"name":"shop"}' >/dev/null
api -X PUT "$PROD/variables" -d '{"variables":{"REGION":"eu"}}' >/dev/null
api -X PUT "$PROD/services/web" -d "$(spec 1)" >/dev/null
api -X PUT "$PROD/services/web" -d "$(spec 2)" >/dev/null
api -X PUT "$PROD/jobs/nightly" -d '{"service":"web","command":["echo","report"],"schedule":"0 3 * * *"}' >/dev/null
api "$P/security-groups" -d '{"name":"web-in","inbound":[{"protocol":"tcp","ports":"8080","peers":["any"]}],"services":["production/web"]}' >/dev/null
wait_for "web runs" running "$PROD/services/web" 1

echo "== deploy lock"
api -X PUT "$PROD/policy" -d '{"locked":true,"reason":"x"}' >/dev/null 2>&1 && fail "a lock without a real reason was accepted"
env=$(api -X PUT "$PROD/policy" -d '{"locked":true,"reason":"release freeze"}')
[ "$(echo "$env" | json "d['lock']['reason'] + ' ' + d['lock']['by']")" = "release freeze e2e@example.com" ] || fail "lock: $env"
[ "$(code PUT "$PROD/services/web" "$(spec 3)")" = 423 ] || fail "a deploy to a locked environment answered $(body)"
body | grep -q '"code":"locked".*release freeze' || fail "lock error: $(body)"
[ "$(code POST "$PROD/services/web/redeploy" '{}')" = 423 ] || fail "redeploy while locked"
[ "$(code PUT "$PROD/variables" '{"variables":{"REGION":"us"}}')" = 423 ] || fail "shared variables while locked"
[ "$(code POST "$PROD/services/web/rollback" '{"revision":1}')" = 200 ] || fail "rollback while locked: $(body)"
[ "$(code POST "$PROD/services/web/scale" '{"desiredCount":2}')" = 200 ] || fail "scale while locked: $(body)"
api -X PUT "$PROD/policy" -d '{"locked":false,"autoDeploy":false}' | json "str(d['lock']) + ' ' + str(d['autoDeploy'])" | grep -qx "None False" || fail "unlock"
[ "$(code PUT "$PROD/services/web" "$(spec 4)")" = 200 ] || fail "deploy after unlock: $(body)"
echo "  ✓ a locked environment refuses deploys, redeploys and variable changes (423 with the reason) but rolls back and scales"

echo "== clone"
out=$(api "$P/environments" -d '{"name":"staging","cloneFrom":"production"}')
[ "$(echo "$out" | json "' '.join(d['services']) + '|' + ' '.join(d['jobs'])")" = "web|nightly" ] || fail "clone: $out"
STG=$P/environments/staging
[ "$(api "$STG/services/web" | json "str(d['desiredCount']) + ' ' + d['spec']['env']['V'] + ' ' + d['spec']['sharedEnv']['REGION']")" = "0 4 eu" ] || fail "cloned service: $(api "$STG/services/web")"
api "$P/security-groups" | json "[g['services'] for g in d['items'] if g['name'] == 'web-in'][0]" | grep -q "staging/web" || fail "the copy did not join the security group"
api "$P/environments/staging" >/dev/null 2>&1 || true
[ "$(api "$P/environments" | json "[str(e['autoDeploy']) for e in d['items'] if e['name'] == 'staging'][0]")" = True ] || fail "the copy inherited the policy"
api "$P/environments" -d '{"name":"qa","cloneFrom":"production","startServices":true}' >/dev/null
wait_for "qa's copy runs" running "$P/environments/qa/services/web" 1
echo "  ✓ staging copies production's services (stopped), variables, jobs and security groups; qa's copy started"

echo "== delete everything"
[ "$(code DELETE "$STG")" = 409 ] || fail "deleted an environment with services: $(body)"
[ "$(code DELETE "$STG?force=true")" = 202 ] || fail "force delete: $(body)"
wait_for "staging gone" sh -c "! docker exec $E2E-ctl curl -fs -b /tmp/jar $P/environments | grep -q '\"name\":\"staging\"'"
[ "$(code DELETE "$P")" = 409 ] || fail "deleted a project with services"
[ "$(code DELETE "$P?force=true")" = 202 ] || fail "project force delete: $(body)"
wait_for "project gone" sh -c "[ \"\$(docker exec $E2E-ctl curl -s -o /dev/null -w '%{http_code}' -b /tmp/jar localhost:7070/api/v1/projects/shop)\" = 404 ]"
# Retired tasks drain for a few seconds after the controller lets go of them.
wait_for "the project's containers stop" sh -c "! docker exec $E2E-ctl docker ps --format '{{.Names}}' | grep -q '^shop-' && ! docker exec $E2E-w1 docker ps --format '{{.Names}}' | grep -q '^shop-'"
echo "  ✓ an environment and then the whole project were deleted with their services; no container is left"

echo "== synctl"
key=$(api localhost:7070/api/v1/iam/access-keys -d '{"description":"e2e"}')
KID=$(echo "$key" | json "d['id']"); KSEC=$(echo "$key" | json "d['secretAccessKey']")
synctl() { x -i -e SYNCLOUD_ENDPOINT=http://127.0.0.1:7070 -e SYNCLOUD_ACCESS_KEY_ID="$KID" -e SYNCLOUD_SECRET_ACCESS_KEY="$KSEC" "$E2E-ctl" /opt/sc/synctl "$@" </dev/null; }
synctl projects create app >/dev/null
api -X PUT localhost:7070/api/v1/projects/app/environments/production/services/api -d "$(spec 1)" >/dev/null
synctl envs lock production -p app --reason "audit" | grep -q "deploys locked: audit" || fail "synctl envs lock"
synctl envs list -p app | grep -q "audit" || fail "synctl envs list shows the lock"
synctl envs unlock production -p app | grep -q "deploys allowed" || fail "synctl envs unlock"
synctl envs auto-deploy production off -p app | grep -q "deploy themselves: false" || fail "synctl envs auto-deploy"
synctl envs create staging --from production -p app | grep -q "1 services (at 0 tasks)" || fail "synctl envs create --from"
synctl envs delete staging -p app --everything | grep -q "Deleting environment" || fail "synctl envs delete --everything"
synctl projects delete app --everything | grep -q "Deleting project app" || fail "synctl projects delete --everything"
echo "  ✓ synctl envs lock/unlock/auto-deploy/create --from/delete --everything, projects delete --everything"
echo "PASS"
