package api

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/cookiejar"
	"strings"
	"testing"
	"time"

	"syncloud/internal/auth"
	"syncloud/internal/iam"
	"syncloud/internal/sigv"
	"syncloud/internal/store"
)

// as returns a second client (its own cookies) for another user.
func (e *testEnv) as(t *testing.T) *testEnv {
	jar, _ := cookiejar.New(nil)
	c := *e
	c.c = &http.Client{Jar: jar}
	return &c
}

func (e *testEnv) setupRoot(t *testing.T) {
	t.Helper()
	if resp, body := e.do(t, "POST", "/api/v1/setup", map[string]string{"setupToken": e.token, "email": "root@example.com", "name": "Root", "password": pw}, nil); resp.StatusCode != 201 {
		t.Fatalf("setup: %d %v", resp.StatusCode, body)
	}
}

// signed sends a request with temporary credentials.
func (e *testEnv) signed(t *testing.T, method, path string, body any, creds map[string]any) *http.Response {
	t.Helper()
	var b []byte
	if body != nil {
		b, _ = json.Marshal(body)
	}
	req, _ := http.NewRequest(method, e.srv.URL+path, bytes.NewReader(b))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set(HeaderSessionToken, creds["sessionToken"].(string))
	sigv.Sign(req, creds["accessKeyId"].(string), creds["secretAccessKey"].(string), b, time.Now())
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	io.Copy(io.Discard, resp.Body)
	resp.Body.Close()
	return resp
}

func TestIAM(t *testing.T) {
	e := newEnv(t)
	e.setupRoot(t)
	ctx := context.Background()
	now := time.Now()
	if err := e.st.CreateProject(ctx, store.Project{ID: "prj_shop", Name: "shop", CreatedAt: now}, store.Environment{ID: "env_shop", Name: "production", CreatedAt: now}); err != nil {
		t.Fatal(err)
	}

	resp, ann := e.do(t, "POST", "/api/v1/iam/users", map[string]string{"email": "ann@example.com", "name": "Ann", "password": pw}, nil)
	if resp.StatusCode != 201 {
		t.Fatalf("create user: %d %v", resp.StatusCode, ann)
	}
	annID := ann["id"].(string)
	resp, grp := e.do(t, "POST", "/api/v1/iam/groups", map[string]any{"name": "devs", "members": []string{annID}}, nil)
	if resp.StatusCode != 201 {
		t.Fatalf("create group: %d %v", resp.StatusCode, grp)
	}
	if resp, body := e.do(t, "POST", "/api/v1/iam/attachments", map[string]string{"principalType": "group", "principalId": grp["id"].(string), "policy": "Developer:shop"}, nil); resp.StatusCode != 200 {
		t.Fatalf("attach: %d %v", resp.StatusCode, body)
	}
	if resp, _ := e.do(t, "POST", "/api/v1/iam/attachments", map[string]string{"principalType": "group", "principalId": grp["id"].(string), "policy": "Developer:nope"}, nil); resp.StatusCode != 400 {
		t.Fatalf("template for a missing project: %d", resp.StatusCode)
	}

	a := e.as(t)
	if resp, _ := a.do(t, "POST", "/api/v1/auth/login", map[string]string{"email": "ann@example.com", "password": pw}, nil); resp.StatusCode != 200 {
		t.Fatalf("ann login: %d", resp.StatusCode)
	}
	for _, c := range []struct {
		method, path string
		want         int
	}{
		{"GET", "/api/v1/settings/domain", 403},
		{"GET", "/api/v1/iam/users", 403},
		{"POST", "/api/v1/nodes/join-tokens", 403},
		{"GET", "/api/v1/iam/me/permissions", 200},
		{"GET", "/api/v1/iam/access-keys", 200},
	} {
		if resp, body := a.do(t, c.method, c.path, map[string]any{}, nil); resp.StatusCode != c.want {
			t.Errorf("ann %s %s: %d (want %d) %v", c.method, c.path, resp.StatusCode, c.want, body)
		}
	}
	if resp, body := a.do(t, "GET", "/api/v1/iam/access-keys?userId=usr_root", nil, nil); resp.StatusCode != 403 && resp.StatusCode != 404 {
		t.Errorf("ann listing another user's keys: %d %v", resp.StatusCode, body)
	}

	sim := func(principal, action, resource string, mfa bool) bool {
		t.Helper()
		resp, body := e.do(t, "POST", "/api/v1/iam/simulate", map[string]any{"principal": principal, "action": action, "resource": resource, "mfa": mfa}, nil)
		if resp.StatusCode != 200 {
			t.Fatalf("simulate: %d %v", resp.StatusCode, body)
		}
		return body["allowed"] == true
	}
	if !sim("ann@example.com", "service:ScaleService", serviceSRN("shop", "production", "web"), false) {
		t.Error("developer cannot scale in their project")
	}
	if sim("ann@example.com", "service:ScaleService", serviceSRN("billing", "production", "web"), false) {
		t.Error("developer can scale in another project")
	}
	if sim("ann@example.com", "project:DeleteProject", projectSRN("shop"), false) {
		t.Error("developer can delete the project")
	}

	// A customer policy with an explicit Deny unless MFA.
	doc := `{"Version":"2026-01","Statement":[{"Effect":"Deny","Action":"service:DeleteService","Resource":"*","Condition":{"Bool":{"syn:MFAPresent":"false"}}}]}`
	resp, pol := e.do(t, "POST", "/api/v1/iam/policies", map[string]any{"name": "no-delete-without-mfa", "document": json.RawMessage(doc)}, nil)
	if resp.StatusCode != 201 {
		t.Fatalf("create policy: %d %v", resp.StatusCode, pol)
	}
	e.do(t, "POST", "/api/v1/iam/attachments", map[string]string{"principalType": "user", "principalId": annID, "policy": pol["id"].(string)}, nil)
	if sim("ann@example.com", "service:DeleteService", serviceSRN("shop", "production", "web"), false) {
		t.Error("explicit deny ignored")
	}
	if !sim("ann@example.com", "service:DeleteService", serviceSRN("shop", "production", "web"), true) {
		t.Error("deny applied despite MFA")
	}
	if resp, _ := e.do(t, "POST", "/api/v1/iam/policies", map[string]any{"name": "bad", "document": json.RawMessage(`{"Version":"2026-01","Statement":[{"Effect":"Maybe","Action":"*","Resource":"*"}]}`)}, nil); resp.StatusCode != 400 {
		t.Errorf("invalid policy accepted: %d", resp.StatusCode)
	}

	// MFA enrollment, then login needs a code.
	resp, mfa := a.do(t, "POST", "/api/v1/iam/mfa", nil, nil)
	if resp.StatusCode != 200 {
		t.Fatalf("begin MFA: %d %v", resp.StatusCode, mfa)
	}
	code, _ := auth.TOTPCode(mfa["secret"].(string), time.Now())
	if resp, body := a.do(t, "POST", "/api/v1/iam/mfa/enable", map[string]string{"code": code}, nil); resp.StatusCode != 200 {
		t.Fatalf("enable MFA: %d %v", resp.StatusCode, body)
	}
	a = e.as(t)
	if resp, body := a.do(t, "POST", "/api/v1/auth/login", map[string]string{"email": "ann@example.com", "password": pw}, nil); resp.StatusCode != 401 || errCode(body) != CodeMFARequired {
		t.Fatalf("login without code: %d %v", resp.StatusCode, body)
	}
	code, _ = auth.TOTPCode(mfa["secret"].(string), time.Now())
	if resp, _ := a.do(t, "POST", "/api/v1/auth/login", map[string]string{"email": "ann@example.com", "password": pw, "otp": code}, nil); resp.StatusCode != 200 {
		t.Fatalf("login with code: %d", resp.StatusCode)
	}

	// A role that trusts Ann with MFA, carrying ReadOnly.
	resp, role := e.do(t, "POST", "/api/v1/iam/roles", map[string]any{"name": "auditor", "trust": map[string]any{"users": []string{annID}, "requireMfa": true}}, nil)
	if resp.StatusCode != 201 {
		t.Fatalf("create role: %d %v", resp.StatusCode, role)
	}
	e.do(t, "POST", "/api/v1/iam/attachments", map[string]string{"principalType": "role", "principalId": role["id"].(string), "policy": "ReadOnly"}, nil)
	resp, creds := a.do(t, "POST", "/api/v1/sts/assume-role", map[string]any{"role": "auditor", "durationSeconds": 900}, nil)
	if resp.StatusCode != 200 || !strings.HasPrefix(creds["accessKeyId"].(string), "SYNAS") {
		t.Fatalf("assume role: %d %v", resp.StatusCode, creds)
	}
	if r := e.signed(t, "GET", "/api/v1/iam/users", nil, creds); r.StatusCode != 200 {
		t.Errorf("role session listing users: %d", r.StatusCode)
	}
	if r := e.signed(t, "POST", "/api/v1/iam/users", map[string]string{"email": "x@example.com", "name": "X", "password": pw}, creds); r.StatusCode != 403 {
		t.Errorf("read-only role created a user: %d", r.StatusCode)
	}
	bad := map[string]any{"accessKeyId": creds["accessKeyId"], "secretAccessKey": creds["secretAccessKey"], "sessionToken": "wrong"}
	if r := e.signed(t, "GET", "/api/v1/iam/users", nil, bad); r.StatusCode != 401 {
		t.Errorf("wrong session token: %d", r.StatusCode)
	}

	// Device login: synctl starts, the person approves, synctl collects once.
	resp, dev := e.as(t).do(t, "POST", "/api/v1/auth/device", nil, nil)
	if resp.StatusCode != 200 {
		t.Fatalf("start device: %d %v", resp.StatusCode, dev)
	}
	if resp, _ := e.as(t).do(t, "POST", "/api/v1/auth/device/token", map[string]any{"deviceCode": dev["deviceCode"]}, nil); resp.StatusCode != 202 {
		t.Fatalf("pending poll: %d", resp.StatusCode)
	}
	if resp, body := a.do(t, "POST", "/api/v1/auth/device/approve", map[string]any{"userCode": dev["userCode"]}, nil); resp.StatusCode != 204 {
		t.Fatalf("approve: %d %v", resp.StatusCode, body)
	}
	resp, dc := e.as(t).do(t, "POST", "/api/v1/auth/device/token", map[string]any{"deviceCode": dev["deviceCode"]}, nil)
	if resp.StatusCode != 200 || dc["sessionToken"] == nil {
		t.Fatalf("collect: %d %v", resp.StatusCode, dc)
	}
	if r := e.signed(t, "GET", "/api/v1/iam/me/permissions", nil, dc); r.StatusCode != 200 {
		t.Errorf("device credentials: %d", r.StatusCode)
	}
	if resp, _ := e.as(t).do(t, "POST", "/api/v1/auth/device/token", map[string]any{"deviceCode": dev["deviceCode"]}, nil); resp.StatusCode != 404 {
		t.Errorf("device credentials collected twice: %d", resp.StatusCode)
	}

	// Disabled users are out at once.
	e.do(t, "PUT", "/api/v1/iam/users/"+annID, map[string]any{"disabled": true}, nil)
	if resp, _ := a.do(t, "GET", "/api/v1/auth/me", nil, nil); resp.StatusCode != 401 {
		t.Errorf("disabled user still signed in: %d", resp.StatusCode)
	}

	// The audit log records denials.
	resp, au := e.do(t, "GET", "/api/v1/audit?action=settings:*", nil, nil)
	if resp.StatusCode != 200 {
		t.Fatalf("audit: %d", resp.StatusCode)
	}
	found := false
	for _, it := range au["items"].([]any) {
		if d, _ := it.(map[string]any)["detail"].(map[string]any); d["denied"] == true {
			found = true
		}
	}
	if !found {
		t.Errorf("no denied event in %v", au)
	}
}

// Every action a managed policy names must exist.
func TestManagedPolicyActionsExist(t *testing.T) {
	actions := map[string]bool{"registry:Pull": true, "registry:Push": true, "registry:Delete": true}
	for _, rt := range (&Server{}).Routes() {
		if a := ActionFor(rt.Method, rt.Path); a != "" {
			actions[a] = true
		}
	}
	for _, m := range iam.ManagedPolicies() {
		name := m.Name
		if m.PerProject {
			name += ":x"
		}
		d, _ := iam.ManagedDocument(name)
		for _, st := range d.Statement {
			for _, a := range st.Action {
				matched := false
				for have := range actions {
					if iam.Match(a, have) {
						matched = true
					}
				}
				if !matched {
					t.Errorf("%s: %q matches no action", m.Name, a)
				}
			}
		}
	}
	for a := range selfService {
		if !actions[a] {
			t.Errorf("self-service action %q does not exist", a)
		}
	}
	for a := range listOps {
		if !actions[a] && !strings.HasPrefix(a, "quota:") && !strings.HasPrefix(a, "usage:") {
			t.Errorf("list action %q does not exist", a)
		}
	}
}
