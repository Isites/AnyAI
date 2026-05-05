package turn

import (
	"context"
	"strings"
	"sync"
)

// Store tracks active and historical turns.
type Store struct {
	mu    sync.RWMutex
	turns map[ID]*Turn
}

// NewStore creates a new turn store.
func NewStore() *Store {
	return &Store{
		turns: make(map[ID]*Turn),
	}
}

// Create creates and stores a new turn.
func (s *Store) Create(parent context.Context, cfg Config) *Turn {
	if s == nil {
		return New(parent, cfg)
	}
	t := New(parent, cfg)
	s.mu.Lock()
	s.turns[t.ID] = t
	s.mu.Unlock()
	return t
}

// Get returns the turn for the given ID.
func (s *Store) Get(id ID) (*Turn, bool) {
	if s == nil {
		return nil, false
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	t, ok := s.turns[id]
	return t, ok
}

// Remove forgets a turn from the store.
func (s *Store) Remove(id ID) {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.turns, id)
}

// GetBySession lists turns for a session.
func (s *Store) GetBySession(sessionID string) []*Turn {
	if s == nil {
		return nil
	}
	sessionID = strings.TrimSpace(sessionID)
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]*Turn, 0, len(s.turns))
	for _, t := range s.turns {
		if t == nil || strings.TrimSpace(t.SessionID) != sessionID {
			continue
		}
		out = append(out, t)
	}
	return out
}

// CancelAllForSession cancels all active turns in a session.
func (s *Store) CancelAllForSession(sessionID string) {
	for _, t := range s.GetBySession(sessionID) {
		if t == nil {
			continue
		}
		t.Cancel("session_cancelled")
	}
}
