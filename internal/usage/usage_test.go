package usage

import "testing"

func TestRecordAndSnapshot(t *testing.T) {
	tr := New()
	tr.Record(100, 20, 900, 0)
	tr.Record(50, 10, 950, 0)

	got := tr.Snapshot()
	if got.Requests != 2 {
		t.Errorf("requests = %d, want 2", got.Requests)
	}
	if got.InputTokens != 150 {
		t.Errorf("input = %d, want 150", got.InputTokens)
	}
	if got.OutputTokens != 30 {
		t.Errorf("output = %d, want 30", got.OutputTokens)
	}
	if got.CacheReadTokens != 1850 {
		t.Errorf("cache read = %d, want 1850", got.CacheReadTokens)
	}
}

func TestNilTrackerIsNoop(t *testing.T) {
	var tr *Tracker // nil
	tr.Record(1, 2, 3, 4)
	if got := tr.Snapshot(); got != (Snapshot{}) {
		t.Errorf("nil tracker snapshot = %+v, want zero", got)
	}
}
