package auth

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha1" //nolint:gosec // TOTP (RFC 6238) uses HMAC-SHA1, which authenticator apps expect
	"crypto/subtle"
	"encoding/base32"
	"encoding/binary"
	"fmt"
	"net/url"
	"strings"
	"time"
)

// TOTP (RFC 6238): 30-second steps, 6 digits, HMAC-SHA1, as every
// authenticator app supports.

const totpStep = 30

var totpEncoding = base32.StdEncoding.WithPadding(base32.NoPadding)

// NewTOTPSecret returns a random 160-bit secret, base32 encoded.
func NewTOTPSecret() string {
	b := make([]byte, 20)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return totpEncoding.EncodeToString(b)
}

// TOTPURI is the otpauth:// URI authenticator apps scan as a QR code.
func TOTPURI(secret, issuer, account string) string {
	label := url.PathEscape(issuer + ":" + account)
	q := url.Values{"secret": {secret}, "issuer": {issuer}, "algorithm": {"SHA1"}, "digits": {"6"}, "period": {"30"}}
	return "otpauth://totp/" + label + "?" + q.Encode()
}

func totpCode(key []byte, counter uint64) string {
	var msg [8]byte
	binary.BigEndian.PutUint64(msg[:], counter)
	m := hmac.New(sha1.New, key)
	m.Write(msg[:])
	sum := m.Sum(nil)
	off := sum[len(sum)-1] & 0x0f
	v := binary.BigEndian.Uint32(sum[off:off+4]) & 0x7fffffff
	return fmt.Sprintf("%06d", v%1000000)
}

// TOTPCode returns the code for t (for tests and enrollment checks).
func TOTPCode(secret string, t time.Time) (string, error) {
	key, err := totpEncoding.DecodeString(strings.ToUpper(strings.TrimSpace(secret)))
	if err != nil {
		return "", err
	}
	return totpCode(key, uint64(t.Unix()/totpStep)), nil
}

// VerifyTOTP accepts the code of the current step or one step either side
// (clock skew).
func VerifyTOTP(secret, code string, t time.Time) bool {
	code = strings.ReplaceAll(strings.TrimSpace(code), " ", "")
	if len(code) != 6 {
		return false
	}
	key, err := totpEncoding.DecodeString(strings.ToUpper(strings.TrimSpace(secret)))
	if err != nil {
		return false
	}
	now := t.Unix() / totpStep
	ok := 0
	for d := int64(-1); d <= 1; d++ {
		ok |= subtle.ConstantTimeCompare([]byte(totpCode(key, uint64(now+d))), []byte(code))
	}
	return ok == 1
}
