package gateway

import (
	"fmt"

	runtimeevents "github.com/Isites/anyai/internal/runtime/events"
)

func (s *Service) ListRuns() []runtimeevents.RunRecord {
	rt, err := s.runtimeOrErr()
	if err != nil {
		return nil
	}
	return rt.ListRuns()
}

func (s *Service) ListRunEvents(runID string) []runtimeevents.EventRecord {
	rt, err := s.runtimeOrErr()
	if err != nil {
		return nil
	}
	events := rt.ListRunEvents(runID)
	run, ok := rt.GetRun(runID)
	if !ok {
		return events
	}
	return runtimeevents.ReplayRunEvents(run, events)
}

func (s *Service) SubscribeRunReplay(runID string) ([]runtimeevents.EventRecord, <-chan runtimeevents.EventRecord, func(), error) {
	rt, err := s.runtimeOrErr()
	if err != nil {
		return nil, nil, nil, err
	}
	return subscribeReplayStream(
		func() (<-chan runtimeevents.EventRecord, func(), error) {
			return rt.SubscribeRun(runID)
		},
		func() ([]runtimeevents.EventRecord, error) {
			run, ok := rt.GetRun(runID)
			if !ok {
				return nil, fmt.Errorf("run %q not found", runID)
			}
			return runtimeevents.ReplayRunEvents(run, rt.ListRunEvents(runID)), nil
		},
	)
}

func (s *Service) CancelRun(runID string) error {
	rt, err := s.runtimeOrErr()
	if err != nil {
		return err
	}
	return rt.CancelRun(runID)
}
