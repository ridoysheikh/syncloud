// Package workload runs services (§4, §5.2–5.3): it validates task
// definitions, places tasks on nodes and reconciles desired with observed state.
package workload

import (
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"sort"
	"strings"
	"time"
)

// Spec is one immutable task definition revision (one container per task).
type Spec struct {
	Image      string            `json:"image"`
	Entrypoint []string          `json:"entrypoint,omitempty"` // replaces the image ENTRYPOINT
	Command    []string          `json:"command,omitempty"`
	Env        map[string]string `json:"env,omitempty"`
	// SharedEnv is a snapshot of the environment's shared variables, taken
	// by the platform for every revision (never user-set). Env wins over it.
	SharedEnv map[string]string `json:"sharedEnv,omitempty"`
	// S3 snapshots the service's S3 bindings (§16) for every revision, set
	// by the platform; credentials are added when a task starts.
	S3        []S3Ref   `json:"s3,omitempty"`
	Ports     []Port    `json:"ports,omitempty"`
	Resources Resources `json:"resources"`
	Placement Placement `json:"placement"`
	// Health is probed by the agent (§5.6); traffic only reaches healthy tasks.
	Health     *HealthCheck `json:"health,omitempty"`
	Deployment Deployment   `json:"deployment"`
	// RedeployedAt is set by the platform for a redeploy of an unchanged
	// spec (Phase 15a), so the restart is a revision of its own.
	RedeployedAt string `json:"redeployedAt,omitempty"`
}

// S3Ref is a bucket bound to the service: its tasks get <EnvPrefix>S3_BUCKET,
// <EnvPrefix>AWS_ACCESS_KEY_ID and so on.
type S3Ref struct {
	Endpoint  string `json:"endpoint"` // name
	Bucket    string `json:"bucket"`
	Prefix    string `json:"prefix,omitempty"`
	EnvPrefix string `json:"envPrefix,omitempty"`
}

// HealthCheck defines how a task's health is probed.
type HealthCheck struct {
	Type        string   `json:"type"`                  // http | tcp | cmd
	Path        string   `json:"path,omitempty"`        // http (default /)
	Port        string   `json:"port,omitempty"`        // port name (default: the first port)
	Command     []string `json:"command,omitempty"`     // cmd: exit 0 = healthy
	Interval    int      `json:"interval,omitempty"`    // seconds (default 10)
	Timeout     int      `json:"timeout,omitempty"`     // seconds (default 3)
	Retries     int      `json:"retries,omitempty"`     // consecutive failures before unhealthy (default 3)
	StartPeriod int      `json:"startPeriod,omitempty"` // seconds in which failures do not count (default 10)
}

// Deployment controls rollouts (§5.4).
type Deployment struct {
	// CircuitBreaker stops a rollout whose new tasks keep failing (default on).
	CircuitBreaker *bool `json:"circuitBreaker,omitempty"`
	// Rollback returns to the previous revision when the breaker trips (default on).
	Rollback *bool `json:"rollback,omitempty"`
	// DrainSeconds keeps a retired task running after it leaves the routes
	// and service VIPs, so Traefik (which polls every 2s) and in-flight
	// requests stop using it before it stops (default 5, 0 = stop at once).
	DrainSeconds *int `json:"drainSeconds,omitempty"`
}

// DefaultDrain is the drain time of tasks with ports when the spec sets none.
var DefaultDrain = 5 * time.Second

func boolPtr(b bool) *bool { return &b }

// Port is a container port. HTTP ports get a public route (§5.7).
type Port struct {
	Name      string `json:"name"`
	Container int    `json:"container"`
	Protocol  string `json:"protocol"` // http (default), tcp, udp
}

// Resources are what a task needs, how placement treats it, and optional
// hard limits.
//
// CPU and memory are shared by default: they say what the task is expected
// to use. Placement goes by what nodes really use, CPU never stops a task
// from being placed (a busy node is slower, not full), and under contention
// tasks get CPU in proportion to their cpu. Reserved (CPUMode or
// MemoryMode "reserved") sets the amount aside: placement only uses a node
// with that much unreserved, whatever the node's load.
type Resources struct {
	CPU         float64 `json:"cpu"`                   // cores expected, or reserved (default 0.1)
	Memory      int     `json:"memory"`                // MiB expected, or reserved (default 128)
	CPUMode     string  `json:"cpuMode,omitempty"`     // "" (shared) | "reserved"
	MemoryMode  string  `json:"memoryMode,omitempty"`  // "" (shared) | "reserved"
	CPULimit    float64 `json:"cpuLimit,omitempty"`    // cores, hard limit (0 = none)
	MemoryLimit int     `json:"memoryLimit,omitempty"` // MiB, hard limit (default = 2 × memory)
}

// Resource modes.
const (
	ResourceShared   = "" // the default; "shared" is accepted for it
	ResourceReserved = "reserved"
)

// ReservedCPU is the CPU the task sets aside (0 when shared).
func (r Resources) ReservedCPU() float64 {
	if r.CPUMode == ResourceReserved {
		return r.CPU
	}
	return 0
}

// ReservedMemory is the memory the task sets aside (0 when shared).
func (r Resources) ReservedMemory() int {
	if r.MemoryMode == ResourceReserved {
		return r.Memory
	}
	return 0
}

type Placement struct {
	Strategy string `json:"strategy"` // spread (default) | binpack
	// Node pins tasks to one node by name (e.g. builds on the controller).
	Node string `json:"node,omitempty"`
	// Pools limits tasks to node pools ("default" is nodes outside any pool).
	Pools []string `json:"pools,omitempty"`
	// Nodes limits tasks to these nodes by name, within the project's
	// allowed nodes (§6.3). Naming the controller node runs tasks there even
	// when it takes no general workloads.
	Nodes []string `json:"nodes,omitempty"`

	// project holds the project's allowed nodes while placing; never stored.
	project []string
	// avoid lists nodes to keep away from (anti-affinity); never stored.
	avoid []string
}

// Avoiding returns spec kept off the named nodes (e.g. a database's other
// members). Never stored.
func (s Spec) Avoiding(nodes []string) Spec {
	s.Placement.avoid = nodes
	return s
}

// WithProjectNodes returns spec limited to a project's allowed nodes as
// well (none = any). The limit is applied at placement and not stored, so
// changing a project's nodes does not create service revisions.
func (s Spec) WithProjectNodes(nodes []string) Spec {
	s.Placement.project = nodes
	return s
}

// Allows reports whether a task may run on the named node.
func (p Placement) Allows(node string) bool {
	return !slices.Contains(p.avoid, node) &&
		(p.Node == "" || p.Node == node) &&
		(len(p.Nodes) == 0 || slices.Contains(p.Nodes, node)) &&
		(len(p.project) == 0 || slices.Contains(p.project, node))
}

// Names reports whether the node is named explicitly (pinned or listed).
func (p Placement) Names(node string) bool {
	return p.Node == node || slices.Contains(p.Nodes, node) || slices.Contains(p.project, node)
}

// ValidNodeName checks a node name.
func ValidNodeName(s string) bool { return nodeNameRE.MatchString(s) }

var (
	nameRE     = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]{0,30}[a-z0-9])?$`)
	envKeyRE   = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)
	nodeNameRE = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$`)
)

// ValidName checks project, environment, service and port names: DNS labels
// short enough to combine into hostnames like <svc>-<env>-<project>.<domain>.
func ValidName(s string) error {
	if !nameRE.MatchString(s) {
		return errors.New("must be 1–32 lowercase letters, digits or hyphens, starting and ending with a letter or digit")
	}
	return nil
}

// AwaitingBuild is the image of a service built from Git before its first
// build: it runs no tasks until a build is deployed (§5.8).
const AwaitingBuild = "@build"

// ValidateEnv checks environment variable names.
func ValidateEnv(env map[string]string) error {
	for k := range env {
		if !envKeyRE.MatchString(k) {
			return fmt.Errorf("invalid environment variable name %q", k)
		}
		if strings.HasPrefix(k, "SYNCLOUD_") {
			return fmt.Errorf("%s: the SYNCLOUD_ prefix is reserved", k)
		}
	}
	return nil
}

// Normalize fills defaults and validates.
func (s *Spec) Normalize() error {
	s.Image = strings.TrimSpace(s.Image)
	if s.Image == "" || strings.ContainsAny(s.Image, " \t\n") {
		return errors.New("image is required, e.g. nginx:1.27 or ghcr.io/org/app:tag")
	}
	if err := ValidateEnv(s.Env); err != nil {
		return err
	}
	seen := map[string]bool{}
	for i := range s.Ports {
		p := &s.Ports[i]
		if p.Protocol == "" {
			p.Protocol = "http"
		}
		if p.Name == "" {
			p.Name = fmt.Sprint(p.Container)
			if i == 0 && p.Protocol == "http" {
				p.Name = "http"
			}
		}
		if p.Protocol != "http" && p.Protocol != "tcp" && p.Protocol != "udp" {
			return fmt.Errorf("port %s: protocol must be http, tcp or udp", p.Name)
		}
		if p.Container < 1 || p.Container > 65535 {
			return fmt.Errorf("port %s: container port must be 1–65535", p.Name)
		}
		if !nameRE.MatchString(p.Name) && !regexp.MustCompile(`^\d+$`).MatchString(p.Name) {
			return fmt.Errorf("port name %q must be a DNS label", p.Name)
		}
		if seen[p.Name] {
			return fmt.Errorf("duplicate port name %q", p.Name)
		}
		seen[p.Name] = true
	}
	r := &s.Resources
	if r.CPU == 0 {
		r.CPU = 0.1
	}
	if r.Memory == 0 {
		r.Memory = 128
	}
	if r.MemoryLimit == 0 {
		r.MemoryLimit = 2 * r.Memory
	}
	if r.CPU < 0.01 || r.CPU > 256 || r.CPULimit < 0 || r.CPULimit > 256 {
		return errors.New("cpu must be between 0.01 and 256 cores")
	}
	if r.Memory < 4 || r.Memory > 4<<20 || r.MemoryLimit < r.Memory {
		return errors.New("memory must be 4 MiB–4 TiB, and memoryLimit at least memory")
	}
	for _, m := range []*string{&r.CPUMode, &r.MemoryMode} {
		switch *m {
		case "shared":
			*m = ResourceShared
		case ResourceShared, ResourceReserved:
		default:
			return fmt.Errorf("cpuMode and memoryMode must be shared or reserved, not %q", *m)
		}
	}
	if d := s.Deployment.DrainSeconds; d != nil && (*d < 0 || *d > 300) {
		return errors.New("deployment.drainSeconds must be between 0 and 300")
	}
	if s.Deployment.CircuitBreaker == nil {
		s.Deployment.CircuitBreaker = boolPtr(true)
	}
	if s.Deployment.Rollback == nil {
		s.Deployment.Rollback = boolPtr(true)
	}
	if h := s.Health; h != nil {
		switch h.Type {
		case "http", "tcp":
			if len(s.Ports) == 0 {
				return fmt.Errorf("an %s health check needs a port", h.Type)
			}
			if h.Port == "" {
				h.Port = s.Ports[0].Name
			}
			if _, ok := s.PortNumber(h.Port); !ok {
				return fmt.Errorf("health check port %q is not one of the service's ports", h.Port)
			}
			if h.Type == "http" && h.Path == "" {
				h.Path = "/"
			}
			if h.Type == "http" && !strings.HasPrefix(h.Path, "/") {
				return errors.New("health check path must start with /")
			}
			h.Command = nil
		case "cmd":
			if len(h.Command) == 0 {
				return errors.New("a cmd health check needs a command")
			}
			h.Path, h.Port = "", ""
		default:
			return errors.New("health check type must be http, tcp or cmd")
		}
		for _, v := range []*int{&h.Interval, &h.Timeout, &h.Retries, &h.StartPeriod} {
			if *v < 0 || *v > 3600 {
				return errors.New("health check timings must be 0–3600 seconds")
			}
		}
		if h.Interval == 0 {
			h.Interval = 10
		}
		if h.Timeout == 0 {
			h.Timeout = 3
		}
		if h.Retries == 0 {
			h.Retries = 3
		}
		if h.StartPeriod == 0 {
			h.StartPeriod = 10
		}
	}
	if s.Placement.Node != "" && !nodeNameRE.MatchString(s.Placement.Node) {
		return errors.New("placement node must be a node name")
	}
	if len(s.Placement.Nodes) > 64 {
		return errors.New("placement nodes: at most 64")
	}
	for _, n := range s.Placement.Nodes {
		if !nodeNameRE.MatchString(n) {
			return fmt.Errorf("placement nodes: %q is not a node name", n)
		}
	}
	slices.Sort(s.Placement.Nodes)
	s.Placement.Nodes = slices.Compact(s.Placement.Nodes)
	switch s.Placement.Strategy {
	case "":
		s.Placement.Strategy = "spread"
	case "spread", "binpack":
	default:
		return errors.New("placement strategy must be spread or binpack")
	}
	return nil
}

// Canonical is the stored form; equal specs give equal strings.
func (s Spec) Canonical() string {
	b, _ := json.Marshal(s)
	return string(b)
}

// ParseSpec reads a stored spec.
func ParseSpec(raw string) (Spec, error) {
	var s Spec
	err := json.Unmarshal([]byte(raw), &s)
	return s, err
}

// PortNumber resolves a port name.
func (s Spec) PortNumber(name string) (int, bool) {
	for _, p := range s.Ports {
		if p.Name == name {
			return p.Container, true
		}
	}
	return 0, false
}

// HTTPPorts returns the ports that get public routes, in order.
func (s Spec) HTTPPorts() []Port {
	var out []Port
	for _, p := range s.Ports {
		if p.Protocol == "http" {
			out = append(out, p)
		}
	}
	return out
}

func sortedKeys(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
