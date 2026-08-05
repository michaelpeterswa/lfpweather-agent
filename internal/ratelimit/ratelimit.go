// Package ratelimit is a small per-key token-bucket limiter used to cap how
// often one client may start a chat. It has no external dependencies and takes
// an injectable clock for testing.
package ratelimit

import (
	"sync"
	"time"
)

type bucket struct {
	tokens float64
	last   time.Time
}

// Limiter allows up to burst requests instantly per key, refilling at
// ratePerSec tokens per second. Keys are typically client IPs.
type Limiter struct {
	mu         sync.Mutex
	buckets    map[string]*bucket
	ratePerSec float64
	burst      float64
	now        func() time.Time
}

// New builds a limiter from a per-minute rate and a burst size. A rpm <= 0
// disables limiting (Allow always returns true).
func New(rpm, burst int) *Limiter {
	return &Limiter{
		buckets:    make(map[string]*bucket),
		ratePerSec: float64(rpm) / 60.0,
		burst:      float64(burst),
		now:        time.Now,
	}
}

// Allow reports whether a request for key may proceed, consuming a token when
// it does.
func (l *Limiter) Allow(key string) bool {
	if l.ratePerSec <= 0 {
		return true
	}

	l.mu.Lock()
	defer l.mu.Unlock()

	now := l.now()
	b, ok := l.buckets[key]
	if !ok {
		// A new key starts full, then spends one token for this request.
		l.buckets[key] = &bucket{tokens: l.burst - 1, last: now}
		return true
	}

	// Refill for the elapsed time, capped at burst.
	b.tokens += now.Sub(b.last).Seconds() * l.ratePerSec
	if b.tokens > l.burst {
		b.tokens = l.burst
	}
	b.last = now

	if b.tokens < 1 {
		return false
	}
	b.tokens--
	return true
}

// reap drops buckets that have been full and idle, so the map does not grow
// without bound. A bucket idle long enough to fully refill carries no state.
func (l *Limiter) reap() {
	l.mu.Lock()
	defer l.mu.Unlock()

	if l.ratePerSec <= 0 {
		return
	}
	fullAfter := l.burst / l.ratePerSec // seconds to refill from empty to full
	cutoff := l.now().Add(-time.Duration(fullAfter) * time.Second)
	for key, b := range l.buckets {
		if b.last.Before(cutoff) {
			delete(l.buckets, key)
		}
	}
}

// Reaper runs reap on an interval until stop is closed.
func (l *Limiter) Reaper(stop <-chan struct{}, interval time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-stop:
			return
		case <-ticker.C:
			l.reap()
		}
	}
}
