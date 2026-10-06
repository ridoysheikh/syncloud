package auth

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base32"
	"encoding/hex"
	"strings"
)

var b32 = base32.StdEncoding.WithPadding(base32.NoPadding)

// NewToken returns a random token with the given prefix, e.g. "syn_sess_…".
// 20 random bytes = 160 bits of entropy.
func NewToken(prefix string) string {
	b := make([]byte, 20)
	if _, err := rand.Read(b); err != nil {
		panic(err) // crypto/rand failing is unrecoverable
	}
	return prefix + strings.ToLower(b32.EncodeToString(b))
}

// NewID returns a short random identifier for database rows.
func NewID(prefix string) string {
	b := make([]byte, 10)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return prefix + strings.ToLower(b32.EncodeToString(b))
}

// HashToken is how tokens are stored: never in plain text.
func HashToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

// TokenMatches compares a presented token with a stored hash in constant time.
func TokenMatches(token, storedHash string) bool {
	return subtle.ConstantTimeCompare([]byte(HashToken(token)), []byte(storedHash)) == 1
}

// NewAccessKey returns a new access key ID ("SYNAK" + 16 chars) and secret (40 chars).
func NewAccessKey() (id, secret string) {
	idb := make([]byte, 10)
	sb := make([]byte, 25)
	if _, err := rand.Read(idb); err != nil {
		panic(err)
	}
	if _, err := rand.Read(sb); err != nil {
		panic(err)
	}
	return "SYNAK" + b32.EncodeToString(idb), b32.EncodeToString(sb)
}

// TempKeyPrefix starts the key IDs of temporary credentials (STS).
const TempKeyPrefix = "SYNAS"

// NewUserCode is a short code a person types to approve a device login:
// two groups of four letters without ambiguous ones (XXXX-XXXX).
func NewUserCode() string {
	const alphabet = "BCDFGHJKLMNPQRSTVWXZ"
	b := make([]byte, 8)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	out := make([]byte, 0, 9)
	for i, x := range b {
		if i == 4 {
			out = append(out, '-')
		}
		out = append(out, alphabet[int(x)%len(alphabet)])
	}
	return string(out)
}
