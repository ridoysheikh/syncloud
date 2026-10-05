package api

import (
	"encoding/json"
	"sort"
	"strings"
	"testing"
)

// SpecOperation is one operation from openapi.json.
type SpecOperation struct {
	Method, Path, ID string
	Public           bool // security: []
}

// SpecOperations parses OpenAPISpec. Exported for the CLI parity test.
func SpecOperations(t testing.TB) []SpecOperation {
	t.Helper()
	var doc struct {
		Paths map[string]map[string]struct {
			OperationID string             `json:"operationId"`
			Security    *[]json.RawMessage `json:"security"`
		} `json:"paths"`
	}
	if err := json.Unmarshal(OpenAPISpec, &doc); err != nil {
		t.Fatalf("openapi.json: %v", err)
	}
	var ops []SpecOperation
	for path, methods := range doc.Paths {
		for m, o := range methods {
			ops = append(ops, SpecOperation{
				Method: strings.ToUpper(m), Path: path, ID: o.OperationID,
				Public: o.Security != nil && len(*o.Security) == 0,
			})
		}
	}
	sort.Slice(ops, func(i, j int) bool { return ops[i].Path+ops[i].Method < ops[j].Path+ops[j].Method })
	return ops
}

// Every route must be documented, every documented operation must exist, and
// the spec's auth requirements must match the routes (§5.1).
func TestSpecMatchesRoutes(t *testing.T) {
	routes := map[string]Route{}
	for _, r := range (&Server{}).Routes() {
		routes[r.Method+" "+r.Path] = r
	}
	ids := map[string]bool{}
	for _, op := range SpecOperations(t) {
		key := op.Method + " " + op.Path
		r, ok := routes[key]
		if !ok {
			t.Errorf("spec documents %s, but no route serves it", key)
			continue
		}
		delete(routes, key)
		if op.ID == "" {
			t.Errorf("%s has no operationId", key)
		} else if ids[op.ID] {
			t.Errorf("duplicate operationId %q", op.ID)
		}
		ids[op.ID] = true
		if op.Public != r.Public {
			t.Errorf("%s: spec public=%v, route public=%v", key, op.Public, r.Public)
		}
	}
	for key := range routes {
		t.Errorf("route %s is missing from openapi.json", key)
	}
}
