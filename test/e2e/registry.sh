#!/usr/bin/env bash
# End-to-end test of the private registry (§5.9, §5.10): docker login with an
# access key, push, browse through the API, deploy "@registry/…" (the node
# pulls with a token minted by the controller), delete a tag.
. "$(dirname "$0")/lib.sh"
WITH_REGISTRY=1
CTL_FLAGS="--registry-pull-host 127.0.0.1:5000"
trap cleanup EXIT
setup_cluster
start_registry
wait_mesh

echo "== push"
key=$(api localhost:7070/api/v1/iam/access-keys -d '{"description":"e2e"}')
KID=$(echo "$key" | grep -o '"id":"SYNAK[A-Z0-9]*"' | cut -d'"' -f4); KSEC=$(echo "$key" | grep -o '"secretAccessKey":"[^"]*"' | cut -d'"' -f4)
x sc-e2e-ctl sh -c "echo '$KSEC' | docker login 127.0.0.1:5000 -u '$KID' --password-stdin" >/dev/null 2>&1 || fail "docker login with an access key failed"
x sc-e2e-ctl docker tag busybox:1.37 127.0.0.1:5000/shop/hello:v1
x sc-e2e-ctl docker push -q 127.0.0.1:5000/shop/hello:v1 >/dev/null || fail "push failed"
echo "  ✓ docker login (access key) and push"

echo "== browse"
api localhost:7070/api/v1/registry/repositories | grep -q '"name":"shop/hello","tags":1' || fail "repository not listed"
img=$(api "localhost:7070/api/v1/registry/images?repository=shop/hello")
echo "$img" | grep -q '"tag":"v1","digest":"sha256:' || fail "image not listed: $img"
echo "$img" | grep -q '"sizeBytes":[1-9]' || fail "image size missing"
echo "$img" | grep -q '"platforms":\["linux/' || fail "platform missing"
echo "  ✓ repository shop/hello with tag v1, digest, size and platform"

echo "== deploy @registry image"
for w in w1 w2; do
  id=$(api localhost:7070/api/v1/nodes | grep -o "\"id\":\"node_[a-z0-9]*\",\"name\":\"$w\"" | cut -d'"' -f4)
  api -X PUT "localhost:7070/api/v1/nodes/$id/schedulable" -d '{"schedulable":false}' >/dev/null
done
x sc-e2e-ctl docker logout 127.0.0.1:5000 >/dev/null
x sc-e2e-ctl docker rmi 127.0.0.1:5000/shop/hello:v1 >/dev/null
api localhost:7070/api/v1/projects -d '{"name":"shop"}' >/dev/null
SVC=localhost:7070/api/v1/projects/shop/environments/production/services/hello
api -X PUT "$SVC" -d '{"image":"@registry/shop/hello:v1","command":["httpd","-f","-p","8080","-h","/etc"],"ports":[{"container":8080}],"resources":{"cpu":0.05,"memory":16}}' >/dev/null
for _ in $(seq 1 60); do api "$SVC/tasks" | grep -q '"state":"running"' && break; sleep 2; done
api "$SVC/tasks" | grep -q '"state":"running"' || { api "$SVC/tasks"; fail "task from @registry did not start"; }
x sc-e2e-ctl docker image inspect 127.0.0.1:5000/shop/hello:v1 >/dev/null || fail "image was not pulled from the registry"
echo "  ✓ node pulled @registry/shop/hello:v1 with a controller-minted token (no docker login)"

echo "== pre-pull"
x sc-e2e-ctl sh -c "echo '$KSEC' | docker login 127.0.0.1:5000 -u '$KID' --password-stdin" >/dev/null 2>&1
x sc-e2e-ctl sh -c "printf 'FROM busybox:1.37\nRUN echo two > /v\n' | docker build -q -t 127.0.0.1:5000/shop/hello:v2 - >/dev/null && docker push -q 127.0.0.1:5000/shop/hello:v2 >/dev/null && docker rmi 127.0.0.1:5000/shop/hello:v2 >/dev/null"
x sc-e2e-ctl docker logout 127.0.0.1:5000 >/dev/null
api -X PUT "$SVC" -d '{"image":"@registry/shop/hello:v2","command":["httpd","-f","-p","8080","-h","/etc"],"ports":[{"container":8080}],"resources":{"cpu":0.05,"memory":16}}' >/dev/null
for _ in $(seq 1 30); do x sc-e2e-ctl grep -q 'pre-pulling image.*shop/hello:v2' /var/log/controller.log && break; sleep 1; done
x sc-e2e-ctl grep -q 'pre-pulling image.*shop/hello:v2.*nodes=1' /var/log/controller.log || fail "no pre-pull for the new revision"
for _ in $(seq 1 60); do api "$SVC/tasks" | grep -q '"revision":2,[^}]*"state":"running"' && break; sleep 2; done
api "$SVC/tasks" | grep -q '"revision":2,[^}]*"state":"running"' || fail "revision 2 did not start"
echo "  ✓ a new image is pre-pulled on the service's node before the rolling update"

echo "== upstream credentials"
# The same registry by host name instead of @registry: an upstream for the
# node, which has no docker login.
UP=localhost:7070/api/v1/projects/shop/environments/production/services/plain
x sc-e2e-ctl docker rmi -f 127.0.0.1:5000/shop/hello:v1 >/dev/null 2>&1 || true # left from the @registry deploy
api -X PUT "$UP" -d '{"image":"127.0.0.1:5000/shop/hello:v1","command":["sleep","3600"],"resources":{"cpu":0.05,"memory":16}}' >/dev/null
for _ in $(seq 1 30); do api "$UP/tasks" | grep -qi 'unauthorized\|denied\|authentication' && break; sleep 1; done
api "$UP/tasks" | grep -qi 'unauthorized\|denied\|authentication' || { api "$UP/tasks"; fail "an anonymous pull should be refused"; }
api -X PUT localhost:7070/api/v1/registry/upstreams -d "{\"host\":\"127.0.0.1:5000\",\"username\":\"$KID\",\"password\":\"$KSEC\"}" | grep -q '"host":"127.0.0.1:5000"' || fail "save upstream credential"
api localhost:7070/api/v1/registry/upstreams | grep -q "$KSEC" && fail "the password is returned by the API"
for _ in $(seq 1 60); do api "$UP/tasks" | grep -q '"state":"running"' && break; sleep 2; done
api "$UP/tasks" | grep -q '"state":"running"' || { api "$UP/tasks"; fail "pull with the upstream credential failed"; }
echo "  ✓ anonymous pull refused; with a stored upstream credential the node pulls (password never returned)"
api -X DELETE "$UP" >/dev/null

echo "== delete"
api -X DELETE "$SVC" >/dev/null
api -X DELETE "localhost:7070/api/v1/registry/images?repository=shop/hello&tag=v1" >/dev/null
api -X DELETE "localhost:7070/api/v1/registry/images?repository=shop/hello&tag=v2" >/dev/null
api localhost:7070/api/v1/registry/repositories | grep -q 'shop/hello' && fail "deleted repository still listed"
echo "  ✓ tag deleted"
echo "PASS"
