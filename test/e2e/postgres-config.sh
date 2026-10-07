#!/usr/bin/env bash
# End-to-end test of PostgreSQL configuration (Phase 13c2): a cluster
# created with one add-on, custom parameters and a replica; a disabled
# add-on cannot be installed, enabling it restarts the members one at a time
# (replicas first) and then it installs; an installed add-on cannot be
# disabled; a reload parameter applies without restarts, a restart parameter
# restarts the members; synchronous replication shows on the Replication
# endpoint; no member container is recreated along the way; the CLI.
#
#   test/e2e/postgres-config.sh          # run and clean up
#   KEEP=1 test/e2e/postgres-config.sh   # leave the nodes running for inspection
WITH_POSTGRES=1
. "$(dirname "$0")/lib.sh"
trap cleanup EXIT
setup_cluster
wait_mesh

P=localhost:7070/api/v1
DB=$P/databases/orders
apie() { x sc-e2e-ctl curl -sS -b /tmp/jar -H 'content-type: application/json' -H 'Origin: http://localhost:7070' "$@"; } # body even on errors
py() { python3 -c "import json,sys; d=json.load(sys.stdin); $1"; }
wait_for() { # description, condition (shell)
  local d=$1; shift
  for _ in $(seq 1 180); do eval "$@" && return 0; sleep 2; done
  echo "--- database:"; api "$DB" | head -c 3000; echo
  echo "--- events:"; api "$DB/events" | head -c 3000; echo
  fail "timed out: $d"
}
health() { api "$DB" | py 'print(d["health"])'; }
show() { api "$DB/pg/query" -d "{\"sql\":\"SHOW $1\"}" | py 'print(d["results"][-1]["rows"][0][0])' 2>/dev/null; }
ext() { apie "$DB/pg/extensions" -d "{\"name\":\"$1\",\"action\":\"${2:-install}\"}"; }
restarts() { api "$DB/events" | py 'print(len([e for e in d["items"] if e["kind"] == "restarted"]))'; }
created() { # the creation time of every member container
  for n in ctl w1 w2; do x sc-e2e-$n sh -c 'for c in $(docker ps -q --filter label=syncloud.database=orders); do docker inspect -f "{{.Name}} {{.Created}}" $c; done'; done | sort
}
put_spec() { # python that edits s (the spec)
  api -X PUT "$DB" -d "$(api "$DB" | py "import json; s = d['spec']; $1; print(json.dumps({'spec': s}))")"
}
put_spec_e() {
  apie -X PUT "$DB" -d "$(api "$DB" | py "import json; s = d['spec']; $1; print(json.dumps({'spec': s}))")"
}

api $P/projects -d '{"name":"shop"}' >/dev/null

echo "== a cluster with one add-on and custom parameters"
apie $P/databases -d '{"name":"bad","engine":"postgres","project":"shop","environment":"production","spec":{"postgres":{"parameters":{"archive_command":"x"}}}}' |
  grep -q 'set by the platform' || fail "a platform parameter was accepted"
apie $P/databases -d '{"name":"bad","engine":"postgres","project":"shop","environment":"production","spec":{"postgres":{"replication":{"mode":"sync"}}}}' |
  grep -q 'at least one replica' || fail "synchronous replication without a replica was accepted"
api $P/databases -d '{"name":"orders","engine":"postgres","project":"shop","environment":"production","spec":{"memory":{"min":512},"replicas":{"min":1,"max":1},"postgres":{"extensions":["vector"],"parameters":{"work_mem":"32MB","idle_in_transaction_session_timeout":"45s"}}}}' >/dev/null
wait_for "healthy" '[ "$(health)" = healthy ]'
[ "$(show work_mem)" = 32MB ] || fail "work_mem is $(show work_mem)"
[ "$(show idle_in_transaction_session_timeout)" = 45s ] || fail "idle_in_transaction_session_timeout is $(show idle_in_transaction_session_timeout)"
[ "$(show shared_preload_libraries)" = pg_stat_statements ] || fail "a plain cluster preloads $(show shared_preload_libraries)"
api "$DB" | py 's = d["spec"]["postgres"]; assert s["extensions"] == ["vector"] and s["replication"]["mode"] == "async", s'
BEFORE=$(created)
[ "$(echo "$BEFORE" | wc -l)" = 2 ] || fail "expected 2 member containers: $BEFORE"
echo "  ✓ PostgreSQL $(show server_version | cut -d' ' -f1) with vector enabled, work_mem 32MB, idle_in_transaction_session_timeout 45s, only pg_stat_statements preloaded"

echo "== add-ons"
ext timescaledb | grep -q 'not enabled' || fail "a disabled add-on was installed"
ext vector | grep -q '"ok":true' || fail "vector did not install"
ext pg_trgm | grep -q '"ok":true' || fail "a contrib module did not install"
put_spec 's["postgres"]["extensions"] = ["timescaledb", "vector"]' >/dev/null
wait_for "timescaledb preloaded on the leader" '[[ "$(show shared_preload_libraries)" == *timescaledb* ]]'
wait_for "both members restarted" '[ "$(restarts)" -ge 2 ]'
api "$DB/events" | py '
r = [e["reason"].split()[1] for e in sorted(d["items"], key=lambda e: e["at"]) if e["kind"] == "restarted"]
assert r[:2] == ["replica", "leader"], r' || fail "members did not restart replica first"
wait_for "healthy again" '[ "$(health)" = healthy ]'
ext timescaledb | grep -q '"ok":true' || fail "timescaledb did not install once enabled"
put_spec_e 's["postgres"]["extensions"] = ["timescaledb"]' | grep -q 'vector is installed in orders' || fail "disabling an installed add-on was accepted"
echo "  ✓ timescaledb refused while off; enabled → the replica, then the leader restarted → installed; disabling installed vector refused"

echo "== parameters"
N=$(restarts)
put_spec 's["postgres"]["parameters"]["work_mem"] = "64MB"' >/dev/null
wait_for "work_mem reloaded" '[ "$(show work_mem)" = 64MB ]'
sleep 15
[ "$(restarts)" = "$N" ] || fail "a reload parameter restarted members"
api "$DB/pg/settings" | py '
s = {x["name"]: x for x in d["items"]}
assert s["work_mem"]["configured"] == "64MB" and s["work_mem"]["value"] == "64MB", s["work_mem"]
assert any(a["name"] == "pg_duckdb" for a in d["addons"])'
put_spec 's["postgres"]["parameters"]["max_locks_per_transaction"] = "128"' >/dev/null
wait_for "max_locks_per_transaction applied" '[ "$(show max_locks_per_transaction)" = 128 ]'
wait_for "the members restarted for it" '[ "$(restarts)" -ge $((N + 2)) ]'
put_spec 'del s["postgres"]["parameters"]["idle_in_transaction_session_timeout"]' >/dev/null
wait_for "idle_in_transaction_session_timeout back to the default" '[ "$(show idle_in_transaction_session_timeout)" = 0 ]'
echo "  ✓ work_mem reloaded without restarts; max_locks_per_transaction restarted the members; an unset parameter returns to the default"

echo "== replication"
put_spec 's["postgres"]["replication"]["mode"] = "sync"; s["postgres"]["replication"]["failoverTtl"] = 20' >/dev/null
wait_for "a synchronous replica" 'api "$DB/pg/replication" | py "assert [r[\"syncState\"] for r in d[\"replicas\"]] in ([\"sync\"], [\"quorum\"]), d[\"replicas\"]" 2>/dev/null'
api "$DB/pg/replication" | py '
assert d["settings"]["mode"] == "sync" and d["settings"]["failoverTtl"] == 20, d["settings"]
assert len(d["members"]) == 2 and d["timeline"] >= 1, d
assert any(s["active"] for s in d["slots"]), d["slots"]'
api "$DB" | py 'assert d["spec"]["postgres"]["synchronous"] is True'
[ "$(created)" = "$BEFORE" ] || fail "member containers were recreated: $BEFORE → $(created)"
echo "  ✓ synchronous replica, slots active; no member container was recreated by any configuration change"

echo "== CLI"
PAT=$(api localhost:7070/api/v1/iam/tokens -d '{"name":"e2e"}' | grep -o '"token":"[^"]*"' | cut -d'"' -f4)
S="x -e SYNCLOUD_TOKEN=$PAT -e SYNCLOUD_ENDPOINT=http://127.0.0.1:7070 sc-e2e-ctl /opt/sc/synctl"
$S db settings orders --changed | grep -q 'max_locks_per_transaction' || fail "synctl db settings"
$S db config set orders log_min_duration_statement=250ms | grep -q 'saved' || fail "synctl db config set"
wait_for "log_min_duration_statement" '[ "$(show log_min_duration_statement)" = 250ms ]'
$S db config unset orders log_min_duration_statement >/dev/null || fail "synctl db config unset"
$S db addons orders | grep -q 'timescaledb *enabled' || fail "synctl db addons"
$S db addon enable orders hypopg | grep -q 'Enabled hypopg' || fail "synctl db addon enable"
$S db replication set orders --mode async >/dev/null || fail "synctl db replication set"
wait_for "asynchronous again" 'api "$DB/pg/replication" | py "assert [r[\"syncState\"] for r in d[\"replicas\"]] == [\"async\"]" 2>/dev/null'
$S db replication orders | grep -q 'Mode:      async' || fail "synctl db replication"
echo "  ✓ synctl db settings, config set/unset, addons, addon enable, replication set and show"

echo "PASS"
