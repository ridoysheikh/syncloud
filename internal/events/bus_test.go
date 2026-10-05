package events

import "testing"

func TestTopicFilteringAndSlowSubscribers(t *testing.T) {
	b := NewBus()
	all := b.Subscribe(10)
	only := b.Subscribe(10, "a")
	slow := b.Subscribe(1)
	defer all.Close()
	defer only.Close()

	b.Publish("a", 1)
	b.Publish("b", 2) // slow's buffer is full: dropped, Publish must not block

	if got := len(all.C); got != 2 {
		t.Fatalf("all: %d events, want 2", got)
	}
	if got := len(only.C); got != 1 || (<-only.C).Topic != "a" {
		t.Fatalf("only: wrong events")
	}
	if got := len(slow.C); got != 1 {
		t.Fatalf("slow: %d events, want 1", got)
	}
	slow.Close()
	slow.Close()      // idempotent
	b.Publish("a", 3) // must not send on the closed subscription
}
