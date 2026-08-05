package broker

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestBudgetDeltaAccumulation(t *testing.T) {
	b := NewBudgetTracker(0)
	b.Record("s1", 100)
	b.Record("s1", 250) // +150
	b.Record("s2", 40)  // +40
	if got := b.Snapshot().DailyTokens; got != 290 {
		t.Errorf("daily = %d, want 290", got)
	}
}

func TestBudgetExceeded(t *testing.T) {
	b := NewBudgetTracker(1000)
	b.Record("s1", 900)
	if b.Exceeded() {
		t.Error("900 < 1000 should not be exceeded")
	}
	b.Record("s1", 1000) // +100 -> 1000
	if !b.Exceeded() {
		t.Error("1000 >= 1000 should be exceeded")
	}
	if snap := b.Snapshot(); snap.Remaining != 0 || !snap.Exceeded {
		t.Errorf("snap = %+v, want remaining 0 + exceeded", snap)
	}
}

func TestBudgetDisabledNeverExceeds(t *testing.T) {
	b := NewBudgetTracker(0)
	b.Record("s1", 1_000_000)
	if b.Exceeded() {
		t.Error("disabled budget must never be exceeded")
	}
	if r := b.Snapshot().Remaining; r != -1 {
		t.Errorf("remaining = %d, want -1 when disabled", r)
	}
}

func TestBudgetCounterReset(t *testing.T) {
	// A new agent reusing the key reports a lower cumulative -> counted from zero.
	b := NewBudgetTracker(0)
	b.Record("s1", 500)
	b.Record("s1", 30)
	if got := b.Snapshot().DailyTokens; got != 530 {
		t.Errorf("daily = %d, want 530", got)
	}
}

func TestBudgetPrune(t *testing.T) {
	b := NewBudgetTracker(0)
	b.Record("s1", 100)
	b.Record("s2", 100)
	b.Prune(map[string]struct{}{"s1": {}}) // drop s2
	b.Record("s2", 50)                     // key gone -> counted from zero (+50)
	if got := b.Snapshot().DailyTokens; got != 250 {
		t.Errorf("daily = %d, want 250 (200 + 50)", got)
	}
}

func TestBudgetDailyReset(t *testing.T) {
	b := NewBudgetTracker(1000)
	day1 := time.Date(2026, 8, 5, 23, 0, 0, 0, time.UTC)
	b.now = func() time.Time { return day1 }
	b.day = day1.Format(time.DateOnly)

	b.Record("s1", 800)
	if b.Snapshot().DailyTokens != 800 {
		t.Fatal("expected 800 on day1")
	}

	// Next UTC day: total resets, but last[] persists so only the new delta counts.
	day2 := time.Date(2026, 8, 6, 1, 0, 0, 0, time.UTC)
	b.now = func() time.Time { return day2 }
	b.Record("s1", 900) // 800 -> 900 = +100, on a fresh day

	snap := b.Snapshot()
	if snap.DailyTokens != 100 {
		t.Errorf("day2 daily = %d, want 100", snap.DailyTokens)
	}
	if snap.Day != day2.Format(time.DateOnly) {
		t.Errorf("day = %s, want %s", snap.Day, day2.Format(time.DateOnly))
	}
}

func TestBudgetDegradeStopsProxy(t *testing.T) {
	b := NewBudgetTracker(100)
	b.Record("x", 100) // at the ceiling

	// The provider target is unreachable — if the request were proxied this
	// would 502, so a 200 SSE proves the budget short-circuited before Acquire.
	h := NewServer(NewDirectProvider("http://127.0.0.1:0"), 65536, b).Handler()

	req := httptest.NewRequest(http.MethodPost, "/v1/chat", strings.NewReader(`{"session_id":"tab-1","message":"hi"}`))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	body := rec.Body.String()
	if !strings.Contains(body, "event: error") || !strings.Contains(body, "resting") {
		t.Errorf("expected a graceful degrade SSE, got %q", body)
	}
	if !strings.Contains(body, "event: done") {
		t.Errorf("degrade should close the stream with done, got %q", body)
	}
}

func TestUsageEndpoint(t *testing.T) {
	b := NewBudgetTracker(1000)
	b.Record("x", 300)
	h := NewServer(NewDirectProvider("http://unused"), 65536, b).Handler()

	req := httptest.NewRequest(http.MethodGet, "/usage", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	body := rec.Body.String()
	if !strings.Contains(body, `"daily_tokens":300`) || !strings.Contains(body, `"remaining":700`) {
		t.Errorf("usage body = %q", body)
	}
}
