#!/usr/bin/env bash
# Real-user test: a person uses the dashboard in a real browser (Chrome) on a
# cluster of real nodes. They sign in, make a project, start a web service and
# a public TCP service with the new-service wizard, meet a port that is taken,
# look the port up on Network → Ports, and delete the service. A teammate with
# a developer role gets the same live port check. The lab is asked only to
# prove the services answer on their addresses (test/e2e/ui/journey.mjs).
#
#   test/e2e/ui.sh                 # run and clean up
#   KEEP=1 test/e2e/ui.sh          # leave the nodes running for inspection
#   UI_SHOTS=./shots test/e2e/ui.sh # where the screenshots go (default /tmp/syncloud-ui-shots)
#
# Needs Docker, Node and Chrome (CHROME_PATH if it is not in the usual place).
export E2E_PREFIX=${E2E_PREFIX:-sc-ui}
WITH_TRAEFIK=1
WORKERS=${WORKERS-w1}
CTL_FLAGS="--public-ports 20000-20001"
. "$(dirname "$0")/lib.sh"
trap cleanup EXIT
UI=test/e2e/ui
command -v node >/dev/null || fail "Node is required for the browser tests"
export UI_SHOTS=${UI_SHOTS:-/tmp/syncloud-ui-shots}

# The controller embeds the dashboard: build the current one first.
make -s web >/dev/null
[ -d "$UI/node_modules" ] || (cd "$UI" && npm ci --silent)

setup_cluster
# Traefik as the system task runs it, with the entrypoints of the ports the
# journey uses: 2525 (chosen) and the first automatic ones.
x "$E2E-ctl" docker load -q -i /opt/sc/traefik.tar >/dev/null
TOK=$(x "$E2E-ctl" cat /data/traefik.token)
x "$E2E-ctl" docker run -d --name traefik --network host traefik:v3.7.13 \
  --entrypoints.web.address=:8080 --entrypoints.websecure.address=:8443 \
  --entrypoints.tcp-2525.address=:2525 --entrypoints.tcp-20000.address=:20000 \
  --providers.http.endpoint=http://127.0.0.1:7070/internal/traefik/config --providers.http.pollInterval=1s \
  "--providers.http.headers.X-Syncloud-Token=$TOK" >/dev/null
wait_mesh

echo "== a person uses the dashboard"
rm -rf "$UI_SHOTS"
UI_URL="http://$CTL_IP:7070" node "$UI/journey.mjs" || { echo "screenshots: $UI_SHOTS" >&2; fail "the browser journey failed"; }
echo "screenshots: $UI_SHOTS"
