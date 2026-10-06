#!/usr/bin/env bash
# End-to-end test of Git builds (§5.8): connect a service to a repository on a
# smart-HTTP Git server, BuildKit builds the commit as a privileged job run on
# ctl-0 and pushes it to the private registry, auto-deploy rolls it out; a
# signed webhook builds the next commit; a broken commit fails without
# deploying; an older build is redeployed by hand.
. "$(dirname "$0")/lib.sh"
WITH_REGISTRY=1
WITH_BUILDS=1
CTL_FLAGS="--registry-pull-host 127.0.0.1:5000 --build-node ctl-0"
trap cleanup EXIT
setup_cluster
start_registry
x sc-e2e-ctl docker load -q -i /opt/sc/buildkit.tar >/dev/null
wait_mesh

echo "== git repository"
x sc-e2e-ctl sh -c '
  set -e
  git config --global user.email e2e@example.com && git config --global user.name e2e && git config --global init.defaultBranch main
  mkdir -p /srv/git && git init -q --bare /srv/git/web.git
  git clone -q /srv/git/web.git /src/web 2>/dev/null
  cd /src/web && mkdir app
  printf "FROM busybox:1.37\nCOPY version /www/version\nCMD [\"httpd\", \"-f\", \"-p\", \"8080\", \"-h\", \"/www\"]\n" > app/Dockerfile
  echo one > app/version
  git add -A && git commit -qm one && git push -q origin main'
x -d sc-e2e-ctl sh -c "/opt/sc/gitserver -root /srv/git -listen 127.0.0.1:3000 > /var/log/gitserver.log 2>&1"
for _ in $(seq 1 20); do x sc-e2e-ctl curl -fs "localhost:3000/web.git/info/refs?service=git-upload-pack" >/dev/null 2>&1 && break; sleep 0.5; done
SHA1=$(x sc-e2e-ctl git -C /src/web rev-parse HEAD)

# Only ctl-0 runs services, so the deployed task is local.
for w in w1 w2; do
  id=$(api localhost:7070/api/v1/nodes | grep -o "\"id\":\"node_[a-z0-9]*\",\"name\":\"$w\"" | cut -d'"' -f4)
  api -X PUT "localhost:7070/api/v1/nodes/$id/schedulable" -d '{"schedulable":false}' >/dev/null
done
api localhost:7070/api/v1/projects -d '{"name":"shop"}' >/dev/null
SVC=localhost:7070/api/v1/projects/shop/environments/production/services/web
# Created like the dashboard wizard does for Git: no image until the first build.
api -X PUT "$SVC" -d '{"image":"@build","command":["httpd","-f","-p","8080","-h","/www"],"ports":[{"container":8080}],"resources":{"cpu":0.05,"memory":16}}' >/dev/null
for _ in $(seq 1 20); do api "$SVC" | grep -q '"status":"waiting for the first build"' && break; sleep 0.5; done
api "$SVC" | grep -q '"status":"waiting for the first build"' || fail "a service without a build is not waiting for it"
api -X PUT localhost:7070/api/v1/projects/shop/environments/production/variables -d '{"variables":{"GREETING":"hello","MODE":"shared"}}' >/dev/null
api -X PUT "$SVC" -d '{"image":"@build","command":["httpd","-f","-p","8080","-h","/www"],"ports":[{"container":8080}],"env":{"MODE":"own"},"resources":{"cpu":0.05,"memory":16}}' >/dev/null
echo "  ✓ service waits for its first build; shared variables set"

echo "== connect"
api -X PUT "$SVC/git" -d '{"url":"http://127.0.0.1:3000/nope.git"}' >/dev/null 2>&1 && fail "a missing repository was accepted"
src=$(api -X PUT "$SVC/git" -d '{"url":"http://127.0.0.1:3000/web.git","branch":"main","context":"app","pollSeconds":15}')
echo "$src" | grep -q '"webhookPath":"/api/v1/hooks/git/git_' || fail "bad source: $src"
HOOK=$(echo "$src" | grep -o '"webhookPath":"[^"]*"' | cut -d'"' -f4)
SECRET=$(echo "$src" | grep -o '"webhookSecret":"[^"]*"' | cut -d'"' -f4)
echo "  ✓ repository checked (ls-remote) and connected; a missing one is refused"

# wait_build SHA STATUS: until the build of SHA reaches STATUS.
wait_build() {
  local b
  for _ in $(seq 1 150); do
    b=$(api "$SVC/builds" | grep -o "{[^{}]*\"sha\":\"$1\"[^{}]*}" || true)
    echo "$b" | grep -q "\"status\":\"$2\"" && { echo "$b"; return; }
    echo "$b" | grep -q '"status":"\(succeeded\|failed\)"' && break
    sleep 2
  done
  echo "$b" >&2
  x sc-e2e-ctl sh -c 'tail -20 /var/log/controller.log' >&2
  fail "build of ${1:0:12} did not reach $2"
}
# wait_serving IMAGE VERSION: until the service's spec is IMAGE and every
# running task serves VERSION.
wait_serving() {
  local ids v ok
  for _ in $(seq 1 60); do
    ok=0
    if api "$SVC" | grep -q "\"image\":\"$1\""; then
      ids=$(api "$SVC/tasks" | grep -o '"desired":"running","state":"running","ip":"[^"]*","containerId":"[^"]*"' | cut -d'"' -f16 || true)
      ok=1; [ -n "$ids" ] || ok=0
      for id in $ids; do
        v=$(x sc-e2e-ctl docker exec "$id" wget -qO- localhost:8080/version 2>/dev/null || true)
        [ "$v" = "$2" ] || ok=0
      done
    fi
    [ $ok = 1 ] && return
    sleep 2
  done
  api "$SVC" >&2; api "$SVC/tasks" >&2
  fail "service does not serve $2 from $1"
}

echo "== first build"
b=$(wait_build "$SHA1" succeeded)
echo "$b" | grep -q "\"image\":\"@registry/shop/web:${SHA1:0:12}\"" || fail "image name: $b"
echo "$b" | grep -q '"deployed":true' || fail "not auto-deployed: $b"
RUN1=$(echo "$b" | grep -o '"runId":"[^"]*"' | cut -d'"' -f4)
BUILD1=$(echo "$b" | grep -o '"id":"bld_[^"]*"' | cut -d'"' -f4)
api "localhost:7070/api/v1/runs/$RUN1" | grep -q '"trigger":"build"' || fail "build did not run as a build job"
echo "  ✓ BuildKit built ${SHA1:0:12} on ctl-0 and pushed it to the private registry"
api localhost:7070/api/v1/registry/repositories | grep -q '"name":"shop/web","tags":2' || fail "image tags missing in the registry"
wait_serving "@registry/shop/web:${SHA1:0:12}" one
echo "  ✓ auto-deployed: the service serves version one"
cid=$(api "$SVC/tasks" | grep -o '"desired":"running","state":"running","ip":"[^"]*","containerId":"[^"]*"' | cut -d'"' -f16 | head -1)
envs=$(x sc-e2e-ctl docker exec "$cid" env)
echo "$envs" | grep -qx 'GREETING=hello' || fail "shared variable missing: $envs"
echo "$envs" | grep -qx 'MODE=own' || fail "the service's own variable must win: $envs"
echo "  ✓ the task has the shared variables, and the service's own value wins"

echo "== webhook"
x sc-e2e-ctl sh -c 'cd /src/web && echo two > app/version && git commit -qam two && git push -q origin main'
SHA2=$(x sc-e2e-ctl git -C /src/web rev-parse HEAD)
body='{"ref":"refs/heads/main"}'
code=$(x sc-e2e-ctl curl -s -o /dev/null -w '%{http_code}' -H 'X-Hub-Signature-256: sha256=00' -H 'X-GitHub-Event: push' "localhost:7070$HOOK" -d "$body")
[ "$code" = 401 ] || fail "badly signed webhook answered $code"
sig=$(printf '%s' "$body" | openssl dgst -sha256 -hmac "$SECRET" | sed 's/^.* //')
code=$(x sc-e2e-ctl curl -s -o /dev/null -w '%{http_code}' -H "X-Hub-Signature-256: sha256=$sig" -H 'X-GitHub-Event: push' "localhost:7070$HOOK" -d "$body")
[ "$code" = 202 ] || fail "signed webhook answered $code"
b=$(wait_build "$SHA2" succeeded)
echo "$b" | grep -q '"trigger":"webhook"' || fail "not triggered by the webhook: $b"
wait_serving "@registry/shop/web:${SHA2:0:12}" two
echo "  ✓ signed webhook (bad signature refused) built and deployed version two"

echo "== broken commit"
x sc-e2e-ctl sh -c 'cd /src/web && echo "RUN exit 3" >> app/Dockerfile && git commit -qam broken && git push -q origin main'
SHA3=$(x sc-e2e-ctl git -C /src/web rev-parse HEAD)
api -X POST "$SVC/builds" -d '{}' >/dev/null
b=$(wait_build "$SHA3" failed)
echo "$b" | grep -q '"deployed":false' || fail "a failed build was deployed"
wait_serving "@registry/shop/web:${SHA2:0:12}" two
echo "  ✓ a failing build is reported and not deployed"

echo "== redeploy and rebuild"
api -X POST "$SVC/builds" -d "{\"sha\":\"$SHA1\"}" >/dev/null 2>&1 && fail "the same commit was built twice"
api -X POST "localhost:7070/api/v1/builds/$BUILD1/deploy" >/dev/null
wait_serving "@registry/shop/web:${SHA1:0:12}" one
echo "  ✓ an earlier build redeployed by hand; a commit is never built twice"

echo "== disconnect"
api -X DELETE "$SVC/git" >/dev/null
api "$SVC/git" >/dev/null 2>&1 && fail "source still present"
code=$(x sc-e2e-ctl curl -s -o /dev/null -w '%{http_code}' -X POST "localhost:7070$HOOK" -d '{}')
[ "$code" = 404 ] || fail "webhook of a removed source answered $code"
echo "  ✓ disconnected; its webhook is gone"
echo "PASS"
