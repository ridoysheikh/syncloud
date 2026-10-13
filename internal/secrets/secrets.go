// Package secrets encrypts values at rest with the controller's master key
// (AES-256-GCM, §14). The key file is protected by permissions; a copy wrapped
// by the recovery key goes into backups (§5.0.1).
package secrets

import (
	"crypto/cipher"
	"crypto/rand"
	"crypto/subtle"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
)

const keyFile = "master.key"

type Box struct {
	aead    cipher.AEAD
	key     []byte
	dataDir string // where the wrapped copy lives ("" = not loaded from disk)

	mu      sync.RWMutex
	wrapped []byte
}

// Wrapped returns the master key wrapped by the recovery key (safe to back up).
func (b *Box) Wrapped() []byte {
	b.mu.RLock()
	defer b.mu.RUnlock()
	return b.wrapped
}

// Verify reports whether recoveryKey unwraps this master key.
func (b *Box) Verify(recoveryKey string) error {
	master, err := Unwrap(b.Wrapped(), recoveryKey)
	if err != nil {
		return err
	}
	if subtle.ConstantTimeCompare(master, b.key) != 1 {
		return ErrWrongRecoveryKey
	}
	return nil
}

// Rewrap wraps the master key with recoveryKey and makes it the copy that
// is stored and put into backups (recovery key rotation, Phase 19b). The
// master key, and so everything sealed with it, is unchanged.
func (b *Box) Rewrap(recoveryKey string) error {
	if b.dataDir == "" {
		return errors.New("the master key was not loaded from a data directory")
	}
	wrapped, err := Wrap(b.key, recoveryKey)
	if err != nil {
		return err
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if err := writeAtomic(filepath.Join(b.dataDir, wrappedFile), wrapped); err != nil {
		return err
	}
	b.wrapped = wrapped
	return nil
}

// writeAtomic replaces path (0600) so a crash leaves the old or new file, never half of one.
func writeAtomic(path string, data []byte) error {
	tmp := path + ".tmp"
	f, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	if _, err := f.Write(data); err != nil {
		f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// LoadOrCreate reads <dataDir>/master.key, creating it (0600) on first start,
// and makes sure a wrapped copy exists. When it creates a new recovery key
// (first start, or an install from before recovery keys), it returns it so the
// caller can show it once; otherwise recoveryKey is "".
func LoadOrCreate(dataDir string) (box *Box, recoveryKey string, err error) {
	box, err = loadOrCreateKey(dataDir)
	if err != nil {
		return nil, "", err
	}
	box.dataDir = dataDir
	wpath := filepath.Join(dataDir, wrappedFile)
	box.wrapped, err = os.ReadFile(wpath)
	if errors.Is(err, os.ErrNotExist) {
		recoveryKey = NewRecoveryKey()
		if box.wrapped, err = Wrap(box.key, recoveryKey); err != nil {
			return nil, "", err
		}
		if err := os.WriteFile(wpath, box.wrapped, 0o600); err != nil {
			return nil, "", err
		}
		return box, recoveryKey, nil
	}
	return box, "", err
}

func loadOrCreateKey(dataDir string) (*Box, error) {
	path := filepath.Join(dataDir, keyFile)
	key, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		key = make([]byte, 32)
		if _, err := rand.Read(key); err != nil {
			return nil, err
		}
		if err := os.MkdirAll(dataDir, 0o700); err != nil {
			return nil, err
		}
		// O_EXCL: never overwrite a key that appeared concurrently.
		f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
		if err != nil {
			return nil, fmt.Errorf("create master key: %w", err)
		}
		if _, err := f.Write(key); err != nil {
			f.Close()
			return nil, err
		}
		if err := f.Close(); err != nil {
			return nil, err
		}
	} else if err != nil {
		return nil, fmt.Errorf("read master key: %w", err)
	}
	return New(key)
}

func New(key []byte) (*Box, error) {
	if len(key) != 32 {
		return nil, fmt.Errorf("master key must be 32 bytes, got %d", len(key))
	}
	aead, err := gcm(key)
	if err != nil {
		return nil, err
	}
	return &Box{aead: aead, key: key}, nil
}

// Seal encrypts plaintext. aad binds the ciphertext to its context (e.g. the
// row ID) so it can't be copied to another row.
func (b *Box) Seal(plaintext, aad []byte) []byte {
	nonce := make([]byte, b.aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		panic(err)
	}
	return b.aead.Seal(nonce, nonce, plaintext, aad)
}

func (b *Box) Open(ciphertext, aad []byte) ([]byte, error) {
	n := b.aead.NonceSize()
	if len(ciphertext) < n {
		return nil, errors.New("ciphertext too short")
	}
	return b.aead.Open(nil, ciphertext[:n], ciphertext[n:], aad)
}

// Restore writes a master key (unwrapped from a backup) and its wrapped copy
// into dataDir.
func Restore(dataDir string, master, wrapped []byte) error {
	if _, err := New(master); err != nil {
		return err
	}
	if err := os.MkdirAll(dataDir, 0o700); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(dataDir, keyFile), master, 0o600); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dataDir, wrappedFile), wrapped, 0o600)
}
