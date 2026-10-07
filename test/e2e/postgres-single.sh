#!/usr/bin/env bash
# End-to-end test of PostgreSQL on one node outside the private network,
# like single-node development: no discovery DNS, so the platform etcd and
# Patroni find each other through Docker network aliases. The etcd member
# starts, a cluster comes up and takes writes, and a changed etcd spec
# (another image) rolls out to the running member.
#
#   test/e2e/postgres-single.sh          # run and clean up
#   KEEP=1 test/e2e/postgres-single.sh   # leave the node running for inspection
WITH_POSTGRES=1
WORKERS=
CTL_NETWORK=off
. "$(dirname "$0")/lib.sh"
trap cleanup EXIT
setup_cluster

P=localhost:7070/api/v1
DB=$P/databases/notes
py() { python3 -c "import json,sys; d=json.load(sys.stdin); $1"; }
wait_for() { # description, condition (shell)
  local d=$1; shift
  for _ in $(seq 1 120); do eval "$@" && return 0; sleep 2; done
  echo "--- database:"; api "$DB" | head -c 3000; echo
  echo "--- etcd:"; x sc-e2e-ctl sh -c 'docker logs --tail 20 syncloud-etcd-e0 2>&1'
  fail "timed out: $d"
}
health() { api "$DB" | py 'print(d["health"])'; }
q() { api "$DB/pg/query" -d "$(python3 -c 'import json,sys; print(json.dumps({"sql": sys.argv[1]}))' "$1")" | py 'print(d["results"][-1]["rows"])'; }

api $P/projects -d '{"name":"dev"}' >/dev/null

echo "== a cluster on a node outside the mesh"
api $P/databases -d '{"name":"notes","engine":"postgres","project":"dev","environment":"production","spec":{"memory":{"min":256},"replicas":{"min":0,"max":0}}}' >/dev/null
wait_for "healthy" '[ "$(health)" = healthy ]'
x sc-e2e-ctl sh -c 'docker inspect -f "{{json .HostConfig.Dns}} {{json .NetworkSettings.Networks.syncloud.Aliases}}" syncloud-etcd-e0' | grep -q '^null .*e0.etcd.syncloud.internal' ||
  fail "etcd: $(x sc-e2e-ctl sh -c 'docker inspect -f "{{json .HostConfig.Dns}} {{json .NetworkSettings.Networks.syncloud.Aliases}}" syncloud-etcd-e0')"
api "$DB/pg/execute" -d '{"sql":"CREATE TABLE t (id int); INSERT INTO t VALUES (1), (2)"}' | py 'assert "error" not in d, d'
[ "$(q "SELECT count(*) FROM t")" = "[['2']]" ] || fail "rows: $(q "SELECT count(*) FROM t")"
echo "  ✓ etcd (no discovery DNS, alias e0.etcd.syncloud.internal) and PostgreSQL $(q "SHOW server_version" | cut -c4-7) up; writes work"

echo "== a changed etcd spec rolls out to the running member"
x sc-e2e-ctl docker tag "$PG_IMAGE" syncloud/pg-test:dev
x sc-e2e-ctl pkill -f "syncloud-controller --dev"
sleep 2
x -d sc-e2e-ctl sh -c "/opt/sc/syncloud-controller --dev --data-dir /data --listen 0.0.0.0:7070 --agent-listen 0.0.0.0:7443 --agent-advertise $CTL_IP:7443 --system-tasks=false --base-domain off --postgres-image 18=syncloud/pg-test:dev >> /var/log/controller.log 2>&1"
wait_for "the etcd member on the new image" '[ "$(x sc-e2e-ctl docker inspect -f "{{.Config.Image}}" syncloud-etcd-e0 2>/dev/null)" = syncloud/pg-test:dev ]'
wait_for "healthy again" '[ "$(health)" = healthy ] && [ "$(q "SELECT count(*) FROM t" 2>/dev/null)" = "[['"'"'2'"'"']]" ]'
echo "  ✓ the etcd member was recreated with the new spec and the cluster kept its data"

echo "PASS"
