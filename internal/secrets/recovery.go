package secrets

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"crypto/hkdf"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base32"
	"errors"
	"strings"
)

// The master key is also kept wrapped (encrypted) by the recovery key
// (§5.0.1). Backups carry only the wrapped copy, so restoring one needs the
// recovery key, which is shown once at install and never stored.

const (
	wrappedFile    = "master.key.wrapped"
	wrapMagic      = "SRK1"
	recoveryPrefix = "SYNRK"
)

var b32 = base32.StdEncoding.WithPadding(base32.NoPadding)

// NewRecoveryKey returns a 200-bit key like SYNRK-ABCDE-FGHIJ-… (8 groups).
func NewRecoveryKey() string {
	raw := make([]byte, 25)
	if _, err := rand.Read(raw); err != nil {
		panic(err)
	}
	s := b32.EncodeToString(raw)
	groups := []string{recoveryPrefix}
	for i := 0; i < len(s); i += 5 {
		groups = append(groups, s[i:i+5])
	}
	return strings.Join(groups, "-")
}

// normalizeRecoveryKey accepts any case, spaces and missing dashes.
func normalizeRecoveryKey(k string) ([]byte, error) {
	k = strings.ToUpper(strings.NewReplacer("-", "", " ", "", "\n", "", "\t", "").Replace(k))
	k = strings.TrimPrefix(k, recoveryPrefix)
	raw, err := b32.DecodeString(k)
	if err != nil || len(raw) != 25 {
		return nil, errors.New("invalid recovery key format")
	}
	return raw, nil
}

// RecoveryKeySuffix is what the setup wizard asks the user to type back.
func RecoveryKeySuffix(k string) string {
	k = strings.ToUpper(strings.ReplaceAll(strings.TrimSpace(k), "-", ""))
	if len(k) < 6 {
		return k
	}
	return k[len(k)-6:]
}

func wrapKey(recovery []byte, salt []byte) ([]byte, error) {
	return hkdf.Key(sha256.New, recovery, salt, "syncloud master key wrap v1", 32)
}

// Wrap encrypts master with the recovery key.
func Wrap(master []byte, recoveryKey string) ([]byte, error) {
	raw, err := normalizeRecoveryKey(recoveryKey)
	if err != nil {
		return nil, err
	}
	salt := make([]byte, 16)
	if _, err := rand.Read(salt); err != nil {
		return nil, err
	}
	k, err := wrapKey(raw, salt)
	if err != nil {
		return nil, err
	}
	aead, err := gcm(k)
	if err != nil {
		return nil, err
	}
	nonce := make([]byte, aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return nil, err
	}
	out := append([]byte(wrapMagic), salt...)
	out = append(out, nonce...)
	return aead.Seal(out, nonce, master, []byte(wrapMagic)), nil
}

// ErrWrongRecoveryKey means the key does not unwrap this master key.
var ErrWrongRecoveryKey = errors.New("the recovery key does not match this backup")

// Unwrap recovers the master key.
func Unwrap(wrapped []byte, recoveryKey string) ([]byte, error) {
	raw, err := normalizeRecoveryKey(recoveryKey)
	if err != nil {
		return nil, err
	}
	if len(wrapped) < 4+16+12 || !bytes.Equal(wrapped[:4], []byte(wrapMagic)) {
		return nil, errors.New("not a wrapped SynCloud master key")
	}
	salt, rest := wrapped[4:20], wrapped[20:]
	k, err := wrapKey(raw, salt)
	if err != nil {
		return nil, err
	}
	aead, err := gcm(k)
	if err != nil {
		return nil, err
	}
	n := aead.NonceSize()
	master, err := aead.Open(nil, rest[:n], rest[n:], []byte(wrapMagic))
	if err != nil {
		return nil, ErrWrongRecoveryKey
	}
	return master, nil
}

func gcm(key []byte) (cipher.AEAD, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	return cipher.NewGCM(block)
}
