package gateway

import "time"

func (s *Service) MemoryStats() MemoryStats {
	rt, err := s.runtimeOrErr()
	if err != nil {
		return MemoryStats{}
	}
	return gatewayMemoryStats(rt.MemoryStats())
}

func (s *Service) MemorySearch(query string, maxItems int, scope MemoryScope, layers ...MemoryLayer) []MemorySearchMatch {
	rt, err := s.runtimeOrErr()
	if err != nil {
		return nil
	}
	return gatewayMemoryMatches(rt.MemorySearch(query, maxItems, runtimeMemoryScope(scope), runtimeMemoryLayers(layers)...))
}

func (s *Service) MemoryGet(id string, scope MemoryScope) (MemoryEntry, bool) {
	rt, err := s.runtimeOrErr()
	if err != nil {
		return MemoryEntry{}, false
	}
	entry, ok := rt.MemoryGet(id, runtimeMemoryScope(scope))
	if !ok {
		return MemoryEntry{}, false
	}
	return gatewayMemoryEntry(entry), true
}

func (s *Service) MemoryStaleCleanup(now time.Time) (int, error) {
	rt, err := s.runtimeOrErr()
	if err != nil {
		return 0, err
	}
	return rt.MemoryStaleCleanup(now)
}

func (s *Service) MemoryReindex() (int, error) {
	rt, err := s.runtimeOrErr()
	if err != nil {
		return 0, err
	}
	return rt.MemoryReindex()
}

func (s *Service) MemoryPromoteEligible(now time.Time) (int, error) {
	rt, err := s.runtimeOrErr()
	if err != nil {
		return 0, err
	}
	return rt.MemoryPromoteEligible(now)
}
