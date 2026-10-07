#!/usr/bin/env bash
# End-to-end test of the PostgreSQL explorer and administration (Phase
# 13b): roles created through the API sign in from a task with the access
# they were given (read-only refuses writes, revoking takes it away), role
# changes and drops with object reassignment, databases, the schema browser
# and DDL, table data, the SQL console (read-only by default, run as a
# role, never as the superuser), extensions, sessions, the platform's
# protections, and the synctl commands.
#
#   test/e2e/postgres-admin.sh          # run and clean up
#   KEEP=1 test/e2e/postgres-admin.sh   # leave the nodes running for inspection
. "$(dirname "$0")/lib.sh"
WITH_POSTGRES=1
trap cleanup EXIT
setup_cluster
wait_mesh

P=localhost:7070/api/v1/projects/shop
ENV=$P/environments/production
DB=localhost:7070/api/v1/databases/shopdb
PG=$DB/pg
PG_IMAGE=$(sed -n 's/.*ImagePostgres *= *"\(.*\)"/\1/p' internal/system/manifest.go)
apie() { x sc-e2e-ctl curl -sS -b /tmp/jar -H 'content-type: application/json' -H 'Origin: http://localhost:7070' "$@"; } # body even on errors
py() { python3 -c "import json,sys; d=json.load(sys.stdin); $1"; }
wait_for() { # description, condition (shell)
  local d=$1; shift
  for _ in $(seq 1 150); do eval "$@" && return 0; sleep 2; done
  echo "--- database:"; api "$DB" | head -c 3000; echo
  fail "timed out: $d"
}
q() { api "$PG/query" -d "$(python3 -c 'import json,sys; print(json.dumps({"sql": sys.argv[1], "role": sys.argv[2], "database": sys.argv[3]}))' "$1" "${2:-}" "${3:-}")"; }
w() { api "$PG/execute" -d "$(python3 -c 'import json,sys; print(json.dumps({"sql": sys.argv[1], "role": sys.argv[2]}))' "$1" "${2:-}")"; }
qerr() { py 'print((d.get("error") or {}).get("message", ""))'; }
rows() { py 'print(d["results"][-1]["rows"])'; }

echo "== create"
api localhost:7070/api/v1/projects -d '{"name":"shop"}' >/dev/null
api localhost:7070/api/v1/databases -d '{"name":"shopdb","engine":"postgres","project":"shop","environment":"production","spec":{"memory":{"min":512},"replicas":{"min":0,"max":0}}}' >/dev/null
wait_for "healthy" '[ "$(api "$DB" | py "print(d[\"health\"])")" = healthy ]'

echo "== console"
w "CREATE TABLE items (id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY, name text NOT NULL, price numeric(10,2) DEFAULT 0 CHECK (price >= 0));
COMMENT ON TABLE items IS 'catalog';
INSERT INTO items (name, price) SELECT 'item ' || g, g FROM generate_series(1, 30) g;
CREATE INDEX items_name ON items (name);
CREATE VIEW cheap AS SELECT * FROM items WHERE price < 5;" | py 'assert "error" not in d, d; print("  ✓ DDL and inserts run with writes allowed:", ", ".join(r["tag"] for r in d["results"]))'
[ "$(q "SELECT current_user, count(*) FROM items" | rows)" = "[['app', '30']]" ] || fail "read as app"
[ "$(q "SELECT 1; COMMIT; DELETE FROM items" | qerr)" = "cannot execute DELETE in a read-only transaction" ] || fail "read-only mode let a write through after COMMIT"
q "SELECT 1 FROM pg_authid" | qerr | grep -q "permission denied" || fail "the console has superuser rights"
[ "$(w "RESET ROLE; SELECT rolsuper FROM pg_roles WHERE rolname = current_user" | rows)" = "[['f']]" ] || fail "RESET ROLE reached a superuser"
api "$PG/query" -d '{"sql":"select 1","role":"syncloud_admin"}' >/dev/null 2>&1 && fail "ran as the platform superuser"
echo "  ✓ read-only by default (COMMIT does not escape it), never a superuser"

echo "== schema browser"
api "$PG/schema" | py '
s = {x["name"]: x for x in d["schemas"]}
assert list(s) == ["public"], list(s)
kinds = {(o["kind"], o["name"]) for o in s["public"]["objects"]}
assert {("table", "items"), ("view", "cheap"), ("sequence", "items_id_seq")} <= kinds, kinds'
api "$PG/object?schema=public&name=items" | py '
assert [c["name"] for c in d["columns"]] == ["id", "name", "price"], d["columns"]
assert d["columns"][0]["identity"] == "always" and d["columns"][1]["notNull"]
assert {k["kind"] for k in d["constraints"]} == {"primary key", "check"}, d["constraints"]
assert any(i["name"] == "items_name" for i in d["indexes"])
assert "CREATE TABLE \"public\".\"items\"" in d["ddl"] and "COMMENT ON TABLE" in d["ddl"] and "CREATE INDEX items_name" in d["ddl"], d["ddl"]'
api "$PG/rows" -d '{"schema":"public","table":"items","orderBy":"price","desc":true,"limit":5,"filters":[{"column":"price","op":"<","value":"20"}],"count":true}' | py '
assert d["total"] == 19 and len(d["rows"]) == 5 and d["more"], d
assert d["rows"][0][2] == "19.00", d["rows"][0]'
echo "  ✓ schemas, columns, constraints, indexes, DDL, filtered and sorted rows"

echo "== roles and privileges"
PW=$(api "$PG/roles" -d '{"name":"reporting","connLimit":5,"comment":"BI"}' | py 'assert d["login"] and d["connLimit"] == 5; print(d["password"])')
[ -n "$PW" ] || fail "no generated password"
api "$PG/privileges" -d '{"preset":{"role":"reporting","access":"read"}}' | py 'assert any("ALTER DEFAULT PRIVILEGES" in s for s in d["statements"]), d'
URL=$(api "$DB/credentials" | py 'print(d["url"])' | sed "s#postgresql://app:[^@]*@#postgresql://reporting:$PW@#")

api -X PUT "$ENV/services/client" -d "{\"image\":\"$PG_IMAGE\",\"command\":[\"sleep\",\"3600\"],\"resources\":{\"cpu\":0.05,\"memory\":128}}" >/dev/null
wait_for "client task running" '[ "$(api "$ENV/services/client" | py "print(d[\"running\"])")" = 1 ]'
CNODE=$(api "$ENV/services/client/tasks" | py 'print([t["node"] for t in d["items"] if t["state"]=="running"][0])')
CTASK=$(api "$ENV/services/client/tasks" | py 'print([t["id"] for t in d["items"] if t["state"]=="running"][0])')
nodec() { [ "$1" = ctl-0 ] && echo sc-e2e-ctl || echo "sc-e2e-$1"; }
sql() { # url, statements (stdin)
  printf '%s\n' "$2" | docker exec -i "$(nodec "$CNODE")" sh -c "docker exec -i -e PGCONNECT_TIMEOUT=3 \$(docker ps -q --filter label=syncloud.task_id=$CTASK) psql '$1' -v ON_ERROR_STOP=1 -At" 2>&1
}
wait_for "reporting signs in from a task" '[ "$(sql "$URL" "SELECT count(*) FROM items")" = 30 ]'
{ sql "$URL" "INSERT INTO items (name) VALUES ('x')" || true; } | grep -q "permission denied" || fail "a read-only role could write"
w "CREATE TABLE later (x int); INSERT INTO later VALUES (1)" >/dev/null
[ "$(sql "$URL" "SELECT count(*) FROM later")" = 1 ] || fail "default privileges did not cover a new table"
echo "  ✓ reporting signs in from a task: reads (also tables created later), cannot write"

api "$PG/privileges" -d '{"changes":[{"privileges":["INSERT"],"grantee":"reporting","object":{"kind":"table","schema":"public","name":"items"}}]}' >/dev/null
sql "$URL" "INSERT INTO items (name) VALUES ('by reporting')" >/dev/null || fail "INSERT after the grant"
api "$PG/privileges?kind=table&schema=public&name=items&role=reporting" | py 'assert d["effective"]["INSERT"] and d["effective"]["SELECT"] and not d["effective"]["DELETE"], d["effective"]'
api "$PG/privileges" -d '{"changes":[{"revoke":true,"privileges":["ALL"],"grantee":"reporting","object":{"kind":"table","schema":"public","name":"items"}}]}' >/dev/null
{ sql "$URL" "SELECT 1 FROM items LIMIT 1" || true; } | grep -q "permission denied" || fail "SELECT after revoking everything"
[ "$(q "SELECT count(*) FROM later" reporting | rows)" = "[['1']]" ] || fail "console run as reporting"
echo "  ✓ grant and revoke on one table take effect; effective rights; the console runs as reporting"

api -X PUT "$PG/roles/reporting" -d '{"connLimit":-1,"validUntil":"2030-01-01T00:00:00Z","memberOf":[{"role":"pg_read_all_data","admin":false,"inherit":true}]}' | py '
assert d["connLimit"] == -1 and d["validUntil"].startswith("2030-01-01") and d["memberOf"][0]["role"] == "pg_read_all_data", d
assert "password" not in d'
[ "$(sql "$URL" "SELECT count(*) FROM items")" = 31 ] || fail "pg_read_all_data membership"
NEWPW=$(api -X PUT "$PG/roles/reporting" -d '{"generatePassword":true}' | py 'print(d["password"])')
{ sql "$URL" "SELECT 1" || true; } | grep -q "password authentication failed" || fail "the old password still works"
URL=${URL/reporting:$PW@/reporting:$NEWPW@}
[ "$(sql "$URL" "SELECT current_user")" = reporting ] || fail "the new password"
api -X PUT "$PG/roles/reporting" -d '{"name":"bi"}' | py 'assert d["name"] == "bi"'
URL=${URL/reporting:/bi:}
[ "$(sql "$URL" "SELECT current_user")" = bi ] || fail "a renamed role keeps its password"
echo "  ✓ alter: limit, expiry, membership, new password (the old one stops working), rename"

w "GRANT CREATE ON SCHEMA public TO bi" >/dev/null
sql "$URL" "CREATE TABLE bi_owned (x int)" >/dev/null || fail "bi creates a table"
api -X DELETE "$PG/roles/bi?reassignTo=app" >/dev/null
api "$PG/object?schema=public&name=bi_owned" | py 'assert d["owner"] == "app", d["owner"]'
api "$PG/roles" | py 'assert "bi" not in [r["name"] for r in d["items"]]'
echo "  ✓ drop: bi's table went to app, then the role was dropped"

echo "== databases and extensions"
api "$PG/databases" -d '{"name":"analytics","comment":"dw"}' >/dev/null
api -X PUT "$PG/databases/analytics" -d '{"name":"warehouse","connLimit":10}' >/dev/null
api "$PG/databases" | py '
x = {d["name"]: d for d in d["items"]}
assert "warehouse" in x and x["warehouse"]["connLimit"] == 10 and x["warehouse"]["owner"] == "app" and x["shopdb"]["primary"], x'
[ "$(q "SELECT current_database()" "" warehouse | rows)" = "[['warehouse']]" ] || fail "console on another database"
api "$PG/extensions?db=warehouse" -d '{"name":"vector","action":"install"}' >/dev/null
api "$PG/extensions?db=warehouse" | py 'assert [e for e in d["items"] if e["name"] == "vector"][0].get("installedVersion"), d'
api -X DELETE "$PG/databases/warehouse" >/dev/null
api "$PG/databases" | py 'assert "warehouse" not in [d["name"] for d in d["items"]]'
echo "  ✓ create, rename and limit, explore and install vector in another database, drop"

echo "== sessions"
docker exec -d "$(nodec "$CNODE")" sh -c "docker exec \$(docker ps -q --filter label=syncloud.task_id=$CTASK) psql '$(api "$DB/credentials" | py 'print(d["url"])')' -c 'SELECT pg_sleep(60)'"
wait_for "the sleeping session is listed" 'api "$PG/sessions" | py "assert any(\"pg_sleep\" in s[\"query\"] and s[\"state\"] == \"active\" for s in d[\"items\"])" 2>/dev/null'
SPID=$(api "$PG/sessions" | py 'print([s["pid"] for s in d["items"] if "pg_sleep" in s["query"]][0])')
api -X POST "$PG/sessions/$SPID/cancel" >/dev/null
wait_for "the query was cancelled" '! api "$PG/sessions" | py "assert any(\"pg_sleep\" in s[\"query\"] and s[\"state\"] == \"active\" for s in d[\"items\"])" 2>/dev/null'
api "$PG/sessions?all=true" | py 'assert any(s["platform"] for s in d["items"])'
PLATPID=$(api "$PG/sessions?all=true" | py 'print([s["pid"] for s in d["items"] if s["platform"]][0])')
apie -X POST "$PG/sessions/$PLATPID/terminate" | grep -q "belongs to the platform" || fail "terminated a platform session"
echo "  ✓ sessions listed, a query cancelled, platform sessions protected"

echo "== protections"
set -f # the JSON bodies below are split into words, never globbed
for req in "-X PUT $PG/roles/syncloud_admin -d {\"login\":false}" \
           "$PG/roles -d {\"name\":\"evil\",\"memberOf\":[{\"role\":\"syncloud_admin\"}]}" \
           "$PG/roles -d {\"name\":\"files\",\"memberOf\":[{\"role\":\"pg_read_server_files\"}]}" \
           "-X PUT $PG/roles/app -d {\"generatePassword\":true}" \
           "-X DELETE $PG/databases/shopdb" \
           "$PG/extensions -d {\"name\":\"plperlu\",\"action\":\"install\"}" \
           "$PG/privileges -d {\"changes\":[{\"privileges\":[\"SELECT\"],\"grantee\":\"replicator\",\"target\":\"all-tables\",\"object\":{\"schema\":\"public\"}}]}"; do
  # shellcheck disable=SC2086
  code=$(x sc-e2e-ctl curl -s -o /dev/null -w '%{http_code}' -b /tmp/jar -H 'content-type: application/json' -H 'Origin: http://localhost:7070' $req)
  [ "$code" = 400 ] || fail "expected 400 for: $req (got $code)"
done
set +f
echo "  ✓ platform roles, superuser and file access, the credentials role, the cluster database, unlisted extensions"

echo "== CLI"
PAT=$(api localhost:7070/api/v1/iam/tokens -d '{"name":"e2e"}' | grep -o '"token":"[^"]*"' | cut -d'"' -f4)
S="x -i -e SYNCLOUD_TOKEN=$PAT sc-e2e-ctl /opt/sc/synctl --endpoint http://127.0.0.1:7070"
$S db sql shopdb "SELECT count(*) AS n FROM items" | grep -q '^31' || fail "synctl db sql"
{ $S db sql shopdb "INSERT INTO items (name) VALUES ('cli')" 2>&1 || true; } | grep -q 'read-only' || fail "synctl db sql is read-only by default"
echo "INSERT INTO items (name) VALUES ('cli')" | $S db sql shopdb --write - | grep -q 'INSERT 0 1' || fail "synctl db sql --write from stdin"
$S db role create shopdb cli_reader | grep -q '^password: ' || fail "synctl db role create"
$S db grant shopdb cli_reader --access read | grep -q 'GRANT SELECT ON ALL TABLES' || fail "synctl db grant --access"
$S db roles shopdb | grep -q cli_reader || fail "synctl db roles"
$S db privileges shopdb --on table:public.items --role cli_reader | grep -q 'cli_reader can: SELECT' || fail "synctl db privileges"
$S db revoke shopdb cli_reader --privileges ALL --on all-tables:public | grep -q 'REVOKE ALL PRIVILEGES ON ALL TABLES' || fail "synctl db revoke"
$S db describe shopdb public.items | grep -q 'numeric(10,2)' || fail "synctl db describe"
$S db describe shopdb items --ddl | grep -q 'CREATE TABLE' || fail "synctl db describe --ddl"
$S db rows shopdb items --where 'price>=29' --count | grep -q '2 matching rows' || fail "synctl db rows"
$S db schema shopdb | grep -q 'items_id_seq' || fail "synctl db schema"
$S db databases shopdb | grep -q 'shopdb \*' || fail "synctl db databases"
$S db extensions shopdb | grep -q '^vector' || fail "synctl db extensions"
$S db role drop shopdb cli_reader | grep -q 'Dropped role cli_reader' || fail "synctl db role drop"
echo "  ✓ synctl db sql, role, grant, revoke, privileges, describe, rows, schema, databases, extensions"

echo "PASS"
