package backup

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/ridoysheikh/syncloud/internal/secrets"
	"github.com/ridoysheikh/syncloud/internal/store"
)

func TestBundleRoundTrip(t *testing.T) {
	ctx := context.Background()
	src := t.TempDir()
	box, rk, err := secrets.LoadOrCreate(src)
	if err != nil {
		t.Fatal(err)
	}
	st, err := store.Open(ctx, filepath.Join(src, "syncloud.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if err := st.SetSetting(ctx, "test.key", "hello"); err != nil {
		t.Fatal(err)
	}
	os.MkdirAll(filepath.Join(src, "registry"), 0o700)
	os.WriteFile(filepath.Join(src, "ca.key"), []byte("CA KEY"), 0o600)
	os.WriteFile(filepath.Join(src, "registry", "registry-token.crt"), []byte("CERT"), 0o644)
	os.WriteFile(filepath.Join(src, "setup-token"), []byte("secret"), 0o600)

	var buf bytes.Buffer
	if err := Create(ctx, st, box, src, &buf); err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(buf.Bytes(), []byte("CA KEY")) {
		t.Fatal("bundle is not encrypted")
	}

	if _, err := Restore(buf.Bytes(), secrets.NewRecoveryKey(), t.TempDir(), false); !errors.Is(err, secrets.ErrWrongRecoveryKey) {
		t.Fatalf("wrong recovery key: %v", err)
	}
	dst := t.TempDir()
	names, err := Restore(buf.Bytes(), rk, dst, false)
	if err != nil {
		t.Fatal(err)
	}
	slices.Sort(names)
	if !slices.Equal(names, []string{"ca.key", "registry/registry-token.crt", "syncloud.db"}) {
		t.Fatalf("files: %v", names)
	}
	if fi, _ := os.Stat(filepath.Join(dst, "registry", "registry-token.crt")); fi.Mode().Perm() != 0o644 {
		t.Fatalf("cert mode %v", fi.Mode())
	}
	// The restored controller opens the same secrets and database.
	box2, rk2, err := secrets.LoadOrCreate(dst)
	if err != nil || rk2 != "" {
		t.Fatalf("restored key: %q %v", rk2, err)
	}
	if got, err := box2.Open(box.Seal([]byte("x"), nil), nil); err != nil || string(got) != "x" {
		t.Fatal("restored master key differs")
	}
	st2, err := store.Open(ctx, filepath.Join(dst, "syncloud.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st2.Close()
	if v, _, _ := st2.GetSetting(ctx, "test.key"); v != "hello" {
		t.Fatalf("restored setting: %q", v)
	}
	if _, err := Restore(buf.Bytes(), rk, dst, false); err == nil {
		t.Fatal("overwrote an existing database without force")
	}
}
