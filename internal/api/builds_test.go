package api

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"testing"
)

func TestValidHookSignature(t *testing.T) {
	body, secret := []byte(`{"ref":"refs/heads/main"}`), "s3cret"
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(body)
	sig := hex.EncodeToString(mac.Sum(nil))

	cases := []struct {
		name string
		h    http.Header
		want bool
	}{
		{"github", http.Header{"X-Hub-Signature-256": {"sha256=" + sig}}, true},
		{"gitea", http.Header{"X-Gitea-Signature": {sig}}, true},
		{"gitlab", http.Header{"X-Gitlab-Token": {secret}}, true},
		{"github wrong", http.Header{"X-Hub-Signature-256": {"sha256=" + sig[:62] + "00"}}, false},
		{"gitlab wrong", http.Header{"X-Gitlab-Token": {"nope"}}, false},
		{"github without prefix", http.Header{"X-Hub-Signature-256": {sig}}, false},
		{"unsigned", http.Header{}, false},
	}
	for _, c := range cases {
		if got := validHookSignature(c.h, body, secret); got != c.want {
			t.Errorf("%s: got %v, want %v", c.name, got, c.want)
		}
	}
	if validHookSignature(http.Header{"X-Hub-Signature-256": {"sha256=" + sig}}, []byte("tampered"), secret) {
		t.Error("tampered body accepted")
	}
}
