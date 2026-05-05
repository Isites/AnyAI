package gateway

import (
	runtimeevents "github.com/Isites/anyai/internal/runtime/events"
	"github.com/Isites/anyai/internal/runtime/session"
)

func (s *Service) ListSessionEvents(agentID, sessionID string) []runtimeevents.EventRecord {
	rt, err := s.runtimeOrErr()
	if err != nil {
		return nil
	}
	return rt.ListSessionEvents(agentID, sessionID)
}

func (s *Service) ListSessions(agentID string) ([]session.SessionInfo, error) {
	rt, err := s.runtimeOrErr()
	if err != nil {
		return nil, err
	}
	return rt.ListSessions(agentID)
}

func (s *Service) LoadSession(agentID, sessionID string) (*session.Session, error) {
	rt, err := s.runtimeOrErr()
	if err != nil {
		return nil, err
	}
	return rt.LoadSession(agentID, sessionID)
}

func (s *Service) CreateSession(agentID, requestedKey, prefix string) (string, error) {
	rt, err := s.runtimeOrErr()
	if err != nil {
		return "", err
	}
	return rt.CreateSession(agentID, requestedKey, prefix)
}

func (s *Service) DeleteSession(agentID, sessionID string) error {
	rt, err := s.runtimeOrErr()
	if err != nil {
		return err
	}
	return rt.DeleteSession(agentID, sessionID)
}

func (s *Service) SubscribeSession(agentID, sessionID string) (<-chan runtimeevents.EventRecord, func(), error) {
	rt, err := s.runtimeOrErr()
	if err != nil {
		return nil, nil, err
	}
	return rt.SubscribeSession(agentID, sessionID)
}
