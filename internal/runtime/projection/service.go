package projection

import (
	runtimeevents "github.com/Isites/anyai/internal/runtime/events"
	runtimesessionops "github.com/Isites/anyai/internal/runtime/session/ops"
	"github.com/Isites/anyai/internal/runtime/task"
)

// Service composes runtime projections so query, subscribe, rebuild, and
// control paths share one authoritative view layer.
type Service struct {
	Run     *RunView
	Session *SessionView
	Task    *TaskView
}

func NewService(
	recorderFn func() *runtimeevents.Recorder,
	taskStoreFn func() *task.Store,
	sessions *runtimesessionops.Service,
) *Service {
	runSvc := NewRunView(recorderFn)
	return &Service{
		Run:     runSvc,
		Session: NewSessionView(sessions),
		Task:    NewTaskView(recorderFn, taskStoreFn),
	}
}

func (s *Service) Rebuild() error {
	if s == nil {
		return nil
	}
	if s.Run != nil {
		if err := s.Run.Rebuild(); err != nil {
			return err
		}
	}
	if s.Session != nil {
		if err := s.Session.Rebuild(); err != nil {
			return err
		}
	}
	if s.Task != nil {
		if err := s.Task.Rebuild(); err != nil {
			return err
		}
	}
	return nil
}
