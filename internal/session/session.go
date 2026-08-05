// Package session holds per-conversation state in memory. A conversation lives
// only as long as its browser tab; nothing is persisted. Idle conversations are
// evicted after a TTL.
package session

import (
	"sync"
	"time"

	"github.com/anthropics/anthropic-sdk-go"
)

// Session is one conversation. Mu serializes the messages of a single session
// so a client cannot interleave two requests on the same conversation.
type Session struct {
	Mu       sync.Mutex
	History  []anthropic.MessageParam
	lastSeen time.Time
}

// Store is a TTL map of sessions keyed by id.
type Store struct {
	mu       sync.Mutex
	sessions map[string]*Session
	ttl      time.Duration
	now      func() time.Time
}

// NewStore builds an empty store with the given idle TTL.
func NewStore(ttl time.Duration) *Store {
	return &Store{
		sessions: make(map[string]*Session),
		ttl:      ttl,
		now:      time.Now,
	}
}

// Get returns the session for id, creating it on first use, and marks it seen.
func (s *Store) Get(id string) *Session {
	s.mu.Lock()
	defer s.mu.Unlock()

	sess, ok := s.sessions[id]
	if !ok {
		sess = &Session{}
		s.sessions[id] = sess
	}
	sess.lastSeen = s.now()
	return sess
}

// reap drops sessions idle longer than the TTL. Returns how many were removed.
func (s *Store) reap() int {
	s.mu.Lock()
	defer s.mu.Unlock()

	cutoff := s.now().Add(-s.ttl)
	removed := 0
	for id, sess := range s.sessions {
		if sess.lastSeen.Before(cutoff) {
			delete(s.sessions, id)
			removed++
		}
	}
	return removed
}

// Len reports the number of live sessions.
func (s *Store) Len() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.sessions)
}

// Reaper runs reap on an interval until ctx-driven stop is closed. Intended to
// be started in a goroutine.
func (s *Store) Reaper(stop <-chan struct{}, interval time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-stop:
			return
		case <-ticker.C:
			s.reap()
		}
	}
}
