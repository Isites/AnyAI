package gateway

import "fmt"

func (s *Service) GetRunTree(runID string) (RunTree, bool) {
	rt, err := s.runtimeOrErr()
	if err != nil {
		return RunTree{}, false
	}
	tree, ok := rt.GetRunTree(runID)
	if !ok {
		return RunTree{}, false
	}
	return gatewayRunTree(tree), true
}

func (s *Service) RunTree(runID string) ([]RunNode, bool) {
	rt, err := s.runtimeOrErr()
	if err != nil {
		return nil, false
	}
	tree, ok := rt.RunTree(runID)
	if !ok {
		return nil, false
	}
	return gatewayRunNodes(tree), true
}

func (s *Service) RunTreeSummary(runID string) ([]RunNode, bool) {
	rt, err := s.runtimeOrErr()
	if err != nil {
		return nil, false
	}
	tree, ok := rt.RunTreeSummary(runID)
	if !ok {
		return nil, false
	}
	return gatewayRunNodes(tree), true
}

func (s *Service) SubscribeRunTreeLive(runID string) (<-chan Event, func(), error) {
	rt, err := s.runtimeOrErr()
	if err != nil {
		return nil, nil, err
	}
	live, ok := rt.(rawRunLiveSource)
	if !ok {
		return nil, nil, fmt.Errorf("live run tree stream not available")
	}
	ch, cancel, err := live.SubscribeRawRunTree(runID)
	return gatewayEventChannel(ch), cancel, err
}

func (s *Service) SubscribeRunTreeReplay(runID string) ([]Event, <-chan Event, func(), error) {
	rt, err := s.runtimeOrErr()
	if err != nil {
		return nil, nil, nil, err
	}
	snapshot, ch, cancel, err := rt.SubscribeRunTreeReplay(runID)
	return gatewayEvents(snapshot), gatewayEventChannel(ch), cancel, err
}
