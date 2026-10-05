package secrets

import (
	"bytes"
	"errors"
	"strings"
	"testing"
)

func TestRecoveryKeyWrapUnwrap(t *testing.T) {
	dir := t.TempDir()
	box, rk, err := LoadOrCreate(dir)
	if err != nil || rk == "" || !strings.HasPrefix(rk, "SYNRK-") || len(strings.Split(rk, "-")) != 9 {
		t.Fatalf("first start: %q %v", rk, err)
	}
	// Second start: same key, no new recovery key.
	again, rk2, err := LoadOrCreate(dir)
	if err != nil || rk2 != "" || !bytes.Equal(again.key, box.key) {
		t.Fatalf("second start: %q %v", rk2, err)
	}
	// Tolerant input: lowercase, spaces instead of dashes.
	sloppy := strings.ToLower(strings.ReplaceAll(rk, "-", " "))
	master, err := Unwrap(box.Wrapped(), sloppy)
	if err != nil || !bytes.Equal(master, box.key) {
		t.Fatalf("unwrap: %v", err)
	}
	if _, err := Unwrap(box.Wrapped(), NewRecoveryKey()); !errors.Is(err, ErrWrongRecoveryKey) {
		t.Fatalf("wrong key: %v", err)
	}
	if _, err := Unwrap(box.Wrapped(), "SYNRK-short"); err == nil {
		t.Fatal("malformed key accepted")
	}
	if RecoveryKeySuffix(rk) != strings.ReplaceAll(rk, "-", "")[len(strings.ReplaceAll(rk, "-", ""))-6:] {
		t.Fatal("suffix")
	}
}
