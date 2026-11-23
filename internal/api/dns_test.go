package api

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

func TestDomainsAndScopedRecordKeys(t *testing.T) {
	e := newEnv(t)
	e.setupRoot(t)

	// Connect a provider, register the domain: all projects may use it.
	resp, p := e.do(t, "POST", "/api/v1/dns/providers", map[string]string{"name": "cf", "token": "secret-token"}, nil)
	if resp.StatusCode != 201 {
		t.Fatalf("create provider: %d %v", resp.StatusCode, p)
	}
	if _, leaked := p["token"]; leaked {
		t.Fatal("the token was returned")
	}
	resp, z := e.do(t, "POST", "/api/v1/domains", map[string]any{"name": "example.com", "provider": p["id"]}, nil)
	if resp.StatusCode != 201 || z["mode"] != "managed" {
		t.Fatalf("add domain: %d %v", resp.StatusCode, z)
	}
	if g := z["grants"].([]any); len(g) != 1 || g[0].(map[string]any)["project"] != "*" {
		t.Fatalf("grants: %v", z["grants"])
	}
	if resp, _ := e.do(t, "POST", "/api/v1/domains", map[string]any{"name": "example.com"}, nil); resp.StatusCode != 409 {
		t.Fatalf("a second registration: %d", resp.StatusCode)
	}

	// An app's key: it may create and delete TXT records under _acme-challenge, nothing else.
	doc := `{"Version":"2026-01","Statement":[{"Effect":"Allow","Action":["domain:ListDomainRecords","domain:CreateDomainRecord","domain:DeleteDomainRecord"],
	  "Resource":"srn:syncloud:domain/example.com/record/_acme-challenge.*","Condition":{"StringEquals":{"syn:DnsRecordType":"TXT"}}}]}`
	resp, pol := e.do(t, "POST", "/api/v1/iam/policies", map[string]any{"name": "acme-writer", "document": json.RawMessage(doc)}, nil)
	if resp.StatusCode != 201 {
		t.Fatalf("policy: %d %v", resp.StatusCode, pol)
	}
	resp, app := e.do(t, "POST", "/api/v1/iam/users", map[string]string{"email": "app@example.com", "name": "App", "password": pw}, nil)
	if resp.StatusCode != 201 {
		t.Fatalf("user: %d %v", resp.StatusCode, app)
	}
	e.do(t, "POST", "/api/v1/iam/attachments", map[string]string{"principalType": "user", "principalId": app["id"].(string), "policy": pol["id"].(string)}, nil)
	a := e.as(t)
	if resp, _ := a.do(t, "POST", "/api/v1/auth/login", map[string]string{"email": "app@example.com", "password": pw}, nil); resp.StatusCode != 200 {
		t.Fatalf("login: %d", resp.StatusCode)
	}

	rec := func(typ, name, content string) map[string]string {
		return map[string]string{"type": typ, "name": name, "content": content}
	}
	resp, made := a.do(t, "POST", "/api/v1/domains/example.com/records", rec("TXT", "_acme-challenge.app", "token-1"), nil)
	if resp.StatusCode != 201 {
		t.Fatalf("the scoped key may create its challenge record: %d %v", resp.StatusCode, made)
	}
	for _, c := range []struct {
		what string
		body map[string]string
	}{
		{"an A record", rec("A", "_acme-challenge.app", "203.0.113.5")},
		{"a TXT record elsewhere", rec("TXT", "www", "x")},
		{"a TXT record at the apex", rec("TXT", "@", "x")},
	} {
		if resp, body := a.do(t, "POST", "/api/v1/domains/example.com/records", c.body, nil); resp.StatusCode != http.StatusForbidden {
			t.Errorf("%s: %d %v (want 403)", c.what, resp.StatusCode, body)
		}
	}
	// Other records exist; the key lists only what it may see.
	e.do(t, "POST", "/api/v1/domains/example.com/records", rec("A", "www", "203.0.113.7"), nil)
	resp, list := a.do(t, "GET", "/api/v1/domains/example.com/records", nil, nil)
	items := list["items"].([]any)
	if resp.StatusCode != 200 || len(items) != 1 || items[0].(map[string]any)["name"] != "_acme-challenge.app.example.com" {
		t.Fatalf("scoped listing: %d %v", resp.StatusCode, list)
	}
	// It may delete its record, not the others.
	wwwID := ""
	_, all := e.do(t, "GET", "/api/v1/domains/example.com/records?name=www.example.com", nil, nil)
	wwwID = all["items"].([]any)[0].(map[string]any)["id"].(string)
	if resp, _ := a.do(t, "DELETE", "/api/v1/domains/example.com/records/"+wwwID, nil, nil); resp.StatusCode != 403 {
		t.Errorf("deleting an out-of-scope record: %d", resp.StatusCode)
	}
	if resp, _ := a.do(t, "DELETE", "/api/v1/domains/example.com/records/"+made["id"].(string), nil, nil); resp.StatusCode != 204 {
		t.Errorf("deleting its own record: %d", resp.StatusCode)
	}
	// Domain administration is not in its policy.
	if resp, _ := a.do(t, "GET", "/api/v1/domains/example.com", nil, nil); resp.StatusCode != 403 {
		t.Errorf("reading the domain: %d", resp.StatusCode)
	}
	if resp, _ := a.do(t, "DELETE", "/api/v1/domains/example.com", nil, nil); resp.StatusCode != 403 {
		t.Errorf("removing the domain: %d", resp.StatusCode)
	}

	// Pointing: a plan changes nothing; apply creates the records; a service address needs force to delete.
	resp, plan := e.do(t, "POST", "/api/v1/domains/example.com/records/point?plan=true", map[string]any{"name": "shop", "targets": []string{"203.0.113.1"}}, nil)
	if resp.StatusCode != 200 || plan["applied"] != nil {
		t.Fatalf("plan: %d %v", resp.StatusCode, plan)
	}
	if resp, body := e.do(t, "POST", "/api/v1/domains/example.com/records/point", map[string]any{"name": "shop", "targets": []string{"203.0.113.1"}}, nil); resp.StatusCode != 200 || body["applied"] == nil {
		t.Fatalf("point: %d %v", resp.StatusCode, body)
	}
	if got := e.dns.Snapshot(); len(got) != 2 {
		t.Fatalf("records: %v", got)
	}

	// An app's record work shows in the audit log.
	if resp, _ := e.do(t, "DELETE", "/api/v1/dns/providers/"+p["id"].(string), nil, nil); resp.StatusCode != 204 {
		t.Fatalf("disconnect: %d", resp.StatusCode)
	}
	resp, z = e.do(t, "GET", "/api/v1/domains/example.com", nil, nil)
	if z["mode"] != "manual" {
		t.Fatalf("a domain without its provider is manual: %v", z)
	}
	if resp, _ := e.do(t, "GET", "/api/v1/domains/example.com/records", nil, nil); resp.StatusCode != 409 {
		t.Fatalf("manual domain records: %d", resp.StatusCode)
	}
}

func TestChallengeDelegation(t *testing.T) {
	e := newEnv(t)
	e.setupRoot(t)

	// example.com has no provider (manual); validate.net is managed.
	if resp, z := e.do(t, "POST", "/api/v1/domains", map[string]any{"name": "example.com"}, nil); resp.StatusCode != 201 {
		t.Fatalf("%d %v", resp.StatusCode, z)
	}
	_, route := e.do(t, "GET", "/api/v1/domains/example.com/challenge?name=*.shop.example.com", nil, nil)
	if route["mode"] != "manual" || route["name"] != "_acme-challenge.shop.example.com" || route["auto"] != false {
		t.Fatalf("manual: %v", route)
	}
	if resp, _ := e.do(t, "GET", "/api/v1/domains/example.com/challenge?name=other.org", nil, nil); resp.StatusCode != 400 {
		t.Fatalf("a name outside the domain: %d", resp.StatusCode)
	}

	_, p := e.do(t, "POST", "/api/v1/dns/providers", map[string]string{"name": "val", "token": "validation"}, nil)
	if resp, z := e.do(t, "POST", "/api/v1/domains", map[string]any{"name": "validate.net", "provider": p["id"]}, nil); resp.StatusCode != 201 {
		t.Fatalf("%d %v", resp.StatusCode, z)
	}
	if resp, body := e.do(t, "PUT", "/api/v1/domains/example.com", map[string]any{"challengeZone": "example.com"}, nil); resp.StatusCode != 400 {
		t.Fatalf("a domain cannot delegate to itself: %d %v", resp.StatusCode, body)
	}
	if resp, body := e.do(t, "PUT", "/api/v1/domains/example.com", map[string]any{"challengeZone": "nowhere.org"}, nil); resp.StatusCode != 400 {
		t.Fatalf("unknown validation domain: %d %v", resp.StatusCode, body)
	}
	resp, z := e.do(t, "PUT", "/api/v1/domains/example.com", map[string]any{"challengeZone": "validate.net"}, nil)
	if resp.StatusCode != 200 || z["challengeZone"] != "validate.net" {
		t.Fatalf("%d %v", resp.StatusCode, z)
	}

	_, route = e.do(t, "GET", "/api/v1/domains/example.com/challenge", nil, nil)
	cname, _ := route["cname"].(map[string]any)
	if route["mode"] != "delegated" || route["auto"] != true || cname == nil || cname["present"] != false || cname["managed"] != false ||
		!strings.HasSuffix(route["target"].(string), ".acme.validate.net") {
		t.Fatalf("delegated: %v", route)
	}
	// A manual domain: nothing to create for it; the response is the CNAME to add by hand.
	_, again := e.do(t, "POST", "/api/v1/domains/example.com/challenge/delegation", nil, nil)
	if again["target"] != route["target"] {
		t.Fatalf("%v", again)
	}

	// An app's key that may only read the challenge route of its domain.
	doc := `{"Version":"2026-01","Statement":[{"Effect":"Allow","Action":["domain:GetDomainChallenge"],"Resource":"srn:syncloud:domain/example.com"}]}`
	_, pol := e.do(t, "POST", "/api/v1/iam/policies", map[string]any{"name": "challenge-reader", "document": json.RawMessage(doc)}, nil)
	_, app := e.do(t, "POST", "/api/v1/iam/users", map[string]string{"email": "app@example.com", "name": "App", "password": pw}, nil)
	e.do(t, "POST", "/api/v1/iam/attachments", map[string]string{"principalType": "user", "principalId": app["id"].(string), "policy": pol["id"].(string)}, nil)
	a := e.as(t)
	a.do(t, "POST", "/api/v1/auth/login", map[string]string{"email": "app@example.com", "password": pw}, nil)
	if resp, _ := a.do(t, "GET", "/api/v1/domains/example.com/challenge", nil, nil); resp.StatusCode != 200 {
		t.Fatalf("the reader: %d", resp.StatusCode)
	}
	if resp, _ := a.do(t, "POST", "/api/v1/domains/example.com/challenge/delegation", nil, nil); resp.StatusCode != 403 {
		t.Fatalf("creating the delegation needs its own action: %d", resp.StatusCode)
	}
	if resp, _ := a.do(t, "PUT", "/api/v1/domains/example.com", map[string]any{"challengeZone": ""}, nil); resp.StatusCode != 403 {
		t.Fatalf("changing the domain: %d", resp.StatusCode)
	}
}

func TestChallengeServerSetupAndUse(t *testing.T) {
	e := newEnv(t)
	e.setupRoot(t)

	_, p := e.do(t, "POST", "/api/v1/dns/providers", map[string]string{"name": "cf", "token": "x"}, nil)
	e.do(t, "POST", "/api/v1/domains", map[string]any{"name": "example.com", "provider": p["id"]}, nil)
	e.do(t, "POST", "/api/v1/domains", map[string]any{"name": "manual.org"}, nil)

	// Off: a manual domain is answered by hand, and the server cannot be chosen.
	_, srv := e.do(t, "GET", "/api/v1/domains/challenge-server", nil, nil)
	if srv["enabled"] != false {
		t.Fatalf("%v", srv)
	}
	_, route := e.do(t, "GET", "/api/v1/domains/manual.org/challenge", nil, nil)
	if route["mode"] != "manual" {
		t.Fatalf("%v", route)
	}
	if resp, body := e.do(t, "PUT", "/api/v1/domains/example.com", map[string]any{"challengeBuiltin": true}, nil); resp.StatusCode != 400 {
		t.Fatalf("%d %v", resp.StatusCode, body)
	}

	// One click: the zone is the sslip.io one for the controller's detected address.
	resp, srv := e.do(t, "PUT", "/api/v1/domains/challenge-server", map[string]any{"enabled": true}, nil)
	if resp.StatusCode != 200 || srv["enabled"] != true || srv["sslip"] != true || srv["zone"] != "_acme-challenge.203-0-113-10.sslip.io" || srv["ns"] != "203-0-113-10.sslip.io" || srv["ip"] != "203.0.113.10" || srv["listening"] != true {
		t.Fatalf("%d %v", resp.StatusCode, srv)
	}
	if _, ok := srv["needed"]; ok {
		t.Fatal("there are no records to create any more")
	}
	if resp, _ := e.do(t, "PUT", "/api/v1/domains/challenge-server", map[string]any{"enabled": true, "ip": "not-an-ip"}, nil); resp.StatusCode != 400 {
		t.Fatalf("a bad address: %d", resp.StatusCode)
	}
	if resp, _ := e.do(t, "POST", "/api/v1/domains/challenge-server/records", nil, nil); resp.StatusCode < 400 {
		t.Fatalf("the records step is gone: %d", resp.StatusCode)
	}
	// The check reports each step; with no resolver reachable it fails without hanging.
	_, chk := e.do(t, "POST", "/api/v1/domains/challenge-server/check", nil, nil)
	if chk["ok"] != false || len(chk["checks"].([]any)) != 2 {
		t.Fatalf("%v", chk)
	}

	// The manual domain now uses it by itself; a provider domain keeps its provider until it chooses.
	_, route = e.do(t, "GET", "/api/v1/domains/manual.org/challenge", nil, nil)
	cname, _ := route["cname"].(map[string]any)
	if route["mode"] != "builtin" || route["auto"] != true || !strings.HasSuffix(route["target"].(string), "._acme-challenge.203-0-113-10.sslip.io") || cname == nil || cname["managed"] != false {
		t.Fatalf("%v", route)
	}
	_, route = e.do(t, "GET", "/api/v1/domains/example.com/challenge", nil, nil)
	if route["mode"] != "direct" {
		t.Fatalf("%v", route)
	}
	resp, z := e.do(t, "PUT", "/api/v1/domains/example.com", map[string]any{"challengeBuiltin": true}, nil)
	if resp.StatusCode != 200 || z["challengeBuiltin"] != true {
		t.Fatalf("%d %v", resp.StatusCode, z)
	}
	_, route = e.do(t, "POST", "/api/v1/domains/example.com/challenge/delegation", nil, nil)
	if route["mode"] != "builtin" || route["cname"].(map[string]any)["managed"] != true {
		t.Fatalf("%v", route)
	}

	// Server settings need their own permission.
	doc := `{"Version":"2026-01","Statement":[{"Effect":"Allow","Action":["domain:GetChallengeServer"],"Resource":"*"}]}`
	_, pol := e.do(t, "POST", "/api/v1/iam/policies", map[string]any{"name": "cs-reader", "document": json.RawMessage(doc)}, nil)
	_, app := e.do(t, "POST", "/api/v1/iam/users", map[string]string{"email": "cs@example.com", "name": "CS", "password": pw}, nil)
	e.do(t, "POST", "/api/v1/iam/attachments", map[string]string{"principalType": "user", "principalId": app["id"].(string), "policy": pol["id"].(string)}, nil)
	a := e.as(t)
	a.do(t, "POST", "/api/v1/auth/login", map[string]string{"email": "cs@example.com", "password": pw}, nil)
	if resp, _ := a.do(t, "GET", "/api/v1/domains/challenge-server", nil, nil); resp.StatusCode != 200 {
		t.Fatalf("reading: %d", resp.StatusCode)
	}
	for _, c := range []struct{ method, path string }{
		{"PUT", "/api/v1/domains/challenge-server"}, {"POST", "/api/v1/domains/challenge-server/check"},
	} {
		if resp, _ := a.do(t, c.method, c.path, map[string]any{"enabled": false}, nil); resp.StatusCode != 403 {
			t.Errorf("%s %s: %d", c.method, c.path, resp.StatusCode)
		}
	}

	// Switching it off.
	if resp, srv := e.do(t, "PUT", "/api/v1/domains/challenge-server", map[string]any{"enabled": false}, nil); resp.StatusCode != 200 || srv["enabled"] != false {
		t.Fatalf("%d %v", resp.StatusCode, srv)
	}
}
