package api

import (
	"strings"
	"testing"
)

// The recovery key is managed only on the controller host
// (syncloud-controller recovery-key): no API can show or change it, not even
// for the root user.
func TestRecoveryKeyNotReachableOverAPI(t *testing.T) {
	e := newEnv(t)
	e.setupRoot(t)
	for _, m := range []string{"GET", "POST"} {
		if resp, _ := e.do(t, m, "/api/v1/backups/recovery-key", nil, nil); resp.StatusCode < 400 {
			t.Fatalf("%s recovery-key: %d", m, resp.StatusCode)
		}
	}
	for _, r := range (&Server{}).Routes() {
		if strings.Contains(r.Path, "recovery") {
			t.Errorf("route %s %s exposes the recovery key", r.Method, r.Path)
		}
	}
}
