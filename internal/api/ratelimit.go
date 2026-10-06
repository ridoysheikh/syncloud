package api

import (
	"sync"
	"time"
)

// attemptLimiter allows at most max attempts per key within window
// (fixed window, in memory). Used for login and setup endpoints.
type attemptLimiter struct {
	mu     sync.Mutex
	max    int
	window time.Duration
	hits   map[string]*attempts
}

type attempts struct {
	start time.Time
	n     int
}

func newAttemptLimiter(max int, window time.Duration) *attemptLimiter {
	return &attemptLimiter{max: max, window: window, hits: map[string]*attempts{}}
}

func (l *attemptLimiter) Allow(key string, now time.Time) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	a, ok := l.hits[key]
	if !ok || now.Sub(a.start) >= l.window {
		if len(l.hits) > 10000 { // bound memory under a spray of IPs
			l.sweep(now)
		}
		l.hits[key] = &attempts{start: now, n: 1}
		return true
	}
	a.n++
	return a.n <= l.max
}

func (l *attemptLimiter) sweep(now time.Time) {
	for k, a := range l.hits {
		if now.Sub(a.start) >= l.window {
			delete(l.hits, k)
		}
	}
}

// Blocked reports whether key has used up its attempts, without counting one.
func (l *attemptLimiter) Blocked(key string, now time.Time) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	a, ok := l.hits[key]
	return ok && now.Sub(a.start) < l.window && a.n >= l.max
}

// Fail counts a failed attempt (for limits on failures only).
func (l *attemptLimiter) Fail(key string, now time.Time) { l.Allow(key, now) }
