#!/usr/bin/env bash
# End-to-end test of alerts (§9) on the real system tasks: channels (a test
# notification), a metric rule on the 5xx rate that fires and resolves, a
# log rule, a service health rule, a failed deployment and a node that
# stops reporting. Notifications go to a webhook sink, as JSON and in
# Slack's format.
. "$(dirname "$0")/lib.sh"
WITH_REGISTRY=1
WITH_TRAEFIK=1
WITH_METRICS=1
WITH_ALERTS=1
CTL_FLAGS="--system-tasks=true"
trap cleanup EXIT
setup_cluster
for t in registry traefik vlogs vmetrics; do x sc-e2e-ctl docker load -q -i /opt/sc/$t.tar >/dev/null; done
wait_mesh
for _ in $(seq 1 60); do
  [ "$(api localhost:7070/api/v1/system/tasks | grep -o '"state":"running"' | wc -l)" -ge 4 ] && break; sleep 2
done
x -d sc-e2e-ctl sh -c "/opt/sc/hooksink -listen 127.0.0.1:9999 -out /tmp/hooks.log"
echo "  ✓ system tasks running; webhook sink on 127.0.0.1:9999"
# hooks PATTERN: wait until the sink received a line matching PATTERN.
hooks() {
  for _ in $(seq 1 ${2:-60}); do x sc-e2e-ctl grep -q "$1" /tmp/hooks.log 2>/dev/null && return; sleep 3; done
  x sc-e2e-ctl cat /tmp/hooks.log; api localhost:7070/api/v1/alerts/active; fail "no notification matching $1"
}
A=localhost:7070/api/v1/alerts

echo "== channels"
api "$A/channels" -d '{"name":"x","type":"slack","config":{"url":"ftp://nope"}}' >/dev/null 2>&1 && fail "a bad URL was accepted"
HOOK=$(api "$A/channels" -d '{"name":"hook","type":"webhook","config":{"url":"http://127.0.0.1:9999/hook"}}' | grep -o '"id":"ach_[a-z0-9]*"' | cut -d'"' -f4)
SLACK=$(api "$A/channels" -d '{"name":"slack","type":"slack","config":{"url":"http://127.0.0.1:9999/slack/T000/B000/secretpart"}}' | grep -o '"id":"ach_[a-z0-9]*"' | cut -d'"' -f4)
api "$A/channels" | grep -q 'secretpart' && fail "a channel secret is returned by the API"
api -X POST "$A/channels/$SLACK/test" | grep -q '"delivered":true' || fail "test notification"
hooks '^/slack/T000/B000/secretpart {"text":"\[TEST\] Test notification: slack' 5
echo "  ✓ webhook and Slack channels (secrets not returned), test notification delivered"

echo "== rules"
SVC=localhost:7070/api/v1/projects/shop/environments/production/services
api localhost:7070/api/v1/projects -d '{"name":"shop"}' >/dev/null
api -X PUT "$SVC/web" -d '{"image":"busybox:1.37","command":["httpd","-f","-p","8080","-h","/etc"],"ports":[{"container":8080}],"resources":{"cpu":0.05,"memory":16}}' >/dev/null
api -X PUT "$SVC/flaky" -d '{"image":"busybox:1.37","command":["sh","-c","while true; do printf \"HTTP/1.1 503 Service Unavailable\\r\\nContent-Length: 0\\r\\nConnection: close\\r\\n\\r\\n\" | nc -l -p 8080; done"],"ports":[{"container":8080}],"resources":{"cpu":0.05,"memory":16}}' >/dev/null
api -X PUT "$SVC/logger" -d '{"image":"busybox:1.37","command":["sh","-c","while true; do echo PANIC: disk on fire; sleep 2; done"],"resources":{"cpu":0.05,"memory":16}}' >/dev/null
api "$A/rules" -d '{"name":"bad","type":"metric","metric":"qps"}' >/dev/null 2>&1 && fail "an invalid rule was accepted"
rule() { api "$A/rules" -d "$1" | grep -q '"id":"alr_' || fail "rule not created: $1"; }
rule "{\"name\":\"flaky 5xx\",\"type\":\"metric\",\"metric\":\"error_rate\",\"op\":\">\",\"threshold\":50,\"windowSeconds\":60,\"project\":\"shop\",\"service\":\"flaky\",\"severity\":\"critical\",\"channels\":[\"$HOOK\",\"$SLACK\"]}"
rule "{\"name\":\"panics\",\"type\":\"log\",\"text\":\"panic\",\"op\":\">\",\"threshold\":3,\"windowSeconds\":60,\"project\":\"shop\",\"channels\":[\"$HOOK\"]}"
rule "{\"name\":\"service health\",\"type\":\"health\",\"forSeconds\":0,\"channels\":[\"$HOOK\"]}"
rule "{\"name\":\"failed deploys\",\"type\":\"deployment\",\"channels\":[\"$HOOK\"]}"
rule "{\"name\":\"nodes\",\"type\":\"node\",\"severity\":\"critical\",\"channels\":[\"$HOOK\"]}"
echo "  ✓ rules validated and created"

echo "== metric rule fires"
for _ in $(seq 1 60); do [ "$(x sc-e2e-ctl curl -s -o /dev/null -w '%{http_code}' -H 'Host: flaky-production-shop.localhost' http://127.0.0.1:8080/)" = 503 ] && break; sleep 2; done
x -d sc-e2e-ctl sh -c 'touch /tmp/load; while [ -f /tmp/load ]; do curl -s -o /dev/null -H "Host: flaky-production-shop.localhost" http://127.0.0.1:8080/boom; sleep 0.3; done'
hooks '^/hook {"status":"firing","rule":"flaky 5xx","severity":"critical","instance":"shop/production/flaky","message":"error_rate 100% > 50%'
hooks '^/slack/T000/B000/secretpart {"text":"\[FIRING\] flaky 5xx: shop/production/flaky (critical)' 5
api "$A/active" | grep -q '"rule":"flaky 5xx"[^}]*\|"state":"firing"' || fail "not active"
echo "  ✓ 5xx rate above 50% fired to the webhook (JSON) and Slack (text)"

echo "== log rule"
hooks '^/hook {"status":"firing","rule":"panics","severity":"warning","instance":"shop/production/logger","message":"[0-9]* lines containing \\"panic\\" in 1m0s'
echo "  ✓ more than 3 lines containing \"panic\" in a minute fired (case-insensitive)"

echo "== failed deployment"
# A health check that never passes trips the circuit breaker, like deploy.sh.
api -X PUT "$SVC/web" -d '{"image":"busybox:1.37","command":["httpd","-f","-p","8080","-h","/etc"],"ports":[{"container":8080}],"resources":{"cpu":0.05,"memory":16},"health":{"type":"http","path":"/missing","interval":2,"retries":2,"startPeriod":1}}' >/dev/null
hooks '^/hook {"status":"event","rule":"failed deploys","severity":"warning","instance":"shop/production/web","message":"deployment dep_' 100
echo "  ✓ a rolled-back deployment notified once"

echo "== health and nodes"
api -X PUT "$SVC/crasher" -d '{"image":"busybox:1.37","command":["sh","-c","sleep 3; exit 1"],"resources":{"cpu":0.05,"memory":16}}' >/dev/null
hooks '^/hook {"status":"firing","rule":"service health","severity":"warning","instance":"shop/production/crasher"' 80
echo "  ✓ a crash-looping service fired the health rule"
x sc-e2e-w2 sh -c 'kill $(pidof syncloud-agent)'
hooks '^/hook {"status":"firing","rule":"nodes","severity":"critical","instance":"node w2","message":"not reporting since' 60
x -d -e SYNCLOUD_WIREGUARD_MODE=userspace sc-e2e-w2 sh -c "/opt/sc/syncloud-agent run --data-dir /agent --network on >> /var/log/agent.log 2>&1"
hooks '^/hook {"status":"resolved","rule":"nodes","severity":"critical","instance":"node w2"' 40
echo "  ✓ a node that stopped reporting fired, and resolved when its agent came back"

echo "== resolve"
# Health probes keep hitting the route, so fix the service rather than
# just stopping the load.
api -X PUT "$SVC/flaky" -d '{"image":"busybox:1.37","command":["httpd","-f","-p","8080","-h","/etc"],"ports":[{"container":8080}],"resources":{"cpu":0.05,"memory":16}}' >/dev/null
hooks '^/hook {"status":"resolved","rule":"flaky 5xx"' 80
x sc-e2e-ctl rm -f /tmp/load
api "$A/events" | grep -q '"kind":"resolved","key":"svc_[a-z0-9]*","label":"shop/production/flaky"[^}]*"delivery":"hook: ok; slack: ok"' || fail "delivery not recorded: $(api "$A/events" | head -c 800)"
echo "  ✓ once flaky answers 200 the 5xx alert resolved; deliveries are recorded per channel"

echo "== synctl"
key=$(api localhost:7070/api/v1/iam/access-keys -d '{"description":"e2e"}')
KID=$(echo "$key" | grep -o '"id":"SYNAK[A-Z0-9]*"' | cut -d'"' -f4); KSEC=$(echo "$key" | grep -o '"secretAccessKey":"[^"]*"' | cut -d'"' -f4)
synctl() { x -i -e SYNCLOUD_ENDPOINT=http://127.0.0.1:7070 -e SYNCLOUD_ACCESS_KEY_ID="$KID" -e SYNCLOUD_SECRET_ACCESS_KEY="$KSEC" sc-e2e-ctl /opt/sc/synctl "$@"; }
synctl alerts events </dev/null | grep -q 'RESOLVED.*flaky 5xx' || fail "synctl alerts events"
synctl alerts rules list </dev/null | grep -q 'error_rate > 50' || fail "synctl alerts rules list"
echo '{"name":"slow web","type":"metric","metric":"latency","threshold":500,"project":"shop","service":"web","channels":["slack"]}' | synctl alerts rules apply -f - | grep -q 'Created rule slow web' || fail "synctl rules apply"
synctl alerts channels delete slack </dev/null >/dev/null 2>&1 && fail "deleted a channel in use"
synctl alerts channels list </dev/null | grep -q 'slack *slack *127.0.0.1:9999' || fail "synctl channels list"
echo "  ✓ synctl alerts events, rules list/apply (channels by name), channels list; a channel in use is not deleted"
echo "PASS"
