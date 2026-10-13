package api

import (
	"context"
	"testing"

	"github.com/ridoysheikh/syncloud/internal/store"
)

func TestRecoveryKey(t *testing.T) {
	e := newEnv(t)
	e.setupRoot(t)
	// The test install's key was never kept, like an install from before.
	resp, body := e.do(t, "GET", "/api/v1/backups/recovery-key", nil, nil)
	if resp.StatusCode != 200 || body["stored"] != false {
		t.Fatalf("get before create: %d %v", resp.StatusCode, body)
	}
	resp, body = e.do(t, "POST", "/api/v1/backups/recovery-key", nil, nil)
	key, _ := body["recoveryKey"].(string)
	if resp.StatusCode != 201 || key == "" || key == e.recoveryKey || body["stored"] != true {
		t.Fatalf("create: %d %v", resp.StatusCode, body)
	}
	resp, body = e.do(t, "GET", "/api/v1/backups/recovery-key", nil, nil)
	if resp.StatusCode != 200 || body["stored"] != true || body["recoveryKey"] != key {
		t.Fatalf("get after create: %d %v", resp.StatusCode, body)
	}
	if resp, _ := e.do(t, "POST", "/api/v1/backups/recovery-key", nil, nil); resp.StatusCode != 409 {
		t.Fatalf("second create: %d", resp.StatusCode)
	}
	// The kept key is sealed, not stored in clear.
	if v, _, _ := e.st.GetSetting(context.Background(), store.SettingRecoveryKey); v == "" || v == key {
		t.Fatalf("stored setting %q", v)
	}

	// An administrator who is not the root user can't see it.
	resp, ann := e.do(t, "POST", "/api/v1/iam/users", map[string]string{"email": "ann@example.com", "name": "Ann", "password": pw}, nil)
	if resp.StatusCode != 201 {
		t.Fatalf("create user: %d %v", resp.StatusCode, ann)
	}
	if resp, body := e.do(t, "POST", "/api/v1/iam/attachments", map[string]string{"principalType": "user", "principalId": ann["id"].(string), "policy": "AdministratorAccess"}, nil); resp.StatusCode != 200 {
		t.Fatalf("attach: %d %v", resp.StatusCode, body)
	}
	a := e.as(t)
	if resp, _ := a.do(t, "POST", "/api/v1/auth/login", map[string]string{"email": "ann@example.com", "password": pw}, nil); resp.StatusCode != 200 {
		t.Fatalf("login: %d", resp.StatusCode)
	}
	if resp, _ := a.do(t, "GET", "/api/v1/backups/recovery-key", nil, nil); resp.StatusCode != 403 {
		t.Fatalf("administrator get: %d", resp.StatusCode)
	}
}
