package gateway

func (s *Service) ListSessionEvents(agentID, sessionID string) []Event {
	rt, err := s.runtimeOrErr()
	if err != nil {
		return nil
	}
	return gatewayEvents(rt.ListSessionEvents(agentID, sessionID))
}

func (s *Service) ListSessions(agentID string) ([]SessionInfo, error) {
	rt, err := s.runtimeOrErr()
	if err != nil {
		return nil, err
	}
	infos, err := rt.ListSessions(agentID)
	if err != nil {
		return nil, err
	}
	return gatewaySessionInfos(infos), nil
}

func (s *Service) LoadSession(agentID, sessionID string) (SessionView, error) {
	rt, err := s.runtimeOrErr()
	if err != nil {
		return SessionView{}, err
	}
	snapshot, err := rt.LoadSessionSnapshot(agentID, sessionID)
	if err != nil {
		return SessionView{}, err
	}
	return gatewaySessionSnapshot(snapshot), nil
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

func (s *Service) SubscribeSession(agentID, sessionID string) (<-chan Event, func(), error) {
	rt, err := s.runtimeOrErr()
	if err != nil {
		return nil, nil, err
	}
	ch, cancel, err := rt.SubscribeSession(agentID, sessionID)
	return gatewayEventChannel(ch), cancel, err
}
