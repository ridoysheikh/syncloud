package alerts

import (
	"bufio"
	"context"
	"errors"
	"io"
	"log/slog"
	"net"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ridoysheikh/syncloud/internal/events"
	"github.com/ridoysheikh/syncloud/internal/metrics"
	"github.com/ridoysheikh/syncloud/internal/secrets"
	"github.com/ridoysheikh/syncloud/internal/store"
)

// fakeSMTP accepts mail without TLS or auth and records each message.
type fakeSMTP struct {
	ln   net.Listener
	mu   sync.Mutex
	msgs []string
	rcpt []string
}

func newFakeSMTP(t *testing.T) *fakeSMTP {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	f := &fakeSMTP{ln: ln}
	t.Cleanup(func() { ln.Close() })
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go f.serve(c)
		}
	}()
	return f
}

func (f *fakeSMTP) port() int { return f.ln.Addr().(*net.TCPAddr).Port }

func (f *fakeSMTP) serve(c net.Conn) {
	defer c.Close()
	r := bufio.NewReader(c)
	say := func(s string) { _, _ = io.WriteString(c, s+"\r\n") }
	say("220 fake")
	for {
		line, err := r.ReadString('\n')
		if err != nil {
			return
		}
		cmd := strings.ToUpper(strings.TrimSpace(line))
		switch {
		case strings.HasPrefix(cmd, "EHLO"), strings.HasPrefix(cmd, "HELO"):
			say("250 fake")
		case strings.HasPrefix(cmd, "RCPT TO:"):
			f.mu.Lock()
			f.rcpt = append(f.rcpt, strings.Trim(strings.TrimSpace(line)[8:], "<>"))
			f.mu.Unlock()
			say("250 ok")
		case cmd == "DATA":
			say("354 go")
			var b strings.Builder
			for {
				l, err := r.ReadString('\n')
				if err != nil {
					return
				}
				if l == ".\r\n" {
					break
				}
				b.WriteString(l)
			}
			f.mu.Lock()
			f.msgs = append(f.msgs, b.String())
			f.mu.Unlock()
			say("250 queued")
		case strings.HasPrefix(cmd, "AUTH"):
			say("235 ok")
		case cmd == "QUIT":
			say("221 bye")
			return
		default:
			say("250 ok")
		}
	}
}

func (f *fakeSMTP) wait(t *testing.T, n int) []string {
	t.Helper()
	for i := 0; i < 100; i++ {
		f.mu.Lock()
		got := append([]string(nil), f.msgs...)
		f.mu.Unlock()
		if len(got) >= n {
			return got
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("got %d emails, want %d", len(f.msgs), n)
	return nil
}

func newTestManager(t *testing.T) (*Manager, *store.Store) {
	t.Helper()
	ctx := context.Background()
	st, err := store.Open(ctx, filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	box, _ := secrets.New(make([]byte, 32))
	m := New(st, box, &fakeMetrics{}, nil, nil, events.NewBus(), slog.New(slog.NewTextHandler(io.Discard, nil)))
	return m, st
}

func TestSMTPSettingsAndEmail(t *testing.T) {
	ctx := context.Background()
	m, _ := newTestManager(t)
	m.Brand = func() (string, []byte) { return "Acme Cloud", []byte("\x89PNGfake") }
	srv := newFakeSMTP(t)

	if v, _ := m.SMTP(ctx); v.Configured {
		t.Fatal("configured before set")
	}
	if err := m.TestSMTP(ctx, "a@example.com"); !errors.Is(err, ErrNoSMTP) {
		t.Fatalf("test without SMTP: %v", err)
	}
	if _, err := m.SetSMTP(ctx, SMTP{Host: "127.0.0.1", Security: "bogus", From: "x@example.com"}); err == nil {
		t.Fatal("bad security accepted")
	}
	v, err := m.SetSMTP(ctx, SMTP{Host: "127.0.0.1", Port: srv.port(), Security: "none", Username: "u", Password: "p", From: "alerts@example.com"})
	if err != nil || !v.Configured || !v.HasPassword || v.Port != srv.port() {
		t.Fatalf("set: %+v %v", v, err)
	}
	// An empty password keeps the stored one.
	if v, err := m.SetSMTP(ctx, SMTP{Host: "127.0.0.1", Port: srv.port(), Security: "none", Username: "u", From: "alerts@example.com"}); err != nil || !v.HasPassword {
		t.Fatalf("keep password: %+v %v", v, err)
	}
	if err := m.TestSMTP(ctx, "ops@example.com"); err != nil {
		t.Fatal(err)
	}
	msg := srv.wait(t, 1)[0]
	for _, want := range []string{"From: \"Acme Cloud\" <alerts@example.com>", "multipart/related", "Content-ID: <logo>", "text/html", "Acme Cloud"} {
		if !strings.Contains(msg, want) {
			t.Errorf("email lacks %q", want)
		}
	}

	// An email channel with recipients only uses the platform SMTP.
	ch, err := m.PutChannel(ctx, "", "ops", "email", ChannelConfig{To: []string{"team@example.com"}})
	if err != nil {
		t.Fatal(err)
	}
	if err := m.TestChannel(ctx, ch.ID); err != nil {
		t.Fatal(err)
	}
	srv.wait(t, 2)
	if err := m.DeleteSMTP(ctx); err != nil {
		t.Fatal(err)
	}
	if err := m.TestChannel(ctx, ch.ID); !errors.Is(err, ErrNoSMTP) {
		t.Fatalf("channel without SMTP: %v", err)
	}
}

func TestSystemRulesAndDefaults(t *testing.T) {
	ctx := context.Background()
	m, _ := newTestManager(t)
	srv := newFakeSMTP(t)
	if _, err := m.SetSMTP(ctx, SMTP{Host: "127.0.0.1", Port: srv.port(), Security: "none", From: "alerts@example.com"}); err != nil {
		t.Fatal(err)
	}
	if v, err := m.SMTP(ctx); !v.Configured {
		t.Fatalf("smtp after set: %+v %v", v, err)
	}
	now := time.Unix(1_800_000_000, 0)
	m.now = func() time.Time { return now }
	used, total := uint64(90), uint64(100)
	comps := []ComponentState{{ID: "sys-traefik", Name: "Traefik", State: "running"}, {ID: "sys-registry", Name: "Registry", State: "exited", Error: "boom"}}
	m.Sources = Sources{
		ControllerDisk: func() (uint64, uint64, error) { return used, total, nil },
		Components:     func() []ComponentState { return comps },
		Backup: func() BackupState {
			t := now.Add(-30 * time.Hour)
			return BackupState{Configured: true, LastRunAt: &t, LastSuccessAt: &t}
		},
	}
	m.metrics = &fakeMetrics{samples: []metrics.Sample{{Labels: map[string]string{"node": "w1"}, Value: 95}, {Labels: map[string]string{"node": "w2"}, Value: 20}}}

	// Before setup there is no address: nothing is created.
	if err := m.EnsureDefaults(ctx, ""); err != nil {
		t.Fatal(err)
	}
	if rs, _ := m.Rules(ctx); len(rs) != 0 {
		t.Fatalf("defaults without an address: %d", len(rs))
	}
	if err := m.EnsureDefaults(ctx, "root@example.com"); err != nil {
		t.Fatal(err)
	}
	rules, _ := m.Rules(ctx)
	if len(rules) != len(DefaultRules()) {
		t.Fatalf("defaults: %d rules", len(rules))
	}
	// Deleting one and ensuring again does not bring it back; restoring does.
	_ = m.DeleteRule(ctx, rules[0].ID)
	_ = m.EnsureDefaults(ctx, "root@example.com")
	if rs, _ := m.Rules(ctx); len(rs) != len(DefaultRules())-1 {
		t.Fatal("ensure re-created a deleted rule")
	}
	if n, err := m.RestoreDefaults(ctx, ""); err != nil || n != 1 {
		t.Fatalf("restore: %d %v", n, err)
	}

	// Evaluate: disk 90% > 85, registry down, backup 30h > 26h, node w1 CPU 95%
	// (memory/disk rules see the same fake samples, so w1 fires for them too).
	m.Evaluate(ctx)
	now = now.Add(3 * time.Minute) // past forSeconds
	m.Evaluate(ctx)
	active, _ := m.Active(ctx)
	firing := map[string]bool{}
	for _, a := range active {
		if a.State == "firing" {
			firing[a.Label] = true
		}
	}
	for _, want := range []string{"controller disk", "Registry", "controller backups", "node w1"} {
		if !firing[want] {
			t.Errorf("%s is not firing: %v", want, firing)
		}
	}
	if firing["node w2"] || firing["Traefik"] {
		t.Errorf("healthy instances firing: %v", firing)
	}
	got := srv.wait(t, 4)
	srv.mu.Lock()
	rcpt := srv.rcpt[0]
	srv.mu.Unlock()
	if rcpt != "root@example.com" || !strings.Contains(strings.Join(got, ""), "[FIRING]") {
		t.Fatalf("delivery to %s", rcpt)
	}

	// Reminders after repeatHours; resolution when the disk is freed.
	before := len(srv.wait(t, 1))
	now = now.Add(13 * time.Hour)
	m.Evaluate(ctx)
	after := srv.wait(t, before+1)
	if !strings.Contains(strings.Join(after[before:], ""), "STILL FIRING") {
		t.Error("no reminder")
	}
	used = 10
	m.Evaluate(ctx)
	time.Sleep(100 * time.Millisecond)
	srv.mu.Lock()
	all := strings.Join(srv.msgs, "")
	srv.mu.Unlock()
	if !strings.Contains(all, "RESOLVED") {
		t.Error("no resolved email for the controller disk")
	}

	// Validation of system types.
	for _, r := range []Rule{
		{Name: "x", Type: "node_overload", Metric: "rps"},
		{Name: "x", Type: "controller_disk", Threshold: 150},
	} {
		if err := r.Validate(); err == nil {
			t.Errorf("%s accepted with %+v", r.Type, r)
		}
	}
	r := Rule{Name: "x", Type: "certificate", Project: "shop"}
	if err := r.Validate(); err != nil || r.Threshold != 14 || r.Project != "" {
		t.Errorf("certificate defaults: %+v %v", r, err)
	}
}

func TestDatabaseFailoverEvent(t *testing.T) {
	ctx := context.Background()
	m, st := newTestManager(t)
	srv := newFakeSMTP(t)
	if _, err := m.SetSMTP(ctx, SMTP{Host: "127.0.0.1", Port: srv.port(), Security: "none", From: "alerts@example.com"}); err != nil {
		t.Fatal(err)
	}
	now := time.Unix(1_800_000_000, 0)
	m.now = func() time.Time { return now }
	if err := st.CreateDatabase(ctx, store.Database{ID: "db_x", Name: "orders", Engine: "postgres", Version: "18", Spec: "{}", State: "{}", Network: "{}", Secrets: []byte("x"), CreatedAt: now, UpdatedAt: now}); err != nil {
		t.Fatal(err)
	}
	old := store.DatabaseEvent{DatabaseID: "db_x", At: now, Kind: "failover", From: "m0", To: "m1", Reason: "before alerts started"}
	if err := st.AddDatabaseEvent(ctx, old); err != nil {
		t.Fatal(err)
	}
	ch, err := m.PutChannel(ctx, "", "ops", "email", ChannelConfig{To: []string{"ops@example.com"}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := m.PutRule(ctx, Rule{Name: "fo", Type: "database_failover", Enabled: true, Channels: []string{ch.ID}}); err != nil {
		t.Fatal(err)
	}
	m.Evaluate(ctx) // starts after the existing event
	if err := st.AddDatabaseEvent(ctx, store.DatabaseEvent{DatabaseID: "db_x", At: now, Kind: "failover", From: "m1", To: "m2", Reason: "Patroni elected a new leader"}); err != nil {
		t.Fatal(err)
	}
	m.Evaluate(ctx)
	m.Evaluate(ctx) // notified once
	got := srv.wait(t, 1)
	time.Sleep(100 * time.Millisecond)
	srv.mu.Lock()
	n := len(srv.msgs)
	srv.mu.Unlock()
	if n != 1 || !strings.Contains(got[0], "m1 to m2") || strings.Contains(got[0], "before alerts started") {
		t.Fatalf("%d emails: %s", n, got[0])
	}
}
