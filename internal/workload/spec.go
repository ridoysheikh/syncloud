// Package workload runs services (§4, §5.2–5.3): it validates task
// definitions, places tasks on nodes and reconciles desired with observed state.
package workload

import (
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strings"
)

// Spec is one immutable task definition revision (one container per task).
type Spec struct {
	Image     string            `json:"image"`
	Command   []string          `json:"command,omitempty"`
	Env       map[string]string `json:"env,omitempty"`
	Ports     []Port            `json:"ports,omitempty"`
	Resources Resources         `json:"resources"`
	Placement Placement         `json:"placement"`
}

// Port is a container port. HTTP ports get a public route (§5.7).
type Port struct {
	Name      string `json:"name"`
	Container int    `json:"container"`
	Protocol  string `json:"protocol"` // http (default), tcp, udp
}

// Resources are reservations used for placement, plus optional hard limits.
type Resources struct {
	CPU         float64 `json:"cpu"`                   // cores reserved (default 0.1)
	Memory      int     `json:"memory"`                // MiB reserved (default 128)
	CPULimit    float64 `json:"cpuLimit,omitempty"`    // cores, hard limit (0 = none)
	MemoryLimit int     `json:"memoryLimit,omitempty"` // MiB, hard limit (default = 2 × memory)
}

type Placement struct {
	Strategy string `json:"strategy"` // spread (default) | binpack
}

var (
	nameRE   = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]{0,30}[a-z0-9])?$`)
	envKeyRE = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)
)

// ValidName checks project, environment, service and port names: DNS labels
// short enough to combine into hostnames like <svc>-<env>-<project>.<domain>.
func ValidName(s string) error {
	if !nameRE.MatchString(s) {
		return errors.New("must be 1–32 lowercase letters, digits or hyphens, starting and ending with a letter or digit")
	}
	return nil
}

// Normalize fills defaults and validates.
func (s *Spec) Normalize() error {
	s.Image = strings.TrimSpace(s.Image)
	if s.Image == "" || strings.ContainsAny(s.Image, " \t\n") {
		return errors.New("image is required, e.g. nginx:1.27 or ghcr.io/org/app:tag")
	}
	for k := range s.Env {
		if !envKeyRE.MatchString(k) {
			return fmt.Errorf("invalid environment variable name %q", k)
		}
		if strings.HasPrefix(k, "SYNCLOUD_") {
			return fmt.Errorf("%s: the SYNCLOUD_ prefix is reserved", k)
		}
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
