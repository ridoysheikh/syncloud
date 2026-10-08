package dbs

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/ridoysheikh/syncloud/internal/store"
)

// probe reads every running member: sentinels say which member is primary,
// data members report INFO. It applies the current memory and policy with
// CONFIG SET, follows failovers and records metrics.
func (m *Manager) probe(ctx context.Context, d store.Database) {
	if d.Deleting {
		return
	}
	spec, err := parseSpec(d.Spec)
	if err != nil {
		return
	}
	st := parseState(d.State)
	sec, err := m.secrets(d)
	if err != nil {
		return
	}
	members, err := m.st.DatabaseMembers(ctx, d.ID)
	if err != nil {
		return
	}
	if d.Engine == EnginePostgres {
		_ = spec
		m.probePostgres(ctx, d, st, sec, members)
		return
	}
	ctx, cancel := context.WithTimeout(ctx, 4*time.Second)
	defer cancel()
	now := m.now()

	primaryHost := ""
	for _, mb := range members {
		if mb.Kind != KindSentinel || mb.IP == "" || mb.State != store.TaskRunning {
			continue
		}
		l := &Live{At: now, Role: "sentinel"}
		v, err := sentinelState(ctx, m.cl.get(addr(mb.IP, SentinelPort), "", sec.AdminPassword), d.Name)
		if err != nil {
			l.Error = err.Error()
		} else {
			l.KnownReplicas, l.KnownPeers = v.Replicas, v.Peers
			if primaryHost == "" {
				primaryHost = v.Primary
			}
		}
		m.setLive(mb.ID, l)
	}

	var lines strings.Builder
	var masters []int
	var primaryOffset int64
	offsets := map[string]int64{}
	for _, mb := range members {
		if mb.Kind != KindData || mb.IP == "" || mb.State != store.TaskRunning {
			continue
		}
		cl := m.cl.get(addr(mb.IP, Port), adminUser, sec.AdminPassword)
		prev := m.liveOf(mb.ID)
		l := &Live{At: now}
		raw, err := cl.Info(ctx, "all").Result()
		if err != nil {
			l.Error = err.Error()
			m.setLive(mb.ID, l)
			continue
		}
		info := parseInfo(raw)
		l.Info, l.Role = info, info["role"]
		l.LinkUp = info["master_link_status"] == "up"
		if l.Role == "master" {
			masters = append(masters, mb.Ordinal)
			primaryOffset = info.Int("master_repl_offset")
		} else {
			offsets[mb.ID] = info.Int("slave_repl_offset")
		}
		// CPU over the last probe, as % of one core.
		cpu := info.Float("used_cpu_sys") + info.Float("used_cpu_user")
		if prev != nil && prev.Info != nil {
			if dt := now.Sub(prev.At).Seconds(); dt > 0 {
				pc := prev.Info.Float("used_cpu_sys") + prev.Info.Float("used_cpu_user")
				l.Info["_cpu_percent"] = strconv.FormatFloat(max(0, (cpu-pc)/dt*100), 'f', 2, 64)
			}
		}
		// The current size and policy, applied online.
		if want := int64(st.MemoryMiB) << 20; want > 0 && info.Int("maxmemory") != want {
			if err := cl.Do(ctx, "CONFIG", "SET", "maxmemory", strconv.FormatInt(want, 10)).Err(); err != nil {
				m.log.Warn("set maxmemory", "database", d.Name, "member", memberName(mb), "err", err)
			}
		}
		if info["maxmemory_policy"] != "" && info["maxmemory_policy"] != spec.EvictionPolicy {
			_ = cl.Do(ctx, "CONFIG", "SET", "maxmemory-policy", spec.EvictionPolicy).Err()
		}
		m.setLive(mb.ID, l)
		writeSamples(&lines, d, mb, l)
	}
	for id, off := range offsets {
		if l := m.liveOf(id); l != nil && primaryOffset > 0 {
			l.LagBytes = max(0, primaryOffset-off)
			fmt.Fprintf(&lines, "syncloud_db_replication_lag_bytes{%s} %d\n", labelsFor(d, id, members), l.LagBytes)
		}
	}

	// Which member is primary: Sentinel's choice, else the one master.
	primary := st.Primary
	if primaryHost != "" {
		if ord, ok := ordinalOf(primaryHost, d); ok {
			primary = ord
		}
	} else if !spec.HasSentinels() && len(masters) == 1 {
		primary = masters[0]
	}
	if primary != st.Primary && memberExists(members, primary) {
		m.log.Info("database primary changed", "database", d.Name, "from", "m"+strconv.Itoa(st.Primary), "to", "m"+strconv.Itoa(primary))
		m.event(ctx, d.ID, "failover", "m"+strconv.Itoa(st.Primary), "m"+strconv.Itoa(primary), "Sentinel promoted a replica", "sentinel")
		st.Primary = primary
		if err := m.st.SetDatabaseState(ctx, d.ID, encode(st)); err == nil {
			m.publish(ctx, d.ID)
			m.changed() // the read-write VIP follows the new primary
		}
	}
	if m.Metrics != nil && lines.Len() > 0 {
		if err := m.Metrics.Import(ctx, lines.String()); err != nil {
			m.log.Debug("database metrics", "err", err)
		}
	}
}

func (m *Manager) setLive(id string, l *Live) {
	m.mu.Lock()
	m.live[id] = l
	m.mu.Unlock()
}

// ordinalOf maps "m<n>.<db>…" to n.
func ordinalOf(host string, d store.Database) (int, bool) {
	rest, ok := strings.CutPrefix(host, "m")
	if !ok {
		return 0, false
	}
	n, suffix, ok := strings.Cut(rest, ".")
	if !ok || suffix != Host(d) {
		return 0, false
	}
	i, err := strconv.Atoi(n)
	return i, err == nil
}

func memberExists(ms []store.DatabaseMember, ord int) bool {
	for _, x := range ms {
		if x.Kind == KindData && x.Ordinal == ord {
			return true
		}
	}
	return false
}

func labelsFor(d store.Database, memberID string, ms []store.DatabaseMember) string {
	name := ""
	for _, x := range ms {
		if x.ID == memberID {
			name = memberName(x)
		}
	}
	return fmt.Sprintf(`database_id=%q,project=%q,environment=%q,database=%q,member=%q`, d.ID, d.Project, d.Environment, d.Name, name)
}

// writeSamples turns a member's INFO into metrics (§9.1).
func writeSamples(b *strings.Builder, d store.Database, mb store.DatabaseMember, l *Live) {
	lb := fmt.Sprintf(`database_id=%q,project=%q,environment=%q,database=%q,member=%q`, d.ID, d.Project, d.Environment, d.Name, memberName(mb))
	i := l.Info
	primary := 0
	if l.Role == "master" {
		primary = 1
	}
	for _, s := range []struct {
		name string
		v    float64
	}{
		{"syncloud_db_up", 1},
		{"syncloud_db_is_primary", float64(primary)},
		{"syncloud_db_used_memory_bytes", i.Float("used_memory")},
		{"syncloud_db_maxmemory_bytes", i.Float("maxmemory")},
		{"syncloud_db_ops_per_sec", i.Float("instantaneous_ops_per_sec")},
		{"syncloud_db_connected_clients", i.Float("connected_clients")},
		{"syncloud_db_keys", float64(i.Keys())},
		{"syncloud_db_cpu_percent", i.Float("_cpu_percent")},
		{"syncloud_db_commands_total", i.Float("total_commands_processed")},
		{"syncloud_db_keyspace_hits_total", i.Float("keyspace_hits")},
		{"syncloud_db_keyspace_misses_total", i.Float("keyspace_misses")},
		{"syncloud_db_evicted_keys_total", i.Float("evicted_keys")},
		{"syncloud_db_expired_keys_total", i.Float("expired_keys")},
		{"syncloud_db_net_input_bytes_total", i.Float("total_net_input_bytes")},
		{"syncloud_db_net_output_bytes_total", i.Float("total_net_output_bytes")},
	} {
		fmt.Fprintf(b, "%s{%s} %s\n", s.name, lb, strconv.FormatFloat(s.v, 'f', -1, 64))
	}
}
