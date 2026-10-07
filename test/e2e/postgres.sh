#!/usr/bin/env bash
# End-to-end test of managed PostgreSQL (Phase 13a): the platform etcd,
# a Patroni cluster with members on distinct nodes, the read-write and
# read-only endpoints from a task, failover when the primary freezes (the
# old primary rejoins as a replica), a switchover from the API, failover
# while the controller is down (through the multi-host HA URL), and
# deletion with volume cleanup.
#
#   test/e2e/postgres.sh          # run and clean up
#   KEEP=1 test/e2e/postgres.sh   # leave the nodes running for inspection
. "$(dirname "$0")/lib.sh"
WITH_POSTGRES=1
trap cleanup EXIT
setup_cluster
wait_mesh

P=localhost:7070/api/v1/projects/shop
ENV=$P/environments/production
DBS=localhost:7070/api/v1/databases
DB=$DBS/orders
py() { python3 -c "import json,sys; d=json.load(sys.stdin); $1"; }
wait_for() { # description, condition (shell)
  local d=$1; shift
  for _ in $(seq 1 150); do eval "$@" && return 0; sleep 2; done
  echo "--- database:"; api "$DB" | head -c 3000; echo
  fail "timed out: $d"
}
health() { api "$DB" | py 'print(d["health"])'; }
primary() { api "$DB" | py 'print(d["state"]["primary"])'; }
nodec() { [ "$1" = ctl-0 ] && echo sc-e2e-ctl || echo "sc-e2e-$1"; }
member() { # ordinal, field
  api "$DB" | py "print([m['$2'] for m in d['members'] if m['name']=='m$1'][0])"
}

echo "== create"
api localhost:7070/api/v1/projects -d '{"name":"shop"}' >/dev/null
api "$DBS" -d '{"name":"orders","engine":"postgres","project":"shop","environment":"production","spec":{"memory":{"min":512},"replicas":{"min":1,"max":1}}}' |
  py 'assert d["engine"]=="postgres" and d["port"]==5432 and d["host"]=="orders.production.shop.syncloud.internal", d'
wait_for "healthy: a leader and a streaming replica" '[ "$(health)" = healthy ]'
api "$DB" | py '
data=[m for m in d["members"] if m["kind"]=="data"]
assert len(data)==2 and len({m["node"] for m in data})==2, data
assert sorted(m["role"] for m in data)==["primary","replica"], data
print("  ✓ m0/m1 on", ", ".join(m["node"] for m in data))'
n=0; for c in sc-e2e-ctl sc-e2e-w1 sc-e2e-w2; do n=$((n + $(x $c docker ps -q --filter label=syncloud.service_id=etcd | wc -l))); done
[ "$n" = 3 ] || fail "expected 3 etcd members, found $n"
echo "  ✓ platform etcd: 3 members on distinct nodes"

eval "$(api "$DB/credentials" | py '
import shlex
print("URL="+shlex.quote(d["url"])); print("RO="+shlex.quote(d["readUrl"])); print("HA="+shlex.quote(d["haUrl"]))
assert d["username"]=="app" and d["database"]=="orders", d
assert d["url"].startswith("postgresql://app:") and d["url"].endswith("@orders.production.shop.syncloud.internal:5432/orders"), d["url"]
assert "target_session_attrs=read-write" in d["haUrl"] and "m0.orders" in d["haUrl"] and "m1.orders" in d["haUrl"], d["haUrl"]')"

echo "== endpoints from a task"
api -X PUT "$ENV/services/client" -d "{\"image\":\"$PG_IMAGE\",\"command\":[\"sleep\",\"3600\"],\"resources\":{\"cpu\":0.05,\"memory\":128}}" >/dev/null
wait_for "client task running" '[ "$(api "$ENV/services/client" | py "print(d[\"running\"])")" = 1 ]'
CNODE=$(api "$ENV/services/client/tasks" | py 'print([t["node"] for t in d["items"] if t["state"]=="running"][0])')
CTASK=$(api "$ENV/services/client/tasks" | py 'print([t["id"] for t in d["items"] if t["state"]=="running"][0])')
sql() { # url, statements (on stdin, so quotes need no escaping)
  printf '%s\n' "$2" | docker exec -i $(nodec $CNODE) sh -c "docker exec -i -e PGCONNECT_TIMEOUT=3 \$(docker ps -q --filter label=syncloud.task_id=$CTASK) psql '$1' -v ON_ERROR_STOP=1 -At" 2>&1
}
ins() { sql "${2:-$URL}" "INSERT INTO items (name) VALUES ('$1')" >/dev/null; }
# The task's resolver may need a moment for the database's names.
wait_for "a write through the read-write endpoint" 'sql "$URL" "CREATE TABLE IF NOT EXISTS items (id serial PRIMARY KEY, name text)" >/dev/null'
ins first
wait_for "the replica has the row" '[ "$(sql "$RO" "SELECT name FROM items")" = first ]'
# psql exits non-zero on the expected error, which pipefail would report.
ro_refuses() { { sql "$RO" "INSERT INTO items (name) VALUES ('x')" || true; } | grep -q 'read-only'; }
wait_for "the read-only endpoint refuses writes" ro_refuses
[ "$(sql "$URL" "SELECT current_user")" = app ] || fail "connected as the wrong user"
{ sql "$URL" "SELECT 1 FROM pg_authid" || true; } | grep -q 'permission denied' || fail "the app user is a superuser"
[ "$(sql "$URL" "SHOW shared_preload_libraries")" = "pg_stat_statements,timescaledb,pg_cron,pg_duckdb" ] || fail "preload libraries"
echo "  ✓ writes on orders, reads on orders-ro, read-only enforced, app is not a superuser, extensions preloaded"

echo "== failover"
OLD=$(primary)
OLDNODE=$(member "$OLD" node); OLDTASK=$(member "$OLD" id)
x $(nodec $OLDNODE) sh -c "docker pause \$(docker ps -q --filter label=syncloud.task_id=$OLDTASK)" >/dev/null
t0=$(date +%s)
wait_for "a new leader" '[ "$(primary)" != "$OLD" ]'
wait_for "writes work again" 'ins after'
echo "  ✓ leader m$OLD frozen; writes on the new leader after $(( $(date +%s) - t0 ))s"
x $(nodec $OLDNODE) sh -c "docker unpause \$(docker ps -aq --filter label=syncloud.task_id=$OLDTASK)" >/dev/null
wait_for "the old leader rejoins as a streaming replica" '[ "$(member "$OLD" role)" = replica ] && [ "$(member "$OLD" linkUp)" = True ]'
[ "$(sql "$URL" "SELECT count(*) FROM items")" = 2 ] || fail "rows lost in the failover"
api "$DB/events" | py 'assert any(e["kind"]=="failover" for e in d["items"]), d'
echo "  ✓ m$OLD back as a replica (pg_rewind), rows intact, failover recorded"

echo "== switchover from the API"
wait_for "healthy before the switchover" '[ "$(health)" = healthy ]'
BEFORE=$(primary)
api -X POST "$DB/failover" >/dev/null
wait_for "the leader moved" '[ "$(primary)" != "$BEFORE" ]'
wait_for "writes after the switchover" 'ins switched'
echo "  ✓ switchover m$BEFORE → m$(primary)"

echo "== failover with the controller down (HA URL)"
wait_for "healthy before stopping the controller" '[ "$(health)" = healthy ]'
LEAD=$(primary); LEADNODE=$(member "$LEAD" node); LEADTASK=$(member "$LEAD" id)
pid=$(x sc-e2e-ctl sh -c 'ps -o pid=,args= | grep "^ *[0-9]* /opt/sc/syncloud-controller" | awk "{print \$1}"')
x sc-e2e-ctl kill "$pid"
x $(nodec $LEADNODE) sh -c "docker pause \$(docker ps -q --filter label=syncloud.task_id=$LEADTASK)" >/dev/null
ok=0
for _ in $(seq 1 45); do
  if ins no-controller "$HA"; then ok=1; break; fi
  sleep 2
done
[ "$ok" = 1 ] || fail "no write through the HA URL while the controller was down"
echo "  ✓ Patroni promoted a replica without the controller; the HA URL found it"
x $(nodec $LEADNODE) sh -c "docker unpause \$(docker ps -aq --filter label=syncloud.task_id=$LEADTASK)" >/dev/null
x -d sc-e2e-ctl sh -c "/opt/sc/syncloud-controller --dev --data-dir /data --listen 0.0.0.0:7070 --agent-listen 0.0.0.0:7443 --agent-advertise $CTL_IP:7443 --system-tasks=false ${CTL_FLAGS:-} >> /var/log/controller.log 2>&1"
for _ in $(seq 1 30); do x sc-e2e-ctl curl -fs localhost:7070/api/v1/system/status >/dev/null 2>&1 && break; sleep 1; done
wait_for "healthy again with the controller back" '[ "$(health)" = healthy ] && [ "$(primary)" != "$LEAD" ]'
wait_for "the read-write endpoint follows" 'ins back'
[ "$(sql "$URL" "SELECT count(*) FROM items")" = 5 ] || fail "rows: $(sql "$URL" "SELECT count(*) FROM items")"
! x sc-e2e-ctl grep -q "restarting database member with a new spec" /var/log/controller.log || fail "the controller restart rolled a member"
echo "  ✓ controller back: the endpoints follow the new leader, every row kept, no member restarted"

echo "== CLI"
PAT=$(api localhost:7070/api/v1/iam/tokens -d '{"name":"e2e"}' | grep -o '"token":"[^"]*"' | cut -d'"' -f4)
S="x -e SYNCLOUD_TOKEN=$PAT sc-e2e-ctl /opt/sc/synctl --endpoint http://127.0.0.1:7070"
$S db list | grep orders | grep -q 'postgres 17' || fail "synctl db list"
$S db credentials orders | grep -q 'haUrl:' || fail "synctl db credentials"
echo "  ✓ synctl db list, credentials"

echo "== delete"
VOLNODE=$(member 0 node)
api -X DELETE "$DB" >/dev/null
wait_for "database gone" '! api "$DB" >/dev/null 2>&1'
wait_for "member volumes removed" '[ -z "$(x $(nodec $VOLNODE) docker volume ls -q --filter name=syncloud-db-)" ]'
echo "  ✓ deleted with its volumes"
echo "PASS"
