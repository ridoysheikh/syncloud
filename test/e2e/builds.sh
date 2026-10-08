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
x $E2E-ctl docker load -q -i /opt/sc/buildkit.tar >/dev/null
wait_mesh

echo "== git repository"
x $E2E-ctl sh -c '
  set -e
  git config --global user.email e2e@example.com && git config --global user.name e2e && git config --global init.defaultBranch main
  mkdir -p /srv/git && git init -q --bare /srv/git/web.git
  git clone -q /srv/git/web.git /src/web 2>/dev/null
  cd /src/web && mkdir app
  printf "FROM busybox:1.37\nCOPY version /www/version\nCMD [\"httpd\", \"-f\", \"-p\", \"8080\", \"-h\", \"/www\"]\n" > app/Dockerfile
  echo one > app/version
  git add -A && git commit -qm one && git push -q origin main'
x -d $E2E-ctl sh -c "/opt/sc/gitserver -root /srv/git -listen 127.0.0.1:3000 > /var/log/gitserver.log 2>&1"
for _ in $(seq 1 20); do x $E2E-ctl curl -fs "localhost:3000/web.git/info/refs?service=git-upload-pack" >/dev/null 2>&1 && break; sleep 0.5; done
SHA1=$(x $E2E-ctl git -C /src/web rev-parse HEAD)

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
    echo "$b" | grep -q '"status":"\(succeeded\|failed\|skipped\)"' && break
    sleep 2
  done
  echo "$b" >&2
  x $E2E-ctl sh -c 'tail -20 /var/log/controller.log' >&2
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
        v=$(x $E2E-ctl docker exec "$id" wget -qO- localhost:8080/version 2>/dev/null || true)
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
api localhost:7070/api/v1/registry/repositories | grep -q '"name":"shop/web","tags":3' || fail "image tags (sha, latest-main, buildcache) missing in the registry"
wait_serving "@registry/shop/web:${SHA1:0:12}" one
echo "  ✓ auto-deployed: the service serves version one"
cid=$(api "$SVC/tasks" | grep -o '"desired":"running","state":"running","ip":"[^"]*","containerId":"[^"]*"' | cut -d'"' -f16 | head -1)
envs=$(x $E2E-ctl docker exec "$cid" env)
echo "$envs" | grep -qx 'GREETING=hello' || fail "shared variable missing: $envs"
echo "$envs" | grep -qx 'MODE=own' || fail "the service's own variable must win: $envs"
echo "  ✓ the task has the shared variables, and the service's own value wins"

echo "== webhook"
x $E2E-ctl sh -c 'cd /src/web && echo two > app/version && git commit -qam two && git push -q origin main'
SHA2=$(x $E2E-ctl git -C /src/web rev-parse HEAD)
body='{"ref":"refs/heads/main"}'
code=$(x $E2E-ctl curl -s -o /dev/null -w '%{http_code}' -H 'X-Hub-Signature-256: sha256=00' -H 'X-GitHub-Event: push' "localhost:7070$HOOK" -d "$body")
[ "$code" = 401 ] || fail "badly signed webhook answered $code"
sig=$(printf '%s' "$body" | openssl dgst -sha256 -hmac "$SECRET" | sed 's/^.* //')
code=$(x $E2E-ctl curl -s -o /dev/null -w '%{http_code}' -H "X-Hub-Signature-256: sha256=$sig" -H 'X-GitHub-Event: push' "localhost:7070$HOOK" -d "$body")
[ "$code" = 202 ] || fail "signed webhook answered $code"
b=$(wait_build "$SHA2" succeeded)
echo "$b" | grep -q '"trigger":"webhook"' || fail "not triggered by the webhook: $b"
wait_serving "@registry/shop/web:${SHA2:0:12}" two
echo "  ✓ signed webhook (bad signature refused) built and deployed version two"

echo "== broken commit"
x $E2E-ctl sh -c 'cd /src/web && echo "RUN exit 3" >> app/Dockerfile && git commit -qam broken && git push -q origin main'
SHA3=$(x $E2E-ctl git -C /src/web rev-parse HEAD)
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

echo "== watch paths"
# hook: a signed push webhook, which makes the controller check the repository now.
hook() {
  local body='{"ref":"refs/heads/main"}' sig
  sig=$(printf '%s' "$body" | openssl dgst -sha256 -hmac "$SECRET" | sed 's/^.* //')
  x $E2E-ctl curl -fs -o /dev/null -H "X-Hub-Signature-256: sha256=$sig" -H 'X-GitHub-Event: push' "localhost:7070$HOOK" -d "$body" || fail "webhook refused"
}
src=$(api -X PUT "$SVC/git" -d '{"url":"http://127.0.0.1:3000/web.git","branch":"main","tags":"v*","context":"app","paths":["app/**","!app/docs/**"],"pollSeconds":15}')
echo "$src" | grep -q '"paths":\["app/\*\*","!app/docs/\*\*"\]' || fail "paths not saved: $src"
api -X PUT "$SVC/git" -d '{"url":"http://127.0.0.1:3000/web.git","paths":["a b"]}' >/dev/null 2>&1 && fail "an invalid path filter was accepted"
x $E2E-ctl sh -c 'cd /src/web && mkdir -p app/docs && echo notes > app/docs/notes.md && echo readme > README.md && git add -A && git commit -qm docs && git push -q origin main'
SHA4=$(x $E2E-ctl git -C /src/web rev-parse HEAD)
hook
b=$(wait_build "$SHA4" skipped)
echo "$b" | grep -q '"deployed":false' || fail "a skipped build was deployed"
x $E2E-ctl sh -c 'cd /src/web && sed -i "/RUN exit 3/d" app/Dockerfile && echo three > app/version && git commit -qam three && git push -q origin main'
SHA5=$(x $E2E-ctl git -C /src/web rev-parse HEAD)
hook
b=$(wait_build "$SHA5" succeeded)
echo "$b" | grep -q "\"baseSha\":\"$SHA4\"" || fail "not compared with the previous commit: $b"
wait_serving "@registry/shop/web:${SHA5:0:12}" three
echo "  ✓ a commit changing only excluded or unwatched paths is skipped; one changing app/ builds and deploys"

echo "== tags"
x $E2E-ctl sh -c 'cd /src/web && git checkout -qb release && echo four > app/version && git commit -qam four && git tag -a v1.0.0 -m "release one" && git push -q origin v1.0.0 && git checkout -q main'
SHA6=$(x $E2E-ctl git -C /src/web rev-parse 'v1.0.0^{commit}')
hook
b=$(wait_build "$SHA6" succeeded)
echo "$b" | grep -q '"ref":"refs/tags/v1.0.0"' || fail "not built from the tag: $b"
api "localhost:7070/api/v1/registry/images?repository=shop/web" | grep -q '"tag":"v1.0.0"' || fail "image not tagged v1.0.0"
wait_serving "@registry/shop/web:${SHA6:0:12}" four
api "$SVC/git" | grep -q "\"v1.0.0\":\"$SHA6\"" || fail "watched refs not listed"
echo "  ✓ a new tag matching v* is built (annotated tag peeled to its commit) and pushed as :v1.0.0"

echo "== static site (no Dockerfile)"
x $E2E-ctl sh -c '
  set -e
  git init -q --bare /srv/git/site.git && git clone -q /srv/git/site.git /src/site 2>/dev/null
  cd /src/site && echo "<h1>static ok</h1>" > index.html && git add -A && git commit -qm site && git push -q origin main'
SITE=localhost:7070/api/v1/projects/shop/environments/production/services/site
api -X PUT "$SITE" -d '{"image":"@build","ports":[{"container":80}],"resources":{"cpu":0.05,"memory":32}}' >/dev/null
api -X PUT "$SITE/git" -d '{"url":"http://127.0.0.1:3000/site.git","branch":"main"}' >/dev/null
SSHA=$(x $E2E-ctl git -C /src/site rev-parse HEAD)
for _ in $(seq 1 200); do api "$SITE/builds" | grep -q '"status":"\(succeeded\|failed\)"' && break; sleep 2; done
api "$SITE/builds" | grep -q "\"sha\":\"$SSHA\"[^}]*\"status\":\"succeeded\"" || { api "$SITE/builds"; fail "static site build failed"; }
ok=0
for _ in $(seq 1 60); do
  cid=$(api "$SITE/tasks" | grep -o '"desired":"running","state":"running","ip":"[^"]*","containerId":"[^"]*"' | cut -d'"' -f16 | head -1 || true)
  [ -n "$cid" ] && x $E2E-ctl docker exec "$cid" wget -qO- localhost:80/ 2>/dev/null | grep -q "static ok" && { ok=1; break; }
  sleep 2
done
[ $ok = 1 ] || fail "the static site is not served"
api -X DELETE "$SITE" >/dev/null
echo "  ✓ a repository with only index.html is built as a static site and served on port 80"

if [ "${WITH_NIXPACKS:-0}" = 1 ]; then # downloads Nixpacks and a Nix base image: slow
  echo "== nixpacks (Node, no Dockerfile)"
  x $E2E-ctl sh -c '
    set -e
    git init -q --bare /srv/git/node.git && git clone -q /srv/git/node.git /src/node 2>/dev/null
    cd /src/node
    printf "{\"name\":\"n\",\"version\":\"1.0.0\",\"scripts\":{\"start\":\"node index.js\"}}\n" > package.json
    echo "require(\"http\").createServer((q, s) => s.end(\"node ok\")).listen(3000)" > index.js
    git add -A && git commit -qm node && git push -q origin main'
  NODE=localhost:7070/api/v1/projects/shop/environments/production/services/node
  api -X PUT "$NODE" -d '{"image":"@build","ports":[{"container":3000}],"resources":{"cpu":0.1,"memory":64}}' >/dev/null
  api -X PUT "$NODE/git" -d '{"url":"http://127.0.0.1:3000/node.git","branch":"main"}' >/dev/null
  for _ in $(seq 1 450); do api "$NODE/builds" | grep -q '"status":"\(succeeded\|failed\)"' && break; sleep 2; done
  api "$NODE/builds" | grep -q '"status":"succeeded"' || { api "$NODE/builds"; fail "nixpacks build failed"; }
  ok=0
  for _ in $(seq 1 60); do
    cid=$(api "$NODE/tasks" | grep -o '"desired":"running","state":"running","ip":"[^"]*","containerId":"[^"]*"' | cut -d'"' -f16 | head -1 || true)
    [ -n "$cid" ] && x $E2E-ctl docker exec "$cid" sh -c 'wget -qO- localhost:3000 || curl -fs localhost:3000' 2>/dev/null | grep -q "node ok" && { ok=1; break; }
    sleep 2
  done
  [ $ok = 1 ] || fail "the Nixpacks-built app is not served"
  api -X DELETE "$NODE" >/dev/null
  echo "  ✓ a Node app without a Dockerfile is built with Nixpacks and served"
fi

echo "== build variables and after-build checks"
BS="$SVC/git/build-settings"
api -X PUT "$BS" -d '{"variables":{"BUILD_NOTE":"from-a-build-var"},"postBuild":["test \"$(cat /www/version)\" = five","test \"$GREETING\" = hello"]}' >/dev/null
api "$BS" | grep -q 'from-a-build-var' && fail "a build variable value was returned"
api "$BS" | grep -q '"variables":\["BUILD_NOTE"\]' || fail "build variable names: $(api "$BS")"
x "$E2E-ctl" sh -c 'cd /src/web && printf "ARG BUILD_NOTE=none\nRUN mkdir -p /www && echo \"\$BUILD_NOTE\" > /www/note\n" >> app/Dockerfile && echo five > app/version && git commit -qam five && git push -q origin main'
SHA7=$(x "$E2E-ctl" git -C /src/web rev-parse HEAD)
hook
b=$(wait_build "$SHA7" succeeded)
echo "$b" | grep -q '"checkRunId":"run_' || fail "no after-build check run: $b"
wait_serving "@registry/shop/web:${SHA7:0:12}" five
cid=$(api "$SVC/tasks" | grep -o '"desired":"running","state":"running","ip":"[^"]*","containerId":"[^"]*"' | cut -d'"' -f16 | head -1)
[ "$(x "$E2E-ctl" docker exec "$cid" cat /www/note)" = from-a-build-var ] || fail "the build variable did not reach the Dockerfile"
echo "  ✓ a build variable became a Dockerfile build argument; the checks passed in the new image (with the service's variables) before it deployed"
x "$E2E-ctl" sh -c 'cd /src/web && echo six > app/version && git commit -qam six && git push -q origin main'
SHA8=$(x "$E2E-ctl" git -C /src/web rev-parse HEAD)
hook
b=$(wait_build "$SHA8" failed)
echo "$b" | grep -q 'after-build check failed (exit 1)' || fail "failure reason: $b"
echo "$b" | grep -q '"deployed":false' || fail "a build whose checks failed was deployed"
wait_serving "@registry/shop/web:${SHA7:0:12}" five
api -X PUT "$BS" -d '{"variables":{"BUILD_NOTE":null},"postBuild":[]}' | grep -q '"postBuild":\[\]' || fail "checks not removed"
echo "  ✓ a build whose after-build check fails is not deployed; version five keeps serving"

echo "== disconnect"
api -X DELETE "$SVC/git" >/dev/null
api "$SVC/git" >/dev/null 2>&1 && fail "source still present"
code=$(x $E2E-ctl curl -s -o /dev/null -w '%{http_code}' -X POST "localhost:7070$HOOK" -d '{}')
[ "$code" = 404 ] || fail "webhook of a removed source answered $code"
echo "  ✓ disconnected; its webhook is gone"
echo "PASS"
