// Package sigv implements SynCloud access-key request signing (§7.1), shared
// by the controller (verify) and synctl/SDKs (sign). Modeled on AWS SigV4,
// simplified: the secret never travels on the wire, the body is covered by a
// hash, and requests older than MaxSkew are rejected.
//
// Signed requests carry:
//
//	X-Syn-Date:           20261006T101500Z
//	X-Syn-Content-Sha256: <hex sha256 of body>
//	Authorization:        SYN1-HMAC-SHA256 Credential=<key id>, Signature=<hex>
//
// Signature = hex(HMAC-SHA256(secret, StringToSign)), where StringToSign is
// the algorithm, date, method, escaped path, canonical query, host and body
// hash, each on its own line.
package sigv

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"
)

const (
	Algorithm         = "SYN1-HMAC-SHA256"
	HeaderDate        = "X-Syn-Date"
	HeaderContentHash = "X-Syn-Content-Sha256"
	DateFormat        = "20060102T150405Z"
	// MaxSkew is how far a request's date may be from the server clock.
	MaxSkew = 5 * time.Minute
)

func BodyHash(body []byte) string {
	sum := sha256.Sum256(body)
	return hex.EncodeToString(sum[:])
}

// CanonicalQuery sorts keys and values so client and server agree.
func CanonicalQuery(rawQuery string) string {
	vals, _ := url.ParseQuery(rawQuery)
	keys := make([]string, 0, len(vals))
	for k := range vals {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var b strings.Builder
	for _, k := range keys {
		vs := append([]string(nil), vals[k]...)
		sort.Strings(vs)
		for _, v := range vs {
			if b.Len() > 0 {
				b.WriteByte('&')
			}
			b.WriteString(url.QueryEscape(k))
			b.WriteByte('=')
			b.WriteString(url.QueryEscape(v))
		}
	}
	return b.String()
}

func StringToSign(method, escapedPath, rawQuery, host, date, bodyHash string) string {
	return strings.Join([]string{
		Algorithm, date, strings.ToUpper(method), escapedPath, CanonicalQuery(rawQuery), strings.ToLower(host), bodyHash,
	}, "\n")
}

func signature(secret, sts string) string {
	m := hmac.New(sha256.New, []byte(secret))
	m.Write([]byte(sts))
	return hex.EncodeToString(m.Sum(nil))
}

// Sign adds the signing headers to req. body must be exactly the request body.
func Sign(req *http.Request, keyID, secret string, body []byte, now time.Time) {
	date := now.UTC().Format(DateFormat)
	hash := BodyHash(body)
	req.Header.Set(HeaderDate, date)
	req.Header.Set(HeaderContentHash, hash)
	sts := StringToSign(req.Method, req.URL.EscapedPath(), req.URL.RawQuery, hostOf(req), date, hash)
	req.Header.Set("Authorization", fmt.Sprintf("%s Credential=%s, Signature=%s", Algorithm, keyID, signature(secret, sts)))
}

func hostOf(req *http.Request) string {
	if req.Host != "" {
		return req.Host
	}
	return req.URL.Host
}

// IsSigned reports whether the Authorization header uses this scheme.
func IsSigned(authz string) bool { return strings.HasPrefix(authz, Algorithm+" ") }

// ParseAuthorization extracts the key ID and signature.
func ParseAuthorization(authz string) (keyID, sig string, err error) {
	rest, ok := strings.CutPrefix(authz, Algorithm+" ")
	if !ok {
		return "", "", fmt.Errorf("not a %s authorization", Algorithm)
	}
	for _, part := range strings.Split(rest, ",") {
		k, v, _ := strings.Cut(strings.TrimSpace(part), "=")
		switch k {
		case "Credential":
			keyID = v
		case "Signature":
			sig = v
		}
	}
	if keyID == "" || sig == "" {
		return "", "", fmt.Errorf("authorization must include Credential and Signature")
	}
	return keyID, sig, nil
}

// Verify checks a request's signature. body must be the full request body as
// received. It returns a client-safe error message on failure.
func Verify(req *http.Request, secret, sig string, body []byte, now time.Time) error {
	date := req.Header.Get(HeaderDate)
	t, err := time.Parse(DateFormat, date)
	if err != nil {
		return fmt.Errorf("missing or malformed %s header", HeaderDate)
	}
	if d := now.Sub(t); d > MaxSkew || d < -MaxSkew {
		return fmt.Errorf("request date is more than %s from server time", MaxSkew)
	}
	hash := req.Header.Get(HeaderContentHash)
	if !hmac.Equal([]byte(hash), []byte(BodyHash(body))) {
		return fmt.Errorf("%s does not match the request body", HeaderContentHash)
	}
	sts := StringToSign(req.Method, req.URL.EscapedPath(), req.URL.RawQuery, hostOf(req), date, hash)
	if !hmac.Equal([]byte(sig), []byte(signature(secret, sts))) {
		return fmt.Errorf("signature does not match")
	}
	return nil
}
