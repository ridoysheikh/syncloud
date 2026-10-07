package alerts

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"syncloud/internal/events"
	"syncloud/internal/metrics"
	"syncloud/internal/secrets"
	"syncloud/internal/store"
)

type fakeMetrics struct{ samples []metrics.Sample }

func (f *fakeMetrics) Instant(context.Context, string) ([]metrics.Sample, error) {
	return f.samples, nil
}

type sink struct {
	mu     sync.Mutex
	bodies []string
}

func (s *sink) handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		s.mu.Lock()
		s.bodies = append(s.bodies, r.URL.Path+" "+string(b))
		s.mu.Unlock()
	})
}

func (s *sink) wait(t *testing.T, n int) []string {
	t.Helper()
	for i := 0; i < 100; i++ {
		s.mu.Lock()
		got := append([]string(nil), s.bodies...)
		s.mu.Unlock()
		if len(got) >= n {
			return got
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("got %d notifications, want %d", len(s.bodies), n)
	return nil
}

func TestFiringAndResolved(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(ctx, filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	box, _ := secrets.New(make([]byte, 32))
	fm := &fakeMetrics{}
	m := New(st, box, fm, nil, nil, events.NewBus(), slog.New(slog.NewTextHandler(io.Discard, nil)))
	now := time.Unix(1_800_000_000, 0)
	m.now = func() time.Time { return now }

	var s sink
	srv := httptest.NewServer(s.handler())
	defer srv.Close()
	ch, err := m.PutChannel(ctx, "", "ops", "slack", ChannelConfig{URL: srv.URL + "/slack"})
	if err != nil {
		t.Fatal(err)
	}
	if ch.Summary != strings.TrimPrefix(srv.URL, "http://") {
		t.Errorf("summary %q", ch.Summary)
	}
	r, err := m.PutRule(ctx, Rule{Name: "busy", Enabled: true, Type: "promql", Query: "x", Threshold: 5, ForSeconds: 60, Channels: []string{ch.ID}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := m.PutRule(ctx, Rule{Name: "bad", Type: "promql", Query: "x", Channels: []string{"ach_nope"}}); err == nil {
		t.Error("unknown channel accepted")
	}

	fm.samples = []metrics.Sample{{Labels: map[string]string{"job": "a"}, Value: 9}}
	m.Evaluate(ctx) // pending
	active, _ := m.Active(ctx)
	if len(active) != 1 || active[0].State != "pending" {
		t.Fatalf("after one check: %+v", active)
	}
	now = now.Add(61 * time.Second)
	m.Evaluate(ctx) // firing
	got := s.wait(t, 1)
	if !strings.Contains(got[0], `"text":"[FIRING] busy: job=a (warning)\n9 > 5`) && !strings.Contains(got[0], `[FIRING] busy: job=a (warning)`) {
		t.Errorf("slack payload %s", got[0])
	}
	now = now.Add(30 * time.Second)
	m.Evaluate(ctx) // still firing: no new notification
	fm.samples = nil
	now = now.Add(30 * time.Second)
	m.Evaluate(ctx) // resolved
	got = s.wait(t, 2)
	if !strings.Contains(got[1], "[RESOLVED] busy") {
		t.Errorf("resolved payload %s", got[1])
	}
	evs, _ := st.ListAlertEvents(ctx, 10, "")
	if len(evs) != 2 || evs[0].Kind != "resolved" || evs[1].Kind != "firing" {
		t.Fatalf("events %+v", evs)
	}
	if active, _ := m.Active(ctx); len(active) != 0 {
		t.Errorf("still active: %+v", active)
	}
	if err := m.DeleteChannel(ctx, ch.ID); err == nil {
		t.Error("deleted a channel a rule uses")
	}
	if err := m.DeleteRule(ctx, r.ID); err != nil {
		t.Fatal(err)
	}
}

func TestSendFormats(t *testing.T) {
	var s sink
	srv := httptest.NewServer(s.handler())
	defer srv.Close()
	TelegramAPI = srv.URL
	n := Notification{Kind: "firing", Rule: "5xx", Severity: "critical", Instance: "shop/production/web", Message: "error_rate 12% > 5%", At: time.Now()}
	ctx := context.Background()
	for typ, cfg := range map[string]ChannelConfig{
		"webhook": {URL: srv.URL + "/hook"}, "discord": {URL: srv.URL + "/discord"}, "telegram": {BotToken: "T", ChatID: "42"},
	} {
		if err := Send(ctx, typ, cfg, n); err != nil {
			t.Fatalf("%s: %v", typ, err)
		}
	}
	got := strings.Join(s.wait(t, 3), "\n")
	for _, want := range []string{`/hook {"status":"firing","rule":"5xx","severity":"critical","instance":"shop/production/web"`,
		`/discord {"content":"[FIRING] 5xx: shop/production/web (critical)\nerror_rate 12% > 5%"}`, `/botT/sendMessage {"chat_id":"42"`} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %s in\n%s", want, got)
		}
	}
	var cfg ChannelConfig
	if _, err := cfg.Validate("email"); err == nil {
		t.Error("email without settings accepted")
	}
	cfg = ChannelConfig{SMTPHost: "smtp.example.com", From: "ops@example.com", To: []string{"a@example.com"}}
	if sum, err := cfg.Validate("email"); err != nil || sum != "a@example.com" || cfg.SMTPPort != 587 {
		t.Errorf("email: %q %v %d", sum, err, cfg.SMTPPort)
	}
	_ = json.Marshal
}

func TestValidateRule(t *testing.T) {
	ok := Rule{Name: "x", Type: "metric", Metric: "error_rate", Threshold: 5}
	if err := ok.Validate(); err != nil || ok.Op != ">" || ok.WindowSeconds != 300 || ok.Severity != "warning" {
		t.Errorf("defaults %+v %v", ok, err)
	}
	for _, r := range []Rule{
		{Name: "", Type: "metric", Metric: "cpu"},
		{Name: "x", Type: "metric", Metric: "qps"},
		{Name: "x", Type: "log"},
		{Name: "x", Type: "log", Text: "a", Level: "panic"},
		{Name: "x", Type: "promql"},
		{Name: "x", Type: "metric", Metric: "cpu", Op: ">="},
		{Name: "x", Type: "metric", Metric: "cpu", WindowSeconds: 10},
		{Name: "x", Type: "nope"},
		{Name: "x", Type: "health", Severity: "page"},
		{Name: "x", Type: "health", Service: `a"b`},
	} {
		if err := r.Validate(); err == nil {
			t.Errorf("accepted %+v", r)
		}
	}
}

func TestErrorsHideSecrets(t *testing.T) {
	err := Send(context.Background(), "slack", ChannelConfig{URL: "http://127.0.0.1:1/services/T0/B0/topsecret"}, Notification{Kind: "test"})
	if err == nil || strings.Contains(err.Error(), "topsecret") {
		t.Fatalf("error %v", err)
	}
	TelegramAPI = "http://127.0.0.1:1"
	err = Send(context.Background(), "telegram", ChannelConfig{BotToken: "123:SECRET", ChatID: "1"}, Notification{Kind: "test"})
	if err == nil || strings.Contains(err.Error(), "SECRET") {
		t.Fatalf("error %v", err)
	}
}
