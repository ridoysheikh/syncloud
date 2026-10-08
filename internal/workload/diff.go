package workload

import (
	"encoding/json"
	"fmt"
	"slices"
	"sort"
	"strings"
)

// Change is one difference between two revisions of a service (Phase 15a).
// Variable values are never included, only their names.
type Change struct {
	Field string `json:"field"`
	From  string `json:"from,omitempty"`
	To    string `json:"to,omitempty"`
}

// Diff summarizes what changes from revision a to b.
func Diff(a, b Spec) []Change {
	out := []Change{}
	add := func(field, from, to string) {
		if from != to {
			out = append(out, Change{Field: field, From: from, To: to})
		}
	}
	add("image", a.Image, b.Image)
	add("entrypoint", strings.Join(a.Entrypoint, " "), strings.Join(b.Entrypoint, " "))
	add("command", strings.Join(a.Command, " "), strings.Join(b.Command, " "))
	out = append(out, envDiff("variable", a.Env, b.Env)...)
	out = append(out, envDiff("shared variable", a.SharedEnv, b.SharedEnv)...)
	add("s3", s3String(a.S3), s3String(b.S3))
	add("ports", portsString(a.Ports), portsString(b.Ports))
	add("cpu", fmtNum(a.Resources.CPU), fmtNum(b.Resources.CPU))
	add("memory", fmtMiB(a.Resources.Memory), fmtMiB(b.Resources.Memory))
	add("cpu limit", fmtNum(a.Resources.CPULimit), fmtNum(b.Resources.CPULimit))
	add("memory limit", fmtMiB(a.Resources.MemoryLimit), fmtMiB(b.Resources.MemoryLimit))
	out = append(out, fieldsDiff("placement", a.Placement, b.Placement)...)
	out = append(out, fieldsDiff("health check", a.Health, b.Health)...)
	out = append(out, fieldsDiff("rollout", a.Deployment, b.Deployment)...)
	if a.RedeployedAt != b.RedeployedAt && b.RedeployedAt != "" {
		out = append(out, Change{Field: "redeploy", To: b.RedeployedAt})
	}
	return out
}

// envDiff names the variables added, removed or changed.
func envDiff(kind string, a, b map[string]string) []Change {
	var keys []string
	for k := range a {
		keys = append(keys, k)
	}
	for k := range b {
		if _, ok := a[k]; !ok {
			keys = append(keys, k)
		}
	}
	sort.Strings(keys)
	var out []Change
	for _, k := range keys {
		va, inA := a[k]
		vb, inB := b[k]
		switch {
		case !inA:
			out = append(out, Change{Field: kind + " " + k, To: "added"})
		case !inB:
			out = append(out, Change{Field: kind + " " + k, From: "removed"})
		case va != vb:
			out = append(out, Change{Field: kind + " " + k, From: "changed", To: "changed"})
		}
	}
	return out
}

func portsString(ps []Port) string {
	var s []string
	for _, p := range ps {
		s = append(s, fmt.Sprintf("%s:%d/%s", p.Name, p.Container, p.Protocol))
	}
	slices.Sort(s)
	return strings.Join(s, ", ")
}

func s3String(refs []S3Ref) string {
	var s []string
	for _, r := range refs {
		s = append(s, r.Endpoint+"/"+r.Bucket+"/"+r.Prefix)
	}
	slices.Sort(s)
	return strings.Join(s, ", ")
}

func fmtNum(f float64) string {
	if f == 0 {
		return ""
	}
	return fmt.Sprint(f)
}

func fmtMiB(n int) string {
	if n == 0 {
		return ""
	}
	return fmt.Sprintf("%d MiB", n)
}

// fieldsDiff compares two settings objects key by key ("health check path:
// /ok → /missing"); an object added or removed as a whole is one change.
func fieldsDiff(name string, a, b any) []Change {
	ma, mb := fieldMap(a), fieldMap(b)
	switch {
	case len(ma) == 0 && len(mb) == 0:
		return nil
	case len(ma) == 0:
		return []Change{{Field: name, To: jsonString(b)}}
	case len(mb) == 0:
		return []Change{{Field: name, From: jsonString(a)}}
	}
	keys := make([]string, 0, len(ma)+len(mb))
	for k := range ma {
		keys = append(keys, k)
	}
	for k := range mb {
		if _, ok := ma[k]; !ok {
			keys = append(keys, k)
		}
	}
	sort.Strings(keys)
	var out []Change
	for _, k := range keys {
		if va, vb := ma[k], mb[k]; va != vb {
			out = append(out, Change{Field: name + " " + k, From: va, To: vb})
		}
	}
	return out
}

// fieldMap is v's JSON object with each value as compact JSON (strings
// unquoted).
func fieldMap(v any) map[string]string {
	b, _ := json.Marshal(v)
	var raw map[string]json.RawMessage
	if json.Unmarshal(b, &raw) != nil {
		return nil
	}
	out := make(map[string]string, len(raw))
	for k, r := range raw {
		var s string
		if json.Unmarshal(r, &s) == nil {
			out[k] = s
		} else {
			out[k] = string(r)
		}
	}
	return out
}

func jsonString(v any) string {
	b, _ := json.Marshal(v)
	if s := string(b); s != "null" && s != "{}" {
		return s
	}
	return ""
}
