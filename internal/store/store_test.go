package store

import (
	"context"
	"errors"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

func openTest(t *testing.T) *Store {
	t.Helper()
	st, err := Open(context.Background(), filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	return st
}

func TestOpenIsIdempotent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "t.db")
	for i := 0; i < 2; i++ {
		st, err := Open(context.Background(), path)
		if err != nil {
			t.Fatalf("open #%d: %v", i+1, err)
		}
		st.Close()
	}
}

func TestReadPoolSeesWritesAndIsReadOnly(t *testing.T) {
	ctx := context.Background()
	st := openTest(t)
	if err := st.SetSetting(ctx, "k", "v1"); err != nil {
		t.Fatal(err)
	}
	if err := st.SetSetting(ctx, "k", "v2"); err != nil {
		t.Fatal(err)
	}
	v, ok, err := st.GetSetting(ctx, "k")
	if err != nil || !ok || v != "v2" {
		t.Fatalf("got %q ok=%v err=%v", v, ok, err)
	}
	if _, err := st.R.ExecContext(ctx, `DELETE FROM settings`); err == nil {
		t.Fatal("read pool accepted a write")
	}
	var mode string
	if err := st.R.QueryRowContext(ctx, `PRAGMA journal_mode`).Scan(&mode); err != nil || mode != "wal" {
		t.Fatalf("journal_mode = %q err=%v", mode, err)
	}
}

func TestCreateRootUserOnlyOnce(t *testing.T) {
	ctx := context.Background()
	st := openTest(t)
	now := time.Now()

	var wg sync.WaitGroup
	errs := make([]error, 5)
	for i := range errs {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			errs[i] = st.CreateRootUser(ctx, User{
				ID: "usr_" + string(rune('a'+i)), Email: string(rune('a'+i)) + "@x.co", Name: "n", PasswordHash: "h", CreatedAt: now,
			})
		}(i)
	}
	wg.Wait()

	ok := 0
	for _, err := range errs {
		switch {
		case err == nil:
			ok++
		case !errors.Is(err, ErrSetupDone):
			t.Fatalf("unexpected error: %v", err)
		}
	}
	if ok != 1 {
		t.Fatalf("%d concurrent setups succeeded, want exactly 1", ok)
	}
}

func TestSessionExpiry(t *testing.T) {
	ctx := context.Background()
	st := openTest(t)
	now := time.Unix(1_800_000_000, 0)
	if err := st.CreateRootUser(ctx, User{ID: "usr_1", Email: "a@b.co", Name: "A", PasswordHash: "h", CreatedAt: now}); err != nil {
		t.Fatal(err)
	}
	if err := st.CreateSession(ctx, Session{TokenHash: "h1", UserID: "usr_1", CreatedAt: now, ExpiresAt: now.Add(time.Hour), LastSeenAt: now}); err != nil {
		t.Fatal(err)
	}
	if _, err := st.SessionByHash(ctx, "h1", now.Add(30*time.Minute)); err != nil {
		t.Fatalf("live session not found: %v", err)
	}
	if _, err := st.SessionByHash(ctx, "h1", now.Add(time.Hour)); !errors.Is(err, ErrNotFound) {
		t.Fatalf("expired session returned: %v", err)
	}
	if n, err := st.DeleteExpiredSessions(ctx, now.Add(2*time.Hour)); err != nil || n != 1 {
		t.Fatalf("deleted %d err=%v", n, err)
	}
}

func TestRegistryEventsCountOnePullPerPull(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()
	at := time.Unix(1_800_000_000, 0)
	ev := func(id, action, tag, digest string, sec int) RegistryEvent {
		return RegistryEvent{EventID: id, At: at.Add(time.Duration(sec) * time.Second), Action: action, Repository: "shop/api",
			Tag: tag, Digest: digest, Actor: "node", Addr: "10.90.0.2"}
	}
	n, err := s.RecordRegistryEvents(ctx, []RegistryEvent{
		ev("1", "push", "v1", "sha256:a", 0),
		ev("2", "pull", "v1", "sha256:a", 10), // HEAD by tag
		ev("3", "pull", "", "sha256:a", 11),   // GET by digest: same pull
		ev("4", "pull", "v1", "sha256:a", 100),
	})
	if err != nil {
		t.Fatal(err)
	}
	if n != 3 {
		t.Errorf("recorded %d events, want 3", n)
	}
	if n, _ := s.RecordRegistryEvents(ctx, []RegistryEvent{ev("1", "push", "v1", "sha256:a", 0)}); n != 0 {
		t.Errorf("a redelivered event was recorded again")
	}
	st, err := s.ImageStatsFor(ctx, "shop/api")
	if err != nil || len(st) != 1 {
		t.Fatalf("stats %v %v", st, err)
	}
	if st[0].Pushes != 1 || st[0].Pulls != 2 || !st[0].LastPulledAt.Equal(at.Add(100*time.Second)) {
		t.Errorf("stats %+v", st[0])
	}
	if _, err := s.RecordRegistryEvents(ctx, []RegistryEvent{ev("5", "delete", "", "sha256:a", 200)}); err != nil {
		t.Fatal(err)
	}
	if st, _ := s.ImageStatsFor(ctx, "shop/api"); len(st) != 0 {
		t.Errorf("stats kept after delete: %v", st)
	}
	evs, _ := s.ListRegistryEvents(ctx, "shop/api", 10, "")
	if len(evs) != 4 || evs[0].Action != "delete" {
		t.Errorf("events %+v", evs)
	}
}
