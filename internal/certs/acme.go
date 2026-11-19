package certs

import (
	"context"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"fmt"
	"time"

	"golang.org/x/crypto/acme"
)

const settingAccountKey = "acme.account_key"

// issued is what an order yields.
type issued struct {
	chainPEM []byte
	keyPEM   []byte
	leaf     *x509.Certificate
}

// finalize waits for an order to be ready, sends the CSR and downloads the chain.
func finalize(ctx context.Context, c *acme.Client, orderURI string, names []string, keyType string) (*issued, error) {
	order, err := c.WaitOrder(ctx, orderURI)
	if err != nil {
		return nil, fmt.Errorf("wait order: %w", err)
	}
	key, keyPEM, err := newKey(keyType)
	if err != nil {
		return nil, err
	}
	csr, _, err := csrFor(names, key)
	if err != nil {
		return nil, err
	}
	return finalizeWith(ctx, c, order, orderURI, csr, keyPEM)
}

func finalizeWith(ctx context.Context, c *acme.Client, order *acme.Order, orderURI string, csr []byte, keyPEM []byte) (*issued, error) {
	ders, _, err := c.CreateOrderCert(ctx, order.FinalizeURL, csr, true)
	if err != nil {
		// CreateOrderCert follows the Location header of the finalize response,
		// which some servers omit; poll the order by its own URL instead.
		o, werr := c.WaitOrder(ctx, orderURI)
		if werr != nil || o.Status != acme.StatusValid || o.CertURL == "" {
			return nil, fmt.Errorf("finalize: %w", err)
		}
		if ders, err = c.FetchCert(ctx, o.CertURL, true); err != nil {
			return nil, fmt.Errorf("download certificate: %w", err)
		}
	}
	leaf, err := x509.ParseCertificate(ders[0])
	if err != nil {
		return nil, err
	}
	var chain []byte
	for _, d := range ders {
		chain = append(chain, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: d})...)
	}
	return &issued{chainPEM: chain, keyPEM: keyPEM, leaf: leaf}, nil
}

// issueHTTP orders a certificate for names over HTTP-01 (no wildcards).
func (a *acmeClient) issueHTTP(ctx context.Context, au Authority, names []string, keyType string) (*issued, error) {
	c, err := a.get(ctx, au)
	if err != nil {
		return nil, err
	}
	order, err := c.AuthorizeOrder(ctx, acme.DomainIDs(names...))
	if err != nil {
		return nil, fmt.Errorf("new order: %w", err)
	}
	for _, u := range order.AuthzURLs {
		z, err := c.GetAuthorization(ctx, u)
		if err != nil {
			return nil, err
		}
		if z.Status == acme.StatusValid {
			continue
		}
		var chal *acme.Challenge
		for _, ch := range z.Challenges {
			if ch.Type == "http-01" {
				chal = ch
			}
		}
		if chal == nil {
			return nil, errors.New("the CA offered no http-01 challenge")
		}
		keyAuth, err := c.HTTP01ChallengeResponse(chal.Token)
		if err != nil {
			return nil, err
		}
		a.m.challenges.Store(chal.Token, keyAuth)
		defer a.m.challenges.Delete(chal.Token)
		if _, err := c.Accept(ctx, chal); err != nil {
			return nil, fmt.Errorf("accept challenge: %w", err)
		}
		if _, err := c.WaitAuthorization(ctx, z.URI); err != nil {
			return nil, fmt.Errorf("http-01 validation of %s: %w", z.Identifier.Value, err)
		}
	}
	return finalize(ctx, c, order.URI, names, keyType)
}

// issue is the automatic per-host certificate: HTTP-01 at the default
// authority, then at the fallback one when the default is rate limited or
// has failed repeatedly.
func (a *acmeClient) issue(ctx context.Context, host string) ([]byte, []byte, time.Time, error) {
	au, err := a.m.Authority(ctx, "")
	if err != nil {
		return nil, nil, time.Time{}, err
	}
	res, err := a.issueHTTP(ctx, au, []string{host}, au.KeyType)
	if err != nil {
		if fb := a.m.fallbackFor(ctx, host, "", err); fb != nil {
			a.m.event(ctx, host, "fallback", "the first authority failed ("+err.Error()+"): trying "+fb.Name, "")
			if res2, err2 := a.issueHTTP(ctx, *fb, []string{host}, fb.KeyType); err2 == nil {
				a.m.noteAuthority(host, fb.ID)
				return res2.chainPEM, res2.keyPEM, res2.leaf.NotAfter.UTC(), nil
			}
		}
		return nil, nil, time.Time{}, err
	}
	a.m.noteAuthority(host, au.ID)
	return res.chainPEM, res.keyPEM, res.leaf.NotAfter.UTC(), nil
}

// rateLimited says whether the CA refused for its rate limit, and until when.
// The error may be wrapped ("new order: …").
func rateLimited(err error) (time.Duration, bool) {
	var e *acme.Error
	if errors.As(err, &e) {
		return acme.RateLimit(e)
	}
	return 0, false
}
