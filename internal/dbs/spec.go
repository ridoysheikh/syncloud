// Package dbs runs managed databases (Phase 12): dedicated data members
// pinned to nodes with node-local volumes, internal and public endpoints, an
// access list per database, and metrics. Valkey is the first engine:
// Sentinel failover, online memory and read-replica autoscaling, and the
// explorer.
package dbs

import (
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/ridoysheikh/syncloud/internal/store"
	"github.com/ridoysheikh/syncloud/internal/workload"
)

const (
	// DefaultVersion of Valkey.
	DefaultVersion = "8.1"
	// Port of data members; sentinels listen on SentinelPort.
	Port         = 6379
	SentinelPort = 26379
	// Sentinels run when a database can have replicas (quorum 2 of 3).
	Sentinels = 3
	quorum    = 2

	maxReplicas  = 5
	minMemoryMiB = 32
	maxMemoryMiB = 64 * 1024
)

// Images by version.
var Images = map[string]string{"8.1": "valkey/valkey:8.1-alpine", "8.0": "valkey/valkey:8.0-alpine"}

// EvictionPolicies are the maxmemory-policy values offered.
var EvictionPolicies = []string{"noeviction", "allkeys-lru", "allkeys-lfu", "allkeys-random", "volatile-lru", "volatile-lfu", "volatile-random", "volatile-ttl"}

// Range is a min/max pair.
type Range struct {
	Min int `json:"min"`
	Max int `json:"max"`
}

// Spec is what the user asks for.
type Spec struct {
	// Memory is maxmemory in MiB; autoscaled between Min and Max.
	Memory Range `json:"memory"`
	// Replicas is the read-replica count; autoscaled between Min and Max.
	Replicas Range `json:"replicas"`
	// CPU reserved per data member, in cores.
	CPU float64 `json:"cpu"`
	// Persistence: aof (AOF everysec + RDB snapshots, default), rdb or none.
	Persistence string `json:"persistence"`
	// EvictionPolicy is maxmemory-policy (default noeviction).
	EvictionPolicy string `json:"evictionPolicy"`
	// Nodes limits members to these nodes, within the project's allowed nodes.
	Nodes []string `json:"nodes,omitempty"`
	// Autoscaling settings.
	Autoscaling Autoscaling `json:"autoscaling"`
	// Postgres is set for PostgreSQL databases (and only for them).
	Postgres *PostgresSpec `json:"postgres,omitempty"`
}

// Autoscaling tunes the autoscaler; a dimension scales only when its
// min < max.
type Autoscaling struct {
	// CPUTarget is the replicas' average CPU (% of one core) to hold.
	CPUTarget float64 `json:"cpuTarget"`
	// MemoryHigh is used/maxmemory (%) above which memory grows.
	MemoryHigh float64 `json:"memoryHigh"`
}

// ForEngine prepares a spec for an engine: PostgreSQL specs carry their
// own section, Valkey specs never do.
func (s *Spec) ForEngine(engine string) {
	if engine == EnginePostgres && s.Postgres == nil {
		s.Postgres = &PostgresSpec{}
	} else if engine != EnginePostgres {
		s.Postgres = nil
	}
}

// Normalize fills in defaults and validates (call ForEngine first).
func (s *Spec) Normalize() error {
	if s.Postgres != nil {
		if err := s.normalizePostgres(); err != nil {
			return err
		}
		return s.normalizeNodes()
	}
	if s.Memory.Min == 0 {
		s.Memory.Min = 256
	}
	if s.Memory.Max == 0 {
		s.Memory.Max = s.Memory.Min
	}
	if s.Memory.Min < minMemoryMiB || s.Memory.Max > maxMemoryMiB || s.Memory.Min > s.Memory.Max {
		return fmt.Errorf("memory must satisfy %d ≤ min ≤ max ≤ %d MiB", minMemoryMiB, maxMemoryMiB)
	}
	if s.Replicas.Min < 0 || s.Replicas.Max > maxReplicas || s.Replicas.Min > s.Replicas.Max {
		return fmt.Errorf("replicas must satisfy 0 ≤ min ≤ max ≤ %d", maxReplicas)
	}
	if s.CPU == 0 {
		s.CPU = 0.1
	}
	if s.CPU < 0.01 || s.CPU > 64 {
		return errors.New("cpu must be between 0.01 and 64 cores")
	}
	switch s.Persistence {
	case "":
		s.Persistence = "aof"
	case "aof", "rdb", "none":
	default:
		return errors.New("persistence must be aof, rdb or none")
	}
	if s.EvictionPolicy == "" {
		s.EvictionPolicy = "noeviction"
	}
	if !slices.Contains(EvictionPolicies, s.EvictionPolicy) {
		return errors.New("evictionPolicy must be one of " + strings.Join(EvictionPolicies, ", "))
	}
	if err := s.normalizeNodes(); err != nil {
		return err
	}
	if s.Autoscaling.CPUTarget == 0 {
		s.Autoscaling.CPUTarget = 60
	}
	if s.Autoscaling.CPUTarget < 5 || s.Autoscaling.CPUTarget > 95 {
		return errors.New("autoscaling.cpuTarget must be between 5 and 95 (%)")
	}
	if s.Autoscaling.MemoryHigh == 0 {
		s.Autoscaling.MemoryHigh = 85
	}
	if s.Autoscaling.MemoryHigh < 50 || s.Autoscaling.MemoryHigh > 95 {
		return errors.New("autoscaling.memoryHigh must be between 50 and 95 (%)")
	}
	return nil
}

func (s *Spec) normalizeNodes() error {
	for _, n := range s.Nodes {
		if !workload.ValidNodeName(n) {
			return fmt.Errorf("nodes: %q is not a node name", n)
		}
	}
	slices.Sort(s.Nodes)
	s.Nodes = slices.Compact(s.Nodes)
	return nil
}

// HasSentinels reports whether the database runs Sentinel: a Valkey
// database that can have replicas.
func (s Spec) HasSentinels() bool { return s.Postgres == nil && s.Replicas.Max > 0 }

// State is what the operator decided and observed.
type State struct {
	// MemoryMiB is the current maxmemory.
	MemoryMiB int `json:"memoryMiB"`
	// Replicas is the current read-replica count.
	Replicas int `json:"replicas"`
	// Primary is the ordinal of the data member that is primary.
	Primary int `json:"primary"`
	// LimitMiB is the container memory limit the members were created with.
	LimitMiB int `json:"limitMiB"`
	// Bootstrapped: the PostgreSQL app user and database exist.
	Bootstrapped bool `json:"bootstrapped,omitempty"`
	// Restore: a PostgreSQL cluster created from another one's archive.
	Restore *RestoreState `json:"restore,omitempty"`
	// Database overrides the app database's name (a restored cluster
	// keeps its source's).
	Database string `json:"database,omitempty"`
	// BootstrapDCS is PostgreSQL's first dynamic configuration, frozen at
	// creation; DCSHash is the last one applied through Patroni (§13c2).
	BootstrapDCS string `json:"bootstrapDcs,omitempty"`
	DCSHash      string `json:"dcsHash,omitempty"`
}

// Secrets are sealed in the database row.
type Secrets struct {
	Password      string `json:"password"`      // the app user ("default")
	AdminPassword string `json:"adminPassword"` // "syncloud": replication, Sentinel, the controller (Postgres: the superuser)
	// PostgreSQL: the replication user, Patroni's REST API and its etcd user.
	ReplicationPassword string `json:"replicationPassword,omitempty"`
	RestPassword        string `json:"restPassword,omitempty"`
	EtcdPassword        string `json:"etcdPassword,omitempty"`
	// WalgKey encrypts the cluster's archive (hex, 32 bytes);
	// RestoreWalgKey reads the source's archive of a restored cluster.
	WalgKey        string `json:"walgKey,omitempty"`
	RestoreWalgKey string `json:"restoreWalgKey,omitempty"`
}

func parseSpec(raw string) (Spec, error) {
	var s Spec
	err := json.Unmarshal([]byte(raw), &s)
	return s, err
}

func parseState(raw string) State {
	var s State
	_ = json.Unmarshal([]byte(raw), &s)
	return s
}

func encode(v any) string {
	b, _ := json.Marshal(v)
	return string(b)
}

// containerLimit is the memory limit of a data member: maxmemory leaves
// room for buffers, replication backlog and fork copy-on-write.
func containerLimit(maxMiB int) int { return maxMiB*5/4 + 64 }

// reservation is what the scheduler counts for a data member at the
// current memory.
func reservation(memMiB int) int { return memMiB*6/5 + 32 }

// memberReservation is a data member's reservation for its engine: a
// PostgreSQL member's memory is its container size.
func memberReservation(spec Spec, st State) int {
	if spec.Postgres != nil {
		return st.MemoryMiB
	}
	return reservation(st.MemoryMiB)
}

// portOf is the engine's client port.
func portOf(d store.Database) int {
	if d.Engine == EnginePostgres {
		return PostgresPort
	}
	return Port
}
