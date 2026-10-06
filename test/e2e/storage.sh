#!/usr/bin/env bash
# End-to-end test of S3 storage (§16) and the metrics explorer (§9.1): MinIO
# deployed as an ordinary service is registered as an S3 endpoint; buckets
# are created, browsed, uploaded to, downloaded from and deleted from through
# the API and synctl; a bucket bound to a service reaches its tasks as
# environment variables with a new revision; PromQL runs over stored metrics.
#
#   test/e2e/storage.sh          # run and clean up
#   KEEP=1 test/e2e/storage.sh   # leave the nodes running for inspection
WITH_METRICS=1
MINIO=minio/minio:RELEASE.2025-04-22T22-12-26Z
. "$(dirname "$0")/lib.sh"
docker image inspect $MINIO >/dev/null 2>&1 || docker pull -q $MINIO >/dev/null
setup_cluster
docker save $MINIO -o "$BIN/minio.tar"
x sc-e2e-w1 docker load -q -i /opt/sc/minio.tar >/dev/null
start_vmetrics
wait_mesh

P=localhost:7070/api/v1
ENV=$P/projects/shop/environments/production
api $P/projects -d '{"name":"shop"}' >/dev/null

echo "== MinIO as a service"
api -X PUT $ENV/services/minio -d "{\"image\":\"$MINIO\",\"command\":[\"server\",\"/data\"],\"env\":{\"MINIO_ROOT_USER\":\"syncloud\",\"MINIO_ROOT_PASSWORD\":\"minio-secret-123\"},\"ports\":[{\"container\":9000}],\"resources\":{\"cpu\":0.2,\"memory\":256},\"placement\":{\"node\":\"w1\"},\"desiredCount\":1}" >/dev/null
for _ in $(seq 1 60); do api $ENV/services/minio/tasks | grep -q '"state":"running","ip":"10\.' && break; sleep 2; done
MIP=$(api $ENV/services/minio/tasks | grep -o '"state":"running","ip":"[0-9.]*"' | head -1 | grep -o '10\.[0-9.]*')
[ -n "$MIP" ] || fail "MinIO not running: $(api $ENV/services/minio/tasks)"
for _ in $(seq 1 30); do x sc-e2e-ctl curl -fs "http://$MIP:9000/minio/health/live" >/dev/null 2>&1 && break; sleep 1; done
echo "  ✓ MinIO runs at $MIP:9000"

echo "== endpoint"
code=$(x sc-e2e-ctl curl -s -o /dev/null -w '%{http_code}' -b /tmp/jar -H 'content-type: application/json' -H 'Origin: http://localhost:7070' $P/s3/endpoints \
  -d "{\"name\":\"minio\",\"url\":\"http://$MIP:9000\",\"accessKeyId\":\"syncloud\",\"secretAccessKey\":\"wrong\",\"pathStyle\":true}")
[ "$code" = 400 ] || fail "wrong credentials accepted ($code)"
api $P/s3/endpoints -d "{\"name\":\"minio\",\"url\":\"http://$MIP:9000\",\"accessKeyId\":\"syncloud\",\"secretAccessKey\":\"minio-secret-123\",\"pathStyle\":true}" | grep -q '"name":"minio"' || fail "add endpoint"
api $P/s3/endpoints | grep -q 'minio-secret' && fail "the secret is returned by the API"
echo "  ✓ credentials are checked before saving; the secret is never returned"

echo "== buckets and objects"
api $P/s3/endpoints/minio/buckets -d '{"name":"media"}' >/dev/null
api $P/s3/endpoints/minio/buckets | grep -q '"name":"media"' || fail "bucket not listed"
B=$P/s3/endpoints/minio/buckets/media
x sc-e2e-ctl sh -c 'head -c 300000 /dev/urandom > /tmp/blob && echo hello > /tmp/hello.txt'
x sc-e2e-ctl curl -fsS -b /tmp/jar -H 'Origin: http://localhost:7070' -H 'content-type: image/png' -X PUT --data-binary @/tmp/blob "$B/object?key=img/a.png" >/dev/null
x sc-e2e-ctl curl -fsS -b /tmp/jar -H 'Origin: http://localhost:7070' -H 'content-type: text/plain' -X PUT --data-binary @/tmp/hello.txt "$B/object?key=hello.txt" >/dev/null
out=$(api "$B/objects")
grep -q '"key":"img/","folder":true' <<<"$out" && grep -q '"key":"hello.txt","folder":false,"size":6' <<<"$out" || fail "listing: $out"
api "$B/objects?prefix=img/" | grep -q '"key":"img/a.png","folder":false,"size":300000' || fail "folder listing"
x sc-e2e-ctl sh -c "curl -fsS -b /tmp/jar '$B/object?key=img/a.png' -o /tmp/back && cmp /tmp/blob /tmp/back" || fail "download differs"
hdr=$(x sc-e2e-ctl curl -fsS -b /tmp/jar -D - -o /dev/null "$B/object?key=hello.txt")
grep -qi 'content-disposition: attachment' <<<"$hdr" || fail "downloads must be attachments: $hdr"
api "$B/usage" | grep -q '"objects":2,"bytes":300006' || fail "usage: $(api "$B/usage")"
api -X DELETE "$B/object?key=img/" | grep -q '"deleted":1' || fail "folder delete"
api "$B/objects" | grep -q 'img/' && fail "folder still listed"
echo "  ✓ bucket created; upload, folders, download (as attachment), usage and folder delete work"

echo "== binding a bucket to a service"
api -X PUT $ENV/services/app -d '{"image":"busybox:1.37","command":["sleep","1d"],"resources":{"cpu":0.05,"memory":16},"desiredCount":1}' >/dev/null
for _ in $(seq 1 30); do api $ENV/services/app | grep -q '"running":1' && break; sleep 2; done
REV=$(api $ENV/services/app | grep -o '"revision":[0-9]*' | head -1 | cut -d: -f2)
out=$(api -X PUT $ENV/services/app/s3 -d '{"bindings":[{"endpoint":"minio","bucket":"media","prefix":"app/"},{"endpoint":"minio","bucket":"media","envPrefix":"BACKUP_"}]}')
grep -q "\"revision\":$((REV + 1))" <<<"$out" || fail "binding did not create a revision: $out"
for _ in $(seq 1 60); do
  C=$(for n in ctl w1 w2; do x sc-e2e-$n docker ps -q --filter label=syncloud.revision=$((REV + 1)) --filter name=shop-production-app- | sed "s/^/$n:/"; done | head -1)
  [ -n "$C" ] && break; sleep 2
done
[ -n "$C" ] || fail "no task of the new revision"
envs=$(x sc-e2e-${C%%:*} docker exec "${C#*:}" env)
grep -q '^S3_BUCKET=media$' <<<"$envs" && grep -q '^S3_PREFIX=app/$' <<<"$envs" && grep -q "^S3_ENDPOINT=http://$MIP:9000$" <<<"$envs" \
  && grep -q '^AWS_SECRET_ACCESS_KEY=minio-secret-123$' <<<"$envs" && grep -q '^BACKUP_S3_BUCKET=media$' <<<"$envs" && grep -q '^S3_FORCE_PATH_STYLE=true$' <<<"$envs" \
  || fail "task environment: $envs"
api $ENV/services/app/revisions | grep -q '"secretAccessKey"\|minio-secret' && fail "credentials stored in the revision"
api $P/s3/endpoints/minio/buckets | grep -q '"boundBy":\["shop/production/app"' || fail "bucket does not show its service"
code=$(x sc-e2e-ctl curl -s -o /dev/null -w '%{http_code}' -b /tmp/jar -H 'Origin: http://localhost:7070' -X DELETE $P/s3/endpoints/minio)
[ "$code" = 409 ] || fail "a bound endpoint was deletable ($code)"
echo "  ✓ two bindings reach the tasks of a new revision as S3_*/AWS_* (and BACKUP_*) variables; credentials stay out of revisions; bound endpoints cannot be deleted"

echo "== synctl"
key=$(api $P/iam/access-keys -d '{"description":"e2e"}')
KID=$(echo "$key" | grep -o '"id":"SYNAK[A-Z0-9]*"' | cut -d'"' -f4); KSEC=$(echo "$key" | grep -o '"secretAccessKey":"[^"]*"' | cut -d'"' -f4)
synctl() { x -i -e SYNCLOUD_ENDPOINT=http://127.0.0.1:7070 -e SYNCLOUD_ACCESS_KEY_ID="$KID" -e SYNCLOUD_SECRET_ACCESS_KEY="$KSEC" sc-e2e-ctl /opt/sc/synctl "$@"; }
out=$(synctl s3 endpoints list </dev/null); grep -q "minio *http://$MIP:9000" <<<"$out" || fail "synctl s3 endpoints list: $out"
out=$(synctl s3 cp /tmp/hello.txt minio/media/docs/ </dev/null); grep -q 'Uploaded /tmp/hello.txt to minio/media/docs/hello.txt' <<<"$out" || fail "synctl s3 cp up: $out"
out=$(synctl s3 ls minio/media/docs/ </dev/null); grep -q 'docs/hello.txt' <<<"$out" || fail "synctl s3 ls: $out"
out=$(synctl s3 cp minio/media/docs/hello.txt - </dev/null); [ "$out" = hello ] || fail "synctl s3 cp down: $out"
out=$(synctl s3 bindings list app -p shop </dev/null); grep -q 'BACKUP_S3_\*' <<<"$out" || fail "synctl s3 bindings list: $out"
out=$(synctl s3 bindings remove app -p shop --env-prefix BACKUP_ </dev/null); grep -q 'has 1 S3 binding' <<<"$out" || fail "synctl s3 bindings remove: $out"
echo "  ✓ synctl s3 endpoints/cp/ls/bindings"

echo "== metrics explorer"
for _ in $(seq 1 40); do api "$P/metrics/query?range=15m&query=sum%20by%20(service)%20(syncloud_task_memory_bytes)" | grep -q '"service":"minio"' && break; sleep 3; done
out=$(api "$P/metrics/query?range=15m&query=sum%20by%20(service)%20(syncloud_task_memory_bytes)")
grep -q '"labels":{"service":"minio"},"points":\[\[' <<<"$out" || fail "explorer: $out"
api $P/metrics/names | grep -q '"syncloud_task_cpu_percent"' || fail "metric names"
code=$(x sc-e2e-ctl curl -s -o /dev/null -w '%{http_code}' -b /tmp/jar "$P/metrics/query?query=sum(")
[ "$code" = 400 ] || fail "a bad query returned $code"
out=$(synctl metrics query 'sum by (service) (syncloud_task_memory_bytes)' --range 15m </dev/null); grep -q 'service=minio' <<<"$out" || fail "synctl metrics query: $out"
echo "  ✓ PromQL range queries with labels, metric names, errors for bad queries, synctl metrics query"

echo "PASS"
