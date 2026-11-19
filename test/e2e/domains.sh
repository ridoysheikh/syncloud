#!/usr/bin/env bash
# Real-user test of domains and certificates (Phase 28): a person in a real
# browser (Chrome) connects Cloudflare, registers domains, points names at
# the cluster, requests wildcard certificates, imports a provider's
# certificate, adds an authority and chooses a domain for a service
# (test/e2e/ui/domains.mjs). The outside world is domainsim: a fake
# Cloudflare, DNS server and ACME CA that agree with each other. It runs a
# dev controller on this machine, so it needs no Docker.
#
#   test/e2e/domains.sh                   # run and clean up
#   KEEP=1 test/e2e/domains.sh            # leave the controller and simulator running
#   UI_SHOTS=./shots test/e2e/domains.sh  # where the screenshots go (default /tmp/syncloud-ui-shots)
#
# Needs Go, Node, Chrome (CHROME_PATH if it is not in the usual place) and openssl.
set -euo pipefail
cd "$(dirname "${BASH_SOURCE[0]}")/../.."
fail() { echo "FAIL: $*" >&2; exit 1; }
command -v node >/dev/null || fail "Node is required for the browser tests"
command -v openssl >/dev/null || fail "openssl is required to make the provider's certificate"
export UI_SHOTS=${UI_SHOTS:-/tmp/syncloud-ui-shots}
UI=test/e2e/ui
T=$(mktemp -d)
PIDS=()
cleanup() {
  [ "${KEEP:-0}" = 1 ] && { echo "left running: $T (pids ${PIDS[*]})"; return; }
  for p in "${PIDS[@]}"; do kill "$p" 2>/dev/null || true; done
  rm -rf "$T"
}
trap cleanup EXIT
free_port() { python3 -c 'import socket; s=socket.socket(); s.bind(("127.0.0.1",0)); print(s.getsockname()[1]); s.close()'; }

echo "== build"
make -s web >/dev/null
git checkout -- internal/web/dist/.gitkeep 2>/dev/null || true
go build -o "$T/controller" ./cmd/controller
go build -o "$T/domainsim" ./test/e2e/domainsim
[ -d "$UI/node_modules" ] || (cd "$UI" && npm ci --silent)

echo "== the simulated outside world"
CF=$(free_port); ACME=$(free_port); DNS=$(free_port); CTRL=$(free_port); ADNS=$(free_port)
"$T/domainsim" --cloudflare "127.0.0.1:$CF" --acme "127.0.0.1:$ACME" --dns "127.0.0.1:$DNS" --control "127.0.0.1:$CTRL" --forward "acme.example.test=127.0.0.1:$ADNS" >"$T/domainsim.log" 2>&1 &
PIDS+=($!)

echo "== the controller"
PORT=$(free_port); AGENT=$(free_port)
mkdir -p "$T/data"
"$T/controller" --dev --data-dir "$T/data" --listen "127.0.0.1:$PORT" --agent-listen "127.0.0.1:$AGENT" --agent-advertise "127.0.0.1:$AGENT" \
  --system-tasks=false --base-domain off \
  --cloudflare-api "http://127.0.0.1:$CF/client/v4" --dns-resolvers "127.0.0.1:$DNS" --acme-dns-listen "127.0.0.1:$ADNS" --acme-directory "http://127.0.0.1:$ACME/directory" >"$T/controller.log" 2>&1 &
PIDS+=($!)
for _ in $(seq 1 60); do curl -fs "127.0.0.1:$PORT/api/v1/system/status" >/dev/null 2>&1 && break; sleep 0.5; done
curl -fs "127.0.0.1:$PORT/api/v1/system/status" >/dev/null || { tail -20 "$T/controller.log"; fail "the controller did not start"; }
TOK=$(cat "$T/data/setup-token"); SUF=$(tr -d '-' <"$T/data/recovery-key" | tail -c 7 | tr -d '\n')
curl -fs -H 'content-type: application/json' "127.0.0.1:$PORT/api/v1/setup" \
  -d "{\"setupToken\":\"$TOK\",\"email\":\"e2e@example.com\",\"name\":\"E2E\",\"password\":\"e2e-password-123\",\"recoveryKeySuffix\":\"$SUF\"}" >/dev/null

echo "== a person manages domains and certificates"
rm -rf "$UI_SHOTS"
UI_URL="http://127.0.0.1:$PORT" SIM_URL="http://127.0.0.1:$CTRL" ACME_DIRECTORY="http://127.0.0.1:$ACME/directory" node "$UI/domains.mjs" || { echo "screenshots: $UI_SHOTS; controller log: $T/controller.log" >&2; tail -20 "$T/controller.log" >&2; fail "the browser journey failed"; }
echo "screenshots: $UI_SHOTS"
