#!/usr/bin/env bash
# End-to-end test of Git connections (§5.8) and the global Traefik settings
# (§5.7), against a real Gitea: a token connection is checked and saved
# sealed; repositories and branches are listed; a service picks a
# repository by name, SynCloud creates the push webhook on it, builds the
# first commit and reports commit statuses; a push is delivered by Gitea's
# webhook (through Traefik, with the new settings) and deployed;
# disconnecting removes the webhook; connections in use cannot be removed.
#
#   test/e2e/integrations.sh          # run and clean up
#   KEEP=1 test/e2e/integrations.sh   # leave the nodes running
WITH_REGISTRY=1
WITH_BUILDS=1
WITH_TRAEFIK=1
GITEA=gitea/gitea:1.24-rootless
CTL_FLAGS="--registry-pull-host 127.0.0.1:5000 --build-node ctl-0"
. "$(dirname "$0")/lib.sh"
trap cleanup EXIT
docker image inspect $GITEA >/dev/null 2>&1 || docker pull -q $GITEA >/dev/null
setup_cluster
docker save $GITEA -o "$BIN/gitea.tar"
start_registry
start_traefik
x sc-e2e-ctl docker load -q -i /opt/sc/buildkit.tar >/dev/null
x sc-e2e-ctl docker load -q -i /opt/sc/gitea.tar >/dev/null
wait_mesh

P=localhost:7070/api/v1
G=http://127.0.0.1:3001/api/v1

echo "== gitea"
# Webhooks to private addresses are off by default in Gitea; the dashboard
# here is http://localhost:8080 (Traefik).
x sc-e2e-ctl docker run -d --name gitea --network host \
  -e GITEA__server__HTTP_PORT=3001 -e GITEA__server__ROOT_URL=http://127.0.0.1:3001/ -e GITEA__server__DISABLE_SSH=true \
  -e GITEA__database__DB_TYPE=sqlite3 -e GITEA__security__INSTALL_LOCK=true -e GITEA__webhook__ALLOWED_HOST_LIST='*' \
  -e GITEA__webhook__DELIVER_TIMEOUT=10 -e GITEA__repository__DEFAULT_BRANCH=main $GITEA >/dev/null
for _ in $(seq 1 60); do x sc-e2e-ctl curl -fs $G/version >/dev/null 2>&1 && break; sleep 1; done
x sc-e2e-ctl docker exec gitea gitea admin user create --admin --username e2e --password e2e-password-123 --email e2e@example.com --must-change-password=false >/dev/null
ga() { x sc-e2e-ctl curl -fsS -u e2e:e2e-password-123 -H 'content-type: application/json' "$@"; }
TOKEN=$(ga -X POST $G/users/e2e/tokens -d '{"name":"syncloud","scopes":["write:repository","read:user"]}' | grep -o '"sha1":"[^"]*"' | cut -d'"' -f4)
[ -n "$TOKEN" ] || fail "no Gitea token"
ga -X POST $G/user/repos -d '{"name":"web","private":true}' >/dev/null
x sc-e2e-ctl sh -c '
  set -e
  git config --global user.email e2e@example.com && git config --global user.name e2e && git config --global init.defaultBranch main
  mkdir -p /src/web/app && cd /src/web && git init -q
  printf "FROM busybox:1.37\nCOPY version /www/version\nCMD [\"httpd\", \"-f\", \"-p\", \"8080\", \"-h\", \"/www\"]\n" > app/Dockerfile
  echo one > app/version
  git add -A && git commit -qm one
  git push -q http://e2e:e2e-password-123@127.0.0.1:3001/e2e/web.git main'
SHA1=$(x sc-e2e-ctl git -C /src/web rev-parse HEAD)
echo "  ✓ Gitea runs with a private repository e2e/web"

echo "== connection"
code=$(x sc-e2e-ctl curl -s -o /tmp/out -w '%{http_code}' -b /tmp/jar -H 'content-type: application/json' -H 'Origin: http://localhost:7070' \
  $P/integrations/git -d '{"kind":"gitea","name":"forge","url":"http://127.0.0.1:3001","token":"wrong"}')
[ "$code" = 400 ] || fail "a wrong token was accepted ($code)"
out=$(api $P/integrations/git -d "{\"kind\":\"gitea\",\"name\":\"forge\",\"url\":\"http://127.0.0.1:3001\",\"token\":\"$TOKEN\"}")
grep -q '"account":"e2e"' <<<"$out" || fail "connection: $out"
api $P/integrations/git | grep -q "$TOKEN" && fail "the token is returned by the API"
out=$(api "$P/integrations/git/forge/repos?q=we")
grep -q '"fullName":"e2e/web","cloneUrl":"http://127.0.0.1:3001/e2e/web.git"' <<<"$out" && grep -q '"private":true' <<<"$out" || fail "repos: $out"
# Gitea indexes branches a moment after a push.
for _ in $(seq 1 20); do api "$P/integrations/git/forge/branches?repo=e2e/web" | grep -q '"items":\["main"\]' && break; sleep 1; done
api "$P/integrations/git/forge/branches?repo=e2e/web" | grep -q '"items":\["main"\]' || fail "branches"
echo "  ✓ a wrong token is refused; the connection lists private repositories and branches; the token is never returned"

echo "== traefik settings"
out=$(api -X PUT $P/traefik/settings -d '{"redirectHttps":false,"minTls":"1.3","retryAttempts":3,"compress":true,"maxBodyMb":8,"hstsSeconds":600,"trustedIPs":["10.0.0.0/8"],"readTimeout":"90s"}')
grep -q '"restarted":true' <<<"$out" || fail "static settings did not report a restart: $out"
grep -q -- '--entrypoints.websecure.forwardedHeaders.trustedIPs=10.0.0.0/8' <<<"$out" || fail "static flags: $out"
code=$(x sc-e2e-ctl curl -s -o /dev/null -w '%{http_code}' -b /tmp/jar -H 'content-type: application/json' -H 'Origin: http://localhost:7070' -X PUT $P/traefik/settings -d '{"proxyProtocol":true}')
[ "$code" = 400 ] || fail "PROXY protocol without trusted proxies accepted ($code)"
cfg=$(api $P/traefik/config)
grep -q '"options":{"default":{"minVersion":"VersionTLS13"}}' <<<"$cfg" || fail "tls options: $cfg"
echo "  ✓ settings saved and validated; TLS options and default middlewares are served"

echo "== service from a connected repository"
api $P/projects -d '{"name":"shop"}' >/dev/null
for w in w1 w2; do
  id=$(api $P/nodes | grep -o "\"id\":\"node_[a-z0-9]*\",\"name\":\"$w\"" | cut -d'"' -f4)
  api -X PUT "$P/nodes/$id/schedulable" -d '{"schedulable":false}' >/dev/null
done
SVC=$P/projects/shop/environments/production/services/web
api -X PUT "$SVC" -d '{"image":"@build","command":["httpd","-f","-p","8080","-h","/www"],"ports":[{"container":8080}],"resources":{"cpu":0.05,"memory":16}}' >/dev/null
src=$(api -X PUT "$SVC/git" -d '{"connection":"forge","repo":"e2e/web","context":"app","pollSeconds":300}')
grep -q '"branch":"main"' <<<"$src" && grep -q '"webhook":"created"' <<<"$src" && grep -q '"connection":"forge","repo":"e2e/web"' <<<"$src" || fail "source: $src"
hooks=$(ga $G/repos/e2e/web/hooks)
grep -q '"url":"http://localhost:8080/api/v1/hooks/git/git_' <<<"$hooks" || fail "no webhook on the repository: $hooks"
echo "  ✓ the default branch was picked and SynCloud created the push webhook on the repository"

# wait_build SHA STATUS
wait_build() {
  local b
  for _ in $(seq 1 150); do
    b=$(api "$SVC/builds" | grep -o "{[^{}]*\"sha\":\"$1\"[^{}]*}" || true)
    grep -q "\"status\":\"$2\"" <<<"$b" && { echo "$b"; return; }
    grep -q '"status":"\(succeeded\|failed\|skipped\)"' <<<"$b" && break
    sleep 2
  done
  echo "$b" >&2
  x sc-e2e-ctl sh -c 'tail -20 /var/log/controller.log' >&2
  fail "build of ${1:0:12} did not reach $2"
}
# wait_status SHA STATE: until Gitea shows the commit status.
wait_status() {
  local s
  for _ in $(seq 1 30); do
    s=$(ga "$G/repos/e2e/web/commits/$1/status")
    grep -q "\"state\":\"$2\"" <<<"$s" && grep -q '"context":"syncloud/shop/production/web"' <<<"$s" && return
    sleep 1
  done
  fail "commit ${1:0:12} status is not $2: $s"
}
serving() {
  local cid
  for _ in $(seq 1 60); do
    cid=$(api "$SVC/tasks" | grep -o '"desired":"running","state":"running","ip":"[^"]*","containerId":"[^"]*"' | cut -d'"' -f16 | head -1 || true)
    [ -n "$cid" ] && [ "$(x sc-e2e-ctl docker exec "$cid" wget -qO- localhost:8080/version 2>/dev/null || true)" = "$1" ] && return
    sleep 2
  done
  fail "the service does not serve $1"
}

echo "== first build"
wait_build "$SHA1" succeeded >/dev/null
serving one
wait_status "$SHA1" success
desc=$(ga "$G/repos/e2e/web/commits/$SHA1/statuses")
grep -q '"description":"Built and deployed"' <<<"$desc" && grep -q '"target_url":"http://localhost:8080/projects/shop/production/services/web?tab=builds"' <<<"$desc" || fail "statuses: $desc"
grep -q '"status":"pending"\|"state":"pending"' <<<"$desc" || fail "no pending status before success: $desc"
echo "  ✓ cloned with the connection's token, built, deployed; Gitea shows pending then success on the commit"
cfg=$(api $P/traefik/config)
grep -q '"middlewares":\["syncloud-body-limit","syncloud-hsts","syncloud-compress","syncloud-retry"\]' <<<"$cfg" || fail "route middlewares: $cfg"
grep -q '"retry":{"attempts":3' <<<"$cfg" || fail "retry attempts: $cfg"
echo "  ✓ the service route carries the global defaults (body limit, HSTS, compression, 3 retries)"

echo "== push through Gitea's webhook"
x sc-e2e-ctl sh -c 'cd /src/web && echo two > app/version && git commit -qam two && git push -q http://e2e:e2e-password-123@127.0.0.1:3001/e2e/web.git main'
SHA2=$(x sc-e2e-ctl git -C /src/web rev-parse HEAD)
b=$(wait_build "$SHA2" succeeded)
grep -q '"trigger":"webhook"' <<<"$b" || fail "not triggered by Gitea's webhook (polling is every 300s): $b"
serving two
wait_status "$SHA2" success
echo "  ✓ Gitea delivered the push through Traefik; built, deployed and reported in seconds"

echo "== broken commit"
x sc-e2e-ctl sh -c 'cd /src/web && echo "RUN exit 3" >> app/Dockerfile && git commit -qam broken && git push -q http://e2e:e2e-password-123@127.0.0.1:3001/e2e/web.git main'
SHA3=$(x sc-e2e-ctl git -C /src/web rev-parse HEAD)
wait_build "$SHA3" failed >/dev/null
wait_status "$SHA3" failure
echo "  ✓ a failing build shows as failure on the commit"

echo "== synctl"
key=$(api $P/iam/access-keys -d '{"description":"e2e"}')
KID=$(echo "$key" | grep -o '"id":"SYNAK[A-Z0-9]*"' | cut -d'"' -f4); KSEC=$(echo "$key" | grep -o '"secretAccessKey":"[^"]*"' | cut -d'"' -f4)
synctl() { x -i -e SYNCLOUD_ENDPOINT=http://127.0.0.1:7070 -e SYNCLOUD_ACCESS_KEY_ID="$KID" -e SYNCLOUD_SECRET_ACCESS_KEY="$KSEC" sc-e2e-ctl /opt/sc/synctl "$@"; }
out=$(synctl integrations git list </dev/null); grep -q 'forge *gitea *http://127.0.0.1:3001 *e2e *1' <<<"$out" || fail "synctl integrations git list: $out"
out=$(synctl integrations git repos forge </dev/null); grep -q 'e2e/web *main *private' <<<"$out" || fail "synctl repos: $out"
out=$(synctl builds source web -p shop </dev/null); grep -q 'e2e/web on forge' <<<"$out" && grep -q 'created on the repository by SynCloud' <<<"$out" || fail "synctl builds source: $out"
out=$(synctl traefik settings set compress=false retryAttempts=1 </dev/null); grep -q 'within 2 seconds' <<<"$out" && grep -q 'retryAttempts *1' <<<"$out" || fail "synctl traefik settings set: $out"
echo "  ✓ synctl integrations git list/repos, builds source, traefik settings set"

echo "== disconnect"
code=$(x sc-e2e-ctl curl -s -o /dev/null -w '%{http_code}' -b /tmp/jar -H 'Origin: http://localhost:7070' -X DELETE $P/integrations/git/forge)
[ "$code" = 409 ] || fail "a connection in use was removed ($code)"
api -X DELETE "$SVC/git" >/dev/null
ga $G/repos/e2e/web/hooks | grep -q 'hooks/git/git_' && fail "the webhook stayed on the repository"
api -X DELETE $P/integrations/git/forge >/dev/null
api $P/integrations/git | grep -q '"items":\[\]' || fail "connection not removed"
echo "  ✓ disconnecting removes the webhook; an unused connection can be removed"

x sc-e2e-ctl docker logs traefik 2>&1 | grep ' ERR ' | grep -iv 'acme\|certificate' | head -3 | grep . && fail "Traefik rejected the configuration"
echo "PASS"
