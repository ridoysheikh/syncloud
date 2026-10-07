#!/usr/bin/env bash
# End-to-end test of PostgreSQL backups and point-in-time recovery (Phase
# 13c): an S3 server (VersityGW; MinIO images are no longer freely
# pullable) deployed as a service is the S3 endpoint; a cluster with
# backups archives its WAL and takes a scheduled base backup; rows are
# written before and after a noted moment; a restore to that moment into a
# new cluster has exactly the rows from before it, and the source is
# untouched; a manual backup runs; a new replica clones from WAL-G; a
# target outside the window is refused.
#
#   test/e2e/postgres-backup.sh          # run and clean up
#   KEEP=1 test/e2e/postgres-backup.sh   # leave the nodes running for inspection
WITH_POSTGRES=1
S3IMG=versity/versitygw:v1.8.0
. "$(dirname "$0")/lib.sh"
docker image inspect $S3IMG >/dev/null 2>&1 || docker pull -q $S3IMG >/dev/null
trap cleanup EXIT
setup_cluster
docker save $S3IMG -o "$BIN/s3.tar"
x sc-e2e-w2 docker load -q -i /opt/sc/s3.tar >/dev/null
wait_mesh

P=localhost:7070/api/v1
ENV=$P/projects/shop/environments/production
DB=$P/databases/orders
apie() { x sc-e2e-ctl curl -sS -b /tmp/jar -H 'content-type: application/json' -H 'Origin: http://localhost:7070' "$@"; } # body even on errors
py() { python3 -c "import json,sys; d=json.load(sys.stdin); $1"; }
wait_for() { # description, condition (shell)
  local d=$1; shift
  for _ in $(seq 1 180); do eval "$@" && return 0; sleep 2; done
  echo "--- backups:"; api "$DB/backups" | head -c 3000; echo
  fail "timed out: $d"
}
health() { api "$P/databases/$1" | py 'print(d["health"])'; }
w() { api "$P/databases/$1/pg/execute" -d "$(python3 -c 'import json,sys; print(json.dumps({"sql": sys.argv[1]}))' "$2")" | py 'assert "error" not in d, d["error"]'; }
q() { api "$P/databases/$1/pg/query" -d "$(python3 -c 'import json,sys; print(json.dumps({"sql": sys.argv[1]}))' "$2")" | py 'print(d["results"][-1]["rows"])'; }

api $P/projects -d '{"name":"shop"}' >/dev/null

echo "== S3 server"
api -X PUT $ENV/services/minio -d "{\"image\":\"$S3IMG\",\"command\":[\"--access\",\"syncloud\",\"--secret\",\"minio-secret-123\",\"--port\",\":9000\",\"posix\",\"/tmp\"],\"ports\":[{\"container\":9000}],\"resources\":{\"cpu\":0.2,\"memory\":256},\"placement\":{\"node\":\"w2\"},\"desiredCount\":1}" >/dev/null
for _ in $(seq 1 60); do api $ENV/services/minio/tasks | grep -q '"state":"running","ip":"10\.' && break; sleep 2; done
MIP=$(api $ENV/services/minio/tasks | grep -o '"state":"running","ip":"[0-9.]*"' | head -1 | grep -o '10\.[0-9.]*')
[ -n "$MIP" ] || fail "the S3 server is not running"
for _ in $(seq 1 30); do x sc-e2e-ctl curl -s -o /dev/null "http://$MIP:9000/" && break; sleep 1; done
api $P/s3/endpoints -d "{\"name\":\"minio\",\"url\":\"http://$MIP:9000\",\"accessKeyId\":\"syncloud\",\"secretAccessKey\":\"minio-secret-123\",\"pathStyle\":true}" >/dev/null
api $P/s3/endpoints/minio/buckets -d '{"name":"pg-backups"}' >/dev/null
echo "  ✓ S3 at $MIP:9000, bucket pg-backups"

echo "== a cluster with backups"
apie $P/databases -d '{"name":"orders","engine":"postgres","project":"shop","environment":"production","spec":{"memory":{"min":512},"replicas":{"min":0,"max":0},"postgres":{"backup":{"endpoint":"nope","bucket":"pg-backups"}}}}' |
  grep -q 'no S3 endpoint' || fail "an unknown endpoint was accepted"
api $P/databases -d '{"name":"orders","engine":"postgres","project":"shop","environment":"production","spec":{"memory":{"min":512},"replicas":{"min":0,"max":1},"postgres":{"backup":{"endpoint":"minio","bucket":"pg-backups","everyHours":24,"retainFull":3,"retainDays":2}}}}' >/dev/null
wait_for "healthy" '[ "$(health orders)" = healthy ]'
api "$DB" | py 'assert d["version"] == "18", d["version"]'
[ "$(q orders "SHOW server_version_num" | cut -c4-5)" = 18 ] || fail "orders does not run PostgreSQL 18: $(q orders "SHOW server_version_num")"
wait_for "the first base backup (scheduled)" 'api "$DB/backups" | py "assert any(r[\"state\"] == \"ok\" and r[\"trigger\"] == \"schedule\" for r in d[\"runs\"]) and len(d[\"backups\"]) >= 1" 2>/dev/null'
api "$DB/backups" | py '
assert d["location"].startswith("s3://pg-backups/syncloud-pg/db_"), d["location"]
b = d["backups"][0]
assert b["name"].startswith("base_") and b["compressedSize"] > 0, b
print("  ✓ scheduled base backup", b["name"], "of", b["uncompressedSize"] // 1024, "KiB; runs:", [(r["trigger"], r["state"]) for r in d["runs"]])'

echo "== writes around a moment"
w orders "CREATE TABLE events (id int PRIMARY KEY, what text, at timestamptz DEFAULT clock_timestamp())"
w orders "INSERT INTO events (id, what) SELECT g, 'before' FROM generate_series(1, 100) g"
sleep 3
T=$(date -u +%Y-%m-%dT%H:%M:%SZ)
sleep 3
w orders "INSERT INTO events (id, what) VALUES (1000, 'after the moment'); DELETE FROM events WHERE id <= 10"
AFTER=$(date -u +%s)
# archive_timeout (60 s) ships the segment holding the later writes.
wait_for "WAL past the moment archived" 'api "$DB/backups" | py "
import datetime
to = datetime.datetime.fromisoformat(d[\"window\"][\"to\"].replace(\"Z\", \"+00:00\")).timestamp()
assert to > $AFTER + 1, (to, $AFTER)" 2>/dev/null'
api "$DB/backups" | py 'a = d["archiver"]; assert a["archivedCount"] > 0 and a["failedCount"] == 0, a; print("  ✓ WAL archived continuously:", a["archivedCount"], "segments, none failed; window", d["window"]["from"], "→", d["window"]["to"])'

echo "== point-in-time restore into a new cluster"
apie $P/databases -d '{"name":"too-early","engine":"postgres","restore":{"from":"orders","targetTime":"2020-01-01T00:00:00Z"}}' | grep -q 'within the restore window' || fail "a target outside the window was accepted"
apie $P/databases -d '{"name":"other-major","engine":"postgres","version":"17","restore":{"from":"orders"}}' | grep -q "keeps the source's version (18)" || fail "a restore into another major version was accepted"
api $P/databases -d "{\"name\":\"orders-at-t\",\"engine\":\"postgres\",\"project\":\"shop\",\"environment\":\"production\",\"spec\":{\"memory\":{\"min\":512}},\"restore\":{\"from\":\"orders\",\"targetTime\":\"$T\"}}" >/dev/null
wait_for "the restored cluster is healthy" '[ "$(health orders-at-t)" = healthy ]'
[ "$(q orders-at-t "SELECT count(*), min(id), max(id) FROM events")" = "[['100', '1', '100']]" ] || fail "restored rows: $(q orders-at-t "SELECT count(*), min(id), max(id) FROM events")"
[ "$(q orders "SELECT count(*), min(id), max(id) FROM events")" = "[['91', '11', '1000']]" ] || fail "the source changed"
api "$P/databases/orders-at-t" | py 'assert d["version"] == "18", d["version"]'
api "$P/databases/orders-at-t/credentials" | py 'assert d["database"] == "orders" and d["url"].endswith("/orders"), d'
api "$P/databases/orders-at-t/events" | py 'assert any(e["kind"] == "restore" for e in d["items"]), d'
echo "  ✓ orders-at-t has the 100 rows from before $T (not the later insert and delete); orders is untouched"
w orders-at-t "INSERT INTO events (id, what) VALUES (2000, 'new timeline')"
api "$P/databases/orders-at-t/backups" | py 'assert d["configured"] and d["location"] != "" and "orders-at-t" not in d["location"], d; print("  ✓ the restored cluster archives to its own prefix:", d["location"])'

echo "== manual backup, WAL-G replica"
api -X POST "$DB/backups" >/dev/null
apie -X POST "$DB/backups" | grep -q 'already running' || fail "a second concurrent backup was started"
wait_for "the manual backup" 'api "$DB/backups" | py "assert d[\"runs\"][0][\"trigger\"] == \"manual\" and d[\"runs\"][0][\"state\"] == \"ok\"" 2>/dev/null'
api "$DB/backups" | py 'assert len(d["backups"]) >= 2, d["backups"]'
api -X PUT "$DB" -d "$(api "$DB" | py 'import json; s = d["spec"]; s["replicas"] = {"min": 1, "max": 1}; print(json.dumps({"spec": s}))')" >/dev/null
wait_for "healthy with a replica" '[ "$(health orders)" = healthy ] && api "$DB" | py "assert len([m for m in d[\"members\"] if m[\"kind\"] == \"data\"]) == 2"'
RNODE=$(api "$DB" | py 'print([m["node"] for m in d["members"] if m["name"] == "m1"][0])')
RC=sc-e2e-$RNODE; [ "$RNODE" = ctl-0 ] && RC=sc-e2e-ctl
x "$RC" sh -c 'docker logs $(docker ps -q --filter label=syncloud.db_member=m1 --filter label=syncloud.database=orders) 2>&1' | grep -q 'replica has been created using walg' || fail "m1 did not clone from WAL-G"
echo "  ✓ manual backup; one run at a time; the new replica m1 cloned from the latest base backup"

echo "== CLI"
PAT=$(api localhost:7070/api/v1/iam/tokens -d '{"name":"e2e"}' | grep -o '"token":"[^"]*"' | cut -d'"' -f4)
# The endpoint comes from the environment: "db backup config" has its own --endpoint (the S3 one).
S="x -e SYNCLOUD_TOKEN=$PAT -e SYNCLOUD_ENDPOINT=http://127.0.0.1:7070 sc-e2e-ctl /opt/sc/synctl"
$S db backups orders | grep -q 'Restore:  any moment from' || fail "synctl db backups"
# Into the source's project: the S3 service here only accepts its own environment.
$S db restore orders orders-clone | grep -q 'Restoring orders into orders-clone (shop/production)' || fail "synctl db restore"
wait_for "the clone is healthy" '[ "$(health orders-clone)" = healthy ]'
[ "$(q orders-clone "SELECT count(*) FROM events WHERE id = 1000")" = "[['1']]" ] || fail "the clone misses the latest rows"
$S db backup config orders-clone --off | grep -q 'are off' || fail "synctl db backup config --off"
echo "  ✓ synctl db backups, restore (a clone of the latest state), backup config --off"

echo "PASS"
