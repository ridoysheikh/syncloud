package secrets

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadOrCreatePersistsKey(t *testing.T) {
	dir := t.TempDir()
	a, _, err := LoadOrCreate(dir)
	if err != nil {
		t.Fatal(err)
	}
	ct := a.Seal([]byte("hello"), []byte("row-1"))

	info, err := os.Stat(filepath.Join(dir, keyFile))
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("key file mode: %v %v", info.Mode(), err)
	}
	b, _, err := LoadOrCreate(dir) // reload: same key
	if err != nil {
		t.Fatal(err)
	}
	pt, err := b.Open(ct, []byte("row-1"))
	if err != nil || string(pt) != "hello" {
		t.Fatalf("open after reload: %q %v", pt, err)
	}
	if _, err := b.Open(ct, []byte("row-2")); err == nil {
		t.Fatal("ciphertext opened with the wrong associated data")
	}
}
