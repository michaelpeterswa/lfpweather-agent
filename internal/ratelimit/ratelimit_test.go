package ratelimit

import (
	"testing"
	"time"
)

func TestBurstThenDeny(t *testing.T) {
	l := New(60, 3) // 1 token/sec, burst 3
	base := time.Unix(1000, 0)
	l.now = func() time.Time { return base }

	// First 3 requests consume the burst.
	for i := 0; i < 3; i++ {
		if !l.Allow("1.2.3.4") {
			t.Fatalf("request %d should be allowed within burst", i+1)
		}
	}
	// 4th is denied — bucket empty, no time elapsed.
	if l.Allow("1.2.3.4") {
		t.Error("4th request should be denied")
	}
}

func TestRefillOverTime(t *testing.T) {
	l := New(60, 2) // 1 token/sec, burst 2
	base := time.Unix(2000, 0)
	now := base
	l.now = func() time.Time { return now }

	l.Allow("ip")
	l.Allow("ip")
	if l.Allow("ip") {
		t.Fatal("should be denied after burst")
	}

	// One second later, one token has refilled.
	now = base.Add(time.Second)
	if !l.Allow("ip") {
		t.Error("should allow after 1s refill")
	}
	if l.Allow("ip") {
		t.Error("only one token refilled; second should be denied")
	}
}

func TestPerKeyIsolation(t *testing.T) {
	l := New(60, 1)
	base := time.Unix(3000, 0)
	l.now = func() time.Time { return base }

	if !l.Allow("a") {
		t.Fatal("a first request should pass")
	}
	if !l.Allow("b") {
		t.Error("b has its own bucket and should pass")
	}
	if l.Allow("a") {
		t.Error("a is out of tokens")
	}
}

func TestDisabled(t *testing.T) {
	l := New(0, 0) // rpm <= 0 disables limiting
	for i := 0; i < 100; i++ {
		if !l.Allow("x") {
			t.Fatal("disabled limiter should always allow")
		}
	}
}

func TestReapDropsIdle(t *testing.T) {
	l := New(60, 5) // full refill takes 5s
	base := time.Unix(4000, 0)
	now := base
	l.now = func() time.Time { return now }

	l.Allow("stale")
	now = base.Add(10 * time.Second) // idle well past full-refill window
	l.reap()

	l.mu.Lock()
	_, present := l.buckets["stale"]
	l.mu.Unlock()
	if present {
		t.Error("idle bucket should have been reaped")
	}
}
