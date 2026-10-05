// Package events is the controller's in-process event bus (replaces Postgres
// LISTEN/NOTIFY, D5). Subscribers that fall behind lose events rather than
// blocking publishers.
package events

import (
	"sync"
	"time"
)

type Event struct {
	Topic string    `json:"topic"`
	At    time.Time `json:"at"`
	Data  any       `json:"data"`
}

type Bus struct {
	mu   sync.RWMutex
	subs map[*Sub]struct{}
}

func NewBus() *Bus { return &Bus{subs: map[*Sub]struct{}{}} }

type Sub struct {
	C      <-chan Event
	c      chan Event
	topics map[string]bool // nil means all topics
	bus    *Bus
	once   sync.Once
}

// Subscribe returns a subscription to the given topics (all topics if none).
// Call Close when done.
func (b *Bus) Subscribe(buffer int, topics ...string) *Sub {
	c := make(chan Event, buffer)
	s := &Sub{C: c, c: c, bus: b}
	if len(topics) > 0 {
		s.topics = map[string]bool{}
		for _, t := range topics {
			s.topics[t] = true
		}
	}
	b.mu.Lock()
	b.subs[s] = struct{}{}
	b.mu.Unlock()
	return s
}

func (s *Sub) Close() {
	s.once.Do(func() {
		s.bus.mu.Lock()
		delete(s.bus.subs, s)
		s.bus.mu.Unlock()
		close(s.c)
	})
}

func (b *Bus) Publish(topic string, data any) {
	e := Event{Topic: topic, At: time.Now(), Data: data}
	b.mu.RLock()
	defer b.mu.RUnlock()
	for s := range b.subs {
		if s.topics != nil && !s.topics[topic] {
			continue
		}
		select {
		case s.c <- e:
		default: // slow subscriber: drop
		}
	}
}
