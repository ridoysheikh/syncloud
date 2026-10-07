package dbs

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"

	"google.golang.org/protobuf/proto"

	agentv1 "syncloud/internal/gen/syncloud/agent/v1"
	"syncloud/internal/store"
	"syncloud/internal/workload"
)

// Task ID prefixes of database containers.
const (
	MemberPrefix   = "dbm_"
	SentinelPrefix = "dbs_"
	// Zone is the internal DNS zone (as discovery's).
	Zone = "syncloud.internal"
	// adminUser is the ACL user of replication, Sentinel and the controller.
	adminUser = "syncloud"
)

// Kinds of members.
const (
	KindData     = "data"
	KindSentinel = "sentinel"
)

// Host is the database's read-write DNS name.
func Host(d store.Database) string {
	return fmt.Sprintf("%s.%s.%s.%s", d.Name, d.Environment, d.Project, Zone)
}

// ReadHost is the read-only DNS name.
func ReadHost(d store.Database) string {
	return fmt.Sprintf("%s-ro.%s.%s.%s", d.Name, d.Environment, d.Project, Zone)
}

// memberHost is a member's stable name: m<n> for data, s<n> for sentinels.
func memberHost(d store.Database, kind string, ordinal int) string {
	p := "m"
	if kind == KindSentinel {
		p = "s"
	}
	return fmt.Sprintf("%s%d.%s", p, ordinal, Host(d))
}

// Volume is a member's node-local volume.
func Volume(d store.Database, kind string, ordinal int) string {
	p := "m"
	if kind == KindSentinel {
		p = "s"
	}
	return fmt.Sprintf("syncloud-db-%s-%s%d", strings.TrimPrefix(d.ID, "db_"), p, ordinal)
}

// memberScript starts a data member: it asks the sentinels which member is
// primary (members restart and replicas join without the controller), and
// replicates from it.
const memberScript = `set -eu
case "$PERSISTENCE" in
  aof) AOF=yes; SAVE='3600 1 300 100 60 10000' ;;
  rdb) AOF=no;  SAVE='3600 1 300 100 60 10000' ;;
  *)   AOF=no;  SAVE='""' ;;
esac
cat > /tmp/valkey.conf <<EOF
port 6379
bind * -::*
protected-mode no
dir /data
appendonly $AOF
appendfsync everysec
save $SAVE
maxmemory ${MAXMEMORY}mb
maxmemory-policy $POLICY
replica-announce-ip $SELF
replica-announce-port 6379
masteruser syncloud
masterauth $ADMIN_PASS
user default on >$PASS ~* &* +@all -config -debug -shutdown -module -replicaof -slaveof -failover -acl -sync -psync -monitor -save -bgsave -bgrewriteaof -cluster
user syncloud on >$ADMIN_PASS ~* &* +@all
EOF
primary=""
if [ -n "$SENTINELS" ]; then
  for _ in $(seq 1 20); do
    for s in $SENTINELS; do
      primary=$(valkey-cli -h "$s" -p 26379 -a "$ADMIN_PASS" --no-auth-warning sentinel get-master-addr-by-name "$NAME" 2>/dev/null | head -1) || true
      [ -n "$primary" ] && break 2
    done
    sleep 2
  done
  [ -n "$primary" ] || primary=$INITIAL
fi
if [ -n "$primary" ] && [ "$primary" != "$SELF" ]; then
  echo "replicaof $primary 6379" >> /tmp/valkey.conf
  echo "starting as a replica of $primary"
else
  echo "starting as the primary"
fi
exec valkey-server /tmp/valkey.conf
`

// sentinelScript starts a sentinel monitoring the current primary (from its
// peers), keeping its state in its volume across restarts.
const sentinelScript = `set -eu
CONF=/data/sentinel.conf
if [ ! -s "$CONF" ]; then
  primary=""
  for s in $PEERS; do
    primary=$(valkey-cli -h "$s" -p 26379 -a "$ADMIN_PASS" --no-auth-warning sentinel get-master-addr-by-name "$NAME" 2>/dev/null | head -1) || true
    [ -n "$primary" ] && break
  done
  [ -n "$primary" ] || primary=$INITIAL
  until nslookup "$primary" >/dev/null 2>&1; do echo "waiting for $primary"; sleep 2; done
  cat > "$CONF" <<EOF
port 26379
dir /data
sentinel resolve-hostnames yes
sentinel announce-hostnames yes
sentinel announce-ip $SELF
requirepass $ADMIN_PASS
sentinel sentinel-pass $ADMIN_PASS
sentinel monitor $NAME $primary 6379 2
sentinel auth-user $NAME syncloud
sentinel auth-pass $NAME $ADMIN_PASS
sentinel down-after-milliseconds $NAME 5000
sentinel failover-timeout $NAME 30000
sentinel parallel-syncs $NAME 1
EOF
fi
until nslookup "$SELF" >/dev/null 2>&1; do sleep 1; done
exec valkey-sentinel "$CONF"
`

// taskSpec is what the agent runs for a member. It holds nothing that
// changes at run time (memory and policies are applied with CONFIG SET), so
// the container is only recreated for real changes.
func taskSpec(d store.Database, spec Spec, st State, sec Secrets, m store.DatabaseMember, dns, search []string) *agentv1.TaskSpec {
	image := Images[d.Version]
	if image == "" {
		image = Images[DefaultVersion]
	}
	sentinels := ""
	if spec.HasSentinels() {
		var hs []string
		for i := range Sentinels {
			hs = append(hs, memberHost(d, KindSentinel, i))
		}
		sentinels = strings.Join(hs, " ")
	}
	env := map[string]string{
		"NAME": d.Name, "SELF": memberHost(d, m.Kind, m.Ordinal), "INITIAL": memberHost(d, KindData, 0),
		"ADMIN_PASS": sec.AdminPassword,
	}
	short := fmt.Sprintf("m%d", m.Ordinal)
	ts := &agentv1.TaskSpec{
		TaskId: m.ID, Image: image, NetworkMode: workload.Network,
		Restart:    agentv1.RestartPolicy_RESTART_POLICY_UNLESS_STOPPED, // members restart in place: their data is here
		Mounts:     []*agentv1.Mount{{Type: agentv1.Mount_TYPE_VOLUME, Source: Volume(d, m.Kind, m.Ordinal), Target: "/data"}},
		DnsServers: dns, DnsSearch: search,
	}
	if m.Kind == KindData {
		env["PASS"], env["SENTINELS"] = sec.Password, sentinels
		// Start at the size the container was made for; the operator sets the
		// current size within seconds. Policies are re-applied the same way.
		env["PERSISTENCE"], env["POLICY"], env["MAXMEMORY"] = spec.Persistence, spec.EvictionPolicy, fmt.Sprint(st.LimitMiB)
		ts.Command = []string{"sh", "-c", memberScript}
		ts.MemoryLimitBytes = int64(containerLimit(st.LimitMiB)) << 20
	} else {
		short = fmt.Sprintf("s%d", m.Ordinal)
		var peers []string
		for i := range Sentinels {
			if i != m.Ordinal {
				peers = append(peers, memberHost(d, KindSentinel, i))
			}
		}
		env["PEERS"] = strings.Join(peers, " ")
		ts.Command = []string{"sh", "-c", sentinelScript}
		ts.MemoryLimitBytes = 64 << 20
	}
	ts.Env = env
	ts.Name = fmt.Sprintf("%s-%s-db-%s-%s", d.Project, d.Environment, d.Name, short)
	ts.Labels = map[string]string{
		"syncloud.project": d.Project, "syncloud.environment": d.Environment, "syncloud.service": d.Name,
		"syncloud.service_id": d.ID, "syncloud.database": d.Name, "syncloud.db_member": short,
	}
	return ts
}

// specHash identifies a member's container spec.
func specHash(ts *agentv1.TaskSpec) string {
	b, _ := proto.MarshalOptions{Deterministic: true}.Marshal(ts)
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:8])
}
