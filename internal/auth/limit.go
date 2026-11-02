package auth

import (
	"sync"
	"time"
)

// limiter is a token bucket per client address plus one global bucket.
// An attempt needs a token from both.
type limiter struct {
	mu      sync.Mutex
	now     func() time.Time
	perCap  float64
	perRate float64
	clients map[string]*bucket
	global  bucket
	gCap    float64
	gRate   float64
}

type bucket struct {
	tokens float64
	last   time.Time
}

// Five attempts per client, then one per 10s; globally 30, then one per
// second.
func newLimiter(now func() time.Time) *limiter {
	l := &limiter{now: now, perCap: 5, perRate: 0.1, gCap: 30, gRate: 1, clients: map[string]*bucket{}}
	l.global = bucket{tokens: l.gCap, last: now()}
	return l
}

func (b *bucket) refill(now time.Time, capacity, rate float64) {
	b.tokens = min(capacity, b.tokens+now.Sub(b.last).Seconds()*rate)
	b.last = now
}

func (l *limiter) allow(client string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.now()
	b, ok := l.clients[client]
	if !ok {
		if len(l.clients) >= 10000 {
			l.prune(now)
		}
		b = &bucket{tokens: l.perCap, last: now}
		l.clients[client] = b
	}
	b.refill(now, l.perCap, l.perRate)
	l.global.refill(now, l.gCap, l.gRate)
	if b.tokens < 1 || l.global.tokens < 1 {
		return false
	}
	b.tokens--
	l.global.tokens--
	return true
}

// prune forgets clients whose buckets have refilled.
func (l *limiter) prune(now time.Time) {
	for k, b := range l.clients {
		b.refill(now, l.perCap, l.perRate)
		if b.tokens >= l.perCap {
			delete(l.clients, k)
		}
	}
}
