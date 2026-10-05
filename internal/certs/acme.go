package certs

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"encoding/base64"
	"encoding/pem"
	"errors"
	"fmt"
	"sync"
	"time"

	"golang.org/x/crypto/acme"
)

const settingAccountKey = "acme.account_key"

// acmeClient issues certificates over ACME with the HTTP-01 challenge.
type acmeClient struct {
	m *Manager

	mu         sync.Mutex
	client     *acme.Client
	registered bool
}

func (a *acmeClient) get(ctx context.Context) (*acme.Client, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.client == nil {
		key, err := a.accountKey(ctx)
		if err != nil {
			return nil, err
		}
		a.client = &acme.Client{Key: key, DirectoryURL: a.m.cfg.DirectoryURL, HTTPClient: a.m.cfg.HTTPClient, UserAgent: "syncloud"}
	}
	if !a.registered {
		acct := &acme.Account{}
		if a.m.cfg.Email != "" {
			acct.Contact = []string{"mailto:" + a.m.cfg.Email}
		}
		if _, err := a.client.Register(ctx, acct, acme.AcceptTOS); err != nil && !errors.Is(err, acme.ErrAccountAlreadyExists) {
			return nil, fmt.Errorf("register ACME account: %w", err)
		}
		a.registered = true
	}
	return a.client, nil
}

// accountKey loads the ACME account key (sealed in settings), creating it once.
func (a *acmeClient) accountKey(ctx context.Context) (*ecdsa.PrivateKey, error) {
	st, box := a.m.st, a.m.box
	if v, ok, err := st.GetSetting(ctx, settingAccountKey); err != nil {
		return nil, err
	} else if ok {
		sealed, err := base64.StdEncoding.DecodeString(v)
		if err != nil {
			return nil, err
		}
		der, err := box.Open(sealed, []byte(settingAccountKey))
		if err != nil {
			return nil, err
		}
		return x509.ParseECPrivateKey(der)
	}
	k, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, err
	}
	der, err := x509.MarshalECPrivateKey(k)
	if err != nil {
		return nil, err
	}
	return k, st.SetSetting(ctx, settingAccountKey, base64.StdEncoding.EncodeToString(box.Seal(der, []byte(settingAccountKey))))
}

func (a *acmeClient) issue(ctx context.Context, host string) ([]byte, []byte, time.Time, error) {
	c, err := a.get(ctx)
	if err != nil {
		return nil, nil, time.Time{}, err
	}
	order, err := c.AuthorizeOrder(ctx, acme.DomainIDs(host))
	if err != nil {
		return nil, nil, time.Time{}, fmt.Errorf("new order: %w", err)
	}
	orderURI := order.URI
	for _, u := range order.AuthzURLs {
		z, err := c.GetAuthorization(ctx, u)
		if err != nil {
			return nil, nil, time.Time{}, err
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
			return nil, nil, time.Time{}, errors.New("the CA offered no http-01 challenge")
		}
		keyAuth, err := c.HTTP01ChallengeResponse(chal.Token)
		if err != nil {
			return nil, nil, time.Time{}, err
		}
		a.m.challenges.Store(chal.Token, keyAuth)
		defer a.m.challenges.Delete(chal.Token)
		if _, err := c.Accept(ctx, chal); err != nil {
			return nil, nil, time.Time{}, fmt.Errorf("accept challenge: %w", err)
		}
		if _, err := c.WaitAuthorization(ctx, z.URI); err != nil {
			return nil, nil, time.Time{}, fmt.Errorf("http-01 validation of %s: %w", host, err)
		}
	}
	if order, err = c.WaitOrder(ctx, orderURI); err != nil {
		return nil, nil, time.Time{}, fmt.Errorf("wait order: %w", err)
	}
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, nil, time.Time{}, err
	}
	csr, err := x509.CreateCertificateRequest(rand.Reader, &x509.CertificateRequest{DNSNames: []string{host}}, key)
	if err != nil {
		return nil, nil, time.Time{}, err
	}
	ders, _, err := c.CreateOrderCert(ctx, order.FinalizeURL, csr, true)
	if err != nil {
		// CreateOrderCert follows the Location header of the finalize response,
		// which some servers omit; poll the order by its own URL instead.
		o, werr := c.WaitOrder(ctx, orderURI)
		if werr != nil || o.Status != acme.StatusValid || o.CertURL == "" {
			return nil, nil, time.Time{}, fmt.Errorf("finalize: %w", err)
		}
		if ders, err = c.FetchCert(ctx, o.CertURL, true); err != nil {
			return nil, nil, time.Time{}, fmt.Errorf("download certificate: %w", err)
		}
	}
	leaf, err := x509.ParseCertificate(ders[0])
	if err != nil {
		return nil, nil, time.Time{}, err
	}
	var chain []byte
	for _, d := range ders {
		chain = append(chain, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: d})...)
	}
	kder, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		return nil, nil, time.Time{}, err
	}
	return chain, pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: kder}), leaf.NotAfter.UTC(), nil
}

func rateLimited(err error) (time.Duration, bool) {
	return acme.RateLimit(err)
}
