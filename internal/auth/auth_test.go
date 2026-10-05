package auth

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"syncloud/internal/store"
)

func TestPasswordRoundTrip(t *testing.T) {
	h, err := HashPassword("correct horse battery")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(h, "$argon2id$v=19$") {
		t.Fatalf("unexpected hash format: %s", h)
	}
	if ok, err := VerifyPassword("correct horse battery", h); err != nil || !ok {
		t.Fatalf("correct password rejected: ok=%v err=%v", ok, err)
	}
	if ok, _ := VerifyPassword("wrong horse battery", h); ok {
		t.Fatal("wrong password accepted")
	}
}

func TestHashPasswordRejectsShort(t *testing.T) {
	if _, err := HashPassword("short"); err != ErrWeakPassword {
		t.Fatalf("want ErrWeakPassword, got %v", err)
	}
}

func TestTokenHashing(t *testing.T) {
	tok := NewToken("syn_test_")
	if !strings.HasPrefix(tok, "syn_test_") || len(tok) != len("syn_test_")+32 {
		t.Fatalf("unexpected token %q", tok)
	}
	if !TokenMatches(tok, HashToken(tok)) {
		t.Fatal("token does not match its own hash")
	}
	if TokenMatches(tok+"x", HashToken(tok)) {
		t.Fatal("different token matched")
	}
}

func TestSetupTokenLifecycle(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(ctx, filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	now := time.Unix(1_800_000_000, 0)
	tok, err := EnsureSetupToken(ctx, st, now)
	if err != nil || tok == "" {
		t.Fatalf("expected a token, got %q err=%v", tok, err)
	}
	if ok, _ := CheckSetupToken(ctx, st, tok, now.Add(59*time.Minute)); !ok {
		t.Fatal("valid token rejected")
	}
	if ok, _ := CheckSetupToken(ctx, st, tok, now.Add(SetupTokenTTL)); ok {
		t.Fatal("expired token accepted")
	}
	if ok, _ := CheckSetupToken(ctx, st, "syn_setup_wrong", now); ok {
		t.Fatal("wrong token accepted")
	}

	// Once an account exists, no token is issued and the old one is gone.
	if err := st.CreateRootUser(ctx, store.User{ID: "usr_1", Email: "a@b.co", Name: "A", PasswordHash: "x", CreatedAt: now}); err != nil {
		t.Fatal(err)
	}
	if tok2, err := EnsureSetupToken(ctx, st, now); err != nil || tok2 != "" {
		t.Fatalf("expected no token after setup, got %q err=%v", tok2, err)
	}
	if ok, _ := CheckSetupToken(ctx, st, tok, now); ok {
		t.Fatal("setup token still valid after setup completed")
	}
}
