#!/usr/bin/env bash
# End-to-end test of the built-in Git server (§5.8) with the controller's
# real system tasks: Forgejo is off by default; turning it on starts the
# system task, creates its administrator and connects it as "git"; a service
# builds from one of its repositories with the webhook and commit statuses
# set up automatically; it cannot be turned off while services use it;
# turning it off removes the container and keeps the repositories, which are
# back when it is turned on again.
#
#   test/e2e/gitserver.sh          # run and clean up
#   KEEP=1 test/e2e/gitserver.sh   # leave the nodes running
. "$(dirname "$0")/lib.sh"
WITH_REGISTRY=1
WITH_TRAEFIK=1
WITH_METRICS=1
WITH_BUILDS=1
FORGEJO=codeberg.org/forgejo/forgejo:13.0.5-rootless
CTL_FLAGS="--system-tasks=true --registry-pull-host 127.0.0.1:5000 --build-node ctl-0"
trap cleanup EXIT
docker image inspect $FORGEJO >/dev/null 2>&1 || docker pull -q $FORGEJO >/dev/null
setup_cluster
docker save $FORGEJO -o "$BIN/forgejo.tar"
for t in registry traefik vlogs vmetrics buildkit forgejo; do x sc-e2e-ctl docker load -q -i /opt/sc/$t.tar >/dev/null; done
wait_mesh

P=localhost:7070/api/v1
G=http://127.0.0.1:3002/api/v1
running() { api $P/system/tasks | grep -o '"state":"running"' | wc -l; }
for _ in $(seq 1 60); do [ "$(running)" -ge 4 ] && break; sleep 2; done

echo "== off by default"
api $P/gitserver | grep -q '"enabled":false,"state":"off"' || fail "git server: $(api $P/gitserver)"
api $P/system/tasks | grep -q sys-git && fail "the Git server is listed while off"
echo "  ✓ off by default and not listed among the platform components"

echo "== turn on"
api -X PUT $P/gitserver -d '{"enabled":true}' | grep -q '"enabled":true' || fail "enable"
for _ in $(seq 1 90); do api $P/gitserver | grep -q '"state":"ready"' && break; sleep 2; done
st=$(api $P/gitserver)
grep -q '"state":"ready"' <<<"$st" || { x sc-e2e-ctl sh -c 'tail -20 /var/log/controller.log'; fail "not ready: $st"; }
grep -q '"url":"http://127.0.0.1:3002/"' <<<"$st" || fail "url: $st"
api $P/system/tasks | grep -q '"taskId":"sys-git"[^}]*"state":"running"' || fail "sys-git not running"
conn=$(api $P/integrations/git/git)
grep -q '"kind":"gitea"' <<<"$conn" && grep -q '"account":"syncloud"' <<<"$conn" || fail "connection: $conn"
PW=$(api $P/gitserver/credentials | grep -o '"password":"[^"]*"' | cut -d'"' -f4)
[ -n "$PW" ] || fail "no administrator password"
ga() { x sc-e2e-ctl curl -fsS -u "syncloud:$PW" -H 'content-type: application/json' "$@"; }
ga $G/user | grep -q '"is_admin":true' || fail "administrator sign-in"
echo "  ✓ Forgejo runs as a system task, its administrator was created and it is connected as \"git\""

echo "== build from it"
ga -X POST $G/user/repos -d '{"name":"web"}' >/dev/null
x sc-e2e-ctl sh -c "
  set -e
  git config --global user.email e2e@example.com && git config --global user.name e2e && git config --global init.defaultBranch main
  mkdir -p /src/web/app && cd /src/web && git init -q
  printf 'FROM busybox:1.37\nCOPY version /www/version\nCMD [\"httpd\", \"-f\", \"-p\", \"8080\", \"-h\", \"/www\"]\n' > app/Dockerfile
  echo one > app/version && git add -A && git commit -qm one
  git push -q http://syncloud:$PW@127.0.0.1:3002/syncloud/web.git main"
SHA1=$(x sc-e2e-ctl git -C /src/web rev-parse HEAD)
api "$P/integrations/git/git/repos" | grep -q '"fullName":"syncloud/web"' || fail "repository not listed"
for w in w1 w2; do
  id=$(api $P/nodes | grep -o "\"id\":\"node_[a-z0-9]*\",\"name\":\"$w\"" | cut -d'"' -f4)
  api -X PUT "$P/nodes/$id/schedulable" -d '{"schedulable":false}' >/dev/null
done
api $P/projects -d '{"name":"shop"}' >/dev/null
SVC=$P/projects/shop/environments/production/services/web
api -X PUT "$SVC" -d '{"image":"@build","command":["httpd","-f","-p","8080","-h","/www"],"ports":[{"container":8080}],"resources":{"cpu":0.05,"memory":16}}' >/dev/null
src=$(api -X PUT "$SVC/git" -d '{"connection":"git","repo":"syncloud/web","context":"app","pollSeconds":300}')
grep -q '"webhook":"created"' <<<"$src" || fail "source: $src"
wait_build() {
  local b
  for _ in $(seq 1 150); do
    b=$(api "$SVC/builds" | grep -o "{[^{}]*\"sha\":\"$1\"[^{}]*}" || true)
    grep -q "\"status\":\"$2\"" <<<"$b" && { echo "$b"; return; }
    grep -q '"status":"\(succeeded\|failed\|skipped\)"' <<<"$b" && break
    sleep 2
  done
  echo "$b" >&2; x sc-e2e-ctl sh -c 'tail -20 /var/log/controller.log' >&2
  fail "build of ${1:0:12} did not reach $2"
}
wait_status() {
  local s
  for _ in $(seq 1 30); do
    s=$(ga "$G/repos/syncloud/web/commits/$1/status")
    grep -q "\"state\":\"$2\"" <<<"$s" && return
    sleep 1
  done
  fail "commit ${1:0:12} status is not $2: $s"
}
wait_build "$SHA1" succeeded >/dev/null
wait_status "$SHA1" success
x sc-e2e-ctl sh -c "cd /src/web && echo two > app/version && git commit -qam two && git push -q http://syncloud:$PW@127.0.0.1:3002/syncloud/web.git main"
SHA2=$(x sc-e2e-ctl git -C /src/web rev-parse HEAD)
b=$(wait_build "$SHA2" succeeded)
grep -q '"trigger":"webhook"' <<<"$b" || fail "not triggered by Forgejo's webhook: $b"
wait_status "$SHA2" success
echo "  ✓ built from the built-in server; a push arrived by webhook; statuses on both commits"

echo "== synctl"
key=$(api $P/iam/access-keys -d '{"description":"e2e"}')
KID=$(echo "$key" | grep -o '"id":"SYNAK[A-Z0-9]*"' | cut -d'"' -f4); KSEC=$(echo "$key" | grep -o '"secretAccessKey":"[^"]*"' | cut -d'"' -f4)
synctl() { x -i -e SYNCLOUD_ENDPOINT=http://127.0.0.1:7070 -e SYNCLOUD_ACCESS_KEY_ID="$KID" -e SYNCLOUD_SECRET_ACCESS_KEY="$KSEC" sc-e2e-ctl /opt/sc/synctl "$@"; }
out=$(synctl integrations git-server </dev/null); grep -q 'ready at http://127.0.0.1:3002/' <<<"$out" || fail "synctl git-server: $out"
out=$(synctl integrations git-server credentials </dev/null); grep -q "password: $PW" <<<"$out" || fail "synctl credentials: $out"
echo "  ✓ synctl integrations git-server and credentials"

echo "== turn off and on"
code=$(x sc-e2e-ctl curl -s -o /dev/null -w '%{http_code}' -b /tmp/jar -H 'content-type: application/json' -H 'Origin: http://localhost:7070' -X PUT $P/gitserver -d '{"enabled":false}')
[ "$code" = 409 ] || fail "turned off while a service builds from it ($code)"
api -X DELETE "$SVC/git" >/dev/null
api -X PUT $P/gitserver -d '{"enabled":false}' | grep -q '"state":"off"' || fail "disable"
for _ in $(seq 1 30); do x sc-e2e-ctl docker ps -a --format '{{.Names}}' | grep -q syncloud-git || break; sleep 1; done
x sc-e2e-ctl docker ps -a --format '{{.Names}}' | grep -q syncloud-git && fail "the container stayed"
api $P/system/tasks | grep -q sys-git && fail "still listed"
api $P/integrations/git | grep -q '"name":"git"' && fail "connection stayed"
x sc-e2e-ctl docker volume ls -q | grep -q '^syncloud-git$' || fail "the data volume was removed"
api -X PUT $P/gitserver -d '{"enabled":true}' >/dev/null
for _ in $(seq 1 90); do api $P/gitserver | grep -q '"state":"ready"' && break; sleep 2; done
api $P/gitserver | grep -q '"state":"ready"' || fail "not ready again: $(api $P/gitserver)"
api "$P/integrations/git/git/repos" | grep -q '"fullName":"syncloud/web"' || fail "the repository did not survive"
echo "  ✓ off removes the container and connection and keeps the data; on again brings the repositories back"

echo "PASS"
