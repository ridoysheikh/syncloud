package secgroup

import (
	"encoding/json"
	"strings"
	"testing"

	"syncloud/internal/store"
)

func defaults(t *testing.T) (in, out []Rule) {
	t.Helper()
	if err := json.Unmarshal([]byte(store.DefaultGroupInbound), &in); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal([]byte(store.DefaultGroupOutbound), &out); err != nil {
		t.Fatal(err)
	}
	return in, out
}

func model(t *testing.T) *Model {
	in, out := defaults(t)
	return &Model{
		Envs: map[string][]string{"shop": {"production", "staging"}, "billing": {"production"}},
		Services: []Service{
			{ID: "svc_web", Project: "shop", Env: "production", Name: "web", IPs: []string{"10.91.1.2"}},
			{ID: "svc_db", Project: "shop", Env: "production", Name: "db", IPs: []string{"10.91.2.2"}},
			{ID: "svc_stg", Project: "shop", Env: "staging", Name: "web", IPs: []string{"10.91.1.9"}},
			{ID: "svc_bill", Project: "billing", Env: "production", Name: "worker", IPs: []string{"10.91.3.3"}},
		},
		Standalone: []Standalone{{Project: "shop", Env: "production", IP: "10.91.1.50"}},
		Groups: []Group{
			{ID: "sg_shopdef", Project: "shop", Name: "default", Default: true, Inbound: in, Outbound: out},
			{ID: "sg_billdef", Project: "billing", Name: "default", Default: true, Inbound: in, Outbound: out},
			{ID: "sg_db", Project: "shop", Name: "db", Services: []string{"svc_db"},
				Inbound: []Rule{
					{Protocol: "tcp", Ports: "5432", Peers: []string{"environment:self"}},
					{Protocol: "tcp", Ports: "5432", Peers: []string{"service:billing/production/worker"}},
				},
				Outbound: []Rule{{Protocol: "udp", Ports: "53", Peers: []string{"any"}}},
			},
		},
		ClusterIPs:  []string{"10.90.0.1", "10.90.0.2"},
		PlatformIPs: []string{"10.90.0.1"},
	}
}

func TestParsePeer(t *testing.T) {
	for in, want := range map[string]string{
		"any": "any", "0.0.0.0/0": "any", "cluster": "cluster", "10.1.2.3": "10.1.2.3/32", "10.1.2.3/8": "10.0.0.0/8",
		"group:web": "group:shop/web", "group:billing/web": "group:billing/web", "service:production/api": "service:shop/production/api",
		"environment:self": "environment:self", "environment:staging": "environment:shop/staging", "project:billing": "project:billing",
	} {
		_, got, err := ParsePeer(in, "shop")
		if err != nil || got != want {
			t.Errorf("%s: %q, %v (want %q)", in, got, err, want)
		}
	}
	for _, bad := range []string{"", "2001:db8::/32", "group:", "group:a/b/c", "service:api", "x:y", "environment:Prod", "10.0.0.1 } accept"} {
		if _, _, err := ParsePeer(bad, "shop"); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
}

func TestEvaluate(t *testing.T) {
	m := model(t)
	web := Endpoint{ServiceID: "svc_web"}
	db := Endpoint{ServiceID: "svc_db"}
	cases := []struct {
		name       string
		src, dst   Endpoint
		proto      string
		port       int
		allowed    bool
		reasonPart string
	}{
		{"same env default", Endpoint{ServiceID: "svc_db"}, web, "tcp", 8080, false, "no outbound rule of shop/db"}, // db may only send DNS
		{"web to db 5432", web, db, "tcp", 5432, true, "shop/db inbound rule 1"},
		{"web to db 22", web, db, "tcp", 22, false, "no inbound rule of shop/db"},
		{"staging to prod web", Endpoint{ServiceID: "svc_stg"}, web, "tcp", 8080, false, "no inbound rule of shop/default"},
		{"billing to db", Endpoint{ServiceID: "svc_bill"}, db, "tcp", 5432, true, "inbound rule 2"},
		{"billing to web", Endpoint{ServiceID: "svc_bill"}, web, "tcp", 80, false, "inbound"},
		{"standalone run to web", Endpoint{Project: "shop", Env: "production"}, web, "tcp", 80, true, ""},
		{"platform", Endpoint{Platform: true}, db, "tcp", 9999, true, "cluster nodes"},
		{"node host", Endpoint{IP: "10.90.0.1"}, db, "tcp", 9999, true, "cluster nodes"},
		{"outside address", Endpoint{IP: "203.0.113.9"}, web, "tcp", 80, false, "inbound"},
	}
	for _, c := range cases {
		v := m.Evaluate(c.src, c.dst, c.proto, c.port)
		if v.Allowed != c.allowed || !strings.Contains(v.Reason, c.reasonPart) {
			t.Errorf("%s: %+v", c.name, v)
		}
	}
}

// TestCompileMatchesEvaluate checks that compiled sets put each service's
// IPs where the evaluator says rules apply.
func TestCompileMatchesEvaluate(t *testing.T) {
	m := model(t)
	pol := Compile(m)
	sets := map[string][]string{}
	for _, s := range pol.GetSets() {
		sets[s.GetName()] = s.GetIps()
	}
	has := func(set, ip string) bool {
		for _, x := range sets[set] {
			if x == ip {
				return true
			}
		}
		return false
	}
	// compiledAllows mirrors the agent: a connection is allowed in when some
	// inbound rule has the destination in its local set and the source in a
	// peer set or CIDR (protocol checks are left to the evaluator test).
	compiledAllows := func(dir, local, peer string) bool {
		for _, r := range pol.GetRules() {
			if r.GetDirection() != dir || !has(r.GetLocalSet(), local) {
				continue
			}
			for _, ps := range r.GetPeerSets() {
				if has(ps, peer) {
					return true
				}
			}
			for _, c := range r.GetPeerCidrs() {
				if c == "0.0.0.0/0" {
					return true
				}
			}
		}
		return false
	}
	if !compiledAllows("in", "10.91.2.2", "10.91.1.2") { // web -> db
		t.Error("web cannot reach db")
	}
	if !compiledAllows("in", "10.91.2.2", "10.91.3.3") { // billing worker -> db
		t.Error("billing cannot reach db")
	}
	if compiledAllows("in", "10.91.1.2", "10.91.1.9") { // staging -> prod web
		t.Error("staging reaches production")
	}
	if !compiledAllows("in", "10.91.1.2", "10.91.1.50") { // standalone run -> web
		t.Error("standalone run cannot reach web")
	}
	if !compiledAllows("out", "10.91.1.50", "1.1.1.1") { // standalone egress
		t.Error("standalone run has no egress")
	}
	for _, r := range pol.GetRules() {
		if strings.HasPrefix(r.GetId(), "sg_db:out:") && (r.GetProtocol() != "udp" || r.GetPorts() != "53") {
			t.Errorf("db outbound rule %+v", r)
		}
	}
	if len(pol.GetPlatformIps()) != 1 {
		t.Error("platform IPs missing")
	}
	// A new local container of web joins web's member sets via its labels.
	found := false
	for _, s := range pol.GetSets() {
		for _, mt := range s.GetMatch() {
			if mt == "svc:svc_web" && has(s.GetName(), "10.91.1.2") {
				found = true
			}
		}
	}
	if !found {
		t.Error("no set matches web's containers by label")
	}
}

func TestValidate(t *testing.T) {
	exists := func(p Peer) bool { return p.Kind != KindService || p.Name != "ghost" }
	rules, err := Validate("shop", []Rule{{Protocol: "TCP", Ports: "5432", Peers: []string{"service:production/web", "10.0.0.0/8"}}}, exists)
	if err != nil || rules[0].Protocol != "tcp" || rules[0].Peers[0] != "service:shop/production/web" || rules[0].Peers[1] != "10.0.0.0/8" {
		t.Fatalf("%+v %v", rules, err)
	}
	for _, bad := range [][]Rule{
		{{Protocol: "tcp", Ports: "1-2-3", Peers: []string{"any"}}},
		{{Protocol: "tcp", Peers: nil}},
		{{Protocol: "icmp", Ports: "22", Peers: []string{"any"}}},
		{{Protocol: "tcp", Peers: []string{"service:production/ghost"}}},
	} {
		if _, err := Validate("shop", bad, exists); err == nil {
			t.Errorf("%+v accepted", bad)
		}
	}
}
