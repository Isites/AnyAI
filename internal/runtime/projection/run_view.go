package projection

import (
	"fmt"

	runtimeevents "github.com/Isites/anyai/internal/runtime/events"
)

// RunView exposes recorder-backed runtime projections for runs, run trees,
// and session event streams.
type RunView struct {
	recorderFn func() *runtimeevents.Recorder
}

func NewRunView(recorderFn func() *runtimeevents.Recorder) *RunView {
	return &RunView{recorderFn: recorderFn}
}

func (s *RunView) Recorder() *runtimeevents.Recorder {
	if s == nil || s.recorderFn == nil {
		return nil
	}
	return s.recorderFn()
}

func (s *RunView) Rebuild() error {
	recorder := s.Recorder()
	if recorder == nil {
		return nil
	}
	return recorder.RebuildFromStorage()
}

func (s *RunView) GetRun(runID string) (runtimeevents.RunRecord, bool) {
	recorder := s.Recorder()
	if recorder == nil {
		return runtimeevents.RunRecord{}, false
	}
	return recorder.GetRun(runID)
}

func (s *RunView) ListRuns() []runtimeevents.RunRecord {
	recorder := s.Recorder()
	if recorder == nil {
		return nil
	}
	return recorder.ListRuns()
}

func (s *RunView) ListRawRunEvents(runID string) []runtimeevents.EventRecord {
	recorder := s.Recorder()
	if recorder == nil {
		return nil
	}
	return recorder.ListRunEvents(runID)
}

func (s *RunView) SubscribeRawRun(runID string) (<-chan runtimeevents.EventRecord, func(), error) {
	recorder := s.Recorder()
	if recorder == nil {
		return nil, nil, fmt.Errorf("run projection recorder not available")
	}
	ch, cancel := recorder.Subscribe(runID)
	return ch, cancel, nil
}

func (s *RunView) GetRawRunTree(runID string) (runtimeevents.RunTreeRecord, bool) {
	recorder := s.Recorder()
	if recorder == nil {
		return runtimeevents.RunTreeRecord{}, false
	}
	return recorder.GetRunTree(runID)
}

func (s *RunView) RawRunTree(runID string) ([]runtimeevents.RunNode, bool) {
	recorder := s.Recorder()
	if recorder == nil {
		return nil, false
	}
	return recorder.RunTree(runID)
}

func (s *RunView) SubscribeRawRunTree(runID string) (<-chan runtimeevents.EventRecord, func(), error) {
	recorder := s.Recorder()
	if recorder == nil {
		return nil, nil, fmt.Errorf("run projection recorder not available")
	}
	ch, cancel := recorder.SubscribeRunTree(runID)
	return ch, cancel, nil
}

func (s *RunView) ListSessionEvents(sessionID string) []runtimeevents.EventRecord {
	recorder := s.Recorder()
	if recorder == nil {
		return nil
	}
	return recorder.ListSessionEvents(sessionID)
}

func (s *RunView) SubscribeSession(sessionID string) (<-chan runtimeevents.EventRecord, func(), error) {
	recorder := s.Recorder()
	if recorder == nil {
		return nil, nil, fmt.Errorf("run projection recorder not available")
	}
	ch, cancel := recorder.SubscribeSession(sessionID)
	return ch, cancel, nil
}

func (s *RunView) EventStorageDir() string {
	recorder := s.Recorder()
	if recorder == nil {
		return ""
	}
	return recorder.StorageDir()
}
