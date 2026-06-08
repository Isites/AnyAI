package gateway

import (
	"fmt"

	runtimeevents "github.com/Isites/anyai/internal/runtime/events"
)

type rawRunLiveSource interface {
	SubscribeRawRun(runID string) (<-chan runtimeevents.EventRecord, func(), error)
	SubscribeRawRunTree(runID string) (<-chan runtimeevents.EventRecord, func(), error)
}

func (s *Service) ListRuns() []Run {
	rt, err := s.runtimeOrErr()
	if err != nil {
		return nil
	}
	return gatewayRuns(rt.ListRuns())
}

func (s *Service) ListRunEvents(runID string) []Event {
	rt, err := s.runtimeOrErr()
	if err != nil {
		return nil
	}
	return gatewayEvents(rt.ListRunEvents(runID))
}

func (s *Service) SubscribeRunReplay(runID string) ([]Event, <-chan Event, func(), error) {
	rt, err := s.runtimeOrErr()
	if err != nil {
		return nil, nil, nil, err
	}
	snapshot, ch, cancel, err := rt.SubscribeRunReplay(runID)
	return gatewayEvents(snapshot), gatewayEventChannel(ch), cancel, err
}

func (s *Service) SubscribeRunLive(runID string) (<-chan Event, func(), error) {
	rt, err := s.runtimeOrErr()
	if err != nil {
		return nil, nil, err
	}
	live, ok := rt.(rawRunLiveSource)
	if !ok {
		return nil, nil, fmt.Errorf("live run stream not available")
	}
	ch, cancel, err := live.SubscribeRawRun(runID)
	return gatewayEventChannel(ch), cancel, err
}

func (s *Service) CancelRun(runID string) error {
	rt, err := s.runtimeOrErr()
	if err != nil {
		return err
	}
	return rt.CancelRun(runID)
}
