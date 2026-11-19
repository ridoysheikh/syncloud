package dns

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"path/filepath"
	"slices"
	"testing"

	"github.com/ridoysheikh/syncloud/internal/secrets"
	"github.com/ridoysheikh/syncloud/internal/store"
)

func newSvc(t *testing.T, mem *Mem) *Service {
	t.Helper()
	st, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	box, _ := secrets.New(make([]byte, 32))
	s := NewService(st, box, slog.New(slog.NewTextHandler(io.Discard, nil)))
	s.Factory = func(typ, token string) (Provider, error) {
		if token == "bad" {
			return &badProvider{}, nil
		}
		return mem, nil
	}
	return s
}

type badProvider struct{ Mem }

func (*badProvider) Verify(context.Context) error { return errors.New("Invalid access token") }

func TestProviderZoneAndGrants(t *testing.T) {
	ctx := context.Background()
	mem := NewMem("example.com")
	s := newSvc(t, mem)

	if _, err := s.AddProvider(ctx, "cf", "cloudflare", "bad"); !errors.Is(err, ErrInvalid) {
		t.Fatalf("a refused token must not be stored: %v", err)
	}
	p, err := s.AddProvider(ctx, "cf", "cloudflare", "good")
	if err != nil {
		t.Fatal(err)
	}
	zs, err := s.ProviderZones(ctx, p.ID)
	if err != nil || len(zs) != 1 || zs[0].Registered {
		t.Fatalf("%+v %v", zs, err)
	}
	z, err := s.AddZone(ctx, AddZoneInput{Name: "Example.com", ProviderID: p.ID})
	if err != nil || z.Mode != "managed" || len(z.Grants) != 1 || z.Grants[0].Project != "*" || !z.Grants[0].Wildcards {
		t.Fatalf("a new domain is usable by all projects: %+v %v", z, err)
	}
	if _, err := s.AddZone(ctx, AddZoneInput{Name: "example.com"}); !errors.Is(err, ErrConflict) {
		t.Fatal(err)
	}
	if _, err := s.AddZone(ctx, AddZoneInput{Name: "nope.org", ProviderID: p.ID}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("a zone the token cannot see: %v", err)
	}

	// All projects, wildcards included; names outside every domain are free.
	for _, h := range []string{"app.example.com", "*.example.com", "*.sub.example.com", "elsewhere.org"} {
		if err := s.Allowed(ctx, "shop", h); err != nil {
			t.Fatalf("%s: %v", h, err)
		}
	}
	// Narrow it: shop may use shop.example.com and below, with wildcards; blog only an exact name.
	grants := []store.ZoneGrant{
		{Project: "shop", Suffix: "shop.example.com", Wildcards: true},
		{Project: "blog", Suffix: "blog.example.com", Wildcards: false},
	}
	if _, err := s.UpdateZone(ctx, "example.com", UpdateZoneInput{Grants: &grants}); err != nil {
		t.Fatal(err)
	}
	ok := map[[2]string]bool{
		{"shop", "a.shop.example.com"}: true, {"shop", "*.shop.example.com"}: true, {"shop", "shop.example.com"}: true,
		{"shop", "app.example.com"}: false, {"shop", "*.example.com"}: false,
		{"blog", "x.blog.example.com"}: true, {"blog", "*.blog.example.com"}: false,
		{"blog", "a.shop.example.com"}: false,
	}
	for k, want := range ok {
		if err := s.Allowed(ctx, k[0], k[1]); (err == nil) != want {
			t.Errorf("%v: want allowed=%v, got %v", k, want, err)
		} else if err != nil && !errors.Is(err, ErrNotGranted) {
			t.Errorf("%v: %v", k, err)
		}
	}
	if err := s.Allowed(ctx, "anyone", "elsewhere.org"); err != nil {
		t.Fatal("names outside registered domains stay free")
	}

	// Removing the provider keeps the domain, as manual.
	if err := s.RemoveProvider(ctx, p.ID); err != nil {
		t.Fatal(err)
	}
	z, _ = s.Zone(ctx, "example.com")
	if z.Mode != "manual" || z.ProviderID != "" {
		t.Fatalf("%+v", z)
	}
	if _, err := s.Records(ctx, "example.com", RecordFilter{}); !errors.Is(err, ErrNoProvider) {
		t.Fatal(err)
	}
}

func TestRecordsPointAndChallenges(t *testing.T) {
	ctx := context.Background()
	mem := NewMem("example.com",
		Record{Type: "A", Name: "app.example.com", Content: "198.51.100.7"},
		Record{Type: "A", Name: "app.example.com", Content: "198.51.100.7"})
	s := newSvc(t, mem)
	p, _ := s.AddProvider(ctx, "cf", "cloudflare", "good")
	if _, err := s.AddZone(ctx, AddZoneInput{Name: "example.com", ProviderID: p.ID}); err != nil {
		t.Fatal(err)
	}

	if _, err := s.CreateRecord(ctx, "example.com", Record{Type: "A", Name: "app", Content: "198.51.100.7"}); !errors.Is(err, ErrConflict) {
		t.Fatalf("an exact duplicate is refused: %v", err)
	}
	if _, err := s.CreateRecord(ctx, "example.com", Record{Type: "CNAME", Name: "app", Content: "lb.example.net"}); !errors.Is(err, ErrConflict) {
		t.Fatalf("a CNAME beside an A is refused: %v", err)
	}
	if _, err := s.CreateRecord(ctx, "example.com", Record{Type: "A", Name: "x", Content: "not-an-ip"}); !errors.Is(err, ErrInvalid) {
		t.Fatal(err)
	}
	if got := Qualify("example.com", "_acme-challenge.app"); got != "_acme-challenge.app.example.com" {
		t.Fatal(got)
	}
	if Qualify("example.com", "@") != "example.com" || Qualify("example.com", "www.example.com.") != "www.example.com" {
		t.Fatal("@ and full names")
	}

	// Pointing: the preview changes nothing, the apply needs confirmation to remove the unmanaged A.
	in := PointInput{Name: "app.example.com", Targets: []string{"203.0.113.1"}, Owner: Owner{Kind: "domain", ID: "app"}}
	pr, err := s.Point(ctx, "example.com", in, true, false)
	if err != nil || len(pr.Plan.Conflicts()) != 2 || len(mem.Snapshot()) != 2 {
		t.Fatalf("%+v %v %v", pr, err, mem.Snapshot())
	}
	pr, err = s.Point(ctx, "example.com", in, false, false)
	if err != nil || pr.Applied.Created != 1 || len(pr.Applied.Skipped) != 0 && len(pr.Applied.Skipped) != 2 {
		t.Fatalf("%+v %v", pr, err)
	}
	if !slices.Contains(mem.Snapshot(), "A app.example.com 203.0.113.1") || len(mem.Snapshot()) != 3 {
		t.Fatalf("created next to the unconfirmed conflict: %v", mem.Snapshot())
	}
	if _, err := s.Point(ctx, "example.com", in, false, true); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(mem.Snapshot(), []string{"A app.example.com 203.0.113.1"}) {
		t.Fatalf("confirmed: %v", mem.Snapshot())
	}
	// Next to its own record: a second pointing changes nothing.
	mem.Calls = nil
	if _, err := s.Point(ctx, "example.com", in, false, false); err != nil || len(mem.Calls) != 0 {
		t.Fatalf("%v %v", err, mem.Calls)
	}
	// The platform created it, so it may remove it when it is stale.
	in.Targets = []string{"203.0.113.9"}
	if _, err := s.Point(ctx, "example.com", in, false, false); err != nil || !slices.Equal(mem.Snapshot(), []string{"A app.example.com 203.0.113.9"}) {
		t.Fatalf("%v %v", err, mem.Snapshot())
	}

	// Challenge records: created, reused, removed; others' left alone.
	mem.add(Record{Type: "TXT", Name: "_acme-challenge.example.com", Content: "someone-elses"})
	id, err := s.PutTXT(ctx, "_acme-challenge.example.com", "abc")
	if err != nil {
		t.Fatal(err)
	}
	if id2, _ := s.PutTXT(ctx, "_acme-challenge.example.com", "abc"); id2 != id {
		t.Fatal("a second put must reuse the record")
	}
	res, err := s.Cleanup(ctx, "example.com", true)
	if err != nil || res.Removed != 1 || !slices.Contains(mem.Snapshot(), "TXT _acme-challenge.example.com someone-elses") {
		t.Fatalf("cleanup removes only what the platform created: %+v %v %v", res, err, mem.Snapshot())
	}
	if err := s.DeleteTXT(ctx, "_acme-challenge.example.com", id); err != nil { // already gone: not an error
		t.Fatal(err)
	}
}
