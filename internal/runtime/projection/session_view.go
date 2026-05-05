package projection

import (
	"fmt"

	runtimeevents "github.com/Isites/anyai/internal/runtime/events"
	runtimesession "github.com/Isites/anyai/internal/runtime/session"
	runtimesessionops "github.com/Isites/anyai/internal/runtime/session/ops"
)

// SessionView exposes session projection and control behavior while sourcing
// session event history directly from persisted session records.
type SessionView struct {
	sessions *runtimesessionops.Service
}

func NewSessionView(sessions *runtimesessionops.Service) *SessionView {
	return &SessionView{
		sessions: sessions,
	}
}

func (s *SessionView) Rebuild() error {
	if s == nil || s.sessions == nil {
		return nil
	}
	if err := s.sessions.RebuildFromEvents(); err != nil {
		return err
	}
	return s.sessions.RebuildIndex()
}

func (s *SessionView) List(agentID string) ([]runtimesession.SessionInfo, error) {
	if s == nil || s.sessions == nil {
		return nil, fmt.Errorf("session projection not available")
	}
	return s.sessions.List(agentID)
}

func (s *SessionView) Create(agentID, key string) error {
	if s == nil || s.sessions == nil {
		return fmt.Errorf("session projection not available")
	}
	return s.sessions.Create(agentID, key)
}

func (s *SessionView) Load(agentID, key string) (*runtimesession.Session, error) {
	if s == nil || s.sessions == nil {
		return nil, fmt.Errorf("session projection not available")
	}
	return s.sessions.Load(agentID, key)
}

func (s *SessionView) Delete(agentID, key string) error {
	if s == nil || s.sessions == nil {
		return fmt.Errorf("session projection not available")
	}
	return s.sessions.Delete(agentID, key)
}

func (s *SessionView) Compact(agentID, key string, keepEntries int) (*runtimesession.Session, error) {
	if s == nil || s.sessions == nil {
		return nil, fmt.Errorf("session projection not available")
	}
	return s.sessions.Compact(agentID, key, keepEntries)
}

func (s *SessionView) ReadIndex() (runtimesessionops.Index, error) {
	if s == nil || s.sessions == nil {
		return runtimesessionops.Index{}, fmt.Errorf("session projection not available")
	}
	return s.sessions.ReadIndex()
}

func (s *SessionView) Events(agentID, sessionID string) []runtimeevents.EventRecord {
	if s == nil || s.sessions == nil {
		return nil
	}
	return s.sessions.EventRecords(agentID, sessionID)
}

func (s *SessionView) Subscribe(agentID, sessionID string) (<-chan runtimeevents.EventRecord, func(), error) {
	if s == nil || s.sessions == nil {
		return nil, nil, fmt.Errorf("session projection session service not available")
	}
	return s.sessions.SubscribeEvents(agentID, sessionID)
}
