package projection

import (
	"fmt"

	runtimeevents "github.com/Isites/anyai/internal/runtime/events"
	"github.com/Isites/anyai/internal/runtime/task"
)

type TaskView struct {
	recorderFn  func() *runtimeevents.Recorder
	taskStoreFn func() *task.Store
}

func NewTaskView(
	recorderFn func() *runtimeevents.Recorder,
	taskStoreFn func() *task.Store,
) *TaskView {
	return &TaskView{recorderFn: recorderFn, taskStoreFn: taskStoreFn}
}

func (s *TaskView) recorder() *runtimeevents.Recorder {
	if s == nil || s.recorderFn == nil {
		return nil
	}
	return s.recorderFn()
}

func (s *TaskView) store() *task.Store {
	if s == nil || s.taskStoreFn == nil {
		return nil
	}
	return s.taskStoreFn()
}

func (s *TaskView) Rebuild() error {
	store := s.store()
	if store == nil {
		return nil
	}
	return store.Rebuild(s.recorder())
}

func (s *TaskView) List() []task.Info {
	store := s.store()
	if store == nil {
		return nil
	}
	return store.List()
}

func (s *TaskView) Get(taskID string) (task.Info, bool) {
	store := s.store()
	if store == nil {
		return task.Info{}, false
	}
	return store.Get(taskID)
}

func (s *TaskView) Subscribe(taskID string) (<-chan runtimeevents.EventRecord, func(), error) {
	store := s.store()
	if store == nil {
		return nil, nil, fmt.Errorf("task projection store not available")
	}
	ch, cancel := store.Subscribe(taskID)
	return ch, cancel, nil
}
