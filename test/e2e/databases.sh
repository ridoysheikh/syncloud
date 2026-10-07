#!/usr/bin/env bash
# End-to-end test of managed Valkey (Phase 12): members on distinct nodes,
# Sentinel, read-write and read-only endpoints from a task, a standalone
# database behind its access list, the public TLS endpoint through Traefik
# (allow-list, following a failover), the explorer and console, failover
# when the primary stops answering, memory and replica autoscaling, and
# deletion with volume cleanup.
#
#   test/e2e/databases.sh          # run and clean up
#   KEEP=1 test/e2e/databases.sh   # leave the nodes running for inspection
. "$(dirname "$0")/lib.sh"
WITH_METRICS=1
WITH_TRAEFIK=1
CTL_FLAGS="--base-domain e2e.test --public-valkey :6379" # dev mode would use 127.0.0.1:16379
trap cleanup EXIT
setup_cluster
start_traefik
start_vmetrics
wait_mesh

P=localhost:7070/api/v1/projects/shop
ENV=$P/environments/production
DBS=localhost:7070/api/v1/databases
DB=$DBS/cache
py() { python3 -c "import json,sys; d=json.load(sys.stdin); $1"; }
wait_for() { # description, condition (shell)
  local d=$1; shift
  for _ in $(seq 1 120); do eval "$@" && return 0; sleep 2; done
  echo "--- database:"; api "$DB" | head -c 3000; echo
  fail "timed out: $d"
}
health() { api "$DB" | py 'print(d["health"])'; }
apie() { x sc-e2e-ctl curl -sS -b /tmp/jar -H 'content-type: application/json' -H 'Origin: http://localhost:7070' "$@"; } # body even on errors
nodec() { [ "$1" = ctl-0 ] && echo sc-e2e-ctl || echo "sc-e2e-$1"; } # node name -> its container
RW=cache.production.shop.syncloud.internal
RO=cache-ro.production.shop.syncloud.internal

echo "== create"
api localhost:7070/api/v1/projects -d '{"name":"shop"}' >/dev/null
api "$DBS" -d '{"name":"cache","project":"shop","environment":"production","spec":{"memory":{"min":64,"max":256},"replicas":{"min":1,"max":2},"autoscaling":{"cpuTarget":5}}}' >/dev/null
wait_for "healthy with a primary, a replica and 3 sentinels" '[ "$(health)" = healthy ]'
api "$DB" | py '
ms=d["members"]; data=[m for m in ms if m["kind"]=="data"]; s=[m for m in ms if m["kind"]=="sentinel"]
assert len(data)==2 and len(s)==3, ms
assert len({m["node"] for m in data})==2, "data members share a node"
assert sorted(m["role"] for m in data)==["primary","replica"], data
print("  ✓ m0/m1 on", ", ".join(m["node"] for m in data), "with 3 sentinels")'
PASS_=$(api "$DB/credentials" | py 'print(d["password"])')

echo "== endpoints from a task"
api -X PUT "$ENV/services/client" -d '{"image":"valkey/valkey:8.1-alpine","command":["sleep","3600"],"resources":{"cpu":0.05,"memory":128}}' >/dev/null
wait_for "client task running" '[ "$(api "$ENV/services/client" | py "print(d[\"running\"])")" = 1 ]'
CNODE=$(api "$ENV/services/client/tasks" | py 'print([t["node"] for t in d["items"] if t["state"]=="running"][0])')
CTASK=$(api "$ENV/services/client/tasks" | py 'print([t["id"] for t in d["items"] if t["state"]=="running"][0])')
cli() { x $(nodec $CNODE) sh -c "docker exec \$(docker ps -q --filter label=syncloud.task_id=$CTASK) valkey-cli --no-auth-warning -a $PASS_ $*"; }
[ "$(cli -h $RW SET greeting hello)" = OK ] || fail "write through the read-write endpoint"
wait_for "the replica has the write" '[ "$(cli -h $RO GET greeting)" = hello ]'
cli -h $RO SET x 1 2>&1 | grep -q READONLY || fail "the read-only endpoint accepted a write"
cli -h $RW CONFIG GET maxmemory 2>&1 | grep -qi 'NOPERM\|no permissions' || fail "apps can run CONFIG"
echo "  ✓ writes on cache, reads on cache-ro, read-only enforced, admin commands refused for apps"

echo "== standalone database behind its access list"
api "$DBS" -d '{"name":"sessions","spec":{"memory":{"min":64,"max":64}}}' | py 'assert d["standalone"] and d["project"]=="" and d["network"]["access"]==[] and d["host"]=="sessions.db.syncloud.internal", d'
apie "$DBS" -d '{"name":"cache","spec":{}}' | grep -q 'taken' || fail "a second database named cache was accepted"
wait_for "sessions healthy" '[ "$(api "$DBS/sessions" | py "print(d[\"health\"])")" = healthy ]'
SPASS=$(api "$DBS/sessions/credentials" | py 'print(d["password"])')
scli() { x $(nodec $CNODE) sh -c "docker exec \$(docker ps -q --filter label=syncloud.task_id=$CTASK) timeout 4 valkey-cli --no-auth-warning -a $SPASS -h sessions.db.syncloud.internal $*"; }
[ "$(scli PING 2>/dev/null)" = PONG ] && fail "a service not on the access list reached the standalone database"
api -X PUT "$DBS/sessions/network" -d '{"access":["environment:shop/production"],"public":{"enabled":false}}' | py 'assert d["network"]["access"]==["environment:shop/production"], d["network"]'
wait_for "the listed environment connects" '[ "$(scli PING 2>/dev/null)" = PONG ]'
apie -X PUT "$DBS/sessions/network" -d '{"access":["project:ghost"]}' | grep -q 'no such' || fail "an access entry for a missing project was accepted"
echo "  ✓ sessions.db.syncloud.internal refused, then reachable once shop/production is on its access list"

echo "== public endpoint (TLS through Traefik, routed by SNI)"
api -X PUT "$DB/network" -d '{"access":["environment:shop/production"],"public":{"enabled":true}}' | py '
p=d["public"]; assert p["enabled"] and p["available"] and p["host"]=="cache.db.e2e.test" and p["readHost"]=="cache-ro.db.e2e.test", p
assert d["network"]["public"]["allow"]==["0.0.0.0/0","::/0"], d["network"]'
api "$DB/credentials" | py 'assert d["publicUrl"].startswith("rediss://default:") and d["publicUrl"].endswith("@cache.db.e2e.test:6379"), d'
OUT=sc-e2e-w1 # another machine than the controller; not over the private network
x $OUT docker image inspect valkey/valkey:8.1-alpine >/dev/null 2>&1 || x $OUT docker pull -q valkey/valkey:8.1-alpine >/dev/null
OUTIP=$(docker inspect -f "{{(index .NetworkSettings.Networks \"$NET\").IPAddress}}" $OUT)
pub() { # host, command...
  local h=$1; shift
  x $OUT docker run --rm --network host valkey/valkey:8.1-alpine timeout 5 valkey-cli --no-auth-warning --tls --insecure --sni "$h" -h "$CTL_IP" -p 6379 -a "$PASS_" "$@" 2>&1
}
wait_for "TLS GET through the public endpoint" '[ "$(pub cache.db.e2e.test GET greeting)" = hello ]'
[ "$(pub cache.db.e2e.test SET public yes)" = OK ] || fail "write through the public endpoint"
pub cache-ro.db.e2e.test SET x 1 | grep -q READONLY || fail "the public read-only endpoint accepted a write"
x $OUT docker run --rm --network host valkey/valkey:8.1-alpine timeout 5 valkey-cli -h "$CTL_IP" -p 6379 -a "$PASS_" PING 2>&1 | grep -q PONG && fail "plain TCP without TLS was answered"
api -X PUT "$DB/network" -d '{"access":["environment:shop/production"],"public":{"enabled":true,"allow":["192.0.2.0/24"]}}' >/dev/null
wait_for "a client outside the allow-list is refused" '[ "$(pub cache.db.e2e.test PING)" != PONG ]'
api -X PUT "$DB/network" -d "{\"access\":[\"environment:shop/production\"],\"public\":{\"enabled\":true,\"allow\":[\"$OUTIP\"]}}" >/dev/null
wait_for "the allowed client connects" '[ "$(pub cache.db.e2e.test PING)" = PONG ]'
echo "  ✓ rediss://…@cache.db.e2e.test:6379 reads and writes, cache-ro is read-only, plain TCP refused, allow-list enforced"

echo "== explorer and console"
api -X PUT "$DB/key?key=user:1" -d '{"type":"hash","hash":{"name":"Ada","role":"admin"},"ttlSeconds":600}' | py 'assert d["type"]=="hash" and d["hash"]["name"]=="Ada" and d["ttlMs"]>0, d'
api "$DB/keys?pattern=user:*" | py 'k=d["keys"]; assert [x["key"] for x in k]==["user:1"] and k[0]["type"]=="hash", d'
api "$DB/key?key=greeting" | py 'assert d["string"]=="hello", d'
api -X PUT "$DB/key/ttl?key=greeting" -d '{"ttlSeconds":-1}' >/dev/null
api "$DB/command" -d '{"args":["INCRBY","visits","5"]}' | py 'assert d["result"]==5, d'
code=$(x sc-e2e-ctl curl -s -o /dev/null -w '%{http_code}' -b /tmp/jar -H 'content-type: application/json' -H 'Origin: http://localhost:7070' "$DB/command" -d '{"args":["CONFIG","GET","*"]}')
[ "$code" = 400 ] || fail "console ran CONFIG: HTTP $code"
api -X DELETE "$DB/key?key=user:1&key=visits" | py 'assert d["deleted"]==2, d'
api "$DB/info" | py 'assert d["replication"]["role"]=="master" and d["replication"]["connected_slaves"]=="1", d["replication"]'
echo "  ✓ hash with TTL, SCAN by pattern, string read, TTL removed, console INCRBY, CONFIG refused, delete, INFO"

echo "== failover"
OLD=$(api "$DB" | py 'print(d["state"]["primary"])')
OLDNODE=$(api "$DB" | py "print([m['node'] for m in d['members'] if m['name']=='m$OLD'][0])")
OLDTASK=$(api "$DB" | py "print([m['id'] for m in d['members'] if m['name']=='m$OLD'][0])")
x $(nodec $OLDNODE) sh -c "docker pause \$(docker ps -q --filter label=syncloud.task_id=$OLDTASK)" >/dev/null
t0=$(date +%s)
wait_for "a new primary" '[ "$(api "$DB" | py "print(d[\"state\"][\"primary\"])")" != "$OLD" ]'
wait_for "writes work again" '[ "$(cli -h $RW SET after failover 2>/dev/null)" = OK ]'
echo "  ✓ primary m$OLD frozen; writes on the new primary after $(( $(date +%s) - t0 ))s"
x $(nodec $OLDNODE) sh -c "docker unpause \$(docker ps -aq --filter label=syncloud.task_id=$OLDTASK)" >/dev/null
wait_for "the old primary rejoins as a replica" "api \"\$DB\" | py \"import sys; sys.exit(0 if [m for m in d['members'] if m['name']=='m$OLD' and m['role']=='replica' and m['linkUp']] else 1)\""
[ "$(cli -h $RW GET greeting)" = hello ] || fail "data lost in the failover"
wait_for "the public endpoint follows the new primary" '[ "$(pub cache.db.e2e.test SET public2 yes)" = OK ]'
api "$DB/events" | py 'assert any(e["kind"]=="failover" for e in d["items"]), d'
echo "  ✓ m$OLD back as a replica, data intact, failover recorded"

echo "== memory autoscaling"
x $(nodec $CNODE) sh -c "docker exec \$(docker ps -q --filter label=syncloud.task_id=$CTASK) sh -c 'for i in \$(seq 1 58); do head -c 1000000 /dev/zero | tr \"\\\\0\" x | valkey-cli --no-auth-warning -a $PASS_ -h $RW -x SET big\$i >/dev/null; done; valkey-cli --no-auth-warning -a $PASS_ -h $RW DBSIZE'" | grep -q '^[56][0-9]' || fail "fill memory"
wait_for "memory grows from 64 MiB" '[ "$(api "$DB" | py "print(d[\"state\"][\"memoryMiB\"])")" -gt 64 ]'
api "$DB/events" | py 'e=[e for e in d["items"] if e["kind"]=="memory"][0]; print("  ✓ memory", e["from"], "→", e["to"], "("+e["reason"]+")")'
wait_for "maxmemory applied online" '[ "$(api "$DB/info" | py "print(d[\"memory\"][\"maxmemory\"])")" -gt $((64<<20)) ]'

echo "== replica autoscaling"
x $(nodec $CNODE) sh -c "docker exec -d \$(docker ps -q --filter label=syncloud.task_id=$CTASK) valkey-benchmark -a $PASS_ -h $RO -t get -n 50000000 -c 30 -q" >/dev/null
wait_for "a second replica" '[ "$(api "$DB" | py "print(d[\"state\"][\"replicas\"])")" = 2 ]'
wait_for "3 data members, healthy" '[ "$(health)" = healthy ] && [ "$(api "$DB" | py "print(len([m for m in d[\"members\"] if m[\"kind\"]==\"data\"]))")" = 3 ]'
api "$DB/events" | py 'e=[e for e in d["items"] if e["kind"]=="replicas"][0]; print("  ✓ replicas", e["from"], "→", e["to"], "("+e["reason"]+")")'
x $(nodec $CNODE) sh -c "docker exec \$(docker ps -q --filter label=syncloud.task_id=$CTASK) pkill valkey-benchmark" >/dev/null 2>&1 || true

echo "== metrics and CLI"
for _ in $(seq 1 30); do api "$DB/metrics?range=15m" | py 'import sys; sys.exit(0 if d["charts"]["ops"] and d["charts"]["memory"] else 1)' && break; sleep 2; done
api "$DB/metrics?range=15m" | py 'c=d["charts"]; assert c["ops"] and c["memory"] and c["keys"], {k:len(v) for k,v in c.items()}'
PAT=$(api localhost:7070/api/v1/iam/tokens -d '{"name":"e2e"}' | grep -o '"token":"[^"]*"' | cut -d'"' -f4)
S="x -e SYNCLOUD_TOKEN=$PAT sc-e2e-ctl /opt/sc/synctl --endpoint http://127.0.0.1:7070"
$S db list | grep 'cache' | grep -q 'shop/production' || fail "synctl db list"
$S db list | grep 'sessions' | grep -q 'standalone' || fail "synctl db list (standalone)"
$S db get cache | grep -q 'read-write: cache.production.shop' || fail "synctl db get"
$S db get cache | grep -q 'public:     cache.db.e2e.test:6379' || fail "synctl db get (public)"
[ "$($S db cmd cache GET greeting)" = '"hello"' ] || fail "synctl db cmd"
$S db network sessions --remove-access environment:shop/production --add-access project:shop | grep -q 'access:     project:shop' || fail "synctl db network"
$S db engines | grep -q 'postgres.*planned' || fail "synctl db engines"
out=$($S db create later --engine postgres 2>&1) && fail "a planned engine was accepted"
echo "$out" | grep -q 'not available' || fail "planned engine: $out"
echo "  ✓ history charts; synctl db list, get, cmd, network, engines"

echo "== delete"
VOLNODE=$(api "$DB" | py "print([m['node'] for m in d['members'] if m['kind']=='data'][0])")
api -X DELETE "$DB" >/dev/null
api -X DELETE "$DBS/sessions" >/dev/null
wait_for "database gone" '! api "$DB" >/dev/null 2>&1'
wait_for "sessions gone" '! api "$DBS/sessions" >/dev/null 2>&1'
api localhost:7070/api/v1/traefik/config | grep -q 'cache.db.e2e.test' && fail "the public route outlived the database"
wait_for "member volumes removed" '[ -z "$(x $(nodec $VOLNODE) docker volume ls -q --filter name=syncloud-db-)" ]'
echo "  ✓ deleted with its volumes"
echo "PASS"
