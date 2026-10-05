package certs

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"path/filepath"
	"testing"
	"time"

	"syncloud/internal/secrets"
	"syncloud/internal/store"
)

func newTestManager(t *testing.T, acme bool) (*Manager, *store.Store, *secrets.Box) {
	t.Helper()
	st, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	box, _ := secrets.New(make([]byte, 32))
	m := New(st, box, nil, slog.New(slog.NewTextHandler(io.Discard, nil)), Config{ACME: acme})
	return m, st, box
}

func TestSelfSignedWhenACMEDisabled(t *testing.T) {
	m, _, _ := newTestManager(t, false)
	m.issue = func(context.Context, string) ([]byte, []byte, time.Time, error) {
		t.Fatal("ACME used while disabled")
		return nil, nil, time.Time{}, nil
	}
	m.SetHosts([]string{"b.example.com", "a.example.com", "a.example.com"})
	m.Reconcile(context.Background())
	l := m.List()
	if len(l) != 2 || l[0].Host != "a.example.com" || l[0].Status != StatusSelfSigned || len(m.Pairs()) != 2 {
		t.Fatalf("%+v", l)
	}
}

func TestIssueBackoffAndReload(t *testing.T) {
	ctx := context.Background()
	m, st, box := newTestManager(t, true)
	now := time.Unix(1_800_000_000, 0)
	m.now = func() time.Time { return now }
	calls := 0
	fail := errors.New("connection refused")
	m.issue = func(_ context.Context, host string) ([]byte, []byte, time.Time, error) {
		calls++
		if calls == 1 {
			return nil, nil, time.Time{}, fail
		}
		c, k, _, err := selfSigned(host, now)
		return c, k, now.Add(90 * 24 * time.Hour), err
	}
	m.SetHosts([]string{"x.example.com"})

	m.Reconcile(ctx) // placeholder + failed attempt
	v := m.List()[0]
	if v.Status != StatusFailed || v.Issuer != IssuerSelfSigned || v.NextAttemptAt == nil || calls != 1 || len(m.Pairs()) != 1 {
		t.Fatalf("after failure: %+v calls=%d", v, calls)
	}
	m.Reconcile(ctx) // still backing off
	if calls != 1 {
		t.Fatal("retried during backoff")
	}
	if err := m.Renew(ctx, "x.example.com"); err != nil {
		t.Fatal(err)
	}
	m.Reconcile(ctx)
	if v := m.List()[0]; v.Status != StatusValid || v.Issuer != IssuerACME || calls != 2 {
		t.Fatalf("after renew: %+v", v)
	}
	m.Reconcile(ctx) // not due
	if calls != 2 {
		t.Fatal("renewed a fresh certificate")
	}
	if err := m.Renew(ctx, "other.example.com"); !errors.Is(err, ErrUnknownHost) {
		t.Fatal(err)
	}

	// A new manager loads the stored certificate and key.
	m2 := New(st, box, nil, m.log, Config{ACME: true})
	if err := m2.Load(ctx); err != nil {
		t.Fatal(err)
	}
	m2.SetHosts([]string{"x.example.com"})
	if p := m2.Pairs(); len(p) != 1 || p[0].KeyPEM != m.Pairs()[0].KeyPEM {
		t.Fatal("reload lost the key")
	}
}
