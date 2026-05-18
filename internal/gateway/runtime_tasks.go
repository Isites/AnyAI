package gateway

func (s *Service) ListTasks() []Task {
	rt, err := s.runtimeOrErr()
	if err != nil {
		return nil
	}
	return gatewayTasks(rt.ListTasks())
}

func (s *Service) GetTask(taskID string) (Task, bool) {
	rt, err := s.runtimeOrErr()
	if err != nil {
		return Task{}, false
	}
	tk, ok := rt.GetTask(taskID)
	if !ok {
		return Task{}, false
	}
	return gatewayTask(tk), true
}

func (s *Service) SubscribeTask(taskID string) (<-chan Event, func(), error) {
	rt, err := s.runtimeOrErr()
	if err != nil {
		return nil, nil, err
	}
	ch, cancel, err := rt.SubscribeTask(taskID)
	return gatewayEventChannel(ch), cancel, err
}

func (s *Service) CancelTask(taskID string) error {
	rt, err := s.runtimeOrErr()
	if err != nil {
		return err
	}
	return rt.CancelTask(taskID)
}
