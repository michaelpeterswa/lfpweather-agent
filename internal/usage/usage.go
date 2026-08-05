// Package usage tracks cumulative Anthropic token usage across the process. It
// is the foundation for later budget enforcement: a running total the broker or
// a daily-budget check can read.
package usage

import "sync/atomic"

// Snapshot is a point-in-time copy of the counters.
type Snapshot struct {
	Requests            int64 `json:"requests"`
	InputTokens         int64 `json:"input_tokens"`
	OutputTokens        int64 `json:"output_tokens"`
	CacheReadTokens     int64 `json:"cache_read_tokens"`
	CacheCreationTokens int64 `json:"cache_creation_tokens"`
}

// Tracker accumulates usage. All methods are safe for concurrent use, including
// on a nil receiver (a nil Tracker is a no-op, so callers need not nil-check).
type Tracker struct {
	requests            atomic.Int64
	inputTokens         atomic.Int64
	outputTokens        atomic.Int64
	cacheReadTokens     atomic.Int64
	cacheCreationTokens atomic.Int64
}

// New returns a zeroed Tracker.
func New() *Tracker {
	return &Tracker{}
}

// Record adds one model response's usage. cacheRead and cacheCreation are the
// cached-token counts; input is the uncached remainder the API billed at full
// rate. A nil Tracker records nothing.
func (t *Tracker) Record(input, output, cacheRead, cacheCreation int64) {
	if t == nil {
		return
	}
	t.requests.Add(1)
	t.inputTokens.Add(input)
	t.outputTokens.Add(output)
	t.cacheReadTokens.Add(cacheRead)
	t.cacheCreationTokens.Add(cacheCreation)
}

// Snapshot reads the current totals. A nil Tracker reports zeros.
func (t *Tracker) Snapshot() Snapshot {
	if t == nil {
		return Snapshot{}
	}
	return Snapshot{
		Requests:            t.requests.Load(),
		InputTokens:         t.inputTokens.Load(),
		OutputTokens:        t.outputTokens.Load(),
		CacheReadTokens:     t.cacheReadTokens.Load(),
		CacheCreationTokens: t.cacheCreationTokens.Load(),
	}
}
