#!/usr/bin/env bash
# End-to-end test of IAM, multi-tenancy and quotas (§7): users, groups and
# managed project policies scoping the API and lists, service accounts with
# access keys, MFA, roles (sts), synctl login (device flow), the audit log,
# quotas at admission, usage metering and Cloud Shell.
#
#   test/e2e/iam.sh          # run and clean up
#   KEEP=1 test/e2e/iam.sh   # leave the nodes running for inspection
CTL_FLAGS="--shell-image busybox:1.37 --shell-synctl /opt/sc/synctl ${CTL_FLAGS:-}"
. "$(dirname "$0")/lib.sh"
trap cleanup EXIT
setup_cluster
wait_mesh

P=localhost:7070/api/v1
# as JAR curl-args…: a request as another signed-in person.
as() { local jar=$1; shift; x sc-e2e-ctl curl -sS -b "/tmp/$jar" -c "/tmp/$jar" -H 'content-type: application/json' -H 'Origin: http://localhost:7070' "$@"; }
code() { x sc-e2e-ctl curl -s -o /dev/null -w '%{http_code}' -b "/tmp/$1" -H 'content-type: application/json' -H 'Origin: http://localhost:7070' "${@:2}"; }
totp() { python3 -c "
import base64,hmac,hashlib,struct,time,sys
k=base64.b32decode(sys.argv[1]+'='*(-len(sys.argv[1])%8)); c=struct.pack('>Q',int(time.time())//30)
h=hmac.new(k,c,hashlib.sha1).digest(); o=h[-1]&15
print('%06d'%((struct.unpack('>I',h[o:o+4])[0]&0x7fffffff)%1000000))" "$1"; }
web() { echo "{\"image\":\"busybox:1.37\",\"command\":[\"httpd\",\"-f\",\"-p\",\"8080\",\"-h\",\"/etc\"],\"ports\":[{\"container\":8080}],\"resources\":{\"cpu\":0.1,\"memory\":16},\"desiredCount\":$1}"; }

echo "== projects"
api $P/projects -d '{"name":"shop"}' >/dev/null
api $P/projects -d '{"name":"billing"}' >/dev/null
api -X PUT $P/projects/shop/environments/production/services/web -d "$(web 1)" >/dev/null
api -X PUT $P/projects/billing/environments/production/services/api -d "$(web 1)" >/dev/null

echo "== users, groups and policies"
ANN=$(api $P/iam/users -d '{"email":"ann@example.com","name":"Ann","password":"ann-password-123"}' | grep -o '"id":"usr_[a-z0-9]*"' | cut -d'"' -f4)
GRP=$(api $P/iam/groups -d "{\"name\":\"shop-devs\",\"members\":[\"$ANN\"]}" | grep -o '"id":"grp_[a-z0-9]*"' | cut -d'"' -f4)
api $P/iam/attachments -d "{\"principalType\":\"group\",\"principalId\":\"$GRP\",\"policy\":\"Developer:shop\"}" >/dev/null
as ann -X POST $P/auth/login -d '{"email":"ann@example.com","password":"ann-password-123"}' | grep -q '"email":"ann@example.com"' || fail "ann login"
svcs=$(as ann $P/services)
echo "$svcs" | grep -q '"project":"shop"' || fail "ann does not see shop: $svcs"
echo "$svcs" | grep -q '"project":"billing"' && fail "ann sees billing: $svcs"
as ann $P/projects | grep -q '"name":"billing"' && fail "ann sees the billing project"
[ "$(code ann $P/settings/domain)" = 403 ] || fail "ann reads cluster settings"
[ "$(code ann -X POST $P/projects/shop/environments/production/services/web/scale -d '{"desiredCount":2}')" = 200 ] || fail "ann cannot scale shop/web"
[ "$(code ann -X POST $P/projects/billing/environments/production/services/api/scale -d '{"desiredCount":2}')" = 403 ] || fail "ann scaled billing/api"
[ "$(code ann -X DELETE $P/projects/shop)" = 403 ] || fail "a developer deleted the project"
[ "$(code ann "$P/logs?since=10m")" = 403 ] || fail "ann queried every project's logs"
[ "$(code ann "$P/logs?since=10m&project=shop")" != 403 ] || fail "ann cannot query shop's logs"
[ "$(code ann $P/traffic)" = 403 ] || fail "ann read cluster-wide traffic"
[ "$(code ann $P/iam/users)" = 403 ] || fail "ann listed users"
as ann -X POST $P/projects -d '{"name":"rogue"}' | grep -q forbidden || fail "ann created a project"
echo "  ✓ a developer of shop sees and changes only shop; cluster settings, users, other projects and cross-project views are denied"

echo "== service account"
CI=$(api $P/iam/users -d '{"kind":"service","name":"github-deployer"}' | grep -o '"id":"usr_[a-z0-9]*"' | cut -d'"' -f4)
api $P/iam/attachments -d "{\"principalType\":\"user\",\"principalId\":\"$CI\",\"policy\":\"Deployer:shop\"}" >/dev/null
key=$(api "$P/iam/access-keys?userId=$CI" -d '{"description":"ci"}')
KID=$(echo "$key" | grep -o '"id":"SYNAK[A-Z0-9]*"' | cut -d'"' -f4); KSEC=$(echo "$key" | grep -o '"secretAccessKey":"[^"]*"' | cut -d'"' -f4)
ci() { x -i -e SYNCLOUD_ENDPOINT=http://127.0.0.1:7070 -e SYNCLOUD_ACCESS_KEY_ID="$KID" -e SYNCLOUD_SECRET_ACCESS_KEY="$KSEC" sc-e2e-ctl /opt/sc/synctl "$@"; }
ci services scale web=1 -p shop </dev/null >/dev/null || fail "the deployer cannot scale"
ci services list -p billing </dev/null >/dev/null 2>&1 && fail "the deployer listed billing"
ci services delete web -p shop </dev/null >/dev/null 2>&1 && fail "the deployer deleted a service"
x sc-e2e-ctl curl -s -X POST $P/auth/login -H 'content-type: application/json' -d '{"email":"github-deployer@service.syncloud.internal","password":"!"}' | grep -q unauthorized || fail "service account signed in"
echo "  ✓ a service account with Deployer:shop scales shop with its access key, and nothing more"

echo "== quotas"
api -X PUT $P/projects/shop/quota -d '{"tasks":3,"cpu":0.3}' >/dev/null
r=$(as ann -X POST $P/projects/shop/environments/production/services/web/scale -d '{"desiredCount":5}')
echo "$r" | grep -q '"code":"quota_exceeded"' && echo "$r" | grep -q 'task quota is 3' || fail "quota not enforced: $r"
[ "$(code ann -X POST $P/projects/shop/environments/production/services/web/scale -d '{"desiredCount":3}')" = 200 ] || fail "scale within quota refused"
[ "$(code ann -X PUT $P/projects/shop/quota -d '{"tasks":100}')" = 403 ] || fail "a developer raised the quota"
api $P/quotas | grep -q '"project":"shop","limits":{"cpu":0.3,"tasks":3},"usage":{"cpu":0.3,"memoryMiB":48,"tasks":3' || fail "quota usage: $(api $P/quotas | head -c 600)"
api $P/quotas | grep -q 'tasks at 100% of its quota' || fail "no warning at 100%"
echo "  ✓ scaling past the task quota is refused with a clear error; usage and warnings are reported; developers cannot raise quotas"

echo "== MFA and roles"
sec=$(as ann -X POST $P/iam/mfa | grep -o '"secret":"[A-Z2-7]*"' | cut -d'"' -f4)
[ -n "$sec" ] || fail "begin MFA"
as ann -X POST $P/iam/mfa/enable -d "{\"code\":\"$(totp "$sec")\"}" | grep -q '"mfaEnabled":true' || fail "enable MFA"
x sc-e2e-ctl rm -f /tmp/ann
as ann -X POST $P/auth/login -d '{"email":"ann@example.com","password":"ann-password-123"}' | grep -q mfa_required || fail "login without a code"
as ann -X POST $P/auth/login -d "{\"email\":\"ann@example.com\",\"password\":\"ann-password-123\",\"otp\":\"$(totp "$sec")\"}" | grep -q '"email"' || fail "login with a code"
ROLE=$(api $P/iam/roles -d "{\"name\":\"auditor\",\"trust\":{\"users\":[\"$ANN\"],\"requireMfa\":true}}" | grep -o '"id":"role_[a-z0-9]*"' | cut -d'"' -f4)
api $P/iam/attachments -d "{\"principalType\":\"role\",\"principalId\":\"$ROLE\",\"policy\":\"ReadOnly\"}" >/dev/null
creds=$(as ann -X POST $P/sts/assume-role -d '{"role":"auditor","durationSeconds":900}')
RK=$(echo "$creds" | grep -o '"accessKeyId":"SYNAS[A-Z0-9]*"' | cut -d'"' -f4); RS=$(echo "$creds" | grep -o '"secretAccessKey":"[^"]*"' | cut -d'"' -f4); RT=$(echo "$creds" | grep -o '"sessionToken":"[^"]*"' | cut -d'"' -f4)
[ -n "$RT" ] || fail "assume role: $creds"
role() { x -i -e SYNCLOUD_ENDPOINT=http://127.0.0.1:7070 -e SYNCLOUD_ACCESS_KEY_ID="$RK" -e SYNCLOUD_SECRET_ACCESS_KEY="$RS" -e SYNCLOUD_SESSION_TOKEN="$RT" sc-e2e-ctl /opt/sc/synctl "$@"; }
role services list -p billing </dev/null | grep -q api || fail "the read-only role cannot read billing"
role services scale api=2 -p billing </dev/null >/dev/null 2>&1 && fail "the read-only role scaled"
echo "  ✓ MFA sign-in, and an MFA-only role assumed with sts (read everything, change nothing)"

echo "== synctl login (device flow)"
x -d sc-e2e-ctl sh -c "HOME=/root/dev /opt/sc/synctl login --endpoint http://127.0.0.1:7070 > /tmp/login.out 2> /tmp/login.err"
for _ in $(seq 1 20); do ucode=$(x sc-e2e-ctl sh -c "grep -o 'code=[A-Z-]*' /tmp/login.err 2>/dev/null" | cut -d= -f2); [ -n "$ucode" ] && break; sleep 0.5; done
[ -n "$ucode" ] || fail "synctl login printed no code"
[ "$(code ann -X POST $P/auth/device/approve -d "{\"userCode\":\"$ucode\"}")" = 204 ] || fail "approve device"
for _ in $(seq 1 20); do x sc-e2e-ctl grep -q "Signed in" /tmp/login.out 2>/dev/null && break; sleep 1; done
x sc-e2e-ctl grep -q "Signed in" /tmp/login.out || fail "synctl login did not finish: $(x sc-e2e-ctl cat /tmp/login.out /tmp/login.err)"
x sc-e2e-ctl sh -c "HOME=/root/dev /opt/sc/synctl iam permissions" | grep -q "Developer:shop" || fail "logged-in synctl has the wrong identity"
echo "  ✓ synctl login: the person approves the code in the dashboard and synctl gets 12-hour credentials as them"

echo "== Cloud Shell"
as ann -X POST $P/shell | grep -q '"state"' || fail "start shell"
for _ in $(seq 1 30); do as ann $P/shell | grep -q '"state":"running"' && break; sleep 1; done
as ann $P/shell | grep -q '"state":"running"' || fail "shell not running: $(as ann $P/shell)"
SC=$(x sc-e2e-ctl docker ps -q --filter label=syncloud.shell_user="$ANN")
for _ in $(seq 1 20); do x sc-e2e-ctl docker exec "$SC" synctl services list -p shop 2>/dev/null | grep -q web && break; sleep 1; done
x sc-e2e-ctl docker exec "$SC" synctl services list -p shop | grep -q web || fail "synctl in the shell: $(x sc-e2e-ctl docker exec "$SC" synctl services list -p shop 2>&1)"
x sc-e2e-ctl docker exec "$SC" synctl services list -p billing >/dev/null 2>&1 && fail "the shell has more than ann's permissions"
[ "$(code ann -X DELETE $P/shell)" = 204 ] || fail "stop shell"
for _ in $(seq 1 10); do [ -z "$(x sc-e2e-ctl docker ps -q --filter label=syncloud.shell_user="$ANN")" ] && break; sleep 1; done
[ -z "$(x sc-e2e-ctl docker ps -q --filter label=syncloud.shell_user="$ANN")" ] || fail "shell container left behind"
echo "  ✓ Cloud Shell runs synctl as the user (over the private network), with the user's permissions, and stops"

echo "== audit and usage"
a=$(api "$P/audit?actor=ann@example.com&since=1h&limit=500")
echo "$a" | grep -q '"action":"service:ScaleService","resource":"srn:syncloud:project/billing/env/production/service/api"' || fail "denied scale not audited"
echo "$a" | grep -q '"denied":true' || fail "no denials in the audit log"
csv=$(api "$P/audit/export?since=1h"); [ "${csv%%$'\n'*}" = "time,actor,actor_id,action,resource,ip,user_agent,detail" ] || fail "audit CSV"
for _ in $(seq 1 40); do api "$P/usage?project=shop" | grep -q '"cpuReservedHours":[0-9.]*[1-9]' && break; sleep 3; done
api "$P/usage?project=shop" | grep -q '"cpuReservedHours":[0-9.]*[1-9]' || fail "no usage metered: $(api "$P/usage?project=shop")"
echo "  ✓ the audit log has denied actions with credentials; usage is metered per minute"

echo "== synctl iam"
key=$(api $P/iam/access-keys -d '{"description":"e2e"}')
KID=$(echo "$key" | grep -o '"id":"SYNAK[A-Z0-9]*"' | cut -d'"' -f4); KSEC=$(echo "$key" | grep -o '"secretAccessKey":"[^"]*"' | cut -d'"' -f4)
synctl() { x -i -e SYNCLOUD_ENDPOINT=http://127.0.0.1:7070 -e SYNCLOUD_ACCESS_KEY_ID="$KID" -e SYNCLOUD_SECRET_ACCESS_KEY="$KSEC" sc-e2e-ctl /opt/sc/synctl "$@"; }
synctl iam users list </dev/null | grep -q 'ann@example.com *user *active *true *shop-devs' || fail "synctl iam users list: $(synctl iam users list </dev/null)"
synctl iam simulate ann@example.com service:ScaleService srn:syncloud:project/billing/env/production/service/api </dev/null | grep -q '^DENIED' || fail "synctl iam simulate"
synctl quota list -p shop </dev/null | grep -q 'shop *0.3/0.3' || fail "synctl quota list: $(synctl quota list -p shop </dev/null)"
out=$(synctl audit --actor ann@example.com --since 1h </dev/null); grep -q DENIED <<<"$out" || fail "synctl audit"
echo "  ✓ synctl iam users/simulate, quota, audit"

echo "PASS"
