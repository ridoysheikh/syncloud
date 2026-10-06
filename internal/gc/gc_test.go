package gc

import (
	"os"
	"path/filepath"
	"testing"
)

func TestPruneUpgrades(t *testing.T) {
	dir := t.TempDir()
	for _, id := range []string{"20260101-000000", "20260201-000000", "20260301-000000", "20260401-000000", "20260501-000000"} {
		if err := os.MkdirAll(filepath.Join(dir, "upgrade", id), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	if n := pruneUpgrades(dir); n != 2 {
		t.Fatalf("removed %d, want 2", n)
	}
	for _, id := range []string{"20260101-000000", "20260201-000000"} {
		if _, err := os.Stat(filepath.Join(dir, "upgrade", id)); err == nil {
			t.Fatalf("%s kept", id)
		}
	}
	if _, err := os.Stat(filepath.Join(dir, "upgrade", "20260501-000000")); err != nil {
		t.Fatal("newest removed")
	}
}
