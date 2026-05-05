package gateway

import (
	runtimeevents "github.com/Isites/anyai/internal/runtime/events"
	"github.com/Isites/anyai/internal/runtime/task"
)

func (s *Service) ListTasks() []task.Info {
	rt, err := s.runtimeOrErr()
	if err != nil {
		return nil
	}
	return rt.ListTasks()
}

func (s *Service) GetTask(taskID string) (task.Info, bool) {
	rt, err := s.runtimeOrErr()
	if err != nil {
		return task.Info{}, false
	}
	return rt.GetTask(taskID)
}

func (s *Service) SubscribeTask(taskID string) (<-chan runtimeevents.EventRecord, func(), error) {
	rt, err := s.runtimeOrErr()
	if err != nil {
		return nil, nil, err
	}
	return rt.SubscribeTask(taskID)
}

func (s *Service) CancelTask(taskID string) error {
	rt, err := s.runtimeOrErr()
	if err != nil {
		return err
	}
	return rt.CancelTask(taskID)
}
