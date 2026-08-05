package session

import (
	"testing"
	"time"

	"github.com/anthropics/anthropic-sdk-go"
)

func mkMessage() anthropic.MessageParam {
	return anthropic.NewUserMessage(anthropic.NewTextBlock("hi"))
}

func TestGetCreatesAndReuses(t *testing.T) {
	s := NewStore(time.Hour)

	a := s.Get("tab-1")
	if a == nil {
		t.Fatal("Get returned nil")
	}
	a.History = append(a.History, mkMessage())

	b := s.Get("tab-1")
	if b != a {
		t.Error("Get returned a different session for the same id")
	}
	if len(b.History) != 1 {
		t.Errorf("history len = %d, want 1 (state should persist)", len(b.History))
	}

	c := s.Get("tab-2")
	if c == a {
		t.Error("different ids should get different sessions")
	}
	if s.Len() != 2 {
		t.Errorf("len = %d, want 2", s.Len())
	}
}

func TestReapEvictsIdle(t *testing.T) {
	s := NewStore(30 * time.Minute)

	base := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	s.now = func() time.Time { return base }
	s.Get("old")

	// "fresh" is touched 40 minutes later.
	s.now = func() time.Time { return base.Add(40 * time.Minute) }
	s.Get("fresh")

	// Reap at +45m: "old" (idle 45m) is evicted, "fresh" (idle 5m) survives.
	s.now = func() time.Time { return base.Add(45 * time.Minute) }
	if removed := s.reap(); removed != 1 {
		t.Errorf("reap removed %d, want 1", removed)
	}
	if s.Len() != 1 {
		t.Errorf("len after reap = %d, want 1", s.Len())
	}
}
