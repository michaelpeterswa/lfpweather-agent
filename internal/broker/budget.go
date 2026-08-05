package broker

import (
	"sync"
	"time"
)

// BudgetSnapshot is the observable state of the daily token budget.
type BudgetSnapshot struct {
	DailyTokens int64  `json:"daily_tokens"`
	Budget      int64  `json:"budget"`    // 0 = disabled
	Remaining   int64  `json:"remaining"` // budget - daily (floored at 0); -1 disabled
	Exceeded    bool   `json:"exceeded"`
	Day         string `json:"day"` // UTC date the daily total covers
}

// BudgetTracker accumulates token usage across all agents into a daily total and
// enforces a ceiling. Usage is reported per session as a monotonic cumulative
// count; the tracker sums the per-session deltas, so ephemeral sandboxes coming
// and going do not double-count. The total resets at UTC midnight.
type BudgetTracker struct {
	mu     sync.Mutex
	budget int64
	daily  int64
	last   map[string]int64 // session key -> last cumulative seen
	day    string
	now    func() time.Time
}

// NewBudgetTracker returns a tracker with the given daily ceiling (0 = disabled).
func NewBudgetTracker(budget int64) *BudgetTracker {
	t := &BudgetTracker{
		budget: budget,
		last:   make(map[string]int64),
		now:    time.Now,
	}
	t.day = t.now().UTC().Format(time.DateOnly)
	return t
}

// Record updates a session's cumulative token count, adding the delta to the
// daily total. A cumulative below the previous value (a new agent reusing the
// key) is counted from zero.
func (t *BudgetTracker) Record(key string, cumulative int64) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.rollLocked()

	prev, ok := t.last[key]
	switch {
	case !ok, cumulative < prev:
		t.daily += cumulative
	default:
		t.daily += cumulative - prev
	}
	t.last[key] = cumulative
}

// Prune drops keys no longer active so the map does not grow; their deltas up to
// the last Record are already counted.
func (t *BudgetTracker) Prune(active map[string]struct{}) {
	t.mu.Lock()
	defer t.mu.Unlock()
	for k := range t.last {
		if _, ok := active[k]; !ok {
			delete(t.last, k)
		}
	}
}

// Exceeded reports whether the daily total has reached the budget. Always false
// when the budget is disabled.
func (t *BudgetTracker) Exceeded() bool {
	if t.budget <= 0 {
		return false
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	t.rollLocked()
	return t.daily >= t.budget
}

// Snapshot returns the current budget state.
func (t *BudgetTracker) Snapshot() BudgetSnapshot {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.rollLocked()

	s := BudgetSnapshot{DailyTokens: t.daily, Budget: t.budget, Day: t.day}
	if t.budget <= 0 {
		s.Remaining = -1
		return s
	}
	s.Exceeded = t.daily >= t.budget
	if r := t.budget - t.daily; r > 0 {
		s.Remaining = r
	}
	return s
}

// rollLocked resets the daily total when the UTC day changes. It keeps last[] so
// an agent active across midnight contributes only its post-midnight delta.
func (t *BudgetTracker) rollLocked() {
	if today := t.now().UTC().Format(time.DateOnly); today != t.day {
		t.day = today
		t.daily = 0
	}
}
