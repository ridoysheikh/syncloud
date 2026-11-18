#!/usr/bin/env bash
# End-to-end test of an operator upgrading a running cluster to this build
# from the previous release (Phase 26's promise: nothing changes for what
# exists). The previous release (a git worktree of its tag) builds and runs
# the cluster; services with public TCP and UDP ports are made through its
# API. Then this build's controller replaces the old one, with the old agents
# still running, and the operator checks that: the services keep serving and
# nothing was rescheduled, Traefik is handed the same routes, the same ports
# and addresses show, the host firewall opens the same ports, the port table
# is filled, and the new abilities (a chosen port) work on the old data.
#
#   test/e2e/upgrade-ports.sh                    # from the latest release tag
#   OLD_REF=v0.3.7 test/e2e/upgrade-ports.sh     # from a given tag or commit
#   KEEP=1 test/e2e/upgrade-ports.sh             # leave the nodes running
NEW_ROOT=$(cd "$(dirname "$0")/../.." && pwd)
OLD_REF=${OLD_REF:-$(git -C "$NEW_ROOT" describe --tags --abbrev=0)}
export E2E_PREFIX=${E2E_PREFIX:-sc-up}
WITH_TRAEFIK=1
WORKERS=${WORKERS-w1}
CTL_FLAGS="--public-ports 20000-20001"
OLD=$(mktemp -d)/old
git -C "$NEW_ROOT" worktree add -q --detach "$OLD" "$OLD_REF" || { echo "FAIL: no $OLD_REF" >&2; exit 1; }
# The previous release's own lab library: its binaries, its cluster.
. "$OLD/test/e2e/lib.sh"
cleanup() {
  [ "${KEEP:-0}" = 1 ] && { echo "nodes kept: ${NODES[*]}"; return; }
  docker rm -fv "${NODES[@]}" >/dev/null 2>&1 || true
  docker network rm "$NET" >/dev/null 2>&1 || true
  rm -rf "$BIN"
  git -C "$NEW_ROOT" worktree remove --force "$OLD" >/dev/null 2>&1 || true
  git -C "$NEW_ROOT" worktree prune
}
trap cleanup EXIT

echo "== the cluster runs $OLD_REF"
setup_cluster
x "$E2E-ctl" docker load -q -i /opt/sc/traefik.tar >/dev/null
TOK=$(x "$E2E-ctl" cat /data/traefik.token)
x "$E2E-ctl" docker run -d --name traefik --network host traefik:v3.7.13 \
  --entrypoints.web.address=:8080 --entrypoints.websecure.address=:8443 \
  --entrypoints.tcp-2525.address=:2525 --entrypoints.tcp-20000.address=:20000 --entrypoints.udp-20001.address=:20001/udp \
  --providers.http.endpoint=http://127.0.0.1:7070/internal/traefik/config --providers.http.pollInterval=1s \
  "--providers.http.headers.X-Syncloud-Token=$TOK" >/dev/null
wait_mesh

P=localhost:7070/api/v1
ENV=$P/projects/shop/environments/production
SVC=$ENV/services/web
json() { python3 -c "import json,sys; d=json.load(sys.stdin); print($1)"; }
traefik_config() { x "$E2E-ctl" curl -fs -H "X-Syncloud-Token: $TOK" localhost:7070/internal/traefik/config; }
ctl_id=$(api $P/nodes | json "next(n['id'] for n in d['items'] if n['name'] == 'ctl-0')")
fw_rules() { api "$P/firewall/nodes/$ctl_id/effective" | json "' '.join(sorted(r['id'] for r in d['rules']))"; }
containers() { for n in ctl $WORKERS; do x "$E2E-$n" docker ps -q --filter name=shop-production-web-; done | sort | tr '\n' ' '; }
# The probe header's value is random at every controller start, by design.
canon() { python3 -c 'import json,re,sys; t=json.dumps(json.load(sys.stdin), sort_keys=True, indent=1); print(re.sub(r"(X-Syncloud-Probe`, `)[A-Za-z0-9]+", r"\1PROBE", t))'; }

api $P/projects -d '{"name":"shop"}' >/dev/null
api -X PUT "$SVC" -d '{"image":"busybox:1.37","command":["sh","-c","mkdir -p /www && echo web > /www/hostname && httpd -p 8081 -h /www && exec httpd -f -p 8080 -h /www"],"ports":[{"name":"http","container":8080},{"name":"raw","container":8081,"protocol":"tcp"},{"name":"game","container":7777,"protocol":"udp"}],"resources":{"cpu":0.05,"memory":16},"health":{"type":"http","path":"/hostname","interval":2,"startPeriod":1},"desiredCount":1}' >/dev/null
api -X PUT "$SVC/routing" -d '{"ports":{"raw":{"public":true,"allow":["127.0.0.1","10.0.0.0/8"]},"game":{"public":true}}}' >/dev/null
tcp_web() { x "$E2E-ctl" curl -s -m 3 "http://127.0.0.1:$1/hostname" | tr -d '\n' || true; }
for _ in $(seq 1 60); do [ "$(tcp_web 20000)" = web ] && break; sleep 1; done
[ "$(tcp_web 20000)" = web ] || fail "$OLD_REF did not serve the public TCP port"
[ "$(api "$SVC/routing" | json "d['items'][1]['publicPort']")" = 20000 ] || fail "$OLD_REF did not assign 20000"
# Wait for the routes Traefik is handed to settle before the picture is taken.
for _ in $(seq 1 30); do traefik_config | grep -q pub-udp-20001 && break; sleep 1; done
traefik_config | canon > "$BIN/traefik-before.json"
api "$SVC/routing" > "$BIN/routing-before.json"
api "$SVC" | json "'\n'.join(sorted(d['endpoints']))" > "$BIN/endpoints-before.txt"
fw_rules > "$BIN/firewall-before.txt"
BEFORE=$(containers)
grep -q "builtin:service-tcp-20000" "$BIN/firewall-before.txt" || fail "no firewall rule for 20000 before the upgrade"
echo "  ✓ $OLD_REF serves web on public TCP 20000 and UDP 20001 (allow-list on the firewall)"

echo "== the operator upgrades the controller; the agents still run $OLD_REF"
CGO_ENABLED=0 GOOS=linux go build -C "$NEW_ROOT" -ldflags "-X github.com/ridoysheikh/syncloud/internal/version.Version=0.0.0-new" -o "$BIN/syncloud-controller-new" ./cmd/controller
CGO_ENABLED=0 GOOS=linux go build -C "$NEW_ROOT" -o "$BIN/synctl-new" ./cmd/synctl
x "$E2E-ctl" sh -c 'pkill -f "[s]yncloud-controller --dev"; sleep 2'
x -d "$E2E-ctl" sh -c "/opt/sc/syncloud-controller-new --dev --data-dir /data --listen 0.0.0.0:7070 --agent-listen 0.0.0.0:7443 --agent-advertise $CTL_IP:7443 --system-tasks=false --base-domain off ${CTL_FLAGS} > /var/log/controller-new.log 2>&1"
for _ in $(seq 1 60); do x "$E2E-ctl" curl -fs localhost:7070/api/v1/system/status >/dev/null 2>&1 && break; sleep 1; done
x "$E2E-ctl" curl -fs -c /tmp/jar -H 'content-type: application/json' localhost:7070/api/v1/auth/login -d '{"email":"e2e@example.com","password":"e2e-password-123"}' >/dev/null || { x "$E2E-ctl" tail -20 /var/log/controller-new.log; fail "the new controller did not start on the old data"; }
[ "$(api $P/system/status | json "d['version']")" = 0.0.0-new ] || fail "the new controller is not the one running"
echo "  ✓ the new controller started on $OLD_REF's database (migration applied)"

echo "== nothing that exists has changed"
wait_mesh # the old agents reconnect to the new controller
# The reconcile and Traefik's poll after a restart: wait until the routes settle.
for _ in $(seq 1 60); do
  [ "$(tcp_web 20000)" = web ] && traefik_config | canon > "$BIN/traefik-after.json" && cmp -s "$BIN/traefik-before.json" "$BIN/traefik-after.json" && break
  sleep 1
done
[ "$(containers)" = "$BEFORE" ] || fail "tasks were rescheduled by the upgrade: $BEFORE -> $(containers)"
[ "$(tcp_web 20000)" = web ] || fail "public TCP 20000 stopped serving"
diff -u "$BIN/traefik-before.json" "$BIN/traefik-after.json" >&2 || fail "Traefik is handed different routes after the upgrade"
python3 - "$BIN/routing-before.json" "$E2E" <<'EOF2' || fail "ports and addresses differ after the upgrade"
import json, subprocess, sys
before = json.load(open(sys.argv[1]))["items"]
out = subprocess.run(["docker", "exec", sys.argv[2] + "-ctl", "curl", "-fsS", "-b", "/tmp/jar", "-H", "Origin: http://localhost:7070",
                      "localhost:7070/api/v1/projects/shop/environments/production/services/web/routing"], capture_output=True, text=True, check=True).stdout
after = json.loads(out)["items"]
assert len(before) == len(after), (before, after)
for b, a in zip(before, after):
    for k, v in b.items():
        assert a.get(k) == v, f"{b['port']}.{k}: {v!r} -> {a.get(k)!r}"
    if b.get("public"):
        assert a.get("portMode") == "auto", a
print("ok")
EOF2
[ "$(api "$SVC" | json "'\n'.join(sorted(d['endpoints']))")" = "$(cat "$BIN/endpoints-before.txt")" ] || fail "service endpoints changed"
[ "$(fw_rules)" = "$(cat "$BIN/firewall-before.txt")" ] || fail "the host firewall's rules changed: $(cat "$BIN/firewall-before.txt") -> $(fw_rules)"
echo "  ✓ same containers, same Traefik routes, same ports and addresses, same firewall rules; public TCP 20000 still serves"

echo "== the port table is filled from what existed"
api $P/ports | python3 -c "
import json,sys
d=json.load(sys.stdin)
rows={(c['protocol'],c['port'],c['node'],c['ownerKind'],c['mode']) for c in d['items']}
want={('tcp',20000,'ctl-0','service','auto'),('udp',20001,'ctl-0','service','auto')}
assert want <= rows, rows
assert not d['conflicts'], d['conflicts']
assert any(r['ports']=='443' for r in d['reserved']), d['reserved']
" || fail "the port table lacks the existing ports"
[ "$(api "$P/projects/shop/ports/check?protocol=tcp&port=20000&environment=production&service=web" | json "d['state']")" = reserved ] || fail "an automatic-range port is not reserved for choosing"
echo "  ✓ 20000/tcp and 20001/udp are in the table as automatic ports, nothing conflicts"

echo "== the new abilities work on the old data"
api -X PUT "$SVC/routing" -d '{"ports":{"raw":{"publicPort":2525}}}' | json "d['items'][1]['address'] + ' ' + d['items'][1]['portMode']" | grep -qx 'localhost:2525 chosen' || fail "choosing a port on an upgraded service"
for _ in $(seq 1 30); do [ "$(tcp_web 2525)" = web ] && break; sleep 1; done
[ "$(tcp_web 2525)" = web ] || fail "the chosen port 2525 does not serve"
api "$P/projects/shop/ports/check?protocol=tcp&port=20000" | json "d['state']" | grep -qx reserved || fail "20000 should stay reserved for automatic ports"
api -X PUT "$SVC/routing" -d '{"ports":{"raw":{"public":false}}}' >/dev/null
api "$P/ports" | json "[c['port'] for c in d['items'] if c['protocol']=='tcp']" | grep -q 2525 && fail "2525 stayed in the port table after closing"
echo "  ✓ a chosen port (2525) serves and is tabled; closing it frees the claim"
echo "PASS"
