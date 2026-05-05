package gateway

import (
	"fmt"

	runtimeevents "github.com/Isites/anyai/internal/runtime/events"
)

func (s *Service) GetRunTree(runID string) (runtimeevents.RunTreeRecord, bool) {
	rt, err := s.runtimeOrErr()
	if err != nil {
		return runtimeevents.RunTreeRecord{}, false
	}
	tree, ok := rt.GetRunTree(runID)
	if !ok {
		return runtimeevents.RunTreeRecord{}, false
	}
	return runtimeevents.ReplayRunTreeRecord(tree), true
}

func (s *Service) RunTree(runID string) ([]runtimeevents.RunNode, bool) {
	rt, err := s.runtimeOrErr()
	if err != nil {
		return nil, false
	}
	tree, ok := rt.RunTree(runID)
	if !ok {
		return nil, false
	}
	return runtimeevents.ReplayRunTree(tree), true
}

func (s *Service) SubscribeRunTreeReplay(runID string) ([]runtimeevents.EventRecord, <-chan runtimeevents.EventRecord, func(), error) {
	rt, err := s.runtimeOrErr()
	if err != nil {
		return nil, nil, nil, err
	}
	return subscribeReplayStream(
		func() (<-chan runtimeevents.EventRecord, func(), error) {
			return rt.SubscribeRunTree(runID)
		},
		func() ([]runtimeevents.EventRecord, error) {
			tree, ok := s.GetRunTree(runID)
			if !ok {
				return nil, fmt.Errorf("run tree %q not found", runID)
			}
			return append([]runtimeevents.EventRecord(nil), tree.Events...), nil
		},
	)
}
