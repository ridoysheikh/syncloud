#!/usr/bin/env bash
# Failure proofing: every kind of long-running container is broken for real
# (stopped behind SynCloud's back, its node rebooted, its agent away), and
# after each failure two things must hold: it runs again without help, and no
# alert is left active. Services, system components, managed Valkey with
# Sentinel, managed PostgreSQL and its platform etcd, deployments and jobs.
#
# A failed check is recorded and the test goes on, so one run shows them all.
#
#   test/e2e/recovery.sh          # run and clean up
#   KEEP=1 test/e2e/recovery.sh   # leave the nodes running for inspection
WITH_REGISTRY=1
WITH_TRAEFIK=1
WITH_METRICS=1
WITH_POSTGRES=1
CTL_FLAGS="--system-tasks=true"
. "$(dirname "$0")/lib.sh"
trap cleanup EXIT
setup_cluster
for t in registry traefik vlogs vmetrics; do x $E2E-ctl docker load -q -i /opt/sc/$t.tar >/dev/null; done
docker image inspect valkey/valkey:8.1-alpine >/dev/null 2>&1 || docker pull -q valkey/valkey:8.1-alpine >/dev/null
docker save valkey/valkey:8.1-alpine -o "$BIN/valkey.tar"
for n in "${NODES[@]}"; do x "$n" docker load -q -i /opt/sc/valkey.tar >/dev/null; done
wait_mesh

P=localhost:7070/api/v1
ENVP=$P/projects/shop/environments/production
SVC=$ENVP/services
A=$P/alerts
FAILED=()
START=$(date +%s)
py() { python3 -c "import json,sys; d=json.load(sys.stdin); $1"; }
ok() { echo "  ✓ $*"; }
bad() { echo "  ✗ $*"; FAILED+=("$*"); }
# until_true SECONDS CONDITION: poll a shell condition.
until_true() {
  local n=$(($1 / 3)); shift
  for _ in $(seq 1 $n); do eval "$@" >/dev/null 2>&1 && return 0; sleep 3; done
  return 1
}
expect() { # description, seconds, condition
  local d=$1 s=$2; shift 2
  if until_true "$s" "$@"; then ok "$d"; else bad "$d"; fi
}
# The default disk alerts are about this machine's disk, which the nodes
# share: they say nothing about the test.
active() { api $A/active | py 'print("\n".join(sorted("%s | %s | %s | %s" % (a["rule"], a["label"], a["state"], a["message"][:160]) for a in d["items"] if "disk" not in a["rule"])))'; }
# quiet CONTEXT: within 2 minutes no alert is active.
quiet() {
  if until_true 120 '[ -z "$(active)" ]'; then ok "no alert left active ($1)"; else
    bad "alerts left active ($1)"; active | sed 's/^/      /'
  fi
}
# fired RULE LABEL: the rule has notified about the instance.
fired() { api "$A/events" | py "assert any(e['rule']=='$1' and '$2' in e['label'] and e['kind'] in ('firing','event') for e in d['items'])"; }

# ── containers on the nodes ────────────────────────────────────────────────
node_of() { # container name -> the node container that has it
  local n
  for n in "${NODES[@]}"; do
    [ -n "$(x "$n" docker ps -aq --filter "name=^$1\$" 2>/dev/null)" ] && { echo "$n"; return 0; }
  done
  return 1
}
is_running() { local n; n=$(node_of "$1") && [ "$(x "$n" docker inspect -f '{{.State.Running}}' "$1" 2>/dev/null)" = true ]; }
# stop_ct NAME: "docker stop" behind SynCloud's back. Docker's restart policy
# leaves such a container exited for good: only SynCloud can bring it back.
stop_ct() {
  local n; n=$(node_of "$1") || { bad "no container $1 to stop"; return 0; }
  x "$n" docker stop -t 2 "$1" >/dev/null
  echo "  … stopped $1 on $n"
}
comes_back() { expect "$1 runs again" "${2:-150}" "is_running $1"; }
svc_running() { api "$SVC/$1" | py 'print(d["running"])'; }
svc_ok() { expect "service $1 has $2 running tasks" "${3:-240}" "[ \"\$(svc_running $1)\" = $2 ]"; }
db_health() { api "$P/databases/$1" | py 'print(d["health"])'; }
db_ok() { expect "database $1 is healthy" "${2:-300}" "[ \"\$(db_health $1)\" = healthy ]"; }
etcd_names() { for n in "${NODES[@]}"; do x "$n" docker ps -a --format '{{.Names}}' --filter name=^syncloud-etcd-; done | sort; }
pgq() { api "$P/databases/ledger/pg/query" -d "$(python3 -c 'import json,sys; print(json.dumps({"sql": sys.argv[1]}))' "$1")" | py 'print(d["results"][-1]["rows"])'; }

echo "== alert rules, without delay"
api $P/projects -d '{"name":"shop"}' >/dev/null
rule() { api "$A/rules" -d "$1" | grep -q '"id":"alr_' || fail "rule not created: $1"; }
rule '{"name":"t-health","type":"health","forSeconds":0}'
rule '{"name":"t-database","type":"database_health","forSeconds":0}'
rule '{"name":"t-component","type":"system_component","forSeconds":0}'
rule '{"name":"t-node","type":"node","forSeconds":0}'
rule '{"name":"t-deploy","type":"deployment"}'
rule '{"name":"t-job","type":"job"}'
expect "the system components are running" 180 '[ "$(api $P/system/tasks | grep -o "\"state\":\"running\"" | wc -l)" -ge 4 ]'

echo "== a first deployment while its dependency is down, which then comes back"
BB='"image":"busybox:1.37","resources":{"cpu":0.05,"memory":16}'
DEP="{$BB,\"command\":[\"httpd\",\"-f\",\"-p\",\"8080\",\"-h\",\"/etc\"],\"ports\":[{\"container\":8080}],\"desiredCount\":1}"
api -X PUT "$SVC/dep" -d "${DEP/\"desiredCount\":1/\"desiredCount\":0}" >/dev/null
api -X PUT "$SVC/app" -d "{$BB,\"command\":[\"sh\",\"-c\",\"wget -q -T 3 -O /dev/null http://dep.production.shop.syncloud.internal:8080/hostname || { sleep 2; exit 1; }; exec httpd -f -p 8080 -h /etc\"],\"ports\":[{\"container\":8080}],\"health\":{\"type\":\"http\",\"path\":\"/hostname\",\"interval\":2,\"retries\":3,\"startPeriod\":10},\"desiredCount\":2}" >/dev/null
expect "the failed deployment raised an alert" 240 'fired t-deploy shop/production/app'
expect "the service health alert fired" 120 'fired t-health shop/production/app'
api -X POST "$SVC/dep/scale" -d '{"desiredCount":1}' >/dev/null
svc_ok dep 1
svc_ok app 2 360
quiet "the app recovered on its own"

echo "== service tasks stopped and killed"
for how in stop kill; do
  n=; c=
  for node in "${NODES[@]}"; do
    c=$(x "$node" docker ps --format '{{.Names}}' --filter name=^shop-production-app- | head -1)
    [ -n "$c" ] && { n=$node; break; }
  done
  x "$n" docker $how "$c" >/dev/null
  echo "  … docker $how $c on $n"
  expect "the $how-ed task was replaced" 150 "[ \"\$(svc_running app)\" = 2 ] && ! x $n docker ps --format '{{.Names}}' | grep -qx $c"
done
quiet "service tasks"

echo "== a failed job, then a successful run"
api -X PUT "$ENVP/jobs/report" -d '{"service":"dep","command":["sh","-c","exit 2"]}' >/dev/null
api -X POST "$ENVP/jobs/report/runs" >/dev/null
expect "the failed job raised an alert" 120 'fired t-job report'
api -X PUT "$ENVP/jobs/report" -d '{"service":"dep","command":["sh","-c","true"]}' >/dev/null
api -X POST "$ENVP/jobs/report/runs" >/dev/null
quiet "the job succeeded again"

echo "== system components stopped"
for c in syncloud-traefik syncloud-registry syncloud-victorialogs syncloud-victoriametrics; do stop_ct $c; done
for c in syncloud-traefik syncloud-registry syncloud-victorialogs syncloud-victoriametrics; do comes_back $c; done
quiet "system components"

echo "== managed Valkey: replica, Sentinel and primary stopped"
api $P/databases -d '{"name":"kv","spec":{"memory":{"min":64,"max":64},"replicas":{"min":1,"max":1}}}' >/dev/null
db_ok kv
api -X POST "$P/databases/kv/command" -d '{"command":"SET proof kept"}' >/dev/null 2>&1 || true
for c in db-kv-m1 db-kv-s0; do
  stop_ct $c; comes_back $c
done
db_ok kv
stop_ct db-kv-m0
comes_back db-kv-m0
db_ok kv
echo "  … all of Valkey stopped at once"
KV="db-kv-m0 db-kv-m1 db-kv-s0 db-kv-s1 db-kv-s2"
for c in $KV; do stop_ct $c; done
for c in $KV; do comes_back $c; done
db_ok kv
quiet "Valkey"

echo "== managed PostgreSQL and the platform etcd"
api $P/databases -d '{"name":"ledger","engine":"postgres","spec":{"memory":{"min":256},"replicas":{"min":1,"max":1}}}' >/dev/null
db_ok ledger 480
expect "etcd has 3 members" 480 '[ "$(etcd_names | wc -l)" = 3 ]'
db_ok ledger
api "$P/databases/ledger/pg/execute" -d '{"sql":"CREATE TABLE proof (id int); INSERT INTO proof VALUES (1), (2)"}' >/dev/null
ETCD=$(etcd_names | tr '\n' ' ')
echo "  … every etcd member stopped at once: $ETCD"
for c in $ETCD; do stop_ct $c; done
for c in $ETCD; do comes_back $c; done
db_ok ledger
for c in db-ledger-m0 db-ledger-m1; do stop_ct $c; comes_back $c; db_ok ledger; done
echo "  … both PostgreSQL members and etcd stopped at once"
for c in $ETCD db-ledger-m0 db-ledger-m1; do stop_ct $c; done
for c in $ETCD db-ledger-m0 db-ledger-m1; do comes_back $c 240; done
db_ok ledger
expect "PostgreSQL kept its data" 90 '[ "$(pgq "SELECT count(*) FROM proof")" = "[['"'"'2'"'"']]" ]'
echo "  … a new database after etcd was down"
for c in $ETCD; do stop_ct $c; done
api $P/databases -d '{"name":"later","engine":"postgres","spec":{"memory":{"min":256},"replicas":{"min":0,"max":0}}}' >/dev/null
db_ok later 480
quiet "PostgreSQL and etcd"
api -X DELETE "$P/databases/later" >/dev/null

echo "== containers stopped while the node's agent is away"
x $E2E-w2 sh -c 'kill $(pidof syncloud-agent)'
VICTIMS=$(x $E2E-w2 docker ps --format '{{.Names}}' | tr '\n' ' ')
echo "  … on w2: $VICTIMS"
for c in $VICTIMS; do x $E2E-w2 docker stop -t 2 "$c" >/dev/null; done
expect "the node alert fired" 150 'fired t-node w2'
x -d -e SYNCLOUD_WIREGUARD_MODE=userspace $E2E-w2 sh -c "/opt/sc/syncloud-agent run --data-dir /agent --network on >> /var/log/agent.log 2>&1"
for c in $VICTIMS; do
  case $c in shop-*) ;; *) comes_back "$c" 240 ;; esac # service tasks are replaced elsewhere
done
svc_ok app 2; svc_ok dep 1; db_ok kv; db_ok ledger
quiet "the agent came back"

echo "== a node reboots"
docker restart -t 5 $E2E-w1 >/dev/null
until_true 90 "x $E2E-w1 docker info" || bad "docker on w1 after the reboot"
x -d $E2E-w1 sh -c "/opt/sc/syncloud-agent run --data-dir /agent --network on >> /var/log/agent.log 2>&1"
svc_ok app 2; svc_ok dep 1; db_ok kv 420; db_ok ledger 420
quiet "after the reboot"

echo
echo "took $((($(date +%s) - START) / 60)) minutes after setup"
if [ ${#FAILED[@]} -gt 0 ]; then
  printf 'FAILED CHECKS (%d):\n' ${#FAILED[@]}; printf '  - %s\n' "${FAILED[@]}"
  echo "--- controller log (warnings and errors)"
  x $E2E-ctl sh -c 'grep -E "level=(WARN|ERROR)" /var/log/controller.log | tail -40' | cut -c1-300
  exit 1
fi
echo "PASS"
